// SPDX-License-Identifier: MIT

package format

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

func TestStringAndParse(t *testing.T) {
	for in, want := range map[string]Format{
		"mbcap": Binary, "binary": Binary, "MBCAP": Binary, "jsonl": JSONL, "csv": CSV, "wide-csv": WideCSV, "wide": WideCSV,
	} {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Fatalf("Parse(%q) = %v, %v", in, got, err)
		}
	}
	if got, err := Parse("xml"); err == nil || got != Unknown {
		t.Fatalf("Parse(xml) = %v, %v", got, err)
	}
	for f, want := range map[Format]string{Binary: "mbcap", JSONL: "jsonl", CSV: "csv", WideCSV: "wide-csv", Unknown: "unknown"} {
		if got := f.String(); got != want {
			t.Fatalf("%d: %q", f, got)
		}
	}
}

func TestFromPath(t *testing.T) {
	for in, want := range map[string]Format{
		"a.mbcap": Binary, "A.MBCAP": Binary, "a.mbcap.jsonl": JSONL, "a.csv": CSV, "a.wide.csv": WideCSV, "a.txt": Unknown, "": Unknown,
	} {
		if got := FromPath(in); got != want {
			t.Fatalf("FromPath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	for in, want := range map[string]Format{
		"":                    Unknown,
		"hello":               Unknown,
		"  \n{\"t\":\"header": JSONL,
		"t,at,s\n":            CSV,
		"\"t\",at\n":          CSV,
		"at,t,s\n":            CSV,
	} {
		if got := Detect(bufio.NewReader(strings.NewReader(in))); got != want {
			t.Fatalf("Detect(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOpenGoldenFiles(t *testing.T) {
	var want *capture.Capture
	for name, f := range map[string]Format{"example.mbcap": Binary, "example.mbcap.jsonl": JSONL, "example.mbcap.csv": CSV} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		r, got, err := Open(bytes.NewReader(raw))
		if err != nil || got != f {
			t.Fatalf("%s: %v, %v", name, got, err)
		}
		c, err := capture.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = c
		} else if d := capture.Diff(want, c); d != "" {
			t.Fatalf("%s differs: %s", name, d)
		}
	}
}

func TestOpenUnknown(t *testing.T) {
	if _, f, err := Open(strings.NewReader("hello")); f != Unknown || !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("%v, %v", f, err)
	}
}

func TestNewWriter(t *testing.T) {
	h := capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}
	for _, f := range []Format{Binary, JSONL, CSV} {
		var buf bytes.Buffer
		w, err := NewWriter(f, &buf, h, nil)
		if err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		if err := w.Close(capture.End{}); err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		r, got, err := Open(&buf)
		if err != nil || got != f {
			t.Fatalf("%v read back as %v, %v", f, got, err)
		}
		if _, err := capture.ReadAll(r); err != nil {
			t.Fatalf("%v: %v", f, err)
		}
	}
	for _, f := range []Format{WideCSV, Unknown} {
		if _, err := NewWriter(f, &bytes.Buffer{}, h, nil); err == nil {
			t.Fatalf("%v: want an error", f)
		}
	}
}
