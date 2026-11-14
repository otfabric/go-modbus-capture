// SPDX-License-Identifier: MIT

package edit

import (
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

// manyStreams is a capture of n streams of one target with one sample each.
func manyStreams(n int) *listReader {
	r := &listReader{h: capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}, err: io.EOF}
	r.recs = append(r.recs, capture.Target{ID: 1})
	for i := 1; i <= n; i++ {
		r.recs = append(r.recs, capture.Stream{ID: uint32(i), Target: 1})
	}
	for i := 1; i <= n; i++ {
		r.recs = append(r.recs, capture.Sample{Stream: uint32(i), At: capture.Ticks(i), Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, byte(i)}})
	}
	return r
}

// A capture splits into at most maxSplitParts parts. The sample that would
// need one more part is an error, and its part is not opened.
func TestSplitPartLimit(t *testing.T) {
	for _, tc := range []struct {
		streams int
		ok      bool
	}{
		{1, true},
		{maxSplitParts - 1, true},
		{maxSplitParts, true},
		{maxSplitParts + 1, false},
		{maxSplitParts + 100, false},
	} {
		opened := 0
		counts, err := Split(manyStreams(tc.streams), ByStream, "", func(string, capture.Header) (capture.Writer, error) {
			opened++
			return &failWriter{}, nil
		})
		if tc.ok != (err == nil) || !tc.ok && !strings.Contains(err.Error(), "parts") {
			t.Errorf("%d streams: err = %v, want ok=%v", tc.streams, err, tc.ok)
		}
		if want := min(tc.streams, maxSplitParts); opened != want || len(counts) != want {
			t.Errorf("%d streams: %d parts opened, %d counted, want %d", tc.streams, opened, len(counts), want)
		}
	}
	// Splitting by target has the same limit; by function code there are
	// never that many parts.
	r := &listReader{h: capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}, err: io.EOF}
	for i := 1; i <= maxSplitParts+1; i++ {
		r.recs = append(r.recs, capture.Target{ID: uint32(i)}, capture.Stream{ID: uint32(i), Target: uint32(i)})
	}
	for i := 1; i <= maxSplitParts+1; i++ {
		r.recs = append(r.recs, capture.Sample{Stream: uint32(i), Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 1}})
	}
	opener := func(string, capture.Header) (capture.Writer, error) { return &failWriter{}, nil }
	if _, err := Split(r, ByTarget, "", opener); err == nil || !strings.Contains(err.Error(), "parts") {
		t.Errorf("%d targets: err = %v", maxSplitParts+1, err)
	}
	if counts, err := Split(manyStreams(maxSplitParts+1), ByFunction, "", opener); err != nil || len(counts) != 1 {
		t.Errorf("by function code: %d parts, err %v", len(counts), err)
	}
}

// Split holds every part until the input ends, and closes them one after
// the other. The parts already closed hold little by then: binary writers,
// which compress, keep none of their buffers once closed.
func TestSplitHoldsLittlePerClosedPart(t *testing.T) {
	const parts = 400
	var writers []capture.Writer
	var before, after runtime.MemStats
	src := manyStreams(parts)
	runtime.GC()
	runtime.ReadMemStats(&before)
	counts, err := Split(src, ByStream, "", func(_ string, h capture.Header) (capture.Writer, error) {
		w, err := mbcap.NewWriter(io.Discard, h, nil)
		writers = append(writers, w)
		return w, err
	})
	if err != nil || len(counts) != parts {
		t.Fatalf("%d parts, err %v", len(counts), err)
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(writers)
	const budget = 32 << 10
	if per := (int64(after.HeapAlloc) - int64(before.HeapAlloc)) / parts; per > budget {
		t.Fatalf("%d closed parts hold %d bytes each, budget %d", parts, per, budget)
	}
}

// Merge checks its inputs before it uses them: an input without a capture,
// or with a header that is not valid, is an error wrapping ErrCorrupt.
func TestMergeChecksInputs(t *testing.T) {
	open := func(capture.Header) (capture.Writer, error) { return &failWriter{}, nil }
	good := Input{Name: "good", Capture: oneCapture(time.Unix(0, 0))}
	before1970 := oneCapture(time.Unix(-1, 0))
	for name, in := range map[string]Input{
		"no capture":                   {Name: "x"},
		"a zero capture":               {Name: "x", Capture: &capture.Capture{}},
		"tick 0":                       {Name: "x", Capture: &capture.Capture{Header: capture.Header{WallStart: time.Unix(0, 0)}}},
		"a tick above one second":      {Name: "x", Capture: &capture.Capture{Header: capture.Header{TickNS: capture.MaxTickNS + 1, WallStart: time.Unix(0, 0)}}},
		"a wall-clock start too early": {Name: "x", Capture: before1970},
	} {
		for _, inputs := range [][]Input{{in}, {good, in}, {in, good}} {
			for _, mode := range []MergeMode{Interleave, Concatenate} {
				if _, err := Merge(inputs, mode, "", open); !errors.Is(err, capture.ErrCorrupt) {
					t.Errorf("%s (%d inputs): err = %v, want ErrCorrupt", name, len(inputs), err)
				}
			}
		}
	}
	// A tick of one second, the largest, is valid.
	slow := oneCapture(time.Unix(0, 0))
	slow.Header.TickNS = capture.MaxTickNS
	if _, err := Merge([]Input{good, {Name: "slow", Capture: slow}}, Interleave, "", open); err != nil {
		t.Fatalf("a tick of one second: %v", err)
	}
}

// Merge does not change its inputs, and the samples it writes share their
// PDUs with them.
func TestMergeLeavesInputs(t *testing.T) {
	a, b := oneCapture(time.Unix(0, 0)), oneCapture(time.Unix(10, 0))
	var got []capture.Sample
	w := &recorder{samples: &got}
	if _, err := Merge([]Input{{Name: "a", Capture: a}, {Name: "b", Capture: b}}, Interleave, "", func(capture.Header) (capture.Writer, error) { return w, nil }); err != nil {
		t.Fatal(err)
	}
	for i, c := range []*capture.Capture{a, b} {
		want := oneCapture(time.Unix(int64(10*i), 0))
		if d := capture.Diff(want, c); d != "" {
			t.Fatalf("input %d changed: %s", i, d)
		}
	}
	if len(got) != 2 || &got[0].Response[0] != &a.Samples[0].Response[0] || &got[1].Request[0] != &b.Samples[0].Request[0] {
		t.Fatalf("the samples written do not share their PDUs with the inputs: %+v", got)
	}
}

// recorder keeps the samples it is given, as they are.
type recorder struct {
	failWriter
	samples *[]capture.Sample
}

func (r *recorder) WriteSample(s capture.Sample) error {
	*r.samples = append(*r.samples, s)
	return nil
}
