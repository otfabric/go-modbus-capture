// SPDX-License-Identifier: MIT

package mbcsv

import (
	"bytes"
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
	for _, s := range fuzzcap.Seeds(f, "*.mbcap.csv") {
		f.Add(s)
	}
	f.Add([]byte("at,t,version,tick_ns,wall_start\n,header,0.1,1,1970-01-01T00:00:00Z\n5,end,,,\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := fuzzcap.ReadAccepted(codec, data)
		if got != nil {
			fuzzcap.CheckRoundTrip(t, codec, got)
			// The wide view of anything readable can be written.
			if _, err := WriteWide(&bytes.Buffer{}, got); err != nil {
				t.Fatalf("WriteWide: %v", err)
			}
		}
	})
}
