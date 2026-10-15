// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

// Chaos test: valid captures, encoded with random options, then damaged at
// random (bits flipped, bytes overwritten, inserted or deleted, the file cut
// at random offsets and at every block boundary, whole blocks dropped,
// duplicated, swapped or moved). Whatever the damage, the reader neither
// panics nor hangs, every error wraps ErrCorrupt or ErrTruncated, the
// samples before the damage come out intact, nothing it returns is made up,
// and Repair writes a file that verifies and holds only original samples.
// More seeds: MBCAP_CHAOS_SEEDS=500 go test -run Chaos ./mbcap (make chaos).
func TestChaos(t *testing.T) {
	for seed := range fuzzcap.ChaosSeeds(t) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(int64(seed)))
			c := fuzzcap.ChaosCapture(int64(seed))
			opts := randomOptions(rng)
			data := encode(t, c, opts)
			if d := capture.Diff(c, decode(t, data)); d != "" {
				t.Fatalf("the undamaged file does not read back: %s", d)
			}
			blocks := splitBlocks(t, data)

			var damages []fuzzcap.Damage
			off := 0
			for _, b := range blocks {
				off += len(b)
				for _, n := range []int{off - 1, off, off + 1} {
					if n >= 0 && n < len(data) {
						damages = append(damages, fuzzcap.Truncation(data, n))
					}
				}
			}
			damages = append(damages, fuzzcap.ByteDamage(rng, data, 120)...)
			for _, d := range damages {
				checkDamage(t, rng, c, data, d, true)
			}
			for _, d := range fuzzcap.PartDamage(rng, data, blocks, 40, "block") {
				checkDamage(t, rng, c, data, d, false)
			}
		})
	}
}

func randomOptions(rng *rand.Rand) *Options {
	o := &Options{BlockSamples: 1 + rng.Intn(40)}
	if rng.Intn(2) == 0 {
		o.Compression = NoCompression
	}
	if rng.Intn(4) == 0 {
		o.BlockBytes = 32 + rng.Intn(1000)
	}
	return o
}

// splitBlocks splits an MBCAP file into its magic number and its blocks.
func splitBlocks(t *testing.T, data []byte) [][]byte {
	t.Helper()
	parts := [][]byte{data[:len(Magic)]}
	for b := data[len(Magic):]; len(b) > 0; {
		length, n := binary.Uvarint(b[1:])
		if n <= 0 || uint64(len(b)) < 1+uint64(n)+length+4 {
			t.Fatalf("cannot split the file into blocks")
		}
		size := 1 + n + int(length) + 4
		parts = append(parts, b[:size])
		b = b[size:]
	}
	return parts
}

