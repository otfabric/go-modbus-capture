// SPDX-License-Identifier: MIT

package fuzzcap

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/synth"
)

// ChaosSeedsEnv names the environment variable that sets the number of seeds
// of the chaos tests, for long runs (make chaos).
const ChaosSeedsEnv = "MBCAP_CHAOS_SEEDS"

// ChaosSeeds returns how many seeds a chaos test runs: the value of
// ChaosSeedsEnv when set, otherwise 4 with -short and 24 without.
func ChaosSeeds(t testing.TB) int {
	t.Helper()
	if s := os.Getenv(ChaosSeedsEnv); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("%s=%q is not a positive number", ChaosSeedsEnv, s)
		}
		return n
	}
	if testing.Short() {
		return 4
	}
	return 24
}

// ChaosCapture returns a valid capture for seed. Seeds cycle through the
// variants: synthetic captures with and without extras (every read function,
// a second target, exceptions, samples out of time order), change-only ones,
// one with an unknown end, and captures built from random bytes by Build
// (odd tick sizes, tags and labels with quotes, commas and newlines).
func ChaosCapture(seed int64) *capture.Capture {
	rng := rand.New(rand.NewSource(seed))
	mixes := []synth.Mix{synth.MostlyStatic, synth.Typical, synth.Busy, synth.AllNoisy}
	cfg := synth.Config{Seed: seed, Polls: 2 + rng.Intn(10), Mix: mixes[rng.Intn(len(mixes))],
		Blocks: []synth.Block{{Start: 4096, Count: uint16(1 + rng.Intn(20))}, {Start: 5000, Count: uint16(1 + rng.Intn(60))}}}
	switch seed % 6 {
	case 0:
		return synth.Generate(cfg)
	case 1:
		cfg.Extras = true
		return synth.Generate(cfg)
	case 2:
		cfg.ChangeOnly = true
		return synth.Generate(cfg)
	case 3:
		cfg.Extras, cfg.ChangeOnly = true, true
		return synth.Generate(cfg)
	case 4:
		cfg.Extras = true
		c := synth.Generate(cfg)
		c.End = capture.End{}
		return c
	default:
		b := make([]byte, 64+rng.Intn(400))
		rng.Read(b)
		return Build(b)
	}
}

// Bounded runs f and fails t if f panics or does not return within d.
func Bounded(t testing.TB, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		f()
	}()
	select {
	case p := <-done:
		if p != nil {
			t.Fatalf("%s: panic: %v", what, p)
		}
	case <-time.After(d):
		t.Fatalf("%s: no result after %v (a hang?)", what, d)
	}
}

// ChaosTimeout bounds one read of damaged input.
const ChaosTimeout = 20 * time.Second

// Outcome is what a reader made of damaged input.
type Outcome struct {
	// Got holds the records returned before the error; nil when the header
	// could not be read.
	Got *capture.Capture
	// Err is the error that ended the read (nil when it reached io.EOF).
	Err error
}

// ReadDamaged reads data with c, bounded in time and without panics. It
// checks what every reader guarantees for any input: an error wraps
// capture.ErrCorrupt or capture.ErrTruncated, and the records returned before
// it form a valid capture. A capture read without error re-encodes to itself.
func ReadDamaged(t *testing.T, c Codec, what string, data []byte) Outcome {
	t.Helper()
	var out Outcome
	Bounded(t, ChaosTimeout, what, func() {
		r, err := c.NewReader(bytes.NewReader(data))
		if err != nil {
			out.Err = err
			return
		}
		out.Got, out.Err = capture.ReadAll(r)
	})
	CheckErrClass(t, what, out.Err)
	if out.Got != nil {
		if err := out.Got.Validate(); err != nil {
			t.Fatalf("%s: the records read before %v are not a valid capture: %v", what, out.Err, err)
		}
		if out.Err == nil {
			CheckRoundTrip(t, c, out.Got)
		}
	}
	return out
}

// CheckErrClass fails t unless err is nil or wraps capture.ErrCorrupt or
// capture.ErrTruncated.
func CheckErrClass(t testing.TB, what string, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, capture.ErrCorrupt) && !errors.Is(err, capture.ErrTruncated) {
		t.Fatalf("%s: error %q (%T) wraps neither ErrCorrupt nor ErrTruncated", what, err, err)
	}
}

// Samples returns the samples of c, or none for a nil capture.
func Samples(c *capture.Capture) []capture.Sample {
	if c == nil {
		return nil
	}
	return c.Samples
}

// PrefixDiff describes the first sample of got that differs from want, or
// returns "" when got is a prefix of want.
func PrefixDiff(want, got []capture.Sample) string {
	if len(got) > len(want) {
		return fmt.Sprintf("%d samples, more than the %d of the original", len(got), len(want))
	}
	for i := range got {
		if !sameSample(&want[i], &got[i]) {
			return fmt.Sprintf("sample %d is %s, the original is %s", i, show(got[i]), show(want[i]))
		}
	}
	return ""
}

