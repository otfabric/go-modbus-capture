// SPDX-License-Identifier: MIT

package mbcap

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Reader reads an MBCAP file as a stream of records. It checks every block's
// CRC and every record against the model's invariants (capture.Checker), and
// at the end the index against the blocks it read.
//
// A Reader decodes one data block at a time and holds its samples until Next
// has returned them: at most 65 536 samples, each with its own copy of the
// request and response PDU. The records Next returns are the caller's. The
// first error of Next sticks. A Reader is not safe for concurrent use.
type Reader struct {
	br  *bufio.Reader
	off uint64 // offset of the next byte to read
	h   capture.Header
	ck  *capture.Checker

	// SkipCorrupt makes the reader skip a block whose CRC does not match,
	// instead of failing. Samples in a skipped data block are lost; a skipped
	// definition makes later samples fail. Set it before the first Next.
	SkipCorrupt bool
	skipped     int

	minor     uint64
	templates map[uint64][]byte
	pending   []capture.Sample
	blocks    []blockInfo
	end       *capture.End
	indexOff  uint64
	done      bool
	err       error
}

// NewReader reads the magic number and the header block.
func NewReader(r io.Reader) (*Reader, error) {
	br := &Reader{br: bufio.NewReaderSize(r, 64<<10), templates: map[uint64][]byte{}}
	var magic [len(Magic)]byte
	if _, err := io.ReadFull(br.br, magic[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("mbcap: %w: shorter than the magic number", capture.ErrTruncated)
		}
		return nil, err
	}
	br.off = uint64(len(Magic))
	if string(magic[:]) != Magic {
		return nil, fmt.Errorf("mbcap: %w: not an MBCAP file (bad magic number)", capture.ErrCorrupt)
	}
	typ, payload, err := br.readBlock()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("mbcap: %w: no header block", capture.ErrTruncated)
	}
	if err != nil {
		return nil, fmt.Errorf("mbcap: header: %w", err)
	}
	if typ != blockHeader {
		return nil, fmt.Errorf("mbcap: %w: the first block is type 0x%02x, not a header", capture.ErrCorrupt, typ)
	}
	h, minor, err := decodeHeader(payload)
	if err != nil {
		return nil, fmt.Errorf("mbcap: header: %w", err)
	}
	if br.ck, err = capture.NewChecker(h); err != nil {
		return nil, fmt.Errorf("mbcap: header: %w", err)
	}
	br.h, br.minor = h, minor
	return br, nil
}

// Header returns the capture header. The result is the caller's: its Tags
// map is a copy.
func (r *Reader) Header() capture.Header {
	h := r.h
	h.Tags = maps.Clone(h.Tags)
	return h
}

// Version returns the format version of the file. The minor version is at
// most math.MaxInt32; NewReader refuses a file with a larger one.
func (r *Reader) Version() (major, minor int) { return capture.VersionMajor, int(r.minor) }

// Skipped returns the number of blocks skipped because of SkipCorrupt.
func (r *Reader) Skipped() int { return r.skipped }

// Next returns the next record, or io.EOF after the last one. An input that
// ends before the index and footer gives an error wrapping
// capture.ErrTruncated after the last complete block's records.
func (r *Reader) Next() (capture.Record, error) {
	for {
		if r.err != nil {
			return nil, r.err
		}
		if len(r.pending) > 0 {
			s := r.pending[0]
			r.pending = r.pending[1:]
			if err := r.ck.Sample(s); err != nil {
				return nil, r.fail(err)
			}
			return s, nil
		}
		if r.done {
			if r.end != nil {
				e := *r.end
				r.end = nil
				return e, nil
			}
			return nil, io.EOF
		}
		rec, err := r.nextBlock()
		if err != nil {
			return nil, r.fail(err)
		}
		if rec != nil {
			return rec, nil
		}
	}
}

func (r *Reader) fail(err error) error {
	if r.err == nil {
		r.err = fmt.Errorf("mbcap: offset %d: %w", r.off, err)
	}
	return r.err
}

