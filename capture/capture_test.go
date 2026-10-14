// SPDX-License-Identifier: MIT

package capture

import (
	"errors"
	"io"
	"testing"
	"time"
)

func TestNames(t *testing.T) {
	for tr := Transport(0); tr.Valid(); tr++ {
		got, err := ParseTransport(tr.String())
		if err != nil || got != tr {
			t.Errorf("transport %d: %q parses to %v, %v", tr, tr.String(), got, err)
		}
	}
	for p := Parity(0); p.Valid(); p++ {
		got, err := ParseParity(p.String())
		if err != nil || got != p {
			t.Errorf("parity %d: %q parses to %v, %v", p, p.String(), got, err)
		}
	}
	if tr, err := ParseTransport(""); err != nil || tr != TransportUnknown {
		t.Error("an empty transport is not unknown")
	}
	if _, err := ParseTransport("x"); err == nil {
		t.Error("an unknown transport parsed")
	}
	if Transport(9).Valid() || Transport(9).String() != "transport(9)" || Parity(5).String() != "parity(5)" {
		t.Error("out-of-range names")
	}
}

func TestTicks(t *testing.T) {
	h := Header{TickNS: 1000, WallStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	if d := h.Duration(1500); d != 1500*time.Microsecond {
		t.Errorf("Duration = %v", d)
	}
	if tk := h.Ticks(1500*time.Microsecond + 999); tk != 1500 {
		t.Errorf("Ticks rounds to %d", tk)
	}
	if h.Ticks(-time.Second) != 0 {
		t.Error("a negative duration is not 0 ticks")
	}
	if d := h.Duration(^Ticks(0)); d != time.Duration(1<<63-1) {
		t.Errorf("Duration does not saturate: %v", d)
	}
	if got := h.Time(2_000_000); !got.Equal(h.WallStart.Add(2 * time.Second)) {
		t.Errorf("Time = %v", got)
	}
}

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{{"0.1", true}, {"0.42", true}, {"1.0", false}, {"0", false}, {"a.b", false}, {"0.-1", false}} {
		_, _, err := ParseVersion(tc.in)
		if (err == nil) != tc.ok || err != nil && !errors.Is(err, ErrCorrupt) {
			t.Errorf("%q: %v", tc.in, err)
		}
	}
}

func sampleCapture() *Capture {
	return &Capture{
		Header:  Header{TickNS: 1000, WallStart: time.Unix(5, 0).UTC(), Producer: "p", Tags: map[string]string{"a": "b"}},
		Targets: []Target{{ID: 1, Label: "x", Tags: map[string]string{"k": "v"}}},
		Streams: []Stream{{ID: 1, Target: 1}},
		Samples: []Sample{{Stream: 1, At: 3, Request: []byte{0x07}, Response: []byte{0x07, 1}}},
		End:     End{Known: true, At: 4},
	}
}

func TestDiff(t *testing.T) {
	if d := Diff(sampleCapture(), sampleCapture()); d != "" {
		t.Fatalf("equal captures differ: %s", d)
	}
	empty := sampleCapture()
	empty.Header.Tags = map[string]string{"a": "b"}
	empty.Targets[0].Tags = nil
	for name, mod := range map[string]func(*Capture){
		"tick":        func(c *Capture) { c.Header.TickNS = 1 },
		"wall start":  func(c *Capture) { c.Header.WallStart = time.Unix(6, 0) },
		"flags":       func(c *Capture) { c.Header.ChangeOnly = true },
		"producer":    func(c *Capture) { c.Header.Producer = "q" },
		"tags":        func(c *Capture) { c.Header.Tags = nil },
		"target":      func(c *Capture) { c.Targets[0].Label = "y" },
		"target tags": func(c *Capture) { c.Targets[0].Tags = nil },
		"targets":     func(c *Capture) { c.Targets = nil },
		"stream":      func(c *Capture) { c.Streams[0].Role = "r" },
		"streams":     func(c *Capture) { c.Streams = nil },
		"pdu":         func(c *Capture) { c.Samples[0].Response = []byte{0x07, 2} },
		"time":        func(c *Capture) { c.Samples[0].At = 2 },
		"samples":     func(c *Capture) { c.Samples = nil },
		"end":         func(c *Capture) { c.End = End{} },
	} {
		b := sampleCapture()
		mod(b)
		if Equal(sampleCapture(), b) {
			t.Errorf("%s: a difference was not found", name)
		}
	}
	// Empty and nil maps are equal; a wall start in another zone is the same instant.
	b := sampleCapture()
	b.Header.WallStart = b.Header.WallStart.In(time.FixedZone("x", 3600))
	b.Targets[0].Tags = map[string]string{"k": "v"}
	if d := Diff(sampleCapture(), b); d != "" {
		t.Errorf("%s", d)
	}
	if err := sampleCapture().Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if s := sampleCapture().Samples[0]; s.Function() != 0x07 || s.IsException() || (&Sample{}).Function() != 0 {
		t.Error("Sample helpers")
	}
}

// memWriter records what Copy writes.
type memWriter struct {
	c      Capture
	closed bool
}

func (m *memWriter) WriteTarget(t Target) error { m.c.Targets = append(m.c.Targets, t); return nil }
func (m *memWriter) WriteStream(s Stream) error { m.c.Streams = append(m.c.Streams, s); return nil }
func (m *memWriter) WriteSample(s Sample) error { m.c.Samples = append(m.c.Samples, s); return nil }
func (m *memWriter) Close(e End) error          { m.c.End, m.closed = e, true; return nil }

// sliceReader returns the records of a capture.
type sliceReader struct {
	h    Header
	recs []Record
}

func (r *sliceReader) Header() Header { return r.h }
func (r *sliceReader) Next() (Record, error) {
	if len(r.recs) == 0 {
		return nil, io.EOF
	}
	rec := r.recs[0]
	r.recs = r.recs[1:]
	return rec, nil
}

func TestCopyAndReadAll(t *testing.T) {
	src := sampleCapture()
	recs := func() *sliceReader {
		return &sliceReader{h: src.Header, recs: []Record{src.Targets[0], src.Streams[0], src.Samples[0], src.End}}
	}
	var m memWriter
	if err := Copy(&m, recs()); err != nil || !m.closed {
		t.Fatalf("Copy: %v", err)
	}
	m.c.Header = src.Header
	if d := Diff(src, &m.c); d != "" {
		t.Fatalf("Copy: %s", d)
	}
	got, err := ReadAll(recs())
	if err != nil {
		t.Fatal(err)
	}
	if d := Diff(src, got); d != "" {
		t.Fatalf("ReadAll: %s", d)
	}
	if err := Write(&m, End{}); err != nil {
		t.Fatal("Write of an End must be a no-op")
	}
}
