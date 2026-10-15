// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

// e2eCapture is a synthetic capture with two targets, three streams, every
// read function and exceptions, with its samples in time order (ties by
// stream), so that splitting it per target and merging the parts by time
// gives it back.
func e2eCapture(t *testing.T) *capture.Capture {
	t.Helper()
	c := synth.Generate(synth.Config{Seed: 7, Polls: 40, Mix: synth.Typical, Extras: true})
	slices.SortStableFunc(c.Samples, func(a, b capture.Sample) int {
		return cmp.Or(cmp.Compare(a.At, b.At), cmp.Compare(a.Stream, b.Stream))
	})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func writeBinary(t *testing.T, path string, c *capture.Capture) {
	t.Helper()
	if err := saveBinary(path, c); err != nil {
		t.Fatal(err)
	}
}

// saveBinary writes c to path in the binary encoding with default options.
func saveBinary(path string, c *capture.Capture) error {
	var buf bytes.Buffer
	w, err := mbcap.NewWriter(&buf, c.Header, nil)
	if err != nil {
		return err
	}
	if err := c.WriteTo(w); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// withoutEditTags returns c with the provenance and source tags removed.
func withoutEditTags(c *capture.Capture) *capture.Capture {
	tags := map[string]string{}
	for k, v := range c.Header.Tags {
		if k != capture.TagProvenance && k != capture.TagSource {
			tags[k] = v
		}
	}
	c.Header.Tags = tags
	return c
}

// The whole tool chain on one capture: convert to every encoding and back,
// split and merge back, filter, trim, and inspect every result.
func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	in := func(name string) string { return filepath.Join(dir, name) }
	orig := e2eCapture(t)
	writeBinary(t, in("site.mbcap"), orig)
	origBytes := readFile(t, in("site.mbcap"))

	// mbcap -> jsonl -> csv -> mbcap, and mbcap -> csv -> jsonl -> mbcap:
	// the binary comes back byte for byte.
	mustRun(t, "convert", in("site.mbcap"), in("site.mbcap.jsonl"))
	mustRun(t, "convert", in("site.mbcap.jsonl"), in("site.mbcap.csv"))
	mustRun(t, "convert", in("site.mbcap.csv"), in("back.mbcap"))
	mustRun(t, "convert", in("site.mbcap"), in("other.mbcap.csv"))
	mustRun(t, "convert", in("other.mbcap.csv"), in("other.mbcap.jsonl"))
	mustRun(t, "convert", in("other.mbcap.jsonl"), in("other.mbcap"))
	for _, f := range []string{"back.mbcap", "other.mbcap"} {
		if !bytes.Equal(readFile(t, in(f)), origBytes) {
			t.Fatalf("%s is not byte-identical to the original", f)
		}
	}
	if !bytes.Equal(readFile(t, in("site.mbcap.csv")), readFile(t, in("other.mbcap.csv"))) ||
		!bytes.Equal(readFile(t, in("site.mbcap.jsonl")), readFile(t, in("other.mbcap.jsonl"))) {
		t.Fatal("one capture converted along two paths gives different text")
	}
	for _, f := range []string{"site.mbcap.jsonl", "site.mbcap.csv"} {
		if d := capture.Diff(orig, load(t, in(f))); d != "" {
			t.Fatalf("%s: %s", f, d)
		}
	}

	// Split per stream: every sample lands in exactly one part.
	parts := filepath.Join(dir, "parts")
	if err := os.Mkdir(parts, 0o755); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, "split", "--by", "stream", "--dir", parts, in("site.mbcap"))
	streamParts := strings.Fields(r.stdout)
	if len(streamParts) != 3 {
		t.Fatalf("split by stream wrote %v", streamParts)
	}
	total := 0
	for _, p := range streamParts {
		pc := load(t, p)
		if len(pc.Streams) != 1 || len(pc.Targets) != 1 {
			t.Errorf("%s: %d streams, %d targets", p, len(pc.Streams), len(pc.Targets))
		}
		total += len(pc.Samples)
	}
	if total != len(orig.Samples) {
		t.Fatalf("the stream parts hold %d samples, want %d", total, len(orig.Samples))
	}
	if prov := load(t, streamParts[0]).Header.Tags[capture.TagProvenance]; strings.Contains(prov, dir) {
		t.Errorf("provenance %q names the output directory", prov)
	}

	// Split per target and merge the parts by time: the original capture
	// comes back, apart from the edit tags.
	r = mustRun(t, "split", "--by", "target", "--format", "jsonl", "--dir", parts, in("site.mbcap"))
	targetParts := strings.Fields(r.stdout)
	if len(targetParts) != 2 {
		t.Fatalf("split by target wrote %v", targetParts)
	}
	mustRun(t, append([]string{"merge", "-o", in("merged.mbcap")}, targetParts...)...)
	merged := load(t, in("merged.mbcap"))
	if src := merged.Header.Tags[capture.TagSource]; src != "site.target-1.mbcap.jsonl, site.target-2.mbcap.jsonl" {
		t.Errorf("source tag %q", src)
	}
	if prov := merged.Header.Tags[capture.TagProvenance]; !strings.HasPrefix(prov, "mbcapctl ") || !strings.HasSuffix(prov, " merge") {
		t.Errorf("provenance tag %q", prov)
	}
	if d := capture.Diff(orig, withoutEditTags(merged)); d != "" {
		t.Fatalf("split and merge do not give the original back: %s", d)
	}

	// Filter: the identity reads of target 2, before 30 s.
	mustRun(t, "filter", "--target", "2", "--fc", "0x2b", "--to", "30s", in("site.mbcap"), in("identity.mbcap.csv"))
	var want []capture.Sample
	for _, s := range orig.Samples {
		if s.Function() == 0x2B && s.At < 30_000_000 {
			want = append(want, s)
		}
	}
	got := load(t, in("identity.mbcap.csv"))
	if len(want) == 0 || !slices.EqualFunc(got.Samples, want, sameSample) || got.End.At != 30_000_000 {
		t.Fatalf("filter kept %d samples (want %d), end %+v", len(got.Samples), len(want), got.End)
	}

	// Trim a window of 5 s from 10 s on: times rebased to the window start.
	mustRun(t, "trim", "--from", "10s", "--to", "15s", in("site.mbcap.jsonl"), in("window.mbcap"))
	want = want[:0]
	for _, s := range orig.Samples {
		if s.At >= 10_000_000 && s.At < 15_000_000 {
			s.At -= 10_000_000
			want = append(want, s)
		}
	}
	got = load(t, in("window.mbcap"))
	if len(want) == 0 || !slices.EqualFunc(got.Samples, want, sameSample) || got.End.At != 5_000_000 {
		t.Fatalf("trim kept %d samples (want %d), end %+v", len(got.Samples), len(want), got.End)
	}
	if !got.Header.WallStart.Equal(orig.Header.WallStart.Add(10e9)) {
		t.Errorf("trim moved the wall-clock start to %v", got.Header.WallStart)
	}

	// Inspect every result.
	outputs := []string{"site.mbcap", "site.mbcap.jsonl", "site.mbcap.csv", "merged.mbcap", "identity.mbcap.csv", "window.mbcap"}
	var paths []string
	for _, f := range outputs {
		paths = append(paths, in(f))
	}
	r = mustRun(t, append([]string{"verify"}, append(paths, streamParts...)...)...)
	if n := strings.Count(r.stdout, ": ok ("); n != len(paths)+len(streamParts) {
		t.Fatalf("verify:\n%s", r.stdout)
	}
	r = mustRun(t, "info", "--json", in("merged.mbcap"))
	var info Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil {
		t.Fatal(err)
	}
	if info.Samples != len(orig.Samples) || len(info.Targets) != 2 || len(info.Streams) != 3 || info.ExcSamples == 0 ||
		info.BinaryIndex == nil || info.BinaryIndex.DataBlocks == 0 {
		t.Errorf("info --json: %+v", info)
	}
	r = mustRun(t, "info", in("site.mbcap.csv"))
	for _, w := range []string{"Inverter A", "Meter on RS-485", "rtu /dev/ttyUSB0 19200 8E1"} {
		if !strings.Contains(r.stdout, w) {
			t.Errorf("info lacks %q:\n%s", w, r.stdout)
		}
	}
	r = mustRun(t, "dump", in("window.mbcap"))
	if n := strings.Count(r.stdout, "\nsample  "); n != len(want) || !strings.Contains(r.stdout, "end     ") {
		t.Errorf("dump lists %d samples, want %d", n, len(want))
	}
}