// SubsetDiff describes the first sample of got that is not a sample of want,
// or returns "" when every sample of got is one of want.
func SubsetDiff(want, got []capture.Sample) string {
	set := make(map[string]bool, len(want))
	for _, s := range want {
		set[sampleKey(s)] = true
	}
	for i, s := range got {
		if !set[sampleKey(s)] {
			return fmt.Sprintf("sample %d %s is not in the original", i, show(s))
		}
	}
	return ""
}

func sameSample(a, b *capture.Sample) bool {
	return a.Stream == b.Stream && a.At == b.At && bytes.Equal(a.Request, b.Request) && bytes.Equal(a.Response, b.Response)
}

func sampleKey(s capture.Sample) string {
	return fmt.Sprintf("%d/%d/%x/%x", s.Stream, s.At, s.Request, s.Response)
}

func show(s capture.Sample) string {
	return fmt.Sprintf("{s=%d at=%d req=%x resp=%x}", s.Stream, s.At, s.Request, s.Response)
}

// A Damage is a copy of an encoded capture with damage applied.
type Damage struct {
	// Name describes the damage, for failure messages.
	Name string
	// Data is the damaged input.
	Data []byte
	// At is the offset of the first byte that differs from the original:
	// everything before it is intact.
	At int
}

// ByteDamage returns n random byte-level damages of data: bit flips,
// overwritten runs, inserted and deleted bytes, and truncations at random
// offsets. Every damage changes data.
func ByteDamage(rng *rand.Rand, data []byte, n int) []Damage {
	var out []Damage
	for range n {
		i := rng.Intn(len(data))
		d := bytes.Clone(data)
		switch rng.Intn(5) {
		case 0:
			bit := rng.Intn(8)
			d[i] ^= 1 << bit
			out = append(out, Damage{fmt.Sprintf("bit %d flipped at %d", bit, i), d, i})
		case 1:
			k := min(1+rng.Intn(8), len(d)-i)
			for j := i; j < i+k; j++ {
				d[j] ^= byte(1 + rng.Intn(255))
			}
			out = append(out, Damage{fmt.Sprintf("%d bytes overwritten at %d", k, i), d, i})
		case 2:
			b := byte(rng.Intn(256))
			d = append(d[:i:i], append([]byte{b}, data[i:]...)...)
			out = append(out, Damage{fmt.Sprintf("byte %02x inserted at %d", b, i), d, i})
		case 3:
			k := min(1+rng.Intn(4), len(d)-i)
			d = append(d[:i:i], data[i+k:]...)
			out = append(out, Damage{fmt.Sprintf("%d bytes deleted at %d", k, i), d, firstDiff(data, d)})
		default:
			out = append(out, Truncation(data, i))
		}
	}
	return out
}

// Truncation returns data cut to n bytes.
func Truncation(data []byte, n int) Damage {
	return Damage{fmt.Sprintf("truncated to %d of %d bytes", n, len(data)), bytes.Clone(data[:n]), n}
}

// Lines splits data after every newline; the parts keep their newlines.
func Lines(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			i = len(data) - 1
		}
		out = append(out, data[:i+1])
		data = data[i+1:]
	}
	return out
}

// Join concatenates parts, such as blocks or lines.
func Join(parts [][]byte) []byte { return bytes.Join(parts, nil) }

// PartDamage returns n random damages of data that move whole parts (lines of
// a text encoding, blocks of a binary one) without changing them: a part
// dropped, duplicated, swapped with another or moved elsewhere. parts must
// join to data.
func PartDamage(rng *rand.Rand, data []byte, parts [][]byte, n int, what string) []Damage {
	if len(parts) < 2 {
		return nil
	}
	var out []Damage
	for range n {
		p := append([][]byte(nil), parts...)
		i, j := rng.Intn(len(p)), rng.Intn(len(p))
		var name string
		switch rng.Intn(4) {
		case 0:
			p = append(p[:i], p[i+1:]...)
			name = fmt.Sprintf("%s %d dropped", what, i)
		case 1:
			p = append(p[:j], append([][]byte{parts[i]}, p[j:]...)...)
			name = fmt.Sprintf("%s %d duplicated before %d", what, i, j)
		case 2:
			p[i], p[j] = p[j], p[i]
			name = fmt.Sprintf("%ss %d and %d swapped", what, i, j)
		default:
			x := p[i]
			p = append(p[:i], p[i+1:]...)
			j = rng.Intn(len(p) + 1)
			p = append(p[:j], append([][]byte{x}, p[j:]...)...)
			name = fmt.Sprintf("%s %d moved to %d", what, i, j)
		}
		d := Join(p)
		if bytes.Equal(d, data) {
			continue
		}
		out = append(out, Damage{name, d, firstDiff(data, d)})
	}
	return out
}

