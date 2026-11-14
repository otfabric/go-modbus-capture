// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

// The limits of docs/FORMAT.md section 1 in the binary encoding: a value one
// beyond a limit is rejected with capture.ErrCorrupt, and what a declared
// length or count costs is bounded by the limit and by the input, not by the
// declaration. The limits the model checks for every encoding (strings, tags,
// targets, streams, change-only requests) are tested in package capture.

// samplesPerBlock is the per-data-block sample limit of FORMAT.md section 1.
const samplesPerBlock = 1 << 16

// bigPDUs returns an FC03 request and response of the largest PDU size.
func bigPDUs() (req, resp []byte) {
	req = append([]byte{0x03}, bytes.Repeat([]byte{0x11}, capture.MaxPDULen-1)...)
	resp = append([]byte{0x03}, bytes.Repeat([]byte{0x22}, capture.MaxPDULen-1)...)
	return req, resp
}

// sameBody is a data block body of count samples of stream 1 and template 1,
// all at time 0: the first RAW with resp, every other one SAME.
func sameBody(count int, resp []byte) []byte {
	var e enc
	e.uvarint(0)
	e.uvarint(uint64(count))
	e.u8(codingRaw)
	e.uvarint(1)
	e.uvarint(1)
	e.zigzag(0)
	e.bytes(resp)
	for range count - 1 {
		e.b = append(e.b, codingSame, 1, 1, 0)
	}
	return e.b
}

func deflateBytes(t testing.TB, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// deflated adds a deflate-compressed data block with body, whose samples are
// all of stream 1.
func (f *file) deflated(t testing.TB, body []byte, first, last uint64) {
	var e enc
	e.u8(codecDeflate)
	e.uvarint(uint64(len(body)))
	e.b = append(e.b, deflateBytes(t, body)...)
	f.blocks = append(f.blocks, blockInfo{offset: uint64(len(f.b)), first: first, last: last, streams: []uint32{1}})
	f.add(blockData, e.b)
}

// firstSample opens data and reads up to its first sample.
func firstSample(data []byte) error {
	r, err := NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	for {
		rec, err := r.Next()
		if err != nil {
			return err
		}
		if _, ok := rec.(capture.Sample); ok {
			return nil
		}
	}
}

func readErr(data []byte) error {
	_, _, err := readAllBytes(data, false)
	return err
}

// A data block holds at most 65 536 samples. The reader refuses a larger
// count before it decodes a sample, so that a block costs memory in
// proportion to the limit, not to how well its body compresses.
func TestDataBlockSampleLimit(t *testing.T) {
	req, resp := bigPDUs()
	build := func(t *testing.T, count int) ([]byte, []byte) {
		f := newFile(t)
		f.template(1, req)
		body := sameBody(count, resp)
		f.deflated(t, body, 0, 0)
		return f.close(0), body
	}
	t.Run("at the limit", func(t *testing.T) {
		data, _ := build(t, samplesPerBlock)
		c, _, err := readAllBytes(data, false)
		if err != nil || len(c.Samples) != samplesPerBlock {
			t.Fatalf("read %d samples, err %v; want %d", len(c.Samples), err, samplesPerBlock)
		}
		// The whole block is decoded by the first Next that reaches it.
		const budget = 3 * MaxBlockRaw
		if got := fuzzcap.AllocBytes(2, func() { _ = firstSample(data) }); got > budget {
			t.Fatalf("a block at the sample limit allocated %d bytes up to its first sample, budget %d", got, budget)
		}
	})
	t.Run("one above the limit", func(t *testing.T) {
		data, _ := build(t, samplesPerBlock+1)
		if err := readErr(data); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "sample count") {
			t.Fatalf("err = %v, want ErrCorrupt naming the sample count", err)
		}
	})
	t.Run("far above the limit", func(t *testing.T) {
		data, body := build(t, 4*samplesPerBlock)
		if err := firstSample(data); !errors.Is(err, capture.ErrCorrupt) {
			t.Fatalf("err = %v, want ErrCorrupt", err)
		}
		budget := 4 * uint64(len(body))
		if got := fuzzcap.AllocBytes(2, func() { _ = firstSample(data) }); got > budget {
			t.Fatalf("a rejected block of a %d-byte body allocated %d bytes, budget %d", len(body), got, budget)
		}
	})
}

