// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/jsonl"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

var update = flag.Bool("update", false, "rewrite the derived golden files in ../testdata")

const testdata = "../testdata"

// The layout of a minimal file, computed independently of this package
// (docs/FORMAT.md section 2, by hand and with a separate CRC32C):
//
//	magic        89 4d 42 43 41 50 0d 0a
//	header       01 0d | 00 01 e807 8094ebdc03 00 0170 00                  | crc
//	             version 0.1, tick 1000 ns, wall start 1 s, no flags, producer "p", no tags
//	target       02 0e | 01 01 01 00 0168 f603 00 00 00 00 00 00            | crc
//	             id 1, tcp, unit 1, label "", host "h", port 502, no serial, no tags
//	stream       03 08 | 01 01 04706f6c6c 00                               | crc
//	template     04 07 | 01 05 0300000001                                  | crc
//	data         10 19 | 00 17 | 05 03                                     | crc
//	             no compression, 23 bytes, base time 5, 3 samples:
//	             00 01 01 00 04 0302002a   RAW, stream 1, template 1, dt 0
//	             02 01 01 04 01 01 002b    DIFF, dt +2, 1 word: index 1 = 002b
//	             01 01 01 01               SAME, dt -1
//	index        20 08 | 01 0a 01 4a 05 07 01 01                           | crc
//	             end 10, 1 block at offset 74, times 5..7, stream 1
//	footer       21 0c | 0000000000000069 4d424345                         | crc
const minimalHex = "894d424341500d0a010d0001e8078094ebdc03000170007602c62c020e010101000168f603000000000000744eee19" +
	"0308010104706f6c6c0073c2d66004070105030000000110edecc510190017050300010100040302002a020101040101002b" +
	"010101016e38c20a2008010a014a05070101cc4430f9210c00000000000000694d424345db726d37"

func minimalCapture() *capture.Capture {
	req := []byte{0x03, 0x00, 0x00, 0x00, 0x01}
	return &capture.Capture{
		Header:  capture.Header{TickNS: 1000, WallStart: time.Unix(1, 0).UTC(), Producer: "p"},
		Targets: []capture.Target{{ID: 1, Transport: capture.TransportTCP, Host: "h", Port: 502, Unit: 1}},
		Streams: []capture.Stream{{ID: 1, Target: 1, Role: "poll"}},
		Samples: []capture.Sample{
			{Stream: 1, At: 5, Request: req, Response: []byte{0x03, 0x02, 0x00, 0x2a}},
			{Stream: 1, At: 7, Request: req, Response: []byte{0x03, 0x02, 0x00, 0x2b}},
			{Stream: 1, At: 6, Request: req, Response: []byte{0x03, 0x02, 0x00, 0x2b}},
		},
		End: capture.End{Known: true, At: 10},
	}
}

// The writer produces exactly the documented bytes, and the reader reads them.
func TestLayoutPinned(t *testing.T) {
	want, _ := hex.DecodeString(minimalHex)
	got := encode(t, minimalCapture(), &Options{Compression: NoCompression})
	if !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from the documented layout:\n got %x\nwant %x", got, want)
	}
	if d := capture.Diff(minimalCapture(), decode(t, want)); d != "" {
		t.Fatalf("decoding the documented bytes: %s", d)
	}
}

func readJSONL(t *testing.T, path string) (*capture.Capture, []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r, err := jsonl.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	c, err := capture.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c, b
}

// golden compares got with the file at path, or rewrites it with -update.
func golden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run make golden)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from what the encoder writes now (run make golden if the change is intended)", path)
	}
}

// Every hand-written JSON Lines source is canonical, and its binary and CSV
// encodings match the checked-in files and decode to the same capture.
func TestGolden(t *testing.T) {
	sources, _ := filepath.Glob(filepath.Join(testdata, "*.mbcap.jsonl"))
	if len(sources) < 6 {
		t.Fatalf("found %d golden sources, want at least 6", len(sources))
	}
	for _, src := range sources {
		name := strings.TrimSuffix(filepath.Base(src), ".mbcap.jsonl")
		t.Run(name, func(t *testing.T) {
			c, text := readJSONL(t, src)

			var buf bytes.Buffer
			w, _ := jsonl.NewWriter(&buf, c.Header)
			if err := c.WriteTo(w); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), text) {
				t.Errorf("%s is not in canonical form; the writer gives:\n%s", src, buf.Bytes())
			}

			bin := encode(t, c, &Options{Compression: NoCompression})
			golden(t, filepath.Join(testdata, name+".mbcap"), bin)
			if d := capture.Diff(c, decode(t, bin)); d != "" {
				t.Errorf("binary: %s", d)
			}
			if d := capture.Diff(c, decode(t, encode(t, c, nil))); d != "" {
				t.Errorf("binary with deflate: %s", d)
			}

			buf.Reset()
			cw, _ := mbcsv.NewWriter(&buf, c.Header)
			if err := c.WriteTo(cw); err != nil {
				t.Fatal(err)
			}
			golden(t, filepath.Join(testdata, name+".mbcap.csv"), buf.Bytes())
			cr, err := mbcsv.NewReader(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			got, err := capture.ReadAll(cr)
			if err != nil {
				t.Fatal(err)
			}
			if d := capture.Diff(c, got); d != "" {
				t.Errorf("long CSV: %s", d)
			}
		})
	}
}

