// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"io"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
	"github.com/otfabric/go-modbus-capture/internal/synth"
)

func encode(t testing.TB, c *capture.Capture, opts *Options) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, c.Header, opts)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := c.WriteTo(w); err != nil {
		t.Fatalf("write: %v", err)
	}
	return buf.Bytes()
}

func decode(t testing.TB, b []byte) *capture.Capture {
	t.Helper()
	r, err := NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	c, err := capture.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return c
}

func TestRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		cfg  synth.Config
		opts *Options
	}{
		{"typical", synth.Config{Seed: 1, Polls: 300, Mix: synth.Typical}, nil},
		{"typical/none", synth.Config{Seed: 1, Polls: 300, Mix: synth.Typical}, &Options{Compression: NoCompression}},
		{"static/change-only", synth.Config{Seed: 2, Polls: 300, Mix: synth.MostlyStatic, ChangeOnly: true}, nil},
		{"busy/extras/small-blocks", synth.Config{Seed: 3, Polls: 200, Mix: synth.Busy, Extras: true}, &Options{BlockSamples: 7}},
		{"noisy/extras/byte-blocks", synth.Config{Seed: 4, Polls: 50, Mix: synth.AllNoisy, Extras: true}, &Options{BlockBytes: 300}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := synth.Generate(tc.cfg)
			if err := c.Validate(); err != nil {
				t.Fatalf("synthetic capture is invalid: %v", err)
			}
			got := decode(t, encode(t, c, tc.opts))
			if d := capture.Diff(c, got); d != "" {
				t.Fatalf("round trip differs: %s", d)
			}
		})
	}
}

// What the reader returns is the caller's, and the writer keeps nothing of
// the caller's: with small data blocks, so that responses are coded against
// the ones before them in several blocks, compressed and not.
func TestOwnership(t *testing.T) {
	fuzzcap.CheckOwnership(t, binaryCodec)
	fuzzcap.CheckOwnership(t, fuzzcap.Codec{
		NewReader: binaryCodec.NewReader,
		NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) {
			return NewWriter(w, h, &Options{Compression: NoCompression})
		},
	})
}
