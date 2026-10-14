// SPDX-License-Identifier: MIT

package mbcap

import (
	"testing"

	"github.com/otfabric/go-modbus-capture/internal/synth"
)

// TestSizeModel logs bytes per sample for the scenarios of docs/SIZE-MODEL.md
// (run with -v). It fails only if the encoding grows far beyond the model.
func TestSizeModel(t *testing.T) {
	for _, sc := range []struct {
		name string
		mix  synth.Mix
		// limit is a generous bound on full+deflate bytes per sample.
		limit float64
	}{
		{"mostly static", synth.MostlyStatic, 10},
		{"typical meter", synth.Typical, 35},
		{"busy device", synth.Busy, 110},
		{"all noisy", synth.AllNoisy, 200},
	} {
		full := synth.Generate(synth.Config{Seed: 1, Polls: 3600, Mix: sc.mix})
		chg := synth.Generate(synth.Config{Seed: 1, Polls: 3600, Mix: sc.mix, ChangeOnly: true})
		n := float64(len(full.Samples))
		raw := float64(len(encode(t, full, &Options{Compression: NoCompression}))) / n
		def := float64(len(encode(t, full, nil))) / n
		craw := float64(len(encode(t, chg, &Options{Compression: NoCompression}))) / n
		cdef := float64(len(encode(t, chg, nil))) / n
		t.Logf("%-14s full %6.1f  full+deflate %6.1f  change-only %6.1f  change-only+deflate %6.1f  (B/sample of %d)",
			sc.name, raw, def, craw, cdef, len(full.Samples))
		if def > sc.limit {
			t.Errorf("%s: %.1f bytes per sample with deflate, the size model allows about %.0f", sc.name, def, sc.limit)
		}
	}
}
