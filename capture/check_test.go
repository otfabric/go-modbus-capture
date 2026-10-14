// SPDX-License-Identifier: MIT

package capture

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validHeader() Header {
	return Header{TickNS: 1000, WallStart: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
}

func TestCheckHeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		mod  func(*Header)
		ok   bool
	}{
		{"valid", func(*Header) {}, true},
		{"tick 0", func(h *Header) { h.TickNS = 0 }, false},
		{"tick above 1 s", func(h *Header) { h.TickNS = MaxTickNS + 1 }, false},
		{"tick 1 s", func(h *Header) { h.TickNS = MaxTickNS }, true},
		{"before 1970", func(h *Header) { h.WallStart = time.Unix(-1, 0) }, false},
		{"invalid UTF-8", func(h *Header) { h.Producer = "\xff" }, false},
		{"carriage return", func(h *Header) { h.Producer = "a\r\nb" }, false},
		{"newline is fine", func(h *Header) { h.Producer = "a\nb" }, true},
		{"empty tag key", func(h *Header) { h.Tags = map[string]string{"": "x"} }, false},
		{"long string", func(h *Header) { h.Producer = strings.Repeat("x", MaxStringLen+1) }, false},
		{"tag bytes", func(h *Header) {
			h.Tags = map[string]string{}
			for i := 0; i < 20; i++ {
				h.Tags[strings.Repeat("k", i+1)] = strings.Repeat("v", 60000)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := validHeader()
			tc.mod(&h)
			err := CheckHeader(&h)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err %v does not wrap ErrCorrupt", err)
			}
		})
	}
}