// A writer never puts more samples into a data block than a reader accepts,
// whatever Options.BlockSamples asks for.
func TestWriterBlockSampleLimit(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, testHeader(), &Options{BlockSamples: 1 << 20, BlockBytes: MaxBlockRaw})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(capture.Target{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 1}); err != nil {
		t.Fatal(err)
	}
	for range samplesPerBlock + 1 {
		if err := w.WriteSample(capture.Sample{Stream: 1, Request: []byte{0x07}, Response: []byte{0x07, 0}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(capture.End{}); err != nil {
		t.Fatal(err)
	}
	idx, err := ReadIndex(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Blocks) != 2 {
		t.Fatalf("%d data blocks for %d samples, want 2", len(idx.Blocks), samplesPerBlock+1)
	}
	if c, _, err := readAllBytes(buf.Bytes(), false); err != nil || len(c.Samples) != samplesPerBlock+1 {
		t.Fatalf("read back %d samples, err %v", len(c.Samples), err)
	}
}

// A block length or a deflate size that the input does not hold costs memory
// in proportion to what was read, not to what was declared.
func TestDeclaredSizeBeyondInput(t *testing.T) {
	const budget = 1 << 20
	f := newFile(t)
	cut := append(bytes.Clone(f.b), blockData)
	cut = binary.AppendUvarint(cut, MaxBlockLen)
	cut = append(cut, codecNone, 0)

	f = newFile(t)
	var e enc
	e.u8(codecDeflate)
	e.uvarint(MaxBlockRaw)
	e.b = append(e.b, deflateBytes(t, make([]byte, 10))...)
	f.add(blockData, e.b)
	short := f.close(0)

	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"block length", cut, capture.ErrTruncated},
		{"deflate size", short, capture.ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := readErr(tc.data); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := fuzzcap.AllocBytes(4, func() { _ = readErr(tc.data) }); got > budget {
				t.Fatalf("a %d-byte input allocated %d bytes, budget %d", len(tc.data), got, budget)
			}
		})
	}
}

// Declared lengths and counts one beyond their limit are rejected before
// anything of that size is allocated.
func TestDeclaredValuesBeyondLimits(t *testing.T) {
	const budget = 1 << 20
	header := func(producerLen, tags uint64) []byte {
		var e enc
		e.uvarint(capture.VersionMajor)
		e.uvarint(capture.VersionMinor)
		e.uvarint(1000)
		e.uvarint(0)
		e.uvarint(0)
		e.uvarint(producerLen)
		if producerLen <= capture.MaxStringLen {
			e.b = append(e.b, bytes.Repeat([]byte{'p'}, int(producerLen))...)
		}
		e.uvarint(tags)
		return frame([]byte(Magic), blockHeader, e.b)
	}
	tooLong := append(bytes.Clone(newFile(t).b), blockData)
	tooLong = binary.AppendUvarint(tooLong, MaxBlockLen+1)
	f := newFile(t)
	var raw enc
	raw.u8(codecDeflate)
	raw.uvarint(MaxBlockRaw + 1)
	f.add(blockData, raw.b)
	for _, tc := range []struct {
		name, want string
		data       []byte
	}{
		{"block payload", "more than", tooLong},
		{"uncompressed data block", "data block size", f.b},
		{"string length", "string length", header(capture.MaxStringLen+1, 0)},
		{"tag count", "tag count", header(0, capture.MaxTags+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := readErr(tc.data); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrCorrupt mentioning %q", err, tc.want)
			}
			if got := fuzzcap.AllocBytes(4, func() { _ = readErr(tc.data) }); got > budget {
				t.Fatalf("allocated %d bytes, budget %d", got, budget)
			}
		})
	}
	// At the limits the header is accepted; the file then ends without an
	// index.
	if err := readErr(header(capture.MaxStringLen, 0)); !errors.Is(err, capture.ErrTruncated) {
		t.Fatalf("a producer of %d bytes: %v, want ErrTruncated after the header", capture.MaxStringLen, err)
	}
}

