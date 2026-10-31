// SPDX-License-Identifier: MIT

// Package e2e runs the built mbcapctl over real files and reads what it
// wrote with the public library, so the binary, its exit codes and the three
// encodings are exercised together, the way a user meets them.
package e2e

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/jsonl"
	"github.com/otfabric/go-modbus-capture/mbcap"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

// bin is the mbcapctl under test, built with the race detector when that is
// available, so the concurrency of a real process is checked as well.
var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mbcapctl-e2e-")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "mbcapctl")
	if err := exec.Command("go", "build", "-race", "-o", bin, "../cmd/mbcapctl").Run(); err != nil {
		// -race needs cgo. The build without it tests the same program.
		if out, err := exec.Command("go", "build", "-o", bin, "../cmd/mbcapctl").CombinedOutput(); err != nil {
			panic(string(out))
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// ctl runs the binary and returns its stdout, stderr and exit code.
func ctl(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("mbcapctl %s: %v", strings.Join(args, " "), err)
	}
	return out.String(), errb.String(), code
}

func ok(t *testing.T, args ...string) string {
	t.Helper()
	out, errb, code := ctl(t, args...)
	if code != 0 {
		t.Fatalf("mbcapctl %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errb)
	}
	return out
}

func testdata(name string) string { return filepath.Join("..", "testdata", name) }

// site is a capture large enough to fill several data blocks, with every read
// function, exceptions, two targets and samples out of time order.
func site(t *testing.T) *capture.Capture {
	t.Helper()
	return synth.Generate(synth.Config{Seed: 7, Polls: 400, Mix: synth.Typical, Extras: true})
}

func writeMbcap(t *testing.T, path string, c *capture.Capture) {
	t.Helper()
	var b bytes.Buffer
	w, err := mbcap.NewWriter(&b, c.Header, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteTo(w); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// read decodes a file with the public reader of its encoding, by its name.
func read(t *testing.T, path string) *capture.Capture {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var r capture.Reader
	switch {
	case strings.HasSuffix(path, ".jsonl"):
		r, err = jsonl.NewReader(f)
	case strings.HasSuffix(path, ".csv"):
		r, err = mbcsv.NewReader(f)
	default:
		r, err = mbcap.NewReader(f)
	}
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	c, err := capture.ReadAll(r)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return c
}

// TestLosslessChain sends a capture through every encoding and back with the
// binary, and reads each stage with the library: nothing may change.
func TestLosslessChain(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.mbcap")
	want := site(t)
	writeMbcap(t, src, want)

	jl := filepath.Join(dir, "b.mbcap.jsonl")
	csv := filepath.Join(dir, "c.mbcap.csv")
	back := filepath.Join(dir, "d.mbcap")
	plain := filepath.Join(dir, "e.mbcap")
	ok(t, "convert", src, jl)
	ok(t, "convert", jl, csv)
	ok(t, "convert", csv, back)
	ok(t, "convert", "--compression", "none", "--block-samples", "16", back, plain)
	for _, p := range []string{jl, csv, back, plain} {
		if d := capture.Diff(want, read(t, p)); d != "" {
			t.Errorf("%s differs from the source: %s", filepath.Base(p), d)
		}
		if out := ok(t, "verify", p); !strings.Contains(out, ": ok (") {
			t.Errorf("verify %s: %s", p, out)
		}
	}
}

// TestPipes connects two real processes: mbcapctl reading stdin and writing
// stdout as documented.
func TestPipes(t *testing.T) {
	src, err := os.ReadFile(testdata("example.mbcap"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "convert", "-", "-")
	cmd.Stdin = bytes.NewReader(src)
	got, err := cmd.Output()
	if err != nil {
		t.Fatalf("convert - -: %v", err)
	}
	want, err := os.ReadFile(testdata("example.mbcap.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("stdout is not the JSON Lines golden file:\n%s", got)
	}
}

// TestDamageAndRepair truncates a file, as a crashed writer would leave it:
// verify must fail with exit 1 and repair must recover a verifiable prefix.
func TestDamageAndRepair(t *testing.T) {
	dir := t.TempDir()
	whole := filepath.Join(dir, "whole.mbcap")
	want := site(t)
	writeMbcap(t, whole, want)
	b, err := os.ReadFile(whole)
	if err != nil {
		t.Fatal(err)
	}
	cut := filepath.Join(dir, "cut.mbcap")
	if err := os.WriteFile(cut, b[:len(b)*2/3], 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, code := ctl(t, "verify", whole, cut); code != 1 || !strings.Contains(out, "cut.mbcap: FAIL") || !strings.Contains(out, "whole.mbcap: ok") {
		t.Fatalf("verify of a truncated file: exit %d\n%s", code, out)
	}
	fixed := filepath.Join(dir, "fixed.mbcap")
	ok(t, "repair", cut, fixed)
	ok(t, "verify", fixed)
	got := read(t, fixed)
	if n := len(got.Samples); n == 0 || n >= len(want.Samples) {
		t.Fatalf("repair kept %d of %d samples", n, len(want.Samples))
	}
	for i, s := range got.Samples {
		if w := want.Samples[i]; s.At != w.At || !bytes.Equal(s.Request, w.Request) || !bytes.Equal(s.Response, w.Response) {
			t.Fatalf("sample %d changed in the repair", i)
		}
	}
}

// TestEditPipeline splits a capture by target, merges the parts and filters
// the result: the samples of the source must survive the round.
func TestEditPipeline(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "site.mbcap")
	want := site(t)
	writeMbcap(t, src, want)

	parts := strings.Fields(ok(t, "split", "--by", "target", "--dir", dir, src))
	if len(parts) != len(want.Targets) {
		t.Fatalf("split wrote %v for %d targets", parts, len(want.Targets))
	}
	merged := filepath.Join(dir, "merged.mbcap")
	ok(t, append([]string{"merge", "-o", merged}, parts...)...)
	got := read(t, merged)
	if len(got.Samples) != len(want.Samples) || len(got.Targets) != len(want.Targets) {
		t.Fatalf("merged: %d samples of %d, %d targets of %d", len(got.Samples), len(want.Samples), len(got.Targets), len(want.Targets))
	}

	only := filepath.Join(dir, "fc3.mbcap")
	ok(t, "filter", "--fc", "3", merged, only)
	var fc3 int
	for _, s := range want.Samples {
		if s.Request[0] == 3 {
			fc3++
		}
	}
	filtered := read(t, only)
	if fc3 == 0 || len(filtered.Samples) != fc3 {
		t.Fatalf("filter kept %d samples, the source has %d of FC03", len(filtered.Samples), fc3)
	}
}

// TestFailuresLeaveNoOutput checks the exit codes of the documented failures,
// and that a failed command does not leave a partial output behind.
func TestFailuresLeaveNoOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mbcap")
	if _, _, code := ctl(t, "convert", testdata("crc-corrupt.mbcap"), out); code != 1 {
		t.Errorf("convert of a corrupt file: exit %d, want 1", code)
	}
	if _, _, code := ctl(t, "convert", filepath.Join(dir, "missing.mbcap"), out); code != 1 {
		t.Errorf("convert of a missing file: exit %d, want 1", code)
	}
	if _, _, code := ctl(t, "no-such-command"); code != 2 {
		t.Errorf("unknown command: exit %d, want 2", code)
	}
	if _, _, code := ctl(t, "merge", testdata("example.mbcap")); code != 2 {
		t.Errorf("merge without -o: exit %d, want 2", code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("failed commands left files behind: %v", entries)
	}
}
