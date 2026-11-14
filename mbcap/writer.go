// SPDX-License-Identifier: MIT

package mbcap

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Compression selects how a Writer compresses data blocks.
type Compression uint8

// Compressions. A block that deflate does not make smaller is stored
// uncompressed either way.
const (
	// Deflate compresses every data block with compress/flate (the default).
	Deflate Compression = iota
	// NoCompression stores data blocks uncompressed: for live capture, where
	// blocks are flushed often and should cost little CPU.
	NoCompression
)

// Default block size limits.
const (
	DefaultBlockSamples = 256
	DefaultBlockBytes   = 64 << 10
)

// Options tune a Writer. The zero value is the default.
type Options struct {
	Compression Compression
	// BlockSamples ends a data block after this many samples (default 256;
	// at most 65 536, the most a reader accepts in one block).
	BlockSamples int
	// BlockBytes ends a data block once its uncompressed body reaches this
	// size (default 64 KiB; at most MaxBlockRaw).
	BlockBytes int
}

// Writer writes an MBCAP file. It never seeks, so it can write to a pipe.
// Blocks are written as they fill; Flush ends the current one, so that the
// file is readable up to there.
//
// A Writer copies what it keeps of a record, so the caller may reuse or
// change a record's PDUs and tags once the call has returned. Close completes
// the file once: after it the Writer holds none of its buffers, and every
// later call returns an error. A Close that refuses its end leaves the Writer
// open. A Writer is not safe for concurrent use.
type Writer struct {
	w      *bufio.Writer
	off    uint64 // offset of the next byte written
	ck     *capture.Checker
	opts   Options
	err    error
	closed bool

	templates map[string]uint64
	nextTmpl  uint64
	scratch   []byte

	// The open data block.
	body     enc
	count    int
	baseAt   capture.Ticks
	prevAt   capture.Ticks
	minAt    capture.Ticks
	maxAt    capture.Ticks
	prev     map[string][]byte
	inBlock  map[uint32]struct{}
	index    []blockInfo
	deflater *flate.Writer
	deflated bytes.Buffer

	// The size of the index so far: indexLen is the bytes of the entries of
	// the blocks written, blockStreams those of the stream ids of the open
	// block.
	indexLen     int
	blockStreams int
}

// Upper bounds on the parts of an index block whose size is only known when
// the file closes or a data block ends.
const (
	// The end flag, the capture end and the block count.
	indexHeadMax = 1 + 2*binary.MaxVarintLen64
	// An entry without its stream ids: offset, first and last time, and the
	// count of at most capture.MaxStreams streams.
	indexEntryMax = 3*binary.MaxVarintLen64 + 3
)

// errIndexFull is returned by WriteSample for a sample whose data block the
// index could not list: the index is a block of at most MaxBlockLen.
var errIndexFull = fmt.Errorf("mbcap: %w: the index has no room for another data block (it is a block of at most %d bytes): close this file and continue in a new one",
	capture.ErrCorrupt, MaxBlockLen)

type blockInfo struct {
	offset, first, last uint64
	streams             []uint32
}

// NewWriter checks h and writes the magic number and the header block.
func NewWriter(w io.Writer, h capture.Header, opts *Options) (*Writer, error) {
	ck, err := capture.NewChecker(h)
	if err != nil {
		return nil, err
	}
	bw := &Writer{w: bufio.NewWriter(w), ck: ck, templates: map[string]uint64{}, nextTmpl: 1,
		prev: map[string][]byte{}, inBlock: map[uint32]struct{}{}}
	if opts != nil {
		bw.opts = *opts
	}
	if bw.opts.BlockSamples <= 0 {
		bw.opts.BlockSamples = DefaultBlockSamples
	}
	bw.opts.BlockSamples = min(bw.opts.BlockSamples, maxBlockSamples)
	if bw.opts.BlockBytes <= 0 {
		bw.opts.BlockBytes = DefaultBlockBytes
	}
	bw.opts.BlockBytes = min(bw.opts.BlockBytes, MaxBlockRaw-2*capture.MaxPDULen-64)
	if bw.opts.Compression > NoCompression {
		return nil, errors.New("mbcap: unknown compression option")
	}
	bw.write([]byte(Magic))
	var e enc
	e.uvarint(capture.VersionMajor)
	e.uvarint(capture.VersionMinor)
	e.uvarint(h.TickNS)
	e.uvarint(uint64(h.WallStart.UnixNano()))
	var flags uint64
	if h.ChangeOnly {
		flags |= headerChangeOnly
	}
	if h.Exceptions {
		flags |= headerExceptions
	}
	e.uvarint(flags)
	e.str(h.Producer)
	e.tags(h.Tags)
	bw.block(blockHeader, e.b)
	return bw, bw.err
}

func (w *Writer) write(b []byte) {
	if w.err != nil {
		return
	}
	n, err := w.w.Write(b)
	w.off += uint64(n)
	w.err = err
}

func (w *Writer) block(typ uint8, payload []byte) {
	w.scratch = frame(w.scratch[:0], typ, payload)
	w.write(w.scratch)
}

