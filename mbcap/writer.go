// SPDX-License-Identifier: MIT

package mbcap

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
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
	// BlockSamples ends a data block after this many samples (default 256).
	BlockSamples int
	// BlockBytes ends a data block once its uncompressed body reaches this
	// size (default 64 KiB; at most MaxBlockRaw).
	BlockBytes int
}

// Writer writes an MBCAP file. It never seeks, so it can write to a pipe.
// Blocks are written as they fill; Flush ends the current one, so that the
// file is readable up to there.
//
// A Writer is not safe for concurrent use: a recorder that samples from
// several goroutines serialises its calls (Flush included). Separate Writers
// share no state and may run concurrently.
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
}

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
func (w *Writer) WriteSample(s capture.Sample) error {
	if err := w.usable(); err != nil {
		return err
	}
	keep, err := w.ck.Admit(s)
	if err != nil || !keep {
		return err
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
	w.inBlock[s.Stream] = struct{}{}
	w.count++
	if w.count >= w.opts.BlockSamples || len(b.b) >= w.opts.BlockBytes {
		w.endBlock()
	}
	return w.err
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
	w.index = append(w.index, blockInfo{offset: w.off, first: uint64(w.minAt), last: uint64(w.maxAt), streams: streams})
	w.block(blockData, e.b)

	w.body.b = w.body.b[:0]
	w.count = 0
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
// Use capture.End{} when the end is unknown.
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
