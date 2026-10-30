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