func (w *Writer) usable() error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return errors.New("mbcap: write after Close")
	}
	return nil
}

// WriteTarget writes a target block.
func (w *Writer) WriteTarget(t capture.Target) error {
	if err := w.usable(); err != nil {
		return err
	}
	if err := w.ck.Target(t); err != nil {
		return err
	}
	var e enc
	e.uvarint(uint64(t.ID))
	e.u8(uint8(t.Transport))
	e.uvarint(uint64(t.Unit))
	e.str(t.Label)
	e.str(t.Host)
	e.uvarint(uint64(t.Port))
	e.str(t.Serial.Device)
	e.uvarint(uint64(t.Serial.Baud))
	e.u8(uint8(t.Serial.Parity))
	e.u8(t.Serial.DataBits)
	e.u8(t.Serial.StopBits)
	e.tags(t.Tags)
	w.block(blockTarget, e.b)
	return w.err
}

// WriteStream writes a stream block.
func (w *Writer) WriteStream(s capture.Stream) error {
	if err := w.usable(); err != nil {
		return err
	}
	if err := w.ck.Stream(s); err != nil {
		return err
	}
	var e enc
	e.uvarint(uint64(s.ID))
	e.uvarint(uint64(s.Target))
	e.str(s.Role)
	e.str(s.Label)
	w.block(blockStream, e.b)
	return w.err
}

// WriteSample adds a sample to the open data block, unless the capture does
// not store it (see capture.Writer). A request seen for the first time gets a
// template block first.
//
// The index lists every data block and is itself a block of at most
// MaxBlockLen, which is room for about 1.7 million data blocks. WriteSample
// returns an error wrapping capture.ErrCorrupt for a sample whose data block
// the index could not list. The sample is not written and the Writer stays
// usable: Close completes the file with the samples written before.
func (w *Writer) WriteSample(s capture.Sample) error {
	if err := w.usable(); err != nil {
		return err
	}
	// A time delta is a signed 64-bit value: a sample 2^63 ticks or more
	// from the previous one starts a new data block, whose base time takes
	// the step.
	jump := w.count > 0 && farApart(w.prevAt, s.At)
	if !w.indexRoom(s.Stream, jump) {
		return errIndexFull
	}
	keep, err := w.ck.Admit(s)
	if err != nil || !keep {
		return err
	}
	if jump {
		w.endBlock()
	}
	tmpl, ok := w.templates[string(s.Request)]
	if !ok && len(w.templates) < MaxTemplates {
		tmpl = w.nextTmpl
		w.nextTmpl++
		w.templates[string(s.Request)] = tmpl
		var e enc
		e.uvarint(tmpl)
		e.bytes(s.Request)
		w.block(blockTemplate, e.b)
		ok = true
	}

	if w.count == 0 {
		w.baseAt, w.prevAt, w.minAt, w.maxAt = s.At, s.At, s.At, s.At
	}
	key := requestKey(s.Stream, s.Request)
	prev, seen := w.prev[key]
	coding := codingRaw
	var diff []byte
	switch {
	case seen && bytes.Equal(prev, s.Response):
		coding = codingSame
	case seen && len(prev) == len(s.Response):
		diff = encodeDiff(prev, s.Response)
		if len(diff) < uvarintLen(uint64(len(s.Response)))+len(s.Response) {
			coding = codingDiff
		}
	}
	flags := uint8(coding)
	if !ok {
		flags |= flagInline
	}
	b := &w.body
	b.u8(flags)
	b.uvarint(uint64(s.Stream))
	if ok {
		b.uvarint(tmpl)
	} else {
		b.bytes(s.Request)
	}
	b.zigzag(int64(s.At - w.prevAt))
	switch coding {
	case codingRaw:
		b.bytes(s.Response)
	case codingDiff:
		b.b = append(b.b, diff...)
	}
	w.prev[key] = append(prev[:0], s.Response...)
	w.prevAt = s.At
	w.minAt, w.maxAt = min(w.minAt, s.At), max(w.maxAt, s.At)
	if _, in := w.inBlock[s.Stream]; !in {
		w.inBlock[s.Stream] = struct{}{}
		w.blockStreams += uvarintLen(uint64(s.Stream))
	}
	w.count++
	if w.count >= w.opts.BlockSamples || len(b.b) >= w.opts.BlockBytes {
		w.endBlock()
	}
	return w.err
}

// farApart reports whether a and b are 2^63 ticks or more apart.
func farApart(a, b capture.Ticks) bool {
	if a > b {
		a, b = b, a
	}
	return b-a >= 1<<63
}

// indexRoom reports whether the index can list the data block that a sample
// of stream goes to: the open one, or with next the one after it. An entry
// counts with its largest size while its block is open.
func (w *Writer) indexRoom(stream uint32, next bool) bool {
	n := indexHeadMax + w.indexLen + indexEntryMax + w.blockStreams
	_, in := w.inBlock[stream]
	switch {
	case next:
		n += indexEntryMax + uvarintLen(uint64(stream))
	case !in:
		n += uvarintLen(uint64(stream))
	}
	return n <= maxIndexLen
}

