// SPDX-License-Identifier: MIT

package format

import (
	"bytes"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/synth"
)

func encode(t *testing.T, f Format, c *capture.Capture) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(f, &buf, c.Header, nil)
	if err != nil {
		t.Fatalf("%v: NewWriter: %v", f, err)
	}
	if err := c.WriteTo(w); err != nil {
		t.Fatalf("%v: write: %v", f, err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, want Format, b []byte) *capture.Capture {
	t.Helper()
	r, f, err := Open(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if f != want {
		t.Fatalf("detected %v, want %v", f, want)
	}
	c, err := capture.ReadAll(r)
	if err != nil {
		t.Fatalf("%v: read: %v", f, err)
	}
	return c
}

// Every encoding converts to every other without loss, in chains.
func TestRoundTripAcrossEncodings(t *testing.T) {
	inputs := map[string]*capture.Capture{
		"typical":            synth.Generate(synth.Config{Seed: 1, Polls: 60, Mix: synth.Typical}),
		"extras":             synth.Generate(synth.Config{Seed: 2, Polls: 40, Mix: synth.Busy, Extras: true}),
		"change-only/extras": synth.Generate(synth.Config{Seed: 3, Polls: 40, Mix: synth.MostlyStatic, ChangeOnly: true, Extras: true}),
	}
	unknownEnd := synth.Generate(synth.Config{Seed: 4, Polls: 5, Mix: synth.Typical})
	unknownEnd.End = capture.End{}
	inputs["unknown end"] = unknownEnd
	chains := [][]Format{
		{Binary, JSONL, CSV, Binary},
		{JSONL, CSV, JSONL},
		{CSV, Binary, CSV},
	}
	for name, c := range inputs {
		for _, chain := range chains {
			t.Run(name+"/"+chainName(chain), func(t *testing.T) {
				cur := c
				for i, f := range chain {
					got := decode(t, f, encode(t, f, cur))
					if d := capture.Diff(c, got); d != "" {
						t.Fatalf("after %v: %s", chainName(chain[:i+1]), d)
					}
					cur = got
				}
			})
		}
	}
}

// Encoding is deterministic: the same capture gives the same bytes.
func TestDeterministic(t *testing.T) {
	c := synth.Generate(synth.Config{Seed: 5, Polls: 30, Mix: synth.Busy, Extras: true})
	for _, f := range []Format{Binary, JSONL, CSV} {
		a, b := encode(t, f, c), encode(t, f, c)
		if !bytes.Equal(a, b) {
			t.Errorf("%v: two encodings of one capture differ", f)
		}
		// Decoding and encoding again gives the same bytes too.
		if again := encode(t, f, decode(t, f, a)); !bytes.Equal(a, again) {
			t.Errorf("%v: decode and encode does not reproduce the bytes", f)
		}
	}
}

func chainName(fs []Format) string {
	s := ""
	for i, f := range fs {
		if i > 0 {
			s += ">"
		}
		s += f.String()
	}
	return s
}
