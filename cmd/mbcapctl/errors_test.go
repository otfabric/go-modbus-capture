// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/format"
)

const missing = "no-such-file.mbcap"

func writeCapture(t *testing.T, path string, c *capture.Capture) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w, err := format.NewWriter(format.FromPath(path), f, c.Header, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteTo(w); err != nil {
		t.Fatal(err)
	}
}

func TestUsageErrors(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mbcap")
	in := golden("example.mbcap")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"info arguments", []string{"info"}, "one file"},
		{"verify arguments", []string{"verify"}, "one or more"},
		{"dump arguments", []string{"dump"}, "one file"},
		{"version arguments", []string{"version", "x"}, "no arguments"},
		{"split arguments", []string{"split"}, "one input"},
		{"split key", []string{"split", "--by", "colour", in}, "--by must be"},
		{"filter arguments", []string{"filter", in}, "an input and an output"},
		{"trim arguments", []string{"trim", in}, "an input and an output"},
		{"repair arguments", []string{"repair", in}, "an input and an output"},
		{"merge without output", []string{"merge", in}, "-o"},
		{"unknown encoding", []string{"convert", in, filepath.Join(dir, "o.bin")}, "cannot tell the encoding"},
		{"compression", []string{"convert", "--compression", "lz4", in, out}, "unknown compression"},
		{"wide filter", []string{"filter", in, filepath.Join(dir, "o.wide.csv")}, "wide-csv is a view"},
		{"wide split", []string{"split", "--by", "stream", "--format", "wide-csv", in}, "wide-csv is a view"},
		{"split format", []string{"split", "--by", "stream", "--format", "xml", in}, "unknown format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mbcapctl(t, tt.args...)
			if r.code != exitUsage || !strings.Contains(r.stderr, tt.want) {
				t.Fatalf("%+v, want %q", r, tt.want)
			}
		})
	}
}

func TestBadFlagValues(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mbcap")
	in := golden("example.mbcap")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"ticks", []string{"filter", "--from", "xt", in, out}, "number of ticks"},
		{"duration", []string{"filter", "--from", "soon", in, out}, "invalid"},
		{"negative time", []string{"filter", "--from", "-5s", in, out}, "cannot be negative"},
		{"function list", []string{"filter", "--fc", "3,x", in, out}, "unsigned 8-bit"},
		{"function range", []string{"filter", "--fc", "300", in, out}, "unsigned 8-bit"},
		{"unknown flag", []string{"filter", "--nope", in, out}, "not defined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mbcapctl(t, tt.args...)
			if r.code != exitUsage || !strings.Contains(r.stderr, tt.want) {
				t.Fatalf("%+v, want %q", r, tt.want)
			}
		})
	}
}

func TestHelpFlag(t *testing.T) {
	r := mbcapctl(t, "filter", "-h")
	if r.code != exitOK || !strings.Contains(r.stderr, "Flags:") {
		t.Fatalf("%+v", r)
	}
}

func TestListAndFlagForms(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mbcap.jsonl")
	mustRun(t, "filter", "--fc=0x03,,4", "--", golden("example.mbcap"), out)
	c := load(t, out)
	if len(c.Samples) == 0 {
		t.Fatal("no samples kept")
	}
	for i := range c.Samples {
		if fc := c.Samples[i].Function(); fc != 3 && fc != 4 {
			t.Fatalf("function %d kept", fc)
		}
	}
	// A time in ticks, and options after the file names.
	out = filepath.Join(dir, "t.mbcap")
	mustRun(t, "trim", golden("example.mbcap"), out, "--from", "0t", "--compression", "none")
	if got := load(t, out); len(got.Samples) == 0 {
		t.Fatal("trim kept nothing")
	}
}

func TestMissingInput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mbcap")
	for _, args := range [][]string{
		{"info", missing},
		{"dump", missing},
		{"convert", missing, out},
		{"convert", missing, filepath.Join(dir, "o.wide.csv")},
		{"split", "--by", "stream", missing},
		{"filter", missing, out},
		{"trim", "--from", "0t", missing, out},
		{"repair", missing, out},
		{"merge", "-o", out, missing},
	} {
		if r := mbcapctl(t, args...); r.code != exitError || !strings.Contains(r.stderr, missing) {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind %v, %v", entries, err)
	}
}