// checkDamage reads one damaged file strictly, with SkipCorrupt, with
// ReadIndex, and repairs it. inPlace is true for damage that changes bytes
// where they are; false for damage that moves intact blocks.
func checkDamage(t *testing.T, rng *rand.Rand, orig *capture.Capture, data []byte, d fuzzcap.Damage, inPlace bool) {
	t.Helper()
	strict := fuzzcap.ReadDamaged(t, binaryCodec, d.Name, d.Data)
	fuzzcap.CheckIntact(t, binaryCodec, data, d, strict)
	got := fuzzcap.Samples(strict.Got)
	if inPlace {
		// Every block is CRC-checked: damaged bytes never come out as
		// samples, so what the reader returns is a prefix of the original,
		// and the damage is always reported.
		if diff := fuzzcap.PrefixDiff(orig.Samples, got); diff != "" {
			t.Fatalf("%s: %s", d.Name, diff)
		}
		if strict.Err == nil {
			t.Fatalf("%s: read without error", d.Name)
		}
	} else if diff := fuzzcap.SubsetDiff(orig.Samples, got); diff != "" {
		t.Fatalf("%s: %s", d.Name, diff)
	}
	if strings.HasPrefix(d.Name, "truncated") && !errors.Is(strict.Err, capture.ErrTruncated) {
		t.Fatalf("%s: error %v, want ErrTruncated", d.Name, strict.Err)
	}

	// SkipCorrupt reads on past damaged blocks; what it returns is still
	// original.
	var skip *capture.Capture
	var skipErr error
	fuzzcap.Bounded(t, fuzzcap.ChaosTimeout, d.Name+" (SkipCorrupt)", func() {
		r, err := NewReader(bytes.NewReader(d.Data))
		if err != nil {
			skipErr = err
			return
		}
		r.SkipCorrupt = true
		skip, skipErr = capture.ReadAll(r)
	})
	fuzzcap.CheckErrClass(t, d.Name+" (SkipCorrupt)", skipErr)
	if skip != nil {
		if err := skip.Validate(); err != nil {
			t.Fatalf("%s (SkipCorrupt): invalid records: %v", d.Name, err)
		}
		if diff := fuzzcap.SubsetDiff(orig.Samples, skip.Samples); diff != "" {
			t.Fatalf("%s (SkipCorrupt): %s", d.Name, diff)
		}
	}

	fuzzcap.Bounded(t, fuzzcap.ChaosTimeout, d.Name+" (ReadIndex)", func() {
		_, _ = ReadIndex(bytes.NewReader(d.Data), int64(len(d.Data)))
	})

	// Repair fails only when the header cannot be read, and then writes
	// nothing usable; otherwise it writes a complete file that verifies.
	var out bytes.Buffer
	var rep RepairReport
	var err error
	fuzzcap.Bounded(t, fuzzcap.ChaosTimeout, d.Name+" (Repair)", func() {
		rep, err = Repair(&out, bytes.NewReader(d.Data), randomOptions(rng))
	})
	if err != nil {
		if strict.Got != nil {
			t.Fatalf("%s: Repair failed on a file with a readable header: %v", d.Name, err)
		}
		fuzzcap.CheckErrClass(t, d.Name+" (Repair)", err)
		return
	}
	// A lost change in a change-only capture is no reason to stop.
	if inPlace && rep.Stopped != nil && strings.Contains(rep.Stopped.Error(), "equal to the previous one") {
		t.Fatalf("%s: Repair stopped at %v", d.Name, rep.Stopped)
	}
	fixed := fuzzcap.ReadDamaged(t, binaryCodec, d.Name+" (repaired)", out.Bytes())
	if fixed.Err != nil {
		t.Fatalf("%s: the repaired file does not verify: %v", d.Name, fixed.Err)
	}
	if rep.Samples != len(fixed.Got.Samples) {
		t.Fatalf("%s: Repair reports %d samples, the file holds %d", d.Name, rep.Samples, len(fixed.Got.Samples))
	}
	if diff := fuzzcap.SubsetDiff(orig.Samples, fixed.Got.Samples); diff != "" {
		t.Fatalf("%s (repaired): %s", d.Name, diff)
	}
	// Repair keeps at least what a strict read returns.
	if diff := fuzzcap.PrefixDiff(fixed.Got.Samples, got); diff != "" {
		t.Fatalf("%s: the repaired file lost samples a strict read returns: %s", d.Name, diff)
	}
	if strings.HasPrefix(d.Name, "truncated") && len(fixed.Got.Samples) != len(got) {
		t.Fatalf("%s: repaired %d samples, a strict read returns %d", d.Name, len(fixed.Got.Samples), len(got))
	}
	if inPlace {
		if diff := fuzzcap.PrefixDiff(orig.Samples, fixed.Got.Samples); diff != "" && rep.SkippedBlocks == 0 {
			t.Fatalf("%s (repaired, nothing skipped): %s", d.Name, diff)
		}
	}
}

// Regression (found by TestChaos): in a change-only capture, a skipped data
// block can leave the next sample equal to the last one kept. Repair stopped
// there with "response equal to the previous one" and lost every later
// block; it now drops that sample and keeps the rest.
func TestRepairChangeOnlyAfterSkippedBlock(t *testing.T) {
	c := &capture.Capture{
		Header:  capture.Header{TickNS: 1000, WallStart: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC), ChangeOnly: true},
		Targets: []capture.Target{{ID: 1, Transport: capture.TransportTCP, Unit: 1}},
		Streams: []capture.Stream{{ID: 1, Target: 1}},
		End:     capture.End{Known: true, At: 100},
	}
	req := []byte{0x03, 0x00, 0x00, 0x00, 0x01}
	for i, v := range []byte{1, 2, 1, 3} { // block 2 (value 2) is damaged below
		c.Samples = append(c.Samples, capture.Sample{Stream: 1, At: capture.Ticks(10 * (i + 1)), Request: req,
			Response: []byte{0x03, 0x02, 0x00, v}})
	}
	data := encode(t, c, &Options{BlockSamples: 1, Compression: NoCompression})
	blocks := splitBlocks(t, data)
	off := 0
	n := 0
	for _, b := range blocks {
		if b[0] == blockData {
			if n++; n == 2 {
				data[off+len(b)-1] ^= 0xFF // the second data block's CRC
				break
			}
		}
		off += len(b)
	}
	var out bytes.Buffer
	rep, err := Repair(&out, bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stopped != nil || rep.SkippedBlocks != 1 || rep.Dropped != 1 || rep.Samples != 2 {
		t.Fatalf("repair report %+v", rep)
	}
	got := decode(t, out.Bytes())
	want := []capture.Sample{c.Samples[0], c.Samples[3]}
	if diff := fuzzcap.PrefixDiff(got.Samples, want); diff != "" || len(got.Samples) != 2 {
		t.Fatalf("repaired samples %+v: %s", got.Samples, diff)
	}
}
