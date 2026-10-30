// SPDX-License-Identifier: MIT

package mbcsv

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

func TestWriterSinkFailure(t *testing.T) {
	w, err := NewWriter(&failAfter{}, header())
	if err == nil {
		var werr error
		for i := uint32(1); i < 200 && werr == nil; i++ {
			werr = w.WriteTarget(capture.Target{ID: i, Label: strings.Repeat("x", 1024)})
		}
		err = werr
	}
	if !errors.Is(err, errSink) {
		t.Fatalf("err %v", err)
	}
	w, err = NewWriter(&failAfter{}, header())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{}); !errors.Is(err, errSink) {
		t.Fatalf("close: %v", err)
	}
}

func TestReaderMoreRules(t *testing.T) {
	long := strings.Repeat("00", capture.MaxPDULen+1)
	tests := []struct {
		name, in string
		err      error
	}{
		{"a row after the end", cols + head + "end,5,,,,,,,,,,\n" + "target,,,,,,1,,1,,,\n", capture.ErrCorrupt},
		{"header without version", "t,tick_ns,wall_start\nheader,1,2026-10-14T13:14:24Z\n", capture.ErrCorrupt},
		{"header with an empty version", cols + "header,,,,,,,,,,1000,2026-10-14T13:14:24Z\n", capture.ErrCorrupt},
		{"unknown major version", cols + "header,,,,,,,,,9.0,1000,2026-10-14T13:14:24Z\n", capture.ErrCorrupt},
		{"stream with a bad id", cols + head + "stream,,,,,,x,1,,,,\n", capture.ErrCorrupt},
		{"end with a bad time", cols + head + "end,x,,,,,,,,,,\n", capture.ErrCorrupt},
		{"a sample with a long PDU", cols + head + defs + "s,5,1,03," + long + ",03,,,,,,\n", capture.ErrCorrupt},
		{"a sample with bad hex", cols + head + defs + "s,5,1,03,zz,03,,,,,,\n", capture.ErrCorrupt},
		{"a sample without a time", cols + head + defs + "s,,1,03,03,03,,,,,,\n", capture.ErrCorrupt},
		{"bad tags", "t,version,tick_ns,wall_start,tags\nheader,0.1,1,2026-10-14T13:14:24Z,nojson\n", capture.ErrCorrupt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := read(tt.in); !errors.Is(err, tt.err) {
				t.Fatalf("err %v, want %v", err, tt.err)
			}
		})
	}
}

func TestReaderSourceError(t *testing.T) {
	if _, err := NewReader(iotest.ErrReader(errSink)); !errors.Is(err, errSink) {
		t.Fatalf("columns: %v", err)
	}
}

func TestWriteWideSinkFailure(t *testing.T) {
	c := &capture.Capture{
		Header:  header(),
		Targets: []capture.Target{{ID: 1, Label: "t"}},
		Streams: []capture.Stream{{ID: 1, Target: 1}},
		Samples: []capture.Sample{
			{Stream: 1, At: 1, Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 1}},
			{Stream: 1, At: 2, Request: []byte{3, 0, 1, 0, 1}, Response: []byte{3, 2, 0, 2}},
		},
	}
	if _, err := WriteWide(&failAfter{}, c); !errors.Is(err, errSink) {
		t.Fatalf("err %v", err)
	}
}