func TestUnwritableOutput(t *testing.T) {
	in := golden("example.mbcap")
	gone := filepath.Join(t.TempDir(), "gone", "o.mbcap")
	for _, args := range [][]string{
		{"convert", in, gone},
		{"convert", in, filepath.Join(filepath.Dir(gone), "o.wide.csv")},
		{"filter", in, gone},
		{"trim", "--from", "0t", in, gone},
		{"repair", in, gone},
	} {
		if r := mbcapctl(t, args...); r.code != exitError {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	if r := mbcapctl(t, "split", "--by", "stream", "--dir", filepath.Dir(gone), in); r.code != exitError {
		t.Fatalf("split: %+v", r)
	}
}

func TestDamagedInput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "o.mbcap")
	in := golden("crc-corrupt.mbcap")
	for _, args := range [][]string{
		{"info", in},
		{"dump", in},
		{"convert", in, filepath.Join(dir, "o.wide.csv")},
		{"split", "--by", "stream", "--dir", dir, in},
		{"filter", in, out},
		{"trim", "--from", "0t", in, out},
		{"merge", "-o", out, in},
	} {
		if r := mbcapctl(t, args...); r.code != exitError {
			t.Fatalf("%v: %+v", args, r)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("left behind %v, %v", entries, err)
	}
	// Not a capture at all.
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := mbcapctl(t, "verify", junk); r.code == exitOK || !strings.Contains(r.stdout+r.stderr, "junk") {
		t.Fatalf("verify: %+v", r)
	}
	if r := mbcapctl(t, "repair", junk, out); r.code != exitError {
		t.Fatalf("repair: %+v", r)
	}
}

func TestRepairReportsSkippedBlocks(t *testing.T) {
	out := filepath.Join(t.TempDir(), "r.mbcap")
	r := mustRun(t, "repair", golden("crc-corrupt.mbcap"), out)
	if !strings.Contains(r.stdout, "skipped 1 damaged blocks") {
		t.Fatalf("repair: %s", r.stdout)
	}
	mustRun(t, "verify", out)
	// An intact file keeps its end.
	r = mustRun(t, "repair", golden("example.mbcap"), filepath.Join(filepath.Dir(out), "e.mbcap"))
	if !strings.Contains(r.stdout, "capture end kept") {
		t.Fatalf("repair: %s", r.stdout)
	}
}

func TestInfoAndDumpFlags(t *testing.T) {
	dir := t.TempDir()
	c := load(t, golden("exception.mbcap"))
	if !c.Header.Exceptions {
		t.Fatal("golden file records no exceptions")
	}
	c.Header.ChangeOnly = false
	c.Header.Tags = map[string]string{"site": "lab"}
	c.Targets[0].Tags = map[string]string{"bus": "A"}
	c.End = capture.End{}
	path := filepath.Join(dir, "x.mbcap.jsonl")
	writeCapture(t, path, c)

	info := mustRun(t, "info", path)
	for _, want := range []string{"exceptions recorded", "not recorded"} {
		if !strings.Contains(info.stdout, want) {
			t.Errorf("info lacks %q:\n%s", want, info.stdout)
		}
	}
	dump := mustRun(t, "dump", path)
	for _, want := range []string{`site="lab"`, `bus="A"`} {
		if !strings.Contains(dump.stdout, want) {
			t.Errorf("dump lacks %q:\n%s", want, dump.stdout)
		}
	}
	co := mustRun(t, "info", golden("change-only.mbcap"))
	if !strings.Contains(co.stdout, "change-only") {
		t.Errorf("info of a change-only capture:\n%s", co.stdout)
	}
	if d := mustRun(t, "dump", golden("change-only.mbcap")); !strings.Contains(d.stdout, "change-only") {
		t.Errorf("dump of a change-only capture:\n%s", d.stdout)
	}
}

func TestSplitNames(t *testing.T) {
	dir := t.TempDir()
	r := mustRun(t, "split", "--by", "target", "--format", "jsonl", "--dir", dir, golden("multi-target.mbcap"))
	if !strings.Contains(r.stdout, ".mbcap.jsonl") {
		t.Fatalf("split: %s", r.stdout)
	}
	for _, p := range strings.Fields(r.stdout) {
		load(t, p)
	}
}

func TestWideConvertReportsSkipped(t *testing.T) {
	out := filepath.Join(t.TempDir(), "w.wide.csv")
	r := mustRun(t, "convert", golden("exception.mbcap"), out)
	if !strings.Contains(r.stderr, "left out") {
		t.Fatalf("convert: %+v", r)
	}
}

func TestHelpers(t *testing.T) {
	if got := functionName(0x41); got != "not a Modbus read function" {
		t.Fatalf("functionName: %q", got)
	}
	if got := endpoint(capture.Target{Transport: capture.TransportUDP}); got != "udp" {
		t.Fatalf("endpoint: %q", got)
	}
	if orDash("") != "-" || orDash("x") != "x" || orNone("") != "none" || orNone("x") != "x" {
		t.Fatal("orDash, orNone")
	}
}

func TestCommitFailsOnAClosedFile(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	_ = tmp.Close()
	o := &output{path: tmp.Name() + ".out", tmp: tmp, w: tmp}
	if err := o.commit(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(tmp.Name()); !os.IsNotExist(err) {
		t.Fatalf("temporary file left: %v", err)
	}
}

func TestStdoutOutputNeedsNoCommit(t *testing.T) {
	var buf bytes.Buffer
	e := &env{stdout: &buf}
	o, err := e.create("-")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.commit(); err != nil {
		t.Fatal(err)
	}
	o.abort()
}