func sameSample(a, b capture.Sample) bool {
	return a.Stream == b.Stream && a.At == b.At && bytes.Equal(a.Request, b.Request) && bytes.Equal(a.Response, b.Response)
}

// A capture with a vendor function code is invalid: every command that
// reads it fails with exit code 1 and a clear message, never a crash, and
// writes no output.
func TestVendorFunctionRejected(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "vendor.mbcap.jsonl")
	jsonl := `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z","producer":"x"}` + "\n" +
		`{"t":"target","id":1,"transport":"tcp","unit":1}` + "\n" +
		`{"t":"stream","id":1,"target":1}` + "\n" +
		`{"t":"s","s":1,"at":5,"req":"0300000001","resp":"03020007"}` + "\n" +
		`{"t":"s","s":1,"at":9,"req":"410102","resp":"410a0b"}` + "\n" +
		`{"t":"end","at":10}` + "\n"
	if err := os.WriteFile(src, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	const why = "function code 0x41 is not a read function of the Modbus specification"
	for _, args := range [][]string{
		{"convert", src, filepath.Join(dir, "out.mbcap")},
		{"convert", src, filepath.Join(dir, "out.wide.csv")},
		{"filter", "--fc", "3", src, filepath.Join(dir, "out.mbcap")},
		{"trim", "--from", "1t", src, filepath.Join(dir, "out.mbcap")},
		{"split", "--by", "fc", "--dir", dir, src},
		{"merge", "-o", filepath.Join(dir, "out.mbcap"), src},
		{"info", src},
		{"dump", src},
		{"verify", src},
	} {
		r := mbcapctl(t, args...)
		out := r.stdout + r.stderr
		if r.code != exitError || !strings.Contains(out, why) || !strings.Contains(out, "line 5") {
			t.Errorf("%s: exit %d\n%s", args[0], r.code, out)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("failed commands left files: %v", entries)
	}
}