// GarbleLines returns n copies of data with random bytes of one line
// replaced: by random printable characters, quotes, commas, braces or any
// byte.
func GarbleLines(rng *rand.Rand, data []byte, n int) []Damage {
	lines := Lines(data)
	const alphabet = `0123456789abcdefABCDEF,"{}[]:-. tsx\`
	var out []Damage
	for range n {
		i := rng.Intn(len(lines))
		line := bytes.Clone(lines[i])
		for k := 1 + rng.Intn(3); k > 0; k-- {
			j := rng.Intn(len(line))
			if rng.Intn(4) == 0 {
				line[j] = byte(rng.Intn(256))
			} else {
				line[j] = alphabet[rng.Intn(len(alphabet))]
			}
		}
		p := append([][]byte(nil), lines...)
		p[i] = line
		d := Join(p)
		if bytes.Equal(d, data) {
			continue
		}
		out = append(out, Damage{fmt.Sprintf("line %d garbled to %q", i, line), d, firstDiff(data, d)})
	}
	return out
}

func firstDiff(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// CheckIntact fails t unless the damaged read got starts with every sample
// held by the intact part of the input, orig[:d.At]: damage never loses or
// changes what comes before it.
func CheckIntact(t *testing.T, c Codec, orig []byte, d Damage, got Outcome) {
	t.Helper()
	var intact []capture.Sample
	if r, err := c.NewReader(bytes.NewReader(orig[:d.At])); err == nil {
		part, _ := capture.ReadAll(r)
		intact = part.Samples
	}
	if len(Samples(got.Got)) < len(intact) {
		t.Fatalf("%s: %d samples read (error %v), but the %d bytes before the damage hold %d",
			d.Name, len(Samples(got.Got)), got.Err, d.At, len(intact))
	}
	if diff := PrefixDiff(Samples(got.Got), intact); diff != "" {
		t.Fatalf("%s: the samples before the damage changed: %s", d.Name, diff)
	}
}

// TextChaos is the chaos test of a line-based text encoding (JSON Lines,
// long CSV). For every seed it encodes a valid capture with c and reads it
// back after damage: cut at every line boundary and next to it, and at random
// offsets; bits flipped and bytes overwritten, inserted or deleted; lines
// dropped, duplicated, swapped, moved and garbled. Whatever the damage, the
// reader neither panics nor hangs, errors wrap ErrCorrupt or ErrTruncated,
// what it returns is a valid capture, and the samples before the damage come
// out intact. A cut input reads as a prefix of the original and is
// ErrTruncated, or reads without error up to a line boundary with the end
// unknown. Lines moved intact give only original samples.
func TextChaos(t *testing.T, c Codec) {
	for seed := range ChaosSeeds(t) {
		t.Run(strconv.Itoa(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(int64(seed)))
			orig := ChaosCapture(int64(seed))
			var buf bytes.Buffer
			w, err := c.NewWriter(&buf, orig.Header)
			if err != nil {
				t.Fatal(err)
			}
			if err := orig.WriteTo(w); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()
			lines := Lines(data)

			var cuts []Damage
			off := 0
			for _, l := range lines {
				off += len(l)
				for _, n := range []int{off - 1, off, off + 1} {
					if n >= 0 && n < len(data) {
						cuts = append(cuts, Truncation(data, n))
					}
				}
			}
			for range 30 {
				cuts = append(cuts, Truncation(data, rng.Intn(len(data))))
			}
			for _, d := range cuts {
				out := ReadDamaged(t, c, d.Name, d.Data)
				CheckIntact(t, c, data, d, out)
				if out.Err != nil && !errors.Is(out.Err, capture.ErrTruncated) {
					t.Fatalf("%s: error %v, want ErrTruncated", d.Name, out.Err)
				}
				if diff := PrefixDiff(orig.Samples, Samples(out.Got)); diff != "" {
					t.Fatalf("%s: %s", d.Name, diff)
				}
				if out.Err == nil && out.Got.End.Known {
					t.Fatalf("%s: read without error and with an end", d.Name)
				}
			}
			for _, d := range PartDamage(rng, data, lines, 60, "line") {
				out := ReadDamaged(t, c, d.Name, d.Data)
				CheckIntact(t, c, data, d, out)
				if diff := SubsetDiff(orig.Samples, Samples(out.Got)); diff != "" {
					t.Fatalf("%s: %s", d.Name, diff)
				}
			}
			// Text has no checksum: a garbled digit can make another valid
			// sample. What the reader returns must still be valid.
			for _, d := range append(ByteDamage(rng, data, 120), GarbleLines(rng, data, 60)...) {
				CheckIntact(t, c, data, d, ReadDamaged(t, c, d.Name, d.Data))
			}
		})
	}
}