// nextBlock reads one block. It returns a definition record, or nil after a
// block that yields none or only pending samples.
func (r *Reader) nextBlock() (capture.Record, error) {
	start := r.off
	typ, payload, err := r.readBlock()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: no index or footer", capture.ErrTruncated)
	}
	if errors.Is(err, errBadCRC) && r.SkipCorrupt {
		r.skipped++
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if r.indexOff != 0 && typ != blockFooter {
		switch typ {
		case blockHeader, blockTarget, blockStream, blockTemplate, blockData, blockIndex:
			return nil, corrupt("block type 0x%02x after the index", typ)
		}
		return nil, nil // unknown block types are skipped here too
	}
	switch typ {
	case blockHeader:
		return nil, corrupt("a second header block")
	case blockTarget:
		t, err := decodeTarget(payload)
		if err != nil {
			return nil, err
		}
		return t, r.ck.Target(t)
	case blockStream:
		d := &dec{b: payload}
		s := capture.Stream{
			ID:     uint32(d.uvarintMax("stream id", 1<<32-1)),
			Target: uint32(d.uvarintMax("target id", 1<<32-1)),
			Role:   d.str(),
			Label:  d.str(),
		}
		if d.err != nil {
			return nil, d.err
		}
		return s, r.ck.Stream(s)
	case blockTemplate:
		return nil, r.decodeTemplate(payload)
	case blockData:
		samples, info, err := r.decodeData(payload)
		if err != nil {
			return nil, err
		}
		info.offset = start
		r.blocks = append(r.blocks, info)
		r.pending = samples
		return nil, nil
	case blockIndex:
		// The index is compared with the data blocks read, unless a block
		// was skipped: its block count first, before any entry is decoded.
		check := !r.SkipCorrupt || r.skipped == 0
		count := -1
		if check {
			count = len(r.blocks)
		}
		end, blocks, err := decodeIndex(payload, count)
		if err != nil {
			return nil, err
		}
		if check {
			if err := r.checkIndex(blocks); err != nil {
				return nil, err
			}
		}
		if err := r.ck.End(end); err != nil {
			return nil, err
		}
		if end.Known {
			r.end = &end
		}
		r.indexOff = start
		return nil, nil
	case blockFooter:
		return nil, r.decodeFooter(payload)
	default:
		return nil, nil // unknown block types are skipped
	}
}

var errBadCRC = fmt.Errorf("%w: block CRC mismatch", capture.ErrCorrupt)

// readBlock reads one block and checks its CRC. At a clean end of input it
// returns io.EOF; within a block, an error wrapping capture.ErrTruncated.
func (r *Reader) readBlock() (uint8, []byte, error) {
	crc := crc32.New(castagnoli)
	typ, err := r.br.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	r.off++
	_, _ = crc.Write([]byte{typ})
	var length uint64
	for shift := uint(0); ; shift += 7 {
		b, err := r.br.ReadByte()
		if err != nil {
			return 0, nil, truncatedOr(err)
		}
		r.off++
		_, _ = crc.Write([]byte{b})
		if shift == 63 && b > 1 || shift > 63 {
			return 0, nil, corrupt("bad block length")
		}
		length |= uint64(b&0x7F) << shift
		if b < 0x80 {
			break
		}
	}
	if length > MaxBlockLen {
		return 0, nil, corrupt("block of %d bytes, more than %d", length, MaxBlockLen)
	}
	buf, err := readN(r.br, length+4)
	r.off += uint64(len(buf))
	if err != nil {
		return 0, nil, truncatedOr(err)
	}
	payload := buf[:length]
	_, _ = crc.Write(payload)
	if crc.Sum32() != binary.BigEndian.Uint32(buf[length:]) {
		return typ, nil, fmt.Errorf("block type 0x%02x: %w", typ, errBadCRC)
	}
	return typ, payload, nil
}

func truncatedOr(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return fmt.Errorf("%w: input ends inside a block", capture.ErrTruncated)
	}
	return err
}

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", capture.ErrCorrupt, fmt.Sprintf(format, args...))
}

func decodeHeader(payload []byte) (capture.Header, uint64, error) {
	d := &dec{b: payload}
	major := d.uvarint()
	// Version returns the minor version as an int, on every platform.
	minor := d.uvarintMax("minor version", math.MaxInt32)
	if d.err == nil && major != capture.VersionMajor {
		return capture.Header{}, 0, corrupt("format version %d.%d: this reader reads major version %d", major, minor, capture.VersionMajor)
	}
	h := capture.Header{TickNS: d.uvarint()}
	wall := d.uvarintMax("wall-clock start", 1<<63-1)
	h.WallStart = time.Unix(0, int64(wall)).UTC()
	flags := d.uvarint()
	if flags&^headerKnownFlags != 0 {
		d.fail("unknown header flags 0x%x", flags)
	}
	h.ChangeOnly, h.Exceptions = flags&headerChangeOnly != 0, flags&headerExceptions != 0
	h.Producer = d.str()
	h.Tags = d.tags()
	return h, minor, d.err
}