// indexEntryLen returns the size of the index entry of b.
func indexEntryLen(b blockInfo) int {
	n := uvarintLen(b.offset) + uvarintLen(b.first) + uvarintLen(b.last) + uvarintLen(uint64(len(b.streams)))
	for _, s := range b.streams {
		n += uvarintLen(uint64(s))
	}
	return n
}

// encodeDiff codes cur against prev (same length) as changed 16-bit words: a
// count, then for each changed word the delta of its index to the previous
// changed one, and the word. An odd trailing byte is padded with 0.
func encodeDiff(prev, cur []byte) []byte {
	var words enc
	n, last := 0, 0
	for i := 0; i < len(cur); i += 2 {
		hi, lo := cur[i], byte(0)
		phi, plo := prev[i], byte(0)
		if i+1 < len(cur) {
			lo, plo = cur[i+1], prev[i+1]
		}
		if hi == phi && lo == plo {
			continue
		}
		w := i / 2
		words.uvarint(uint64(w - last))
		words.b = append(words.b, hi, lo)
		last = w
		n++
	}
	var e enc
	e.uvarint(uint64(n))
	return append(e.b, words.b...)
}

func uvarintLen(v uint64) int {
	var buf [binary.MaxVarintLen64]byte
	return binary.PutUvarint(buf[:], v)
}

// endBlock writes the open data block, if it holds samples.
func (w *Writer) endBlock() {
	if w.count == 0 || w.err != nil {
		return
	}
	var body enc
	body.uvarint(uint64(w.baseAt))
	body.uvarint(uint64(w.count))
	body.b = append(body.b, w.body.b...)

	codec, data := uint8(codecNone), body.b
	if w.opts.Compression == Deflate {
		if w.deflater == nil {
			w.deflater, _ = flate.NewWriter(&w.deflated, flate.DefaultCompression)
		}
		w.deflated.Reset()
		w.deflater.Reset(&w.deflated)
		_, _ = w.deflater.Write(body.b)
		_ = w.deflater.Close()
		if w.deflated.Len() < len(body.b) {
			codec, data = codecDeflate, w.deflated.Bytes()
		}
	}
	var e enc
	e.u8(codec)
	e.uvarint(uint64(len(body.b)))
	e.b = append(e.b, data...)

	streams := make([]uint32, 0, len(w.inBlock))
	for s := range w.inBlock {
		streams = append(streams, s)
	}
	slices.Sort(streams)
	info := blockInfo{offset: w.off, first: uint64(w.minAt), last: uint64(w.maxAt), streams: streams}
	w.index = append(w.index, info)
	w.indexLen += indexEntryLen(info)
	w.block(blockData, e.b)

	w.body.b = w.body.b[:0]
	w.count = 0
	w.blockStreams = 0
	clear(w.prev)
	clear(w.inBlock)
}

// Flush ends the open data block and flushes everything written to the
// underlying writer. The file is then readable up to here. A live recorder
// calls it on a timer.
func (w *Writer) Flush() error {
	if err := w.usable(); err != nil {
		return err
	}
	w.endBlock()
	if w.err == nil {
		w.err = w.w.Flush()
	}
	return w.err
}

// Close ends the open data block, writes the index and footer, and flushes.
// Use capture.End{} when the end is unknown. The Writer is then closed and
// holds none of its buffers, whether or not the underlying writer took what
// was written. Two errors leave the Writer as it was: an end that the capture
// refuses (one before a sample), after which Close may be called again, and
// the error of an earlier call that failed on the underlying writer.
func (w *Writer) Close(end capture.End) error {
	if err := w.usable(); err != nil {
		return err
	}
	if err := w.ck.End(end); err != nil {
		return err
	}
	w.endBlock()
	w.closed = true
	indexOff := w.off
	w.block(blockIndex, encodeIndex(end, w.index))
	payload := binary.BigEndian.AppendUint64(nil, indexOff)
	w.block(blockFooter, append(payload, footerTag...))
	if w.err == nil {
		w.err = w.w.Flush()
	}
	// A closed writer keeps none of its buffers: a caller may hold many.
	w.w, w.scratch, w.body.b = nil, nil, nil
	w.templates, w.prev, w.inBlock, w.index = nil, nil, nil, nil
	w.deflater, w.deflated = nil, bytes.Buffer{}
	return w.err
}

func encodeIndex(end capture.End, blocks []blockInfo) []byte {
	var e enc
	if end.Known {
		e.u8(1)
	} else {
		e.u8(0)
	}
	e.uvarint(uint64(end.At))
	e.uvarint(uint64(len(blocks)))
	for _, b := range blocks {
		e.uvarint(b.offset)
		e.uvarint(b.first)
		e.uvarint(b.last)
		e.uvarint(uint64(len(b.streams)))
		for _, s := range b.streams {
			e.uvarint(uint64(s))
		}
	}
	return e.b
}
