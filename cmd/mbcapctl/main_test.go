// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/format"
)

func golden(name string) string { return filepath.Join("..", "..", "testdata", name) }

type result struct {
	code           int
	stdout, stderr string
}

func mbcapctl(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, strings.NewReader(""), &out, &errb)
	return result{code, out.String(), errb.String()}
}

func mustRun(t *testing.T, args ...string) result {
	t.Helper()
	r := mbcapctl(t, args...)
	if r.code != exitOK {
		t.Fatalf("mbcapctl %s: exit %d\n%s%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

func load(t *testing.T, path string) *capture.Capture {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, _, err := format.Open(f)
	if err != nil {
		t.Fatal(err)
	}
	c, err := capture.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

func TestUsage(t *testing.T) {
	if r := mbcapctl(t); r.code != exitUsage || !strings.Contains(r.stdout, "never opens a Modbus connection") {
		t.Fatalf("no arguments: %+v", r)
	}
	if r := mbcapctl(t, "help"); r.code != exitOK {
		t.Fatalf("help: %+v", r)
	}
	// There is no capture or replay command.
	for _, cmd := range []string{"record", "scan", "read", "serve", "replay"} {
		if r := mbcapctl(t, cmd); r.code != exitUsage || !strings.Contains(r.stderr, "unknown command") {
			t.Errorf("%s: %+v", cmd, r)
		}
	}
	if r := mbcapctl(t, "convert", "only-one"); r.code != exitUsage {
		t.Errorf("convert with one argument: %+v", r)
	}
	if r := mbcapctl(t, "version"); r.code != exitOK || !strings.HasPrefix(r.stdout, "mbcapctl ") {
		t.Errorf("version: %+v", r)
	}
}

func TestInfo(t *testing.T) {
	r := mustRun(t, "info", golden("multi-target.mbcap"))
	for _, want := range []string{"Inverter A", "rtu /dev/ttyUSB0 19200 8E1", "every minute", "Data blocks:", "1m0s"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("info lacks %q:\n%s", want, r.stdout)
		}
	}
	r = mustRun(t, "stat", "--json", golden("example.mbcap.csv"))
	var info Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil {
		t.Fatal(err)
	}
	if info.Samples != 5 || info.Format != "csv" || len(info.Streams) != 2 || info.Streams[0].Cadence == nil ||
		*info.Streams[0].Cadence != 1000450 || info.Functions["03"] != 4 {
		t.Errorf("info --json: %+v", info)
	}
	r = mustRun(t, "info", golden("truncated.mbcap"))
	if !strings.Contains(r.stdout, "truncated") {
		t.Errorf("info of a truncated file does not say so:\n%s", r.stdout)
	}
}

func TestVerify(t *testing.T) {
	r := mustRun(t, "verify", golden("example.mbcap"), golden("example.mbcap.jsonl"), golden("example.mbcap.csv"))
	if strings.Count(r.stdout, ": ok (") != 3 {
		t.Fatalf("verify:\n%s", r.stdout)
	}
	r = mbcapctl(t, "verify", golden("example.mbcap"), golden("truncated.mbcap"), golden("crc-corrupt.mbcap"))
	if r.code != exitError || strings.Count(r.stdout, "FAIL") != 2 || !strings.Contains(r.stdout, "mbcapctl repair") {
		t.Fatalf("verify of damaged files: %+v", r)
	}
}

func TestDump(t *testing.T) {
	r := mustRun(t, "dump", golden("exception.mbcap"))
	if !strings.Contains(r.stdout, "8302  exception") || !strings.Contains(r.stdout, "exceptions") {
		t.Fatalf("dump:\n%s", r.stdout)
	}
	r = mustRun(t, "dump", "--wall", golden("example.mbcap"))
	if !strings.Contains(r.stdout, "2026-10-14T13:14:25.701599Z") {
		t.Fatalf("dump --wall:\n%s", r.stdout)
	}
}

func TestConvertRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := golden("multi-target.mbcap.jsonl")
	want := load(t, src)
	steps := []string{"a.mbcap", "b.mbcap.csv", "c.mbcap.jsonl", "d.mbcap"}
	prev := src
	for _, s := range steps {
		out := filepath.Join(dir, s)
		mustRun(t, "convert", prev, out)
		if d := capture.Diff(want, load(t, out)); d != "" {
			t.Fatalf("%s: %s", s, d)
		}
		prev = out
	}
	// convert copies the capture as it is: no provenance is added.
	if _, ok := load(t, prev).Header.Tags[capture.TagProvenance]; ok {
		t.Error("convert added provenance")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "c.mbcap.jsonl"))
	orig, _ := os.ReadFile(src)
	if !bytes.Equal(b, orig) {
		t.Error("jsonl > mbcap > csv > jsonl does not reproduce the bytes")
	}
	mustRun(t, "convert", "--format", "jsonl", "--compression", "none", src, filepath.Join(dir, "x.txt"))
	mustRun(t, "convert", src, filepath.Join(dir, "v.wide.csv"))
	wide, _ := os.ReadFile(filepath.Join(dir, "v.wide.csv"))
	if !strings.HasPrefix(string(wide), "at,1/03/0,1/03/1,") {
		t.Errorf("wide view:\n%s", wide)
	}
	if r := mbcapctl(t, "convert", src, filepath.Join(dir, "noext")); r.code != exitUsage {
		t.Errorf("an output without extension or --format: %+v", r)
	}
}

func TestOutputSafety(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.mbcap.jsonl")
	orig, _ := os.ReadFile(golden("example.mbcap.jsonl"))
	if err := os.WriteFile(in, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if r := mbcapctl(t, "filter", "--fc", "3", in, in); r.code != exitUsage || !strings.Contains(r.stderr, "also an input") {
		t.Fatalf("writing over the input: %+v", r)
	}
	// A command that fails leaves no output file behind.
	out := filepath.Join(dir, "out.mbcap")
	if r := mbcapctl(t, "convert", golden("crc-corrupt.mbcap"), out); r.code != exitError {
		t.Fatalf("converting a damaged file: %+v", r)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("a failed command left files: %v", entries)
	}
	if b, _ := os.ReadFile(in); !bytes.Equal(b, orig) {
		t.Fatal("the input changed")
	}
}

func TestFilterAndTrim(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "f.mbcap")
	r := mustRun(t, "filter", golden("multi-target.mbcap"), out, "--target", "1", "--fc", "3,0x2b", "--to", "2s")
	if !strings.Contains(r.stderr, "kept 3 of 7 samples") {
		t.Fatalf("filter: %s", r.stderr)
	}
	c := load(t, out)
	if len(c.Samples) != 3 || len(c.Targets) != 1 || c.End.At != 2_000_000 {
		t.Fatalf("filter result: %d samples, %d targets, end %+v", len(c.Samples), len(c.Targets), c.End)
	}
	prov := c.Header.Tags[capture.TagProvenance]
	if !strings.Contains(prov, "mbcapctl") || !strings.Contains(prov, "--fc=3,43") || strings.Contains(prov, dir) {
		t.Errorf("provenance %q", prov)
	}

	out = filepath.Join(dir, "t.mbcap.jsonl")
	mustRun(t, "trim", "--from", "1001000t", "--to", "1m", golden("multi-target.mbcap"), out)
	c = load(t, out)
	if len(c.Samples) != 2 || c.Samples[0].At != 0 || c.Samples[1].At != 1_000_000 || c.End.At != 60_000_000-1_001_000 {
		t.Fatalf("trim result: %+v end %+v", c.Samples, c.End)
	}
	if r := mbcapctl(t, "trim", golden("example.mbcap"), filepath.Join(dir, "x.mbcap")); r.code != exitUsage {
		t.Errorf("trim without a window: %+v", r)
	}
}

func TestSplit(t *testing.T) {
	dir := t.TempDir()
	r := mustRun(t, "split", "--by", "fc", "--dir", dir, golden("multi-target.mbcap"))
	files := strings.Fields(r.stdout)
	if len(files) != 3 {
		t.Fatalf("split by fc wrote %v", files)
	}
	total := 0
	for _, f := range files {
		if !strings.HasPrefix(filepath.Base(f), "multi-target.fc-") || !strings.HasSuffix(f, ".mbcap") {
			t.Errorf("part name %s", f)
		}
		total += len(load(t, f).Samples)
	}
	if total != 7 {
		t.Errorf("the parts hold %d samples, want 7", total)
	}
	r = mustRun(t, "split", "--by", "stream", "--format", "csv", "--dir", dir, golden("multi-target.mbcap.jsonl"))
	if n := len(strings.Fields(r.stdout)); n != 4 || !strings.Contains(r.stdout, "multi-target.stream-4.mbcap.csv") {
		t.Errorf("split by stream: %s", r.stdout)
	}
	if r := mbcapctl(t, "split", golden("example.mbcap")); r.code != exitUsage {
		t.Errorf("split without --by: %+v", r)
	}
}

func TestCatMerge(t *testing.T) {
	dir := t.TempDir()
	a, b := golden("example.mbcap"), golden("fc43-paged.mbcap.jsonl")
	out := filepath.Join(dir, "m.mbcap")
	mustRun(t, "merge", "-o", out, a, b)
	m := load(t, out)
	if len(m.Samples) != 7 || len(m.Targets) != 2 || m.Header.Tags[capture.TagSource] != "example.mbcap, fc43-paged.mbcap.jsonl" {
		t.Fatalf("merge: %d samples, %d targets, tags %v", len(m.Samples), len(m.Targets), m.Header.Tags)
	}
	for i := 1; i < len(m.Samples); i++ {
		if m.Samples[i].At < m.Samples[i-1].At {
			t.Fatalf("merge did not interleave by time")
		}
	}
	out = filepath.Join(dir, "c.mbcap.jsonl")
	mustRun(t, "cat", "-o", out, b, a)
	if c := load(t, out); c.Samples[0].Request[0] != 0x2B || len(c.Samples) != 7 {
		t.Fatalf("cat did not keep input order")
	}
	if r := mbcapctl(t, "merge", a, b); r.code != exitUsage {
		t.Errorf("merge without -o: %+v", r)
	}
}

func TestRepair(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "r.mbcap")
	r := mustRun(t, "repair", golden("truncated.mbcap"), out)
	if !strings.Contains(r.stdout, "kept 4 samples") || !strings.Contains(r.stdout, "truncated") {
		t.Fatalf("repair: %s", r.stdout)
	}
	mustRun(t, "verify", out)
	if r := mbcapctl(t, "repair", golden("truncated.mbcap"), filepath.Join(dir, "r.mbcap.jsonl")); r.code != exitUsage {
		t.Errorf("repair to jsonl: %+v", r)
	}
}

func TestStdinStdout(t *testing.T) {
	src, _ := os.ReadFile(golden("example.mbcap"))
	var out, errb bytes.Buffer
	if code := run([]string{"convert", "-", "-"}, bytes.NewReader(src), &out, &errb); code != exitOK {
		t.Fatalf("convert - -: %d %s", code, errb.String())
	}
	want, _ := os.ReadFile(golden("example.mbcap.jsonl"))
	if !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("stdin mbcap to stdout jsonl:\n%s", out.String())
	}
}