func decodeTarget(payload []byte) (capture.Target, error) {
	d := &dec{b: payload}
	t := capture.Target{
		ID:        uint32(d.uvarintMax("target id", 1<<32-1)),
		Transport: capture.Transport(d.u8()),
		Unit:      uint8(d.uvarintMax("unit id", 255)),
		Label:     d.str(),
		Host:      d.str(),
		Port:      uint16(d.uvarintMax("port", 65535)),
	}
	t.Serial.Device = d.str()
	t.Serial.Baud = uint32(d.uvarintMax("baud", 1<<32-1))
	t.Serial.Parity = capture.Parity(d.u8())
	t.Serial.DataBits = d.u8()
	t.Serial.StopBits = d.u8()
	t.Tags = d.tags()
	return t, d.err
}

func (r *Reader) decodeTemplate(payload []byte) error {
	d := &dec{b: payload}
	id := d.uvarintMax("template id", 1<<63)
	pdu := d.bytes(capture.MaxPDULen)
	if d.err != nil {
		return d.err
	}
	switch {
	case id == 0:
		return corrupt("template id 0")
	case len(pdu) == 0:
		return corrupt("template %d: empty request", id)
	case len(r.templates) >= MaxTemplates:
		return corrupt("more than %d templates", MaxTemplates)
	}
	if _, dup := r.templates[id]; dup {
		return corrupt("template %d defined twice", id)
	}
	r.templates[id] = pdu
	return nil
}

func (r *Reader) decodeData(payload []byte) ([]capture.Sample, blockInfo, error) {
	d := &dec{b: payload}
	codec := d.u8()
	rawLen := d.uvarintMax("data block size", MaxBlockRaw)
	if d.err != nil {
		return nil, blockInfo{}, d.err
	}
	var body []byte
	switch codec {
	case codecNone:
		// Bytes after the declared body are ignored, as in any known block.
		if uint64(len(d.b)) < rawLen {
			return nil, blockInfo{}, corrupt("uncompressed data block of %d bytes, declared %d", len(d.b), rawLen)
		}
		body = d.b[:rawLen]
	case codecDeflate:
		fr := flate.NewReader(bytes.NewReader(d.b))
		var err error
		if body, err = readN(fr, rawLen); err != nil {
			return nil, blockInfo{}, corrupt("deflate: %v", err)
		}
		if n, _ := fr.Read(make([]byte, 1)); n != 0 {
			return nil, blockInfo{}, corrupt("deflate: more data than the declared %d bytes", rawLen)
		}
	case codecZstd:
		return nil, blockInfo{}, corrupt("data block compressed with zstd, which this reader does not support")
	default:
		return nil, blockInfo{}, corrupt("data block compressed with unknown codec %d", codec)
	}
	return r.decodeBody(body)
}

func (r *Reader) decodeBody(body []byte) ([]capture.Sample, blockInfo, error) {
	d := &dec{b: body}
	base := d.uvarint()
	// Every sample takes at least 4 bytes, and a block holds at most
	// maxBlockSamples of them.
	count := d.uvarintMax("sample count", min(uint64(len(body)/4), maxBlockSamples))
	if d.err != nil {
		return nil, blockInfo{}, d.err
	}
	if count == 0 {
		return nil, blockInfo{}, corrupt("data block without samples")
	}
	// The slice grows with the samples decoded, not with the declared count.
	samples := make([]capture.Sample, 0, min(count, DefaultBlockSamples))
	// The previous response to each (stream, request) of the block: slots
	// maps the request key to its place in last.
	slots := map[string]int{}
	var last [][]byte
	var key []byte
	streams := map[uint32]struct{}{}
	at := base
	info := blockInfo{first: ^uint64(0)}
	for i := range count {
		flags := d.u8()
		if flags&^flagKnownMask != 0 {
			d.fail("sample %d: unknown flags 0x%02x (reserved bits or sample kind)", i, flags)
		}
		stream := uint32(d.uvarintMax("stream id", 1<<32-1))
		var req []byte
		if flags&flagInline != 0 {
			req = d.bytes(capture.MaxPDULen)
		} else {
			id := d.uvarint()
			var ok bool
			if req, ok = r.templates[id]; !ok && d.err == nil {
				d.fail("sample %d: undefined template %d", i, id)
			}
		}
		dt := d.zigzag()
		if d.err != nil {
			return nil, blockInfo{}, d.err
		}
		if dt < 0 && uint64(-dt) > at || dt > 0 && at+uint64(dt) < at {
			return nil, blockInfo{}, corrupt("sample %d: time out of range", i)
		}
		at += uint64(dt)

		key = append(binary.BigEndian.AppendUint32(key[:0], stream), req...)
		slot, seen := slots[string(key)]
		var prev []byte
		if seen {
			prev = last[slot]
		}
		var resp []byte
		switch flags & flagCodingMask {
		case codingRaw:
			resp = d.bytes(capture.MaxPDULen)
		case codingSame:
			if !seen {
				d.fail("sample %d: SAME without a previous response in the block", i)
			}
			resp = append([]byte(nil), prev...)
		case codingDiff:
			if !seen {
				d.fail("sample %d: DIFF without a previous response in the block", i)
			}
			resp = applyDiff(d, prev)
		default:
			d.fail("sample %d: reserved response coding 3", i)
		}
		if d.err != nil {
			return nil, blockInfo{}, d.err
		}
		// last keeps the slice the sample returns: every sample of the block
		// is decoded before Next returns the first one.
		if seen {
			last[slot] = resp
		} else {
			slots[string(key)] = len(last)
			last = append(last, resp)
		}
		streams[stream] = struct{}{}
		info.first, info.last = min(info.first, at), max(info.last, at)
		if len(samples) == cap(samples) {
			grown := make([]capture.Sample, len(samples), min(count, 2*uint64(len(samples))))
			copy(grown, samples)
			samples = grown
		}
		samples = append(samples, capture.Sample{Stream: stream, At: capture.Ticks(at), Request: slices.Clone(req), Response: resp})
	}
	for s := range streams {
		info.streams = append(info.streams, s)
	}
	slices.Sort(info.streams)
	return samples, info, nil
}

