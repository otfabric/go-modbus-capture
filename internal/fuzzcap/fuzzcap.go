// SPDX-License-Identifier: MIT

// Package fuzzcap holds what the fuzz tests of the encodings share: seed
// inputs, a capture built from fuzz bytes, and the round-trip property.
package fuzzcap

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Seeds returns the golden files of testdata matching pattern.
func Seeds(t testing.TB, pattern string) [][]byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "testdata", pattern))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

// Codec is one encoding under test.
type Codec struct {
	NewReader func(io.Reader) (capture.Reader, error)
	NewWriter func(io.Writer, capture.Header) (capture.Writer, error)
}

// ReadAccepted reads data with c. It returns nil when the reader rejects the
// input, which is fine for hostile input; what it must not do is panic.
func ReadAccepted(c Codec, data []byte) *capture.Capture {
	r, err := c.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	got, err := capture.ReadAll(r)
	if err != nil {
		return nil
	}
	return got
}

// CheckRoundTrip asserts that a capture a reader accepted is valid, and that
// encoding it again with c gives the same capture.
func CheckRoundTrip(t *testing.T, c Codec, got *capture.Capture) {
	t.Helper()
	if err := got.Validate(); err != nil {
		t.Fatalf("the reader accepted an invalid capture: %v", err)
	}
	var buf bytes.Buffer
	w, err := c.NewWriter(&buf, got.Header)
	if err != nil {
		t.Fatalf("NewWriter on an accepted header: %v", err)
	}
	if err := got.WriteTo(w); err != nil {
		t.Fatalf("writing an accepted capture: %v", err)
	}
	again := ReadAccepted(c, buf.Bytes())
	if again == nil {
		t.Fatalf("the re-encoded capture does not read back")
	}
	if d := capture.Diff(got, again); d != "" {
		t.Fatalf("re-encoding changed the capture: %s", d)
	}
}

// Build makes a valid capture from arbitrary bytes: header flags, a few
// targets and streams, and samples with PDUs, times (sometimes out of order)
// and repeats taken from data. It returns the capture as a writer stores it:
// samples a change-only capture or one without exceptions does not keep are
// left out.
func Build(data []byte) *capture.Capture {
	next := func() byte {
		if len(data) == 0 {
			return 0
		}
		b := data[0]
		data = data[1:]
		return b
	}
	flags := next()
	h := capture.Header{
		TickNS:     uint64(next())%5*250 + 1,
		WallStart:  time.Unix(int64(next())<<20, int64(next())).UTC(),
		ChangeOnly: flags&1 != 0,
		Exceptions: flags&2 != 0,
		Producer:   string([]byte{'p', 'a' + next()%26}),
	}
	if flags&4 != 0 {
		h.Tags = map[string]string{"k": string(rune('a' + next()%26)), "unicode": "ünï\t\n\"<&>"}
	}
	c := &capture.Capture{Header: h}
	nt := int(next()%3) + 1
	for i := 1; i <= nt; i++ {
		c.Targets = append(c.Targets, capture.Target{ID: uint32(i * 7), Label: "t,\"x\"\n", Transport: capture.Transport(next() % 7),
			Host: "h", Port: uint16(next()) << 4, Unit: next(),
			Serial: capture.Serial{Device: "/dev/x", Baud: uint32(next()) * 100, Parity: capture.Parity(next() % 3), DataBits: 8, StopBits: 1}})
	}
	ns := int(next()%4) + 1
	for i := 1; i <= ns; i++ {
		c.Streams = append(c.Streams, capture.Stream{ID: uint32(i), Target: c.Targets[i%nt].ID, Role: "poll"})
	}
	ck, err := capture.NewChecker(h)
	if err != nil {
		panic("fuzzcap: Build made an invalid header: " + err.Error())
	}
	for _, t := range c.Targets {
		_ = ck.Target(t)
	}
	for _, s := range c.Streams {
		_ = ck.Stream(s)
	}
	var at capture.Ticks = 1000
	var prev capture.Sample
	for len(data) > 0 {
		op := next()
		var s capture.Sample
		if op&0x80 != 0 && prev.Request != nil {
			s = prev // a repeat: SAME, or dropped in a change-only capture
			s.Response = append([]byte(nil), prev.Response...)
			if op&0x40 != 0 && len(s.Response) > 1 {
				s.Response[len(s.Response)-1] ^= next() | 1 // a DIFF
			}
		} else {
			fc := next()%0x7F + 1
			if capture.IsWriteFunction(fc) {
				fc = 0x03
			}
			req := []byte{fc}
			for n := next() % 8; n > 0; n-- {
				req = append(req, next())
			}
			resp := []byte{fc}
			if op&0x20 != 0 {
				resp = []byte{fc | 0x80, next()}
			} else {
				for n := next() % 12; n > 0; n-- {
					resp = append(resp, next())
				}
			}
			s = capture.Sample{Stream: uint32(op%byte(ns)) + 1, Request: req, Response: resp}
		}
		dt := int64(int8(next())) * 37
		if dt < 0 && capture.Ticks(-dt) > at {
			dt = 0
		}
		at = capture.Ticks(int64(at) + dt)
		s.At = at
		prev = s
		if keep, err := ck.Admit(s); err == nil && keep {
			c.Samples = append(c.Samples, s)
		}
	}
	c.End = capture.End{Known: flags&8 == 0, At: ck.MaxAt() + capture.Ticks(next())}
	if !c.End.Known {
		c.End.At = 0
	}
	return c
}
