// SPDX-License-Identifier: MIT

package jsonl

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

var errSink = errors.New("sink failed")

type failAfter struct{ n int }

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) > f.n {
		return 0, errSink
	}
	f.n -= len(p)
	return len(p), nil
}

func header() capture.Header { return capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)} }

func TestWriterMisuse(t *testing.T) {
	if _, err := NewWriter(&bytes.Buffer{}, capture.Header{}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("bad header: %v", err)
	}
	w, err := NewWriter(&bytes.Buffer{}, header())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(capture.Target{}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("target: %v", err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 9}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("stream: %v", err)
	}
	if err := w.WriteSample(capture.Sample{Stream: 9}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("sample: %v", err)
	}
	if err := w.Close(capture.End{At: 5}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("end: %v", err)
	}
	w, err = NewWriter(&bytes.Buffer{}, header())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("second Close: %v", err)
	}
	if err := w.WriteTarget(capture.Target{ID: 1}); err == nil {
		t.Fatal("write after Close")
	}
}

// A writer buffers, so the failure comes when the buffer flushes. It sticks.
func TestWriterSinkFailure(t *testing.T) {
	w, err := NewWriter(&failAfter{}, header())
	if err != nil {
		t.Fatal(err)
	}
	var werr error
	for i := uint32(1); i < 200 && werr == nil; i++ {
		werr = w.WriteTarget(capture.Target{ID: i, Label: strings.Repeat("x", 1024)})
	}
	if !errors.Is(werr, errSink) {
		t.Fatalf("err %v", werr)
	}
	if err := w.WriteTarget(capture.Target{ID: 999}); !errors.Is(err, errSink) {
		t.Fatalf("error did not stick: %v", err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 1}); !errors.Is(err, errSink) {
		t.Fatalf("stream: %v", err)
	}
	if err := w.Close(capture.End{}); !errors.Is(err, errSink) {
		t.Fatalf("close: %v", err)
	}
	w, err = NewWriter(&failAfter{}, header())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{}); !errors.Is(err, errSink) {
		t.Fatalf("flush on close: %v", err)
	}
}

func TestReaderMoreRules(t *testing.T) {
	long := strings.Repeat("00", capture.MaxPDULen+1)
	tests := []struct {
		name string
		in   string
		err  error
	}{
		{"blank lines before the header", "\n \n" + head, nil},
		{"a line across the read buffer", head + `{"t":"note","x":"` + strings.Repeat("a", 8<<10) + `"}` + "\n", nil},
		{"bad header JSON", `{"t":"header","version":5}` + "\n", capture.ErrCorrupt},
		{"bad wall start", `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"yesterday"}` + "\n", capture.ErrCorrupt},
		{"target without id", head + `{"t":"target","unit":1}` + "\n", capture.ErrCorrupt},
		{"stream of the wrong shape", head + `{"t":"stream","id":"x"}` + "\n", capture.ErrCorrupt},
		{"stream without id", head + `{"t":"stream","target":1}` + "\n", capture.ErrCorrupt},
		{"end of the wrong shape", head + `{"t":"end","at":"x"}` + "\n", capture.ErrCorrupt},
		{"end without at", head + `{"t":"end"}` + "\n", capture.ErrCorrupt},
		{"sample with a long PDU", head + defs + `{"t":"s","s":1,"at":5,"req":"` + long + `","resp":"03"}` + "\n", capture.ErrCorrupt},
		{"bad serial parity", head + `{"t":"target","id":1,"unit":1,"serial":{"parity":"mark"}}` + "\n", capture.ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := read(tt.in)
			if tt.err == nil && err != nil || !errors.Is(err, tt.err) {
				t.Fatalf("err %v, want %v", err, tt.err)
			}
		})
	}
}

func TestReaderSourceError(t *testing.T) {
	if _, err := NewReader(iotest.ErrReader(errSink)); !errors.Is(err, errSink) {
		t.Fatalf("header: %v", err)
	}
	r, err := NewReader(iotest.TimeoutReader(strings.NewReader(head + strings.Repeat(`{"t":"note"}`+"\n", 4000))))
	if err != nil {
		t.Fatal(err)
	}
	var last error
	for last == nil {
		_, last = r.Next()
	}
	if !errors.Is(last, iotest.ErrTimeout) {
		t.Fatalf("err %v", last)
	}
}
