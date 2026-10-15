// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/otfabric/go-modbus-capture/internal/synth"
)

// session runs a series of commands with different flags in its own
// directory and returns their output and the files they wrote.
func session(t *testing.T, dir string, seed int) string {
	t.Helper()
	src := filepath.Join(dir, "in.mbcap")
	if err := saveBinary(src, synth.Generate(synth.Config{Seed: int64(seed), Polls: 10, Mix: synth.Busy, Extras: true})); err != nil {
		t.Error(err)
		return ""
	}
	p := func(name string) string { return filepath.Join(dir, name) }
	fc := []string{"3", "1,2", "0x2b", "4,7,8"}[seed%4]
	by := []string{"target", "stream", "fc"}[seed%3]
	cmds := [][]string{
		{"convert", "--compression", []string{"none", "deflate"}[seed%2], "--block-samples", fmt.Sprint(1 + seed), src, p("a.mbcap")},
		{"convert", "--format", []string{"jsonl", "csv"}[seed%2], src, p("a.txt")},
		{"convert", src, p("a.wide.csv")},
		{"filter", "--fc", fc, "--from", fmt.Sprintf("%dt", seed*1000), src, p("f.mbcap.jsonl")},
		{"trim", "--to", fmt.Sprintf("%ds", 2+seed), src, p("t.mbcap.csv")},
		{"split", "--by", by, "--dir", dir, src},
		{"merge", "-o", p("m.mbcap"), src, p("f.mbcap.jsonl")},
		{"cat", "-o", p("c.mbcap.jsonl"), p("t.mbcap.csv"), src},
		{"info", []string{"--json", "--json=false"}[seed%2], src},
		{"dump", []string{"--wall", "--wall=false"}[seed%2], p("f.mbcap.jsonl")},
		{"verify", src, p("a.mbcap"), p("m.mbcap")},
		{"repair", "--block-samples", "3", p("a.mbcap"), p("r.mbcap")},
	}
	var out strings.Builder
	for _, args := range cmds {
		var stdout, stderr bytes.Buffer
		code := run(args, strings.NewReader(""), &stdout, &stderr)
		_, _ = fmt.Fprintf(&out, "$ %s\nexit %d\n%s%s", strings.Join(args, " "), code, stdout.String(), stderr.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Error(err)
		return ""
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Error(err)
		}
		_, _ = fmt.Fprintf(&out, "== %s\n%s", e.Name(), b)
	}
	// File names differ between directories; the rest must not.
	return strings.ReplaceAll(out.String(), dir, "DIR")
}

// The command function keeps no state between runs: many command lines with
// different flags running at once (under -race) give the same output and
// files as one at a time.
func TestParallelCommands(t *testing.T) {
	const seeds, copies = 6, 3
	want := make([]string, seeds)
	for s := range seeds {
		want[s] = session(t, t.TempDir(), s)
	}
	var wg sync.WaitGroup
	got := make([][]string, seeds)
	for s := range seeds {
		got[s] = make([]string, copies)
		for i := range copies {
			dir := t.TempDir()
			wg.Add(1)
			go func() {
				defer wg.Done()
				got[s][i] = session(t, dir, s)
			}()
		}
	}
	wg.Wait()
	for s := range seeds {
		for i := range copies {
			if got[s][i] != want[s] {
				t.Errorf("seed %d, copy %d: the commands give other results when run concurrently", s, i)
			}
		}
	}
}
