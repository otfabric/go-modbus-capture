// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

var errSink = errors.New("sink failed")

// failAfter accepts n bytes, then fails.
type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) > f.n {
		return 0, errSink
	}
	f.n -= len(p)
	return len(p), nil
}

func testHeader() capture.Header {
	return capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}
}

func sample(at capture.Ticks, resp byte) capture.Sample {
	return capture.Sample{Stream: 1, At: at, Request: []byte{0x03, 0, 0, 0, 1}, Response: []byte{0x03, 2, 0, resp}}
}

func TestNewWriterOptions(t *testing.T) {
	if _, err := NewWriter(&bytes.Buffer{}, testHeader(), &Options{Compression: NoCompression + 1}); err == nil {
		t.Fatal("unknown compression accepted")
	}
	if _, err := NewWriter(&failAfter{}, testHeader(), &Options{}); err != nil {
		t.Fatalf("buffered header must not reach the sink yet: %v", err)
	}
}

// Whatever step the sink fails at, the error comes back and sticks.
func TestWriterSinkFailure(t *testing.T) {
	full := func(w *Writer) error {
		if err := w.WriteTarget(capture.Target{ID: 1, Label: strings.Repeat("x", 1<<13)}); err != nil {
			return err
		}
		if err := w.WriteStream(capture.Stream{ID: 1, Target: 1, Label: strings.Repeat("y", 1<<13)}); err != nil {
			return err
		}
		for i := range 40 {
			if err := w.WriteSample(sample(capture.Ticks(i), byte(i))); err != nil {
				return err
			}
		}
		if err := w.Flush(); err != nil {
			return err
		}
		return w.Close(capture.End{Known: true, At: 100})
	}
	for _, room := range []int{0, 4096, 8192, 16384} {
		w, err := NewWriter(&failAfter{n: room}, testHeader(), &Options{BlockSamples: 4, Compression: NoCompression})
		if err != nil {
			t.Fatalf("room %d: %v", room, err)
		}
		if err := full(w); !errors.Is(err, errSink) {
			t.Fatalf("room %d: %v", room, err)
		}
		if err := w.WriteTarget(capture.Target{ID: 9}); !errors.Is(err, errSink) {
			t.Fatalf("room %d: error did not stick: %v", room, err)
		}
		if err := w.Flush(); !errors.Is(err, errSink) {
			t.Fatalf("room %d: flush: %v", room, err)
		}
		if err := w.Close(capture.End{}); !errors.Is(err, errSink) {
			t.Fatalf("room %d: close: %v", room, err)
		}
	}
}

