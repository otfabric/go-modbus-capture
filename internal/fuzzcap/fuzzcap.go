// SPDX-License-Identifier: MIT

// Package fuzzcap holds what the fuzz, limit and ownership tests of the
// encodings share: seed inputs, a capture built from fuzz bytes, the
// round-trip property, the ownership property and an allocation measure.
package fuzzcap

import (
	"bytes"
	"io"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// AllocBytes returns the bytes f allocates per call, averaged over runs
// calls. It counts every allocation, live or not (runtime.MemStats
// TotalAlloc), so it measures what an input costs, not what is retained.
func AllocBytes(runs int, f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / uint64(runs)
}

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

// owned is a capture with what ownership matters for: tags on the header and
// on a target, and requests whose responses repeat and change, so that an
// encoding that codes a response against the previous one has a previous one
// to keep.
func owned() *capture.Capture {
	c := &capture.Capture{
		Header: capture.Header{TickNS: 1000, WallStart: time.Unix(1, 0).UTC(), Producer: "p", Tags: map[string]string{"site": "lab", "k": "v"}},
		Targets: []capture.Target{
			{ID: 1, Label: "a", Transport: capture.TransportTCP, Host: "h", Port: 502, Unit: 1, Tags: map[string]string{"bus": "A"}},
			{ID: 2, Transport: capture.TransportRTU, Serial: capture.Serial{Device: "/dev/x", Baud: 9600, DataBits: 8, StopBits: 1}},
		},
		Streams: []capture.Stream{{ID: 1, Target: 1, Role: "poll"}, {ID: 2, Target: 2, Role: "poll", Label: "l"}},
	}
	reqs := [][]byte{{0x03, 0, 0, 0, 2}, {0x04, 0, 9, 0, 1}}
	for i := range 24 {
		req := reqs[i%2]
		resp := []byte{req[0], 4, 0, 1, 0, byte(i / 6)} // the same response three times, then a changed one
		c.Samples = append(c.Samples, capture.Sample{Stream: uint32(i%2) + 1, At: capture.Ticks(10 * i), Request: req, Response: resp})
	}
	c.End = capture.End{Known: true, At: 1000}
	return c
}

func scribble(b []byte) {
	for i := range b {
		b[i] ^= 0xFF
	}
}

func scribbleTags(m map[string]string) {
	for k := range m {
		m[k] = "changed"
	}
	if m != nil {
		m["added"] = "x"
	}
}

// CheckOwnership asserts the ownership rules of capture.Reader and
// capture.Writer for c. A writer keeps nothing of the caller's: the capture
// is written while every PDU and tag map handed to the writer is changed
// right after the call, and reads back as it was. What a reader returns is
// the caller's: the encoded capture is read while the header tags and every
// record returned are changed, and still reads as it was.
func CheckOwnership(t *testing.T, c Codec) {
	t.Helper()
	want := owned()

	src := owned()
	var buf bytes.Buffer
	w, err := c.NewWriter(&buf, src.Header)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	scribbleTags(src.Header.Tags)
	for _, tg := range src.Targets {
		err = firstError(err, w.WriteTarget(tg))
		scribbleTags(tg.Tags)
	}
	for _, s := range src.Streams {
		err = firstError(err, w.WriteStream(s))
	}
	for _, s := range src.Samples {
		s.Request, s.Response = bytes.Clone(s.Request), bytes.Clone(s.Response)
		err = firstError(err, w.WriteSample(s))
		scribble(s.Request)
		scribble(s.Response)
	}
	if err = firstError(err, w.Close(src.End)); err != nil {
		t.Fatalf("writing: %v", err)
	}
	data := buf.Bytes()
	if d := diffRead(c, want, data, false); d != "" {
		t.Fatalf("a writer kept something of the caller's: after the caller changed what it had passed, the capture reads back as: %s", d)
	}
	if d := diffRead(c, want, data, true); d != "" {
		t.Fatalf("a reader shares what it returns: after the caller changed what it got, the capture reads as: %s", d)
	}
}

func firstError(err, next error) error {
	if err != nil {
		return err
	}
	return next
}

// diffRead reads data with c and describes how it differs from want. With
// change it changes the header tags and every record right after the reader
// returned it, having kept a copy; the header is read again at the end.
func diffRead(c Codec, want *capture.Capture, data []byte, change bool) string {
	r, err := c.NewReader(bytes.NewReader(data))
	if err != nil {
		return err.Error()
	}
	got := &capture.Capture{}
	if change {
		scribbleTags(r.Header().Tags)
	}
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err.Error()
		}
		switch v := rec.(type) {
		case capture.Target:
			kept := v
			kept.Tags = maps.Clone(v.Tags)
			got.Targets = append(got.Targets, kept)
			if change {
				scribbleTags(v.Tags)
			}
		case capture.Stream:
			got.Streams = append(got.Streams, v)
		case capture.Sample:
			got.Samples = append(got.Samples, capture.Sample{Stream: v.Stream, At: v.At, Request: bytes.Clone(v.Request), Response: bytes.Clone(v.Response)})
			if change {
				scribble(v.Request)
				scribble(v.Response)
			}
		case capture.End:
			got.End = v
		}
	}
	got.Header = r.Header()
	return capture.Diff(want, got)
}

// Build makes a valid capture from arbitrary bytes: header flags, a few
// targets and streams, and samples with PDUs, times (sometimes out of order,
// sometimes about 2^63 ticks from the sample before) and repeats taken from
// data.
// It returns the capture as a writer stores it: samples a change-only capture
// or one without exceptions does not keep are left out.
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
			if !capture.IsReadFunction(fc) || fc == 0x08 || fc == 0x2B {
				fc = 0x03 // FC08 and FC43 need a valid sub-function or MEI type
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
		step := int8(next())
		dt := int64(step) * 37
		if step == -128 || dt < 0 && capture.Ticks(-dt) > at || dt > 0 && capture.Ticks(dt) > ^capture.Ticks(0)-at {
			dt = 0
		}
		at = capture.Ticks(int64(at) + dt)
		if step == -128 || step == -127 || step == 127 {
			// The largest steps also move the time to the other half of its
			// range: by 2^63 ticks, or 127*37 ticks more or less, forward
			// and back. A time delta holds less than 2^63 forward.
			at ^= 1 << 63
		}
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
