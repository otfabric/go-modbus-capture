// SPDX-License-Identifier: MIT

package mbcsv

import (
	"io"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

// Damaged long CSV (cut, bit flips, inserted and deleted bytes, rows
// dropped, duplicated, reordered and garbled) never makes the reader panic
// or hang, and gives ErrCorrupt or ErrTruncated with the samples before the
// damage intact (see fuzzcap.TextChaos). More seeds: make chaos.
func TestChaos(t *testing.T) {
	fuzzcap.TextChaos(t, fuzzcap.Codec{
		NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
		NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return NewWriter(w, h) },
	})
}
