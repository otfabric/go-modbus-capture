// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"io"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/jsonl"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

var binaryCodec = fuzzcap.Codec{
	NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
	NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) {
		return NewWriter(w, h, &Options{BlockSamples: 5})
	},
}

// The reader never panics on hostile input and does not allocate without
// bound; what it accepts is a valid capture that re-encodes to itself.
func FuzzReader(f *testing.F) {
	for _, s := range fuzzcap.Seeds(f, "*.mbcap") {
		f.Add(s)
	}
	f.Add(encode(f, synth.Generate(synth.Config{Seed: 1, Polls: 4, Mix: synth.Busy, Extras: true}), nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := fuzzcap.ReadAccepted(binaryCodec, data)
		if got != nil {
			fuzzcap.CheckRoundTrip(t, binaryCodec, got)
		}
		// Repair never fails on a readable header, and writes a readable file.
		var out bytes.Buffer
		if _, err := Repair(&out, bytes.NewReader(data), nil); err == nil {
			if fuzzcap.ReadAccepted(binaryCodec, out.Bytes()) == nil {
				t.Fatalf("Repair wrote a file that does not read")
			}
		}
		_, _ = ReadIndex(bytes.NewReader(data), int64(len(data)))
	})
}

// Any valid capture survives binary, JSON Lines and long CSV unchanged.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{0x03, 1, 2, 3, 4, 2, 2, 0x10, 0x03, 4, 0, 0, 0, 1, 3, 2, 0, 5, 0x90, 0x05, 0xc0, 0x11, 0x22})
	f.Add([]byte{0x0f, 0, 0, 0, 0, 0, 1, 0xa1, 0x2b, 3, 0x0e, 0x01, 0x00, 0x05})
	f.Fuzz(func(t *testing.T, data []byte) {
		c := fuzzcap.Build(data)
		if err := c.Validate(); err != nil {
			t.Fatalf("Build made an invalid capture: %v", err)
		}
		for _, codec := range []fuzzcap.Codec{
			binaryCodec,
			{NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
				NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) {
					return NewWriter(w, h, &Options{Compression: NoCompression, BlockBytes: 40})
				}},
			{NewReader: func(r io.Reader) (capture.Reader, error) { return jsonl.NewReader(r) },
				NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return jsonl.NewWriter(w, h) }},
			{NewReader: func(r io.Reader) (capture.Reader, error) { return mbcsv.NewReader(r) },
				NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return mbcsv.NewWriter(w, h) }},
		} {
			var buf bytes.Buffer
			w, err := codec.NewWriter(&buf, c.Header)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.WriteTo(w); err != nil {
				t.Fatal(err)
			}
			got := fuzzcap.ReadAccepted(codec, buf.Bytes())
			if got == nil {
				t.Fatalf("an encoded valid capture does not read back")
			}
			if d := capture.Diff(c, got); d != "" {
				t.Fatalf("round trip changed the capture: %s", d)
			}
		}
	})
}
