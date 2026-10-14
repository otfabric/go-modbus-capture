// SPDX-License-Identifier: MIT

package format

import (
	"bytes"
	"io"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

var benchCapture = synth.Generate(synth.Config{Seed: 1, Polls: 3600, Mix: synth.Typical, Extras: true})

type benchCodec struct {
	name string
	f    Format
	opts *mbcap.Options
}

var benchCodecs = []benchCodec{
	{"mbcap", Binary, nil},
	{"mbcap-none", Binary, &mbcap.Options{Compression: mbcap.NoCompression}},
	{"jsonl", JSONL, nil},
	{"csv", CSV, nil},
}

func encodeBench(b *testing.B, bc benchCodec, w io.Writer) {
	cw, err := NewWriter(bc.f, w, benchCapture.Header, bc.opts)
	if err != nil {
		b.Fatal(err)
	}
	if err := benchCapture.WriteTo(cw); err != nil {
		b.Fatal(err)
	}
}

// Throughput is in bytes of the encoded file; samples/s is reported too.
func BenchmarkEncode(b *testing.B) {
	for _, bc := range benchCodecs {
		b.Run(bc.name, func(b *testing.B) {
			var buf bytes.Buffer
			encodeBench(b, bc, &buf)
			b.SetBytes(int64(buf.Len()))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				buf.Reset()
				encodeBench(b, bc, &buf)
			}
			b.ReportMetric(float64(len(benchCapture.Samples))*float64(b.N)/b.Elapsed().Seconds(), "samples/s")
		})
	}
}

func BenchmarkDecode(b *testing.B) {
	for _, bc := range benchCodecs {
		b.Run(bc.name, func(b *testing.B) {
			var buf bytes.Buffer
			encodeBench(b, bc, &buf)
			data := buf.Bytes()
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				r, err := NewReader(bc.f, bytes.NewReader(data))
				if err != nil {
					b.Fatal(err)
				}
				if _, err := capture.ReadAll(r); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(benchCapture.Samples))*float64(b.N)/b.Elapsed().Seconds(), "samples/s")
		})
	}
}