// A block payload of exactly MaxBlockLen and a data block body of exactly
// MaxBlockRaw are within the limits.
func TestSizesAtTheLimits(t *testing.T) {
	f := newFile(t)
	f.template(1, []byte{0x03, 0, 0, 0, 1})
	f.add(0x30, make([]byte, MaxBlockLen)) // a block of an unknown type is skipped by its length
	// One sample, then bytes that no sample of the block uses.
	body := sameBody(1, []byte{0x03, 2, 0, 7})
	body = append(body, make([]byte, MaxBlockRaw-len(body))...)
	f.deflated(t, body, 0, 0)
	c, _, err := readAllBytes(f.close(0), false)
	if err != nil || len(c.Samples) != 1 {
		t.Fatalf("read %d samples, err %v", len(c.Samples), err)
	}
}

// readN returns exactly the bytes asked for, whatever their number is to the
// size it reads in, and reports an input that ends early as io.ReadFull does.
func TestReadN(t *testing.T) {
	src := make([]byte, 5*readChunk)
	for i := range src {
		src[i] = byte(i * 7)
	}
	for _, n := range []int{0, 1, readChunk - 1, readChunk, readChunk + 1, 2 * readChunk, 2*readChunk + 1, 4*readChunk + 5, len(src)} {
		got, err := readN(bytes.NewReader(src), uint64(n))
		if err != nil || !bytes.Equal(got, src[:n]) || cap(got) > max(n, 1) {
			t.Errorf("readN(%d): %d bytes (cap %d), err %v", n, len(got), cap(got), err)
		}
		if n == 0 {
			continue
		}
		// One byte short.
		got, err = readN(bytes.NewReader(src[:n-1]), uint64(n))
		want := io.ErrUnexpectedEOF
		if n == 1 {
			want = io.EOF
		}
		if err != want || !bytes.Equal(got, src[:n-1]) {
			t.Errorf("readN(%d) of %d bytes: %d bytes, err %v, want %v", n, n-1, len(got), err, want)
		}
	}
	if _, err := readN(iotestErr{}, 10); !errors.Is(err, errSink) {
		t.Errorf("a failing reader: %v", err)
	}
}

// A file defines at most MaxTemplates request templates; the reader's memory
// for them grows with the input.
func TestTemplateLimit(t *testing.T) {
	f := newFile(t)
	for i := range MaxTemplates + 1 {
		f.template(uint64(i+1), []byte{0x07, byte(i), byte(i >> 8), byte(i >> 16)})
	}
	data := f.close(0)
	if err := readErr(data); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "templates") {
		t.Fatalf("err = %v, want ErrCorrupt naming the templates", err)
	}
	// About 14 times the input without the race detector and 16 with it:
	// a map entry and a copy of the request per 14-byte template block.
	budget := 24 * uint64(len(data))
	if got := fuzzcap.AllocBytes(1, func() { _ = readErr(data) }); got > budget {
		t.Fatalf("a %d-byte input allocated %d bytes, budget %d", len(data), got, budget)
	}
}

// The streaming reader compares the index's block count with the data
// blocks it read before it decodes the entries.
func TestIndexCountCheckedFirst(t *testing.T) {
	const n = 1 << 20
	var e enc
	e.u8(0)
	e.uvarint(0)
	e.uvarint(n)
	for range n {
		e.b = append(e.b, 8, 0, 0, 0) // offset 8, times 0, no streams
	}
	f := newFile(t)
	f.add(blockIndex, e.b)
	if err := readErr(f.b); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "lists 1048576 data blocks, the file has 0") {
		t.Fatalf("err = %v, want ErrCorrupt naming the block counts", err)
	}
	budget := 4 * uint64(len(e.b))
	if got := fuzzcap.AllocBytes(2, func() { _ = readErr(f.b) }); got > budget {
		t.Fatalf("an index block of %d bytes allocated %d bytes, budget %d", len(e.b), got, budget)
	}
}