// applyDiff decodes a DIFF response against prev.
func applyDiff(d *dec, prev []byte) []byte {
	words := uint64(len(prev)+1) / 2
	n := d.uvarintMax("changed word count", words)
	out := append([]byte(nil), prev...)
	idx := uint64(0)
	for j := uint64(0); j < n && d.err == nil; j++ {
		delta := d.uvarint()
		if j > 0 && delta == 0 {
			d.fail("DIFF word index repeats")
		}
		idx += delta
		w := d.take(2)
		if d.err != nil {
			break
		}
		if idx >= words || idx < delta {
			d.fail("DIFF word index %d beyond the %d words of the response", idx, words)
			break
		}
		out[2*idx] = w[0]
		if 2*idx+1 < uint64(len(out)) {
			out[2*idx+1] = w[1]
		}
	}
	return out
}

// decodeIndex decodes an index block. With count >= 0 the index must list
// count data blocks: another number is an error before an entry is decoded.
func decodeIndex(payload []byte, count int) (capture.End, []blockInfo, error) {
	d := &dec{b: payload}
	known := d.u8()
	end := capture.End{Known: known == 1, At: capture.Ticks(d.uvarint())}
	if d.err == nil && known > 1 {
		d.fail("index end flag %d", known)
	}
	// Every entry takes at least 4 bytes.
	n := d.uvarintMax("index block count", uint64(len(payload)/4))
	if d.err == nil && count >= 0 && n != uint64(count) {
		d.fail("the index lists %d data blocks, the file has %d", n, count)
	}
	var blocks []blockInfo
	for i := uint64(0); i < n && d.err == nil; i++ {
		b := blockInfo{offset: d.uvarint(), first: d.uvarint(), last: d.uvarint()}
		ns := d.uvarintMax("index stream count", uint64(len(d.b)))
		for j := uint64(0); j < ns && d.err == nil; j++ {
			b.streams = append(b.streams, uint32(d.uvarintMax("stream id", 1<<32-1)))
		}
		blocks = append(blocks, b)
	}
	return end, blocks, d.err
}

// checkIndex compares the entries of the index with the data blocks read.
// decodeIndex has checked that there are as many.
func (r *Reader) checkIndex(blocks []blockInfo) error {
	for i, b := range blocks {
		got := r.blocks[i]
		if b.offset != got.offset || b.first != got.first || b.last != got.last || !slices.Equal(b.streams, got.streams) {
			return corrupt("index entry %d does not match the data block at offset %d", i, got.offset)
		}
	}
	return nil
}

func (r *Reader) decodeFooter(payload []byte) error {
	if r.indexOff == 0 {
		return corrupt("footer without an index")
	}
	if len(payload) != 12 || string(payload[8:]) != footerTag {
		return corrupt("malformed footer")
	}
	if off := binary.BigEndian.Uint64(payload); off != r.indexOff {
		return corrupt("footer points to offset %d, the index is at %d", off, r.indexOff)
	}
	if _, err := r.br.ReadByte(); err != io.EOF {
		if err == nil {
			return corrupt("data after the footer")
		}
		return err
	}
	r.done = true
	return nil
}