// damaged builds the damaged variants of the example capture.
func damaged(t *testing.T) map[string][]byte {
	c, _ := readJSONL(t, filepath.Join(testdata, "example.mbcap.jsonl"))
	good := encode(t, c, &Options{Compression: NoCompression, BlockSamples: 2})
	idx, err := ReadIndex(bytes.NewReader(good), int64(len(good)))
	if err != nil {
		t.Fatal(err)
	}
	last := idx.Blocks[len(idx.Blocks)-1].Offset

	out := map[string][]byte{}
	// Cut inside the last data block, as a crash during a write would.
	out["truncated"] = append([]byte(nil), good[:last+5]...)
	// Cut after a complete data block: no index or footer.
	out["no-index"] = append([]byte(nil), good[:last]...)
	// One flipped bit in the payload of the first data block.
	crc := append([]byte(nil), good...)
	crc[idx.Blocks[0].Offset+4] ^= 0x01
	out["crc-corrupt"] = crc

	// A block of a type this version does not know, between definitions.
	var buf bytes.Buffer
	w, err := NewWriter(&buf, c.Header, &Options{Compression: NoCompression, BlockSamples: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(c.Targets[0]); err != nil {
		t.Fatal(err)
	}
	w.block(0x7E, []byte("a block from a later minor version"))
	for _, s := range c.Streams {
		if err := w.WriteStream(s); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range c.Samples {
		if err := w.WriteSample(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(c.End); err != nil {
		t.Fatal(err)
	}
	out["unknown-block"] = buf.Bytes()
	return out
}

func readAllBytes(b []byte, skip bool) (*capture.Capture, int, error) {
	r, err := NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	r.SkipCorrupt = skip
	c, err := capture.ReadAll(r)
	return c, r.Skipped(), err
}

func TestDamagedFiles(t *testing.T) {
	example, _ := readJSONL(t, filepath.Join(testdata, "example.mbcap.jsonl"))
	files := damaged(t)
	for name, b := range files {
		golden(t, filepath.Join(testdata, name+".mbcap"), b)
	}

	t.Run("truncated", func(t *testing.T) {
		c, _, err := readAllBytes(files["truncated"], false)
		if !errors.Is(err, capture.ErrTruncated) {
			t.Fatalf("err = %v, want ErrTruncated", err)
		}
		// Two complete data blocks of two samples precede the cut one.
		if len(c.Samples) != 4 || c.End.Known {
			t.Fatalf("read %d samples, end %+v; want the 4 samples of the complete blocks and no end", len(c.Samples), c.End)
		}
	})
	t.Run("no-index", func(t *testing.T) {
		c, _, err := readAllBytes(files["no-index"], false)
		if !errors.Is(err, capture.ErrTruncated) || len(c.Samples) != 4 {
			t.Fatalf("err = %v with %d samples, want ErrTruncated after 4", err, len(c.Samples))
		}
		if _, err := ReadIndex(bytes.NewReader(files["no-index"]), int64(len(files["no-index"]))); !errors.Is(err, ErrNoFooter) {
			t.Fatalf("ReadIndex: %v, want ErrNoFooter", err)
		}
	})
	t.Run("crc-corrupt", func(t *testing.T) {
		if _, _, err := readAllBytes(files["crc-corrupt"], false); !errors.Is(err, capture.ErrCorrupt) {
			t.Fatalf("err = %v, want ErrCorrupt", err)
		}
		c, skipped, err := readAllBytes(files["crc-corrupt"], true)
		if err != nil || skipped != 1 || len(c.Samples) != 3 || !c.End.Known {
			t.Fatalf("with SkipCorrupt: err %v, %d skipped, %d samples, end %+v; want 1 block (2 samples) lost and the rest", err, skipped, len(c.Samples), c.End)
		}
	})
	t.Run("unknown-block", func(t *testing.T) {
		c, _, err := readAllBytes(files["unknown-block"], false)
		if err != nil {
			t.Fatal(err)
		}
		if d := capture.Diff(example, c); d != "" {
			t.Fatalf("the unknown block changed the capture: %s", d)
		}
	})
}

func TestRepair(t *testing.T) {
	files := damaged(t)
	for _, tc := range []struct {
		name      string
		samples   int
		truncated bool
		skipped   int
		endKnown  bool
	}{
		{"truncated", 4, true, 0, false},
		{"no-index", 4, true, 0, false},
		{"crc-corrupt", 3, false, 1, true},
		{"unknown-block", 5, false, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			rep, err := Repair(&out, bytes.NewReader(files[tc.name]), nil)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Samples != tc.samples || rep.Truncated != tc.truncated || rep.SkippedBlocks != tc.skipped || rep.End.Known != tc.endKnown || rep.Stopped != nil {
				t.Fatalf("report %+v", rep)
			}
			c, _, err := readAllBytes(out.Bytes(), false)
			if err != nil {
				t.Fatalf("the repaired file does not read: %v", err)
			}
			if len(c.Samples) != tc.samples {
				t.Fatalf("the repaired file has %d samples, want %d", len(c.Samples), tc.samples)
			}
			if _, err := ReadIndex(bytes.NewReader(out.Bytes()), int64(out.Len())); err != nil {
				t.Fatalf("the repaired file has no index: %v", err)
			}
		})
	}
}

// Readers stop cleanly at every possible cut of a valid file.
func TestEveryTruncation(t *testing.T) {
	c, _ := readJSONL(t, filepath.Join(testdata, "multi-target.mbcap.jsonl"))
	for _, opts := range []*Options{{Compression: NoCompression, BlockSamples: 3}, nil} {
		good := encode(t, c, opts)
		for n := 0; n < len(good); n++ {
			r, err := NewReader(bytes.NewReader(good[:n]))
			if err == nil {
				_, err = capture.ReadAll(r)
			}
			if !errors.Is(err, capture.ErrTruncated) {
				t.Fatalf("cut at %d of %d bytes: err = %v, want ErrTruncated", n, len(good), err)
			}
		}
	}
}
