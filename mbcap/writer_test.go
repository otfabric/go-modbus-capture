// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
)

func newTestWriter(t *testing.T, w io.Writer, opts *Options) *Writer {
	t.Helper()
	bw, err := NewWriter(w, testHeader(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := bw.WriteTarget(capture.Target{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := bw.WriteStream(capture.Stream{ID: 1, Target: 1}); err != nil {
		t.Fatal(err)
	}
	return bw
}

// The index is a block like any other, at most MaxBlockLen (FORMAT.md
// section 1). A writer refuses a sample that would start a data block its
// index could not list; the refusal does not stick, so the file still
// closes, and reads back. The limit is lowered for the test.
func TestWriterIndexLimit(t *testing.T) {
	defer func(n int) { maxIndexLen = n }(maxIndexLen)
	maxIndexLen = 1000

	var buf bytes.Buffer
	w := newTestWriter(t, &buf, &Options{BlockSamples: 1})
	var refused error
	written := 0
	for i := 0; i < 10000 && refused == nil; i++ {
		if refused = w.WriteSample(sample(capture.Ticks(i), byte(i))); refused == nil {
			written++
		}
	}
	if !errors.Is(refused, capture.ErrCorrupt) {
		t.Fatalf("after %d samples: %v, want the writer to refuse a sample with ErrCorrupt", written, refused)
	}
	if err := w.WriteTarget(capture.Target{ID: 2}); err != nil {
		t.Fatalf("a definition after the refusal: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("Flush after the refusal: %v", err)
	}
	if err := w.Close(capture.End{}); err != nil {
		t.Fatalf("Close after the refusal: %v", err)
	}
	c := decode(t, buf.Bytes())
	if len(c.Samples) != written {
		t.Fatalf("read back %d samples, wrote %d", len(c.Samples), written)
	}
	idx, err := ReadIndex(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []blockInfo
	for _, b := range idx.Blocks {
		blocks = append(blocks, blockInfo{offset: b.Offset, first: uint64(b.First), last: uint64(b.Last), streams: b.Streams})
	}
	if n := len(encodeIndex(idx.End, blocks)); n > maxIndexLen {
		t.Fatalf("index of %d bytes, limit %d", n, maxIndexLen)
	}

	// Repair stops at the refusal and still writes a complete file.
	var out bytes.Buffer
	rep, err := Repair(&out, bytes.NewReader(buf.Bytes()), &Options{BlockSamples: 1})
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if rep.Samples != written || rep.Stopped != nil {
		t.Fatalf("Repair of a full file: %+v", rep)
	}
	maxIndexLen = 500
	out.Reset()
	if rep, err = Repair(&out, bytes.NewReader(buf.Bytes()), &Options{BlockSamples: 1}); err != nil {
		t.Fatalf("Repair into a smaller index: %v", err)
	}
	if rep.Stopped == nil || rep.Samples >= written {
		t.Fatalf("Repair into a smaller index: %+v, want it stopped early", rep)
	}
	if c := decode(t, out.Bytes()); len(c.Samples) != rep.Samples {
		t.Fatalf("repaired file holds %d samples, report says %d", len(c.Samples), rep.Samples)
	}
}

// Whatever the limit, with several streams in a block and with time steps
// that end a block early: the only samples a writer refuses are refused for
// the index, and what it closes reads back, with an index within the limit.
func TestWriterIndexLimitSweep(t *testing.T) {
	defer func(n int) { maxIndexLen = n }(maxIndexLen)
	streams := []uint32{1, 200, 70_000, 1 << 31}
	write := func(t *testing.T, limit int) (accepted, refused int) {
		maxIndexLen = limit
		var buf bytes.Buffer
		w, err := NewWriter(&buf, testHeader(), &Options{BlockSamples: 3})
		if err != nil {
			t.Fatal(err)
		}
		want := &capture.Capture{Header: testHeader(), Targets: []capture.Target{{ID: 1}}}
		if err := w.WriteTarget(want.Targets[0]); err != nil {
			t.Fatal(err)
		}
		for _, id := range streams {
			want.Streams = append(want.Streams, capture.Stream{ID: id, Target: 1})
			if err := w.WriteStream(capture.Stream{ID: id, Target: 1}); err != nil {
				t.Fatal(err)
			}
		}
		at := capture.Ticks(0)
		for i := range 120 {
			at += capture.Ticks(i)
			if i%7 == 6 {
				at ^= 1 << 63
			}
			s := sample(at, byte(i))
			s.Stream = streams[(i*i)%len(streams)]
			switch err := w.WriteSample(s); {
			case err == nil:
				want.Samples = append(want.Samples, s)
			case errors.Is(err, capture.ErrCorrupt) && strings.Contains(err.Error(), "index"):
				refused++
			default:
				t.Fatalf("limit %d, sample %d: %v", limit, i, err)
			}
		}
		if err := w.Close(capture.End{}); err != nil {
			t.Fatalf("limit %d: Close: %v", limit, err)
		}
		if d := capture.Diff(want, decode(t, buf.Bytes())); d != "" {
			t.Fatalf("limit %d: round trip: %s", limit, d)
		}
		idx, err := ReadIndex(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatalf("limit %d: %v", limit, err)
		}
		var blocks []blockInfo
		for _, b := range idx.Blocks {
			blocks = append(blocks, blockInfo{offset: b.Offset, first: uint64(b.First), last: uint64(b.Last), streams: b.Streams})
		}
		if n := len(encodeIndex(idx.End, blocks)); n > limit {
			t.Fatalf("limit %d: index of %d bytes", limit, n)
		}
		return len(want.Samples), refused
	}
	// The first sample needs room for the head of the index and for one
	// entry with its stream id, each at its largest size.
	const first = indexHeadMax + indexEntryMax + 1
	for limit := indexHeadMax; limit <= 1200; limit++ {
		accepted, refused := write(t, limit)
		if accepted+refused != 120 {
			t.Fatalf("limit %d: %d written and %d refused of 120", limit, accepted, refused)
		}
		if (accepted > 0) != (limit >= first) {
			t.Fatalf("limit %d: %d samples written; the first one fits from %d", limit, accepted, first)
		}
		if limit == 1200 && refused != 0 {
			t.Fatalf("limit %d: %d samples refused", limit, refused)
		}
	}
}

// Samples 2^63 ticks or more apart, in either direction, never share a data
// block, so that every time delta fits its zigzag varint. The file reads
// back.
func TestWriterTimeJumps(t *testing.T) {
	times := []capture.Ticks{0, 1<<63 + 1, 1<<64 - 1, 1, 1 << 63, 0, 1<<63 - 1}
	var buf bytes.Buffer
	w := newTestWriter(t, &buf, nil)
	want := &capture.Capture{Header: testHeader(), Targets: []capture.Target{{ID: 1}}, Streams: []capture.Stream{{ID: 1, Target: 1}},
		End: capture.End{Known: true, At: 1<<64 - 1}}
	for i, at := range times {
		s := sample(at, byte(i))
		if err := w.WriteSample(s); err != nil {
			t.Fatalf("sample at %d: %v", at, err)
		}
		want.Samples = append(want.Samples, s)
	}
	if err := w.Close(want.End); err != nil {
		t.Fatal(err)
	}
	if d := capture.Diff(want, decode(t, buf.Bytes())); d != "" {
		t.Fatalf("round trip: %s", d)
	}

	// A jump of 2^63-1 fits a delta: the samples share a block.
	buf.Reset()
	w = newTestWriter(t, &buf, nil)
	for i, at := range []capture.Ticks{0, 1<<63 - 1} {
		if err := w.WriteSample(sample(at, byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(capture.End{}); err != nil {
		t.Fatal(err)
	}
	idx, err := ReadIndex(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil || len(idx.Blocks) != 1 {
		t.Fatalf("index %+v, err %v; want one data block", idx, err)
	}
}

// A Close that rejects its end leaves the writer as it was: a valid Close
// afterwards completes the file.
func TestWriterCloseAfterRejectedEnd(t *testing.T) {
	var buf bytes.Buffer
	w := newTestWriter(t, &buf, nil)
	if err := w.WriteSample(sample(50, 1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{Known: true, At: 10}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("end before the sample: %v", err)
	}
	if err := w.Close(capture.End{Known: true, At: 60}); err != nil {
		t.Fatalf("a valid Close after the rejected one: %v", err)
	}
	c := decode(t, buf.Bytes())
	if len(c.Samples) != 1 || c.End != (capture.End{Known: true, At: 60}) {
		t.Fatalf("read back %d samples, end %+v", len(c.Samples), c.End)
	}
}

// A closed writer keeps none of its buffers, so that a caller holding many
// closed writers (edit.Split holds every part until the input ends) holds
// little memory.
func TestCloseReleasesBuffers(t *testing.T) {
	w := newTestWriter(t, io.Discard, &Options{BlockSamples: 2})
	for i := range 5 {
		if err := w.WriteSample(sample(capture.Ticks(i), byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(capture.End{}); err != nil {
		t.Fatal(err)
	}
	if w.w != nil || w.prev != nil || w.inBlock != nil || w.templates != nil || w.index != nil || w.scratch != nil || w.body.b != nil {
		t.Fatalf("a closed writer still holds buffers: %+v", w)
	}
	if err := w.WriteSample(sample(9, 9)); err == nil {
		t.Fatal("write after Close succeeded")
	}
	if err := w.Close(capture.End{}); err == nil {
		t.Fatal("a second Close succeeded")
	}

	// Measured: closed writers that each compressed a block.
	const n = 64
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	closed := make([]*Writer, n)
	for i := range closed {
		w := newTestWriter(t, io.Discard, nil)
		if err := w.WriteSample(sample(0, 1)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(capture.End{}); err != nil {
			t.Fatal(err)
		}
		closed[i] = w
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(closed)
	const budget = 32 << 10
	if per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / n; per > budget {
		t.Fatalf("a closed writer keeps %d bytes, budget %d", per, budget)
	}
}