func TestCheckerRecords(t *testing.T) {
	req := []byte{0x03, 0, 0, 0, 1}
	resp := []byte{0x03, 2, 0, 7}
	type step func(c *Checker) error
	target := func(t Target) step { return func(c *Checker) error { return c.Target(t) } }
	stream := func(s Stream) step { return func(c *Checker) error { return c.Stream(s) } }
	sample := func(s Sample) step { return func(c *Checker) error { return c.Sample(s) } }
	end := func(e End) step { return func(c *Checker) error { return c.End(e) } }
	t1 := target(Target{ID: 1})
	s1 := stream(Stream{ID: 1, Target: 1})
	for _, tc := range []struct {
		name   string
		header func(*Header)
		steps  []step
		ok     bool
	}{
		{"valid", nil, []step{t1, s1, sample(Sample{Stream: 1, At: 5, Request: req, Response: resp}), end(End{Known: true, At: 5})}, true},
		{"target id 0", nil, []step{target(Target{})}, false},
		{"target twice", nil, []step{t1, t1}, false},
		{"unknown transport", nil, []step{target(Target{ID: 1, Transport: 9})}, false},
		{"unknown parity", nil, []step{target(Target{ID: 1, Serial: Serial{Parity: 3}})}, false},
		{"stream of undefined target", nil, []step{stream(Stream{ID: 1, Target: 2})}, false},
		{"stream twice", nil, []step{t1, s1, s1}, false},
		{"sample of undefined stream", nil, []step{t1, sample(Sample{Stream: 1, Request: req, Response: resp})}, false},
		{"empty request", nil, []step{t1, s1, sample(Sample{Stream: 1, Response: resp})}, false},
		{"request too long", nil, []step{t1, s1, sample(Sample{Stream: 1, Request: make([]byte, 254), Response: resp})}, false},
		{"function code 0", nil, []step{t1, s1, sample(Sample{Stream: 1, Request: []byte{0}, Response: []byte{0}})}, false},
		{"a write", nil, []step{t1, s1, sample(Sample{Stream: 1, Request: []byte{0x06, 0, 1, 0, 2}, Response: []byte{0x06, 0, 1, 0, 2}})}, false},
		{"response to another function", nil, []step{t1, s1, sample(Sample{Stream: 1, Request: req, Response: []byte{0x04, 0}})}, false},
		{"exception without the flag", nil, []step{t1, s1, sample(Sample{Stream: 1, Request: req, Response: []byte{0x83, 2}})}, false},
		{"exception with the flag", func(h *Header) { h.Exceptions = true }, []step{t1, s1, sample(Sample{Stream: 1, Request: req, Response: []byte{0x83, 2}})}, true},
		{"repeat in a change-only capture", func(h *Header) { h.ChangeOnly = true }, []step{t1, s1,
			sample(Sample{Stream: 1, At: 1, Request: req, Response: resp}),
			sample(Sample{Stream: 1, At: 2, Request: req, Response: resp})}, false},
		{"change in a change-only capture", func(h *Header) { h.ChangeOnly = true }, []step{t1, s1,
			sample(Sample{Stream: 1, At: 1, Request: req, Response: resp}),
			sample(Sample{Stream: 1, At: 2, Request: req, Response: []byte{0x03, 2, 0, 8}}),
			sample(Sample{Stream: 1, At: 3, Request: req, Response: resp})}, true},
		{"times out of order", nil, []step{t1, s1,
			sample(Sample{Stream: 1, At: 9, Request: req, Response: resp}),
			sample(Sample{Stream: 1, At: 3, Request: req, Response: resp})}, true},
		{"end before a sample", nil, []step{t1, s1, sample(Sample{Stream: 1, At: 9, Request: req, Response: resp}), end(End{Known: true, At: 8})}, false},
		{"record after the end", nil, []step{t1, end(End{}), s1}, false},
		{"unknown end with a time", nil, []step{end(End{At: 1})}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := validHeader()
			if tc.header != nil {
				tc.header(&h)
			}
			c, err := NewChecker(h)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.steps {
				if err = s(c); err != nil {
					break
				}
			}
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// Admit drops what a capture does not store, where Sample rejects it.
func TestAdmit(t *testing.T) {
	h := validHeader()
	h.ChangeOnly = true
	c, _ := NewChecker(h)
	_ = c.Target(Target{ID: 1})
	_ = c.Stream(Stream{ID: 1, Target: 1})
	req, resp := []byte{0x03, 0, 0, 0, 1}, []byte{0x03, 2, 0, 7}
	for i, want := range []bool{true, false, false} {
		keep, err := c.Admit(Sample{Stream: 1, At: Ticks(i), Request: req, Response: resp})
		if err != nil || keep != want {
			t.Fatalf("sample %d: keep=%v err=%v, want keep=%v", i, keep, err, want)
		}
	}
	if keep, err := c.Admit(Sample{Stream: 1, Request: req, Response: []byte{0x83, 1}}); err != nil || keep {
		t.Fatalf("exception without the flag: keep=%v err=%v, want dropped", keep, err)
	}
}

func TestSplitAddresses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		req, resp []byte
		want      []uint16
	}{
		{"coils LSB first", []byte{0x01, 0, 10, 0, 10}, []byte{0x01, 2, 0b1100_1101, 0b01}, []uint16{1, 0, 1, 1, 0, 0, 1, 1, 1, 0}},
		{"registers", []byte{0x03, 0x10, 0, 0, 2}, []byte{0x03, 4, 0, 1, 0x01, 0xa7}, []uint16{1, 0x01a7}},
		{"wrong byte count", []byte{0x03, 0, 0, 0, 2}, []byte{0x03, 2, 0, 1}, nil},
		{"exception", []byte{0x03, 0, 0, 0, 1}, []byte{0x83, 2}, nil},
		{"other function", []byte{0x07}, []byte{0x07, 1}, nil},
		{"range past 65535", []byte{0x04, 0xff, 0xff, 0, 2}, []byte{0x04, 4, 0, 1, 0, 2}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			av, ok := SplitAddresses(&Sample{Request: tc.req, Response: tc.resp})
			if ok != (tc.want != nil) {
				t.Fatalf("ok = %v", ok)
			}
			if ok && (len(av.Values) != len(tc.want) || av.Start != uint16(tc.req[1])<<8|uint16(tc.req[2])) {
				t.Fatalf("got %+v, want values %v", av, tc.want)
			}
			for i := range tc.want {
				if av.Values[i] != tc.want[i] {
					t.Fatalf("value %d = %d, want %d", i, av.Values[i], tc.want[i])
				}
			}
		})
	}
}
