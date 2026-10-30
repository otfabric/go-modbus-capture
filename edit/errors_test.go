// SPDX-License-Identifier: MIT

package edit

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

var errBoom = errors.New("boom")

// failWriter fails the named step and records nothing.
type failWriter struct{ step string }

func (w *failWriter) fail(step string) error {
	if w.step == step {
		return errBoom
	}
	return nil
}

func (w *failWriter) WriteTarget(capture.Target) error { return w.fail("target") }
func (w *failWriter) WriteStream(capture.Stream) error { return w.fail("stream") }
func (w *failWriter) WriteSample(capture.Sample) error { return w.fail("sample") }
func (w *failWriter) Close(capture.End) error          { return w.fail("close") }

// listReader returns recs, then err.
type listReader struct {
	h    capture.Header
	recs []capture.Record
	err  error
}

func (r *listReader) Header() capture.Header { return r.h }
func (r *listReader) Next() (capture.Record, error) {
	if len(r.recs) == 0 {
		return nil, r.err
	}
	rec := r.recs[0]
	r.recs = r.recs[1:]
	return rec, nil
}

func records(err error) *listReader {
	return &listReader{
		h: capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)},
		recs: []capture.Record{
			capture.Target{ID: 1},
			capture.Stream{ID: 1, Target: 1},
			capture.Sample{Stream: 1, At: 5, Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 1}},
			capture.End{Known: true, At: 10},
		},
		err: err,
	}
}

func TestFilterPropagatesErrors(t *testing.T) {
	opener := func(step string) OpenFunc {
		return func(capture.Header) (capture.Writer, error) { return &failWriter{step: step}, nil }
	}
	for _, step := range []string{"target", "stream", "sample", "close"} {
		if _, err := Filter(records(io.EOF), Selection{}, "", opener(step)); !errors.Is(err, errBoom) {
			t.Fatalf("%s: %v", step, err)
		}
	}
	// A target held back for a selected stream fails when it is written.
	if _, err := Filter(records(io.EOF), Selection{Streams: []uint32{1}}, "", opener("target")); !errors.Is(err, errBoom) {
		t.Fatalf("pending target: %v", err)
	}
	if _, err := Filter(records(errBoom), Selection{}, "", opener("")); !errors.Is(err, errBoom) {
		t.Fatalf("read: %v", err)
	}
	fail := func(capture.Header) (capture.Writer, error) { return nil, errBoom }
	if _, err := Filter(records(io.EOF), Selection{}, "", fail); !errors.Is(err, errBoom) {
		t.Fatalf("open: %v", err)
	}
}

func TestSplitPropagatesErrors(t *testing.T) {
	opener := func(step string) SplitOpenFunc {
		return func(string, capture.Header) (capture.Writer, error) { return &failWriter{step: step}, nil }
	}
	for _, step := range []string{"target", "stream", "sample"} {
		if _, err := Split(records(io.EOF), ByTarget, "", opener(step)); !errors.Is(err, errBoom) {
			t.Fatalf("%s: %v", step, err)
		}
	}
	if _, err := Split(records(io.EOF), ByTarget, "", opener("close")); !errors.Is(err, errBoom) || !strings.Contains(err.Error(), "closing part") {
		t.Fatalf("close: %v", err)
	}
	if _, err := Split(records(errBoom), ByTarget, "", opener("")); !errors.Is(err, errBoom) {
		t.Fatalf("read: %v", err)
	}
	fail := func(string, capture.Header) (capture.Writer, error) { return nil, errBoom }
	if _, err := Split(records(io.EOF), ByTarget, "", fail); !errors.Is(err, errBoom) {
		t.Fatalf("open: %v", err)
	}
	src := &listReader{recs: []capture.Record{capture.Sample{Stream: 7}}, err: io.EOF}
	if _, err := Split(src, ByTarget, "", opener("")); err == nil || !strings.Contains(err.Error(), "undefined stream") {
		t.Fatalf("undefined stream: %v", err)
	}
}

func oneCapture(wall time.Time) *capture.Capture {
	return &capture.Capture{
		Header:  capture.Header{TickNS: 1000, WallStart: wall, Producer: "p", Tags: map[string]string{"k": "v"}},
		Targets: []capture.Target{{ID: 1}},
		Streams: []capture.Stream{{ID: 1, Target: 1}},
		Samples: []capture.Sample{{Stream: 1, At: 5, Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 1}}},
		End:     capture.End{Known: true, At: 10},
	}
}

func TestMergeErrors(t *testing.T) {
	if _, err := Merge(nil, Interleave, "", nil); err == nil || !strings.Contains(err.Error(), "nothing to merge") {
		t.Fatalf("no inputs: %v", err)
	}
	inputs := []Input{{Name: "a", Capture: oneCapture(time.Unix(0, 0))}, {Name: "b", Capture: oneCapture(time.Unix(10, 0))}}
	for _, step := range []string{"target", "stream", "sample", "close"} {
		open := func(capture.Header) (capture.Writer, error) { return &failWriter{step: step}, nil }
		if _, err := Merge(inputs, Concatenate, "", open); !errors.Is(err, errBoom) {
			t.Fatalf("%s: %v", step, err)
		}
	}
	open := func(capture.Header) (capture.Writer, error) { return nil, errBoom }
	if _, err := Merge(inputs, Interleave, "", open); !errors.Is(err, errBoom) {
		t.Fatalf("open: %v", err)
	}
}

// The header keeps what all inputs share, whichever input comes first.
func TestMergeHeader(t *testing.T) {
	late, early := oneCapture(time.Unix(10, 0)), oneCapture(time.Unix(0, 0))
	early.Header.Producer = "q"
	early.Header.Tags = nil
	var got capture.Header
	open := func(h capture.Header) (capture.Writer, error) {
		got = h
		return &failWriter{}, nil
	}
	if _, err := Merge([]Input{{Name: "a", Capture: late}, {Name: "b", Capture: early}}, Interleave, "", open); err != nil {
		t.Fatal(err)
	}
	if !got.WallStart.Equal(time.Unix(0, 0)) {
		t.Fatalf("wall start %v", got.WallStart)
	}
	if got.Producer != "" || got.Tags["k"] != "" {
		t.Fatalf("header %+v", got)
	}
	if got.Tags[capture.TagSource] == "" {
		t.Fatalf("no source tag: %+v", got)
	}
}

func TestMergeOverflow(t *testing.T) {
	a := oneCapture(time.Unix(0, 0))
	a.Samples[0].At = capture.Ticks(1 << 63)
	a.End.At = capture.Ticks(1 << 63)
	b := oneCapture(time.Unix(0, 1))
	open := func(capture.Header) (capture.Writer, error) { return &failWriter{}, nil }
	if _, err := Merge([]Input{{Name: "a", Capture: a}, {Name: "b", Capture: b}}, Interleave, "", open); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("err %v", err)
	}
}
