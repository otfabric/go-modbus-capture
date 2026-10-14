// SPDX-License-Identifier: MIT

package jsonl

import (
	"io"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

var codec = fuzzcap.Codec{
	NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
	NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return NewWriter(w, h) },
}

func FuzzReader(f *testing.F) {
	for _, s := range fuzzcap.Seeds(f, "*.mbcap.jsonl") {
		f.Add(s)
	}
	f.Add([]byte(`{"t":"header","version":"0.1","tick_ns":1,"wall_start":"1970-01-01T00:00:00Z"}` + "\n" + `{"t":"x","id":"not a number"}` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if got := fuzzcap.ReadAccepted(codec, data); got != nil {
			fuzzcap.CheckRoundTrip(t, codec, got)
		}
	})
}
