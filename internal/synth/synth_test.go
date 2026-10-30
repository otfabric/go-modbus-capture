// SPDX-License-Identifier: MIT

package synth

import (
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
)

func TestGenerateIsValidAndDeterministic(t *testing.T) {
	for name, cfg := range map[string]Config{
		"typical":     {Seed: 1, Polls: 20, Mix: Typical},
		"static":      {Seed: 2, Polls: 20, Mix: MostlyStatic, ChangeOnly: true},
		"busy extras": {Seed: 3, Polls: 25, Mix: Busy, Extras: true},
		"noisy extras change-only": {Seed: 4, Polls: 25, Mix: AllNoisy, Extras: true, ChangeOnly: true,
			Blocks: []Block{{0, 4}}},
	} {
		t.Run(name, func(t *testing.T) {
			c := Generate(cfg)
			if err := c.Validate(); err != nil {
				t.Fatal(err)
			}
			if d := capture.Diff(c, Generate(cfg)); d != "" {
				t.Fatalf("not deterministic: %s", d)
			}
			if !c.End.Known || len(c.Samples) == 0 {
				t.Fatalf("end %+v, %d samples", c.End, len(c.Samples))
			}
		})
	}
}

func TestGenerateDefaultBlocks(t *testing.T) {
	c := Generate(Config{Seed: 1, Polls: 3, Mix: Typical})
	if want := 3 * len(DefaultBlocks); len(c.Samples) != want {
		t.Fatalf("%d samples, want %d", len(c.Samples), want)
	}
	if len(c.Targets) != 1 || len(c.Streams) != 1 {
		t.Fatalf("%d targets, %d streams", len(c.Targets), len(c.Streams))
	}
}

func TestChangeOnlyDropsUnchangedResponses(t *testing.T) {
	cfg := Config{Seed: 1, Polls: 50, Mix: Mix{Static: 1}}
	all := Generate(cfg)
	cfg.ChangeOnly = true
	changed := Generate(cfg)
	// A static register map: one sample per block, whatever the poll count.
	if len(all.Samples) != 50*len(DefaultBlocks) || len(changed.Samples) != len(DefaultBlocks) {
		t.Fatalf("%d samples, %d change-only", len(all.Samples), len(changed.Samples))
	}
}

func TestExtrasCoverEveryReadFunction(t *testing.T) {
	c := Generate(Config{Seed: 1, Polls: 11, Mix: Typical, Extras: true})
	seen := map[uint8]bool{}
	exceptions := 0
	for i := range c.Samples {
		s := &c.Samples[i]
		seen[s.Function()] = true
		if s.IsException() {
			exceptions++
		}
	}
	for _, fc := range []uint8{0x01, 0x02, 0x03, 0x04, 0x07, 0x08, 0x0B, 0x0C, 0x11, 0x14, 0x18, 0x2B} {
		if !seen[fc] {
			t.Errorf("function 0x%02x missing", fc)
		}
	}
	if exceptions != 3 { // every fifth round of 11
		t.Errorf("%d exceptions, want 3", exceptions)
	}
	if len(c.Targets) != 2 || len(c.Streams) != 3 {
		t.Errorf("%d targets, %d streams", len(c.Targets), len(c.Streams))
	}
}