func TestWriterRejectsInvalidRecords(t *testing.T) {
	w, err := NewWriter(&bytes.Buffer{}, testHeader(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(capture.Target{}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("target: %v", err)
	}
	if err := w.WriteSample(sample(0, 1)); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("sample: %v", err)
	}
	if err := w.WriteTarget(capture.Target{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSample(sample(50, 1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{Known: true, At: 1}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("end before a sample: %v", err)
	}
}

func TestReaderVersion(t *testing.T) {
	r, err := NewReader(bytes.NewReader(newFile(t).close(0)))
	if err != nil {
		t.Fatal(err)
	}
	if major, minor := r.Version(); major != capture.VersionMajor || minor != capture.VersionMinor {
		t.Fatalf("version %d.%d", major, minor)
	}
}

func TestReaderInvalidHeaders(t *testing.T) {
	var unknownMajor enc
	unknownMajor.uvarint(capture.VersionMajor + 1)
	unknownMajor.uvarint(0)
	tests := []struct {
		name string
		make func() []byte
		want string
	}{
		{"first block not a header", func() []byte { return frame([]byte(Magic), blockTarget, nil) }, "not a header"},
		{"unknown major", func() []byte { return frame([]byte(Magic), blockHeader, unknownMajor.b) }, "reads major version"},
		{"cut header", func() []byte { return frame([]byte(Magic), blockHeader, []byte{0}) }, "header"},
		{"a second header", func() []byte {
			f := newFile(t)
			f.add(blockHeader, nil)
			return f.b
		}, "second header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := readAllBytes(tt.make(), false)
			if !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := NewReader(strings.NewReader("")); err == nil {
		t.Fatal("empty input")
	}
}

func TestReaderBadBlocks(t *testing.T) {
	long := bytes.Repeat([]byte{0xFF}, 20)
	tests := []struct {
		name string
		make func(f *file) []byte
		want string
	}{
		{"cut template", func(f *file) []byte {
			f.add(blockTemplate, []byte{1})
			return f.b
		}, "bad varint"},
		{"template id 0", func(f *file) []byte {
			f.template(0, []byte{3})
			return f.b
		}, "template id 0"},
		{"empty template", func(f *file) []byte {
			f.template(1, nil)
			return f.b
		}, "empty request"},
		{"bad varint", func(f *file) []byte {
			f.add(blockTemplate, long)
			return f.b
		}, "bad varint"},
		{"string longer than its block", func(f *file) []byte {
			f.add(blockTemplate, []byte{1, 0x7f})
			return f.b
		}, "exceeds"},
		{"data block without a codec", func(f *file) []byte {
			f.add(blockData, nil)
			return f.b
		}, "unexpected end"},
		{"uncompressed length mismatch", func(f *file) []byte {
			f.add(blockData, []byte{codecNone, 5, 1})
			return f.b
		}, "declared 5"},
		{"data block without samples", func(f *file) []byte {
			f.data(0, 0, nil, 0, 0)
			return f.b
		}, "without samples"},
		{"cut data block", func(f *file) []byte {
			f.add(blockData, []byte{codecNone, 1, 0})
			return f.b
		}, "bad varint"},
		{"index without footer tag", func(f *file) []byte {
			f.add(blockIndex, encodeIndex(capture.End{}, nil))
			f.add(blockFooter, []byte{1, 2, 3})
			return f.b
		}, "malformed footer"},
		{"footer without an index", func(f *file) []byte {
			f.add(blockFooter, []byte{1})
			return f.b
		}, "footer without an index"},
		{"index end flag", func(f *file) []byte {
			f.add(blockIndex, []byte{7, 0, 0})
			return f.b
		}, "index end flag"},
		{"index block count", func(f *file) []byte {
			f.add(blockIndex, encodeIndex(capture.End{}, []blockInfo{{}}))
			return f.b
		}, "lists 1 data blocks"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := readAllBytes(tt.make(newFile(t)), false)
			if !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
}

// A reader error other than truncation reaches the caller and sticks.
func TestReaderSourceError(t *testing.T) {
	data := testFile(t)
	r, err := NewReader(io.MultiReader(bytes.NewReader(data[:len(data)/2]), iotestErr{}))
	if err != nil {
		t.Fatal(err)
	}
	var first error
	for first == nil {
		_, first = r.Next()
	}
	if _, again := r.Next(); !errors.Is(again, first) && again.Error() != first.Error() {
		t.Fatalf("error did not stick: %v, then %v", first, again)
	}
}

type iotestErr struct{}

func (iotestErr) Read([]byte) (int, error) { return 0, errSink }

func testFile(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, testHeader(), &Options{BlockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(capture.Target{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 1}); err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		if err := w.WriteSample(sample(capture.Ticks(i), byte(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(capture.End{Known: true, At: 10}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestReadIndex(t *testing.T) {
	data := testFile(t)
	idx, err := ReadIndex(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if idx.End != (capture.End{Known: true, At: 10}) || len(idx.Blocks) != 3 {
		t.Fatalf("index %+v", idx)
	}
	if _, err := ReadIndex(bytes.NewReader(nil), 0); !errors.Is(err, ErrNoFooter) {
		t.Fatalf("empty: %v", err)
	}
	if _, err := ReadIndex(bytes.NewReader(data[:40]), 400); err == nil {
		t.Fatal("size beyond the data")
	}
	damaged := bytes.Clone(data)
	damaged[len(damaged)-1] ^= 1
	if _, err := ReadIndex(bytes.NewReader(damaged), int64(len(damaged))); !errors.Is(err, ErrNoFooter) {
		t.Fatalf("bad CRC: %v", err)
	}
	footer := func(body []byte, off uint64) []byte {
		out := bytes.Clone(body)
		var p [8]byte
		for i := range p {
			p[7-i] = byte(off >> (8 * i))
		}
		return frame(out, blockFooter, append(p[:], footerTag...))
	}
	body := data[:len(data)-FooterLen]
	out := footer(body, 1)
	if _, err := ReadIndex(bytes.NewReader(out), int64(len(out))); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("outside: %v", err)
	}
	// The file's last block is the index, so this points at the header block.
	out = footer(body, uint64(len(Magic)))
	if _, err := ReadIndex(bytes.NewReader(out), int64(len(out))); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("not an index: %v", err)
	}
	// A valid footer over an index block whose payload does not decode.
	bad := frame([]byte(Magic), blockIndex, []byte{9, 0, 0})
	out = footer(bad, uint64(len(Magic)))
	if _, err := ReadIndex(bytes.NewReader(out), int64(len(out))); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("bad index: %v", err)
	}
	// An index block damaged on disk.
	cut := bytes.Clone(data)
	cut[len(cut)-FooterLen-1] ^= 0xFF
	if _, err := ReadIndex(bytes.NewReader(cut), int64(len(cut))); err == nil {
		t.Fatal("index CRC not checked")
	}
}

func TestRepairReports(t *testing.T) {
	var out bytes.Buffer
	if _, err := Repair(&out, strings.NewReader("nope"), nil); err == nil {
		t.Fatal("a file without a header")
	}
	if _, err := Repair(&failAfter{}, bytes.NewReader(testFile(t)), nil); !errors.Is(err, errSink) {
		t.Fatalf("sink failure: %v", err)
	}
	if _, err := Repair(&bytes.Buffer{}, bytes.NewReader(testFile(t)), &Options{Compression: NoCompression + 1}); err == nil {
		t.Fatal("bad options")
	}

	// A complete file keeps every sample and its end.
	rep, err := Repair(&out, bytes.NewReader(testFile(t)), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Samples != 6 || rep.Truncated || rep.Stopped != nil || !rep.End.Known {
		t.Fatalf("report %+v", rep)
	}

	// Damage in the middle stops at the corrupt structure, not at the CRC.
	f := newFile(t)
	f.template(1, []byte{0x03, 0, 0, 0, 1})
	f.add(blockTarget, []byte{0})
	out.Reset()
	rep, err = Repair(&out, bytes.NewReader(f.b), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stopped == nil || rep.Truncated || rep.End.Known {
		t.Fatalf("report %+v", rep)
	}

	// A truncated file ends unknown.
	out.Reset()
	rep, err = Repair(&out, bytes.NewReader(mustRead(t, "truncated.mbcap")), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Truncated || rep.End.Known {
		t.Fatalf("report %+v", rep)
	}
	if _, _, err := readAllBytes(out.Bytes(), false); err != nil {
		t.Fatalf("repaired file: %v", err)
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
