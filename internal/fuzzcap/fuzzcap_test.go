// SPDX-License-Identifier: MIT

package fuzzcap

import (
	"bytes"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/jsonl"
)

var codec = Codec{
	NewReader: func(r io.Reader) (capture.Reader, error) { return jsonl.NewReader(r) },
	NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return jsonl.NewWriter(w, h) },
}

func TestSeeds(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Seeds looks in ../testdata of the calling package.
	if err := os.Chdir(filepath.Join("..", "..", "edit")); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	}()
	seeds := Seeds(t, "example.mbcap*")
	if len(seeds) != 3 {
		t.Fatalf("%d seeds", len(seeds))
	}
	if !bytes.HasPrefix(seeds[0], []byte("\x89MBCAP")) && !bytes.HasPrefix(seeds[0], []byte("{")) {
		t.Fatalf("seed starts with %q", seeds[0][:8])
	}
	if got := Seeds(t, "no-such-file*"); got != nil {
		t.Fatalf("no match: %d seeds", len(got))
	}
}

func TestReadAccepted(t *testing.T) {
	if ReadAccepted(codec, []byte("hello\n")) != nil {
		t.Fatal("a rejected header")
	}
	if ReadAccepted(codec, []byte(`{"t":"header"`)) != nil {
		t.Fatal("a cut input")
	}
	head := `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z"}` + "\n"
	if ReadAccepted(codec, []byte(head+"hello\n")) != nil {
		t.Fatal("a bad record")
	}
	if c := ReadAccepted(codec, []byte(head)); c == nil || c.Header.TickNS != 1000 {
		t.Fatalf("got %+v", c)
	}
}

// Build makes a capture that is valid whatever the bytes, and that survives
// the round trip.
func TestBuild(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	inputs := [][]byte{nil, {0}, {0xFF, 0xFF, 0xFF}}
	for range 200 {
		b := make([]byte, rng.Intn(300))
		rng.Read(b)
		inputs = append(inputs, b)
	}
	for _, in := range inputs {
		c := Build(in)
		if err := c.Validate(); err != nil {
			t.Fatalf("Build(%x): %v", in, err)
		}
		CheckRoundTrip(t, codec, c)
	}
}

func TestBuildFlags(t *testing.T) {
	c := Build([]byte{1 | 2 | 4 | 8})
	if !c.Header.ChangeOnly || !c.Header.Exceptions || c.Header.Tags["unicode"] == "" {
		t.Fatalf("header %+v", c.Header)
	}
	if c.End.Known || c.End.At != 0 {
		t.Fatalf("end %+v", c.End)
	}
	c = Build([]byte{0})
	if c.Header.ChangeOnly || c.Header.Exceptions || c.Header.Tags != nil || !c.End.Known {
		t.Fatalf("capture %+v", c)
	}
}

// AllocBytes counts what a call allocates, kept or not.
func TestAllocBytes(t *testing.T) {
	var keep []byte
	got := AllocBytes(4, func() { keep = make([]byte, 1<<20) })
	if got < 1<<20 || got > 1<<20+64<<10 {
		t.Fatalf("a call allocating 1 MiB measured as %d bytes", got)
	}
	if got := AllocBytes(4, func() { keep[0]++ }); got > 4<<10 {
		t.Fatalf("a call allocating nothing measured as %d bytes", got)
	}
}

// Build makes times that step by about 2^63 ticks, in both directions.
func TestBuildTimeSteps(t *testing.T) {
	c := Build([]byte{0, 3, 1, 2, 0, 0, 1, 0, 1, 0, 0, 0,
		0, 2, 0, 2, 2, 1, 0x00, 0, 2, 0, 2, 2, 2, 0x80, 0, 2, 0, 2, 2, 3, 0x7f,
		0, 2, 0, 2, 2, 4, 0x7f, 0, 2, 0, 2, 2, 5, 0x81, 0, 2, 0, 2, 2, 6, 0x81})
	want := []capture.Ticks{1000, 1<<63 + 1000, 5699, 1<<63 + 10398, 5699, 1<<63 + 1000}
	if len(c.Samples) != len(want) {
		t.Fatalf("%d samples, want %d", len(c.Samples), len(want))
	}
	for i, s := range c.Samples {
		if s.At != want[i] {
			t.Errorf("sample %d at %d, want %d", i, s.At, want[i])
		}
	}
	if c.End != (capture.End{Known: true, At: 1<<63 + 10398}) {
		t.Fatalf("end %+v", c.End)
	}
	CheckRoundTrip(t, codec, c)

	// Times stay in range: a step that would pass the last tick is 0.
	c = Build([]byte{0, 3, 1, 2, 0, 0, 1, 0, 1, 0, 0, 0,
		0, 2, 0, 2, 2, 1, 0x80, 0, 2, 0, 2, 2, 2, 0x81, 0, 2, 0, 2, 2, 3, 0x7e, 0, 2, 0, 2, 2, 4, 0x63})
	want = []capture.Ticks{1<<63 + 1000, 1<<64 - 3699, 1<<64 - 3699, 1<<64 - 36}
	if len(c.Samples) != len(want) || c.End != (capture.End{Known: true, At: 1<<64 - 36}) {
		t.Fatalf("near the last tick: %d samples, end %+v", len(c.Samples), c.End)
	}
	for i, s := range c.Samples {
		if s.At != want[i] {
			t.Errorf("near the last tick: sample %d at %d, want %d", i, s.At, want[i])
		}
	}
	CheckRoundTrip(t, codec, c)
}

// The JSON Lines codec keeps the ownership rules.
func TestCheckOwnership(t *testing.T) {
	CheckOwnership(t, codec)
}
