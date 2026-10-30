// SPDX-License-Identifier: MIT

package capture

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestDurationEdges(t *testing.T) {
	if got := (Header{}).Duration(5); got != 0 {
		t.Fatalf("tick 0: %v", got)
	}
	h := Header{TickNS: 1000}
	if got := h.Duration(^Ticks(0)); got != time.Duration(1<<63-1) {
		t.Fatalf("no saturation: %v", got)
	}
	if got := h.Ticks(-time.Second); got != 0 {
		t.Fatalf("negative duration: %d", got)
	}
}

func TestParseParity(t *testing.T) {
	for in, want := range map[string]Parity{"": ParityNone, "none": ParityNone, "even": ParityEven, "odd": ParityOdd} {
		if got, err := ParseParity(in); err != nil || got != want {
			t.Fatalf("ParseParity(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseParity("mark"); err == nil {
		t.Fatal("unknown parity accepted")
	}
}

func TestSerialIsZero(t *testing.T) {
	if !(Serial{}).IsZero() || (Serial{Baud: 9600}).IsZero() {
		t.Fatal("IsZero")
	}
}

// Every record type is a Record, and Write sends each to its method.
func TestWriteDispatch(t *testing.T) {
	m := &memWriter{}
	for _, rec := range []Record{Target{ID: 1}, Stream{ID: 2}, Sample{Stream: 2}, End{Known: true}} {
		if err := Write(m, rec); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.c.Targets) != 1 || len(m.c.Streams) != 1 || len(m.c.Samples) != 1 {
		t.Fatalf("written %+v", m.c)
	}
	if m.closed {
		t.Fatal("Write must not close on an End")
	}
	if err := Write(m, nil); err == nil {
		t.Fatal("a nil record must fail")
	}
}

type failingWriter struct {
	memWriter
	target, stream, sample, end error
}

func (f *failingWriter) WriteTarget(Target) error { return f.target }
func (f *failingWriter) WriteStream(Stream) error { return f.stream }
func (f *failingWriter) WriteSample(Sample) error { return f.sample }
func (f *failingWriter) Close(End) error          { return f.end }

func TestWriteToStopsAtTheFirstError(t *testing.T) {
	boom := errors.New("boom")
	c := sampleCapture()
	for name, w := range map[string]*failingWriter{
		"target": {target: boom}, "stream": {stream: boom}, "sample": {sample: boom}, "close": {end: boom},
	} {
		if err := c.WriteTo(w); !errors.Is(err, boom) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	m := &memWriter{}
	if err := c.WriteTo(m); err != nil {
		t.Fatal(err)
	}
	if !m.closed || !Equal(c, &Capture{Header: c.Header, Targets: m.c.Targets, Streams: m.c.Streams, Samples: m.c.Samples, End: m.c.End}) {
		t.Fatalf("written %+v", m.c)
	}
}

type errReader struct {
	head Header
	recs []Record
	err  error
}

func (r *errReader) Header() Header { return r.head }
func (r *errReader) Next() (Record, error) {
	if len(r.recs) == 0 {
		return nil, r.err
	}
	rec := r.recs[0]
	r.recs = r.recs[1:]
	return rec, nil
}

func TestReadAllKeepsRecordsBeforeAnError(t *testing.T) {
	c, err := ReadAll(&errReader{recs: []Record{Target{ID: 1}}, err: ErrTruncated})
	if !errors.Is(err, ErrTruncated) || c == nil || len(c.Targets) != 1 {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestCopyErrors(t *testing.T) {
	boom := errors.New("boom")
	if err := Copy(&memWriter{}, &errReader{err: boom}); !errors.Is(err, boom) {
		t.Fatalf("read error: %v", err)
	}
	src := &errReader{recs: []Record{Target{ID: 1}}, err: io.EOF}
	w := &failingWriter{target: boom}
	if err := Copy(w, src); !errors.Is(err, boom) {
		t.Fatalf("write error: %v", err)
	}
	if err := Copy(&failingWriter{end: boom}, &errReader{err: io.EOF}); !errors.Is(err, boom) {
		t.Fatalf("close error: %v", err)
	}
}

func TestValidate(t *testing.T) {
	good := func() *Capture { return sampleCapture() }
	if err := good().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Capture)
		want   string
	}{
		{"header", func(c *Capture) { c.Header.TickNS = 0 }, "tick size"},
		{"target", func(c *Capture) { c.Targets[0].ID = 0 }, "target id 0"},
		{"stream", func(c *Capture) { c.Streams[0].ID = 0 }, "stream id 0"},
		{"sample", func(c *Capture) { c.Samples[0].Stream = 99 }, "sample 0"},
		{"end", func(c *Capture) { c.End = End{At: 1} }, "unknown capture end"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := good()
			tt.mutate(c)
			err := c.Validate()
			if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCheckHeaderLimits(t *testing.T) {
	ok := Header{TickNS: 1, WallStart: time.Unix(1, 0)}
	if err := CheckHeader(&ok); err != nil {
		t.Fatal(err)
	}
	many := map[string]string{}
	for i := 0; i <= MaxTags; i++ {
		many[fmt.Sprintf("k%d", i)] = ""
	}
	tests := []struct {
		name   string
		mutate func(*Header)
		want   string
	}{
		{"tick too large", func(h *Header) { h.TickNS = MaxTickNS + 1 }, "tick size"},
		{"before 1970", func(h *Header) { h.WallStart = time.Unix(-1, 0) }, "outside"},
		{"producer too long", func(h *Header) { h.Producer = strings.Repeat("x", MaxStringLen+1) }, "producer"},
		{"producer not UTF-8", func(h *Header) { h.Producer = "\xff" }, "UTF-8"},
		{"producer with CR", func(h *Header) { h.Producer = "a\rb" }, "carriage return"},
		{"too many tags", func(h *Header) { h.Tags = many }, "tags, more than"},
		{"tags too large", func(h *Header) { h.Tags = map[string]string{"k": strings.Repeat("v", MaxTagBytes)} }, "tags are more"},
		{"empty tag key", func(h *Header) { h.Tags = map[string]string{"": "v"} }, "empty key"},
		{"bad tag key", func(h *Header) { h.Tags = map[string]string{"\xff": "v"} }, "tag key"},
		{"bad tag value", func(h *Header) { h.Tags = map[string]string{"k": "\xff"} }, "tag k"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := ok
			tt.mutate(&h)
			if err := CheckHeader(&h); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
	if _, err := NewChecker(Header{}); err == nil {
		t.Fatal("NewChecker took tick 0")
	}
}

func checker(t *testing.T, h Header) *Checker {
	t.Helper()
	h.TickNS = 1000
	h.WallStart = time.Unix(0, 0)
	c, err := NewChecker(h)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestCheckerTargetAndStream(t *testing.T) {
	c := checker(t, Header{})
	if err := c.Target(Target{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Stream(Stream{ID: 1, Target: 1}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", MaxStringLen+1)
	targets := []struct {
		t    Target
		want string
	}{
		{Target{ID: 0}, "target id 0"},
		{Target{ID: 2, Transport: 99}, "unknown transport"},
		{Target{ID: 2, Serial: Serial{Parity: 9}}, "unknown parity"},
		{Target{ID: 1}, "defined twice"},
		{Target{ID: 2, Label: long}, "label"},
		{Target{ID: 2, Host: long}, "host"},
		{Target{ID: 2, Serial: Serial{Device: long}}, "serial device"},
		{Target{ID: 2, Tags: map[string]string{"": ""}}, "empty key"},
	}
	for _, tt := range targets {
		if err := c.Target(tt.t); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%+v: %v, want %q", tt.t, err, tt.want)
		}
	}
	streams := []struct {
		s    Stream
		want string
	}{
		{Stream{ID: 0, Target: 1}, "stream id 0"},
		{Stream{ID: 1, Target: 1}, "defined twice"},
		{Stream{ID: 2, Target: 9}, "undefined target"},
		{Stream{ID: 2, Target: 1, Role: long}, "role"},
		{Stream{ID: 2, Target: 1, Label: long}, "label"},
	}
	for _, tt := range streams {
		if err := c.Stream(tt.s); !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("%+v: %v, want %q", tt.s, err, tt.want)
		}
	}
}

func TestCheckerLimits(t *testing.T) {
	c := checker(t, Header{})
	for i := 1; i <= MaxTargets; i++ {
		if err := c.Target(Target{ID: uint32(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Target(Target{ID: MaxTargets + 1}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("err %v", err)
	}
	for i := 1; i <= MaxStreams; i++ {
		if err := c.Stream(Stream{ID: uint32(i), Target: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Stream(Stream{ID: MaxStreams + 1, Target: 1}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("err %v", err)
	}
}

func TestCheckPDUs(t *testing.T) {
	tests := []struct {
		name      string
		req, resp []byte
		want      string
	}{
		{"valid", []byte{3, 0, 0, 0, 1}, []byte{3, 2, 0, 7}, ""},
		{"exception", []byte{3, 0, 0, 0, 1}, []byte{0x83, 2}, ""},
		{"empty request", nil, []byte{3}, "request PDU"},
		{"long request", make([]byte, MaxPDULen+1), []byte{3}, "request PDU"},
		{"empty response", []byte{3}, nil, "response PDU"},
		{"long response", []byte{3}, make([]byte, MaxPDULen+1), "response PDU"},
		{"write", []byte{6, 0, 0, 0, 1}, []byte{6}, "is a write"},
		{"not a read", []byte{0x41}, []byte{0x41}, "not a read function"},
		{"FC08 too short", []byte{8, 0}, []byte{8}, "FC08"},
		{"FC43 other MEI", []byte{0x2B, 0x0D}, []byte{0x2B}, "FC43"},
		{"FC43 too short", []byte{0x2B}, []byte{0x2B}, "FC43"},
		{"wrong function", []byte{3}, []byte{4}, "does not answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPDUs(tt.req, tt.resp)
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCheckerSamples(t *testing.T) {
	req, resp := []byte{3, 0, 0, 0, 1}, []byte{3, 2, 0, 7}
	exc := []byte{0x83, 2}
	setup := func(t *testing.T, h Header) *Checker {
		c := checker(t, h)
		if err := c.Target(Target{ID: 1}); err != nil {
			t.Fatal(err)
		}
		if err := c.Stream(Stream{ID: 1, Target: 1}); err != nil {
			t.Fatal(err)
		}
		return c
	}

	c := setup(t, Header{})
	if err := c.Sample(Sample{Stream: 2, Request: req, Response: resp}); err == nil || !strings.Contains(err.Error(), "undefined stream") {
		t.Fatalf("err %v", err)
	}
	if err := c.Sample(Sample{Stream: 1, Request: req, Response: exc}); err == nil || !strings.Contains(err.Error(), "does not record exceptions") {
		t.Fatalf("err %v", err)
	}
	if keep, err := c.Admit(Sample{Stream: 1, Request: req, Response: exc}); keep || err != nil {
		t.Fatalf("Admit exception: %v, %v", keep, err)
	}
	if err := c.Sample(Sample{Stream: 1, At: 9, Request: req, Response: resp}); err != nil {
		t.Fatal(err)
	}
	if err := c.Sample(Sample{Stream: 1, At: 4, Request: req, Response: resp}); err != nil {
		t.Fatalf("out-of-order samples are allowed: %v", err)
	}
	if c.MaxAt() != 9 {
		t.Fatalf("MaxAt %d", c.MaxAt())
	}

	co := setup(t, Header{ChangeOnly: true})
	if keep, err := co.Admit(Sample{Stream: 1, Request: req, Response: resp}); !keep || err != nil {
		t.Fatalf("first: %v, %v", keep, err)
	}
	if err := co.Sample(Sample{Stream: 1, Request: req, Response: resp}); err == nil || !strings.Contains(err.Error(), "change-only") {
		t.Fatalf("err %v", err)
	}
	if keep, err := co.Admit(Sample{Stream: 1, Request: req, Response: []byte{3, 2, 0, 8}}); !keep || err != nil {
		t.Fatalf("changed: %v, %v", keep, err)
	}
}

func TestCheckerEnd(t *testing.T) {
	c := checker(t, Header{})
	if err := c.End(End{}); err != nil {
		t.Fatal(err)
	}
	if err := c.Target(Target{ID: 1}); err == nil || !strings.Contains(err.Error(), "after the capture end") {
		t.Fatalf("target: %v", err)
	}
	if err := c.Stream(Stream{ID: 1}); err == nil {
		t.Fatal("stream after the end")
	}
	if _, err := c.Admit(Sample{}); err == nil {
		t.Fatal("sample after the end")
	}
	if err := c.End(End{}); err == nil {
		t.Fatal("a second end")
	}
}

func TestSplitAddressesEdges(t *testing.T) {
	tests := []struct {
		name      string
		req, resp []byte
		want      []uint16
	}{
		{"coils, LSB first", []byte{1, 0, 16, 0, 10}, []byte{1, 2, 0b00000101, 0b00000010}, []uint16{1, 0, 1, 0, 0, 0, 0, 0, 0, 1}},
		{"inputs", []byte{2, 0, 0, 0, 2}, []byte{2, 1, 0b10}, []uint16{0, 1}},
		{"holding", []byte{3, 0, 1, 0, 2}, []byte{3, 4, 0x12, 0x34, 0xAB, 0xCD}, []uint16{0x1234, 0xABCD}},
		{"input registers", []byte{4, 0, 0, 0, 1}, []byte{4, 2, 0, 1}, []uint16{1}},
		{"last address", []byte{3, 0xFF, 0xFF, 0, 1}, []byte{3, 2, 0, 1}, []uint16{1}},
		{"wrong request length", []byte{3, 0, 0}, []byte{3, 2, 0, 1}, nil},
		{"short response", []byte{3, 0, 0, 0, 1}, []byte{3}, nil},
		{"exception", []byte{3, 0, 0, 0, 1}, []byte{0x83, 2}, nil},
		{"zero quantity", []byte{3, 0, 0, 0, 0}, []byte{3, 0}, nil},
		{"address wrap", []byte{3, 0xFF, 0xFF, 0, 2}, []byte{3, 4, 0, 0, 0, 0}, nil},
		{"byte count mismatch", []byte{3, 0, 0, 0, 1}, []byte{3, 3, 0, 1}, nil},
		{"register size mismatch", []byte{3, 0, 0, 0, 2}, []byte{3, 2, 0, 1}, nil},
		{"coil size mismatch", []byte{1, 0, 0, 0, 9}, []byte{1, 1, 0}, nil},
		{"other function", []byte{7, 0, 0, 0, 1}, []byte{7, 1, 0}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SplitAddresses(&Sample{Request: tt.req, Response: tt.resp})
			if ok != (tt.want != nil) {
				t.Fatalf("ok %v, %+v", ok, got)
			}
			if !ok {
				return
			}
			if got.Function != tt.req[0] || got.Start != uint16(tt.req[1])<<8|uint16(tt.req[2]) || fmt.Sprint(got.Values) != fmt.Sprint(tt.want) {
				t.Fatalf("got %+v, want %v", got, tt.want)
			}
		})
	}
}
