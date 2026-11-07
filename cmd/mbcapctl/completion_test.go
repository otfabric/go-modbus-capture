// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	r := mustRun(t, "version")
	if !regexp.MustCompile(`^mbcapctl \S+ \(commit \S+, built \S+, go\S+, \w+/\w+\)\n$`).MatchString(r.stdout) {
		t.Errorf("version: %q", r.stdout)
	}
	r = mustRun(t, "version", "--json")
	if strings.Count(r.stdout, "\n") != 1 {
		t.Errorf("version --json is not one line: %q", r.stdout)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &v); err != nil {
		t.Fatal(err)
	}
	keys := []string{"name", "version", "tag", "commit", "built", "libraries", "go", "platform"}
	at := -1
	for _, k := range keys {
		i := strings.Index(r.stdout, `"`+k+`":`)
		if i <= at {
			t.Fatalf("key %q missing or out of order: %s", k, r.stdout)
		}
		at = i
	}
	if v["name"] != "mbcapctl" || v["tag"] != "" {
		t.Errorf("version --json: %s", r.stdout)
	}

	// Injected values.
	defer func(v, tg, c, b string) { version, tag, commit, buildDate = v, tg, c, b }(version, tag, commit, buildDate)
	version, tag, commit, buildDate = "0.2.0", "v0.2.0", "1802d0f9afe8642d1e278120b76fcdfac967cb9e", "2026-11-05T15:41:49Z"
	r = mustRun(t, "version")
	if !strings.HasPrefix(r.stdout, "mbcapctl 0.2.0 (tag v0.2.0, commit 1802d0f9afe8642d1e278120b76fcdfac967cb9e, built 2026-11-05T15:41:49Z, go") {
		t.Errorf("version: %q", r.stdout)
	}
	if got := provenanceStep("filter", flag.NewFlagSet("x", flag.ContinueOnError)); got != "mbcapctl 0.2.0 filter" {
		t.Errorf("provenance %q", got)
	}
}

func completionScript(t *testing.T, shell string) string {
	t.Helper()
	r := mustRun(t, "completion", shell)
	if r.stdout == "" {
		t.Fatalf("completion %s: empty", shell)
	}
	return r.stdout
}

// TestCompletionCoversTheCommandTable checks that every script names every
// command, every flag and every value of a flag with a fixed set.
func TestCompletionCoversTheCommandTable(t *testing.T) {
	model := completionModel()
	for _, shell := range shells {
		s := completionScript(t, shell)
		for _, c := range model {
			if !strings.Contains(s, c.name) {
				t.Errorf("%s: command %s missing", shell, c.name)
			}
			for _, f := range c.flags {
				if !strings.Contains(s, f.name) {
					t.Errorf("%s: %s flag %s missing", shell, c.name, f.name)
				}
				if f.kind == compChoice {
					for _, v := range f.values {
						if !strings.Contains(s, v) {
							t.Errorf("%s: %s --%s value %s missing", shell, c.name, f.name, v)
						}
					}
				}
			}
			for _, v := range c.args.values {
				if !strings.Contains(s, v) {
					t.Errorf("%s: %s argument value %s missing", shell, c.name, v)
				}
			}
		}
	}
}

// TestCompletionModel pins what the main flags complete, and fails when a
// flag is added without saying what it takes: a fixed set (choiceVar), a
// file or directory (pathVar, dirVar), or a free value listed here.
func TestCompletionModel(t *testing.T) {
	freeValues := map[string]bool{"block-samples": true, "from": true, "to": true, "target": true, "stream": true, "fc": true}
	byName := map[string]compCmd{}
	for _, c := range completionModel() {
		byName[c.name] = c
		for _, f := range c.flags {
			if f.kind == compValue && !freeValues[f.name] {
				t.Errorf("%s --%s takes a value but completes nothing: use choiceVar, pathVar or dirVar, or list it as a free value", c.name, f.name)
			}
		}
	}
	flagOf := func(cmd, name string) compFlag {
		for _, f := range byName[cmd].flags {
			if f.name == name {
				return f
			}
		}
		t.Fatalf("%s has no flag %s", cmd, name)
		return compFlag{}
	}
	want := map[[2]string]string{
		{"convert", "format"}:     "mbcap jsonl csv wide-csv",
		{"repair", "compression"}: "deflate none",
		{"split", "by"}:           "target stream fc",
		{"filter", "format"}:      "mbcap jsonl csv wide-csv",
		{"trim", "compression"}:   "deflate none",
		{"cat", "format"}:         "mbcap jsonl csv wide-csv",
		{"merge", "compression"}:  "deflate none",
		{"completion", ""}:        "bash zsh fish powershell",
		{"version", ""}:           "",
		{"info", ""}:              "mbcap jsonl csv",
		{"merge", "o"}:            "mbcap jsonl csv",
		{"split", "dir"}:          "",
		{"filter", "fc"}:          "",
		{"version", "json"}:       "",
		{"dump", "wall"}:          "",
	}
	for k, v := range want {
		var got []string
		if k[1] == "" {
			got = byName[k[0]].args.values
		} else {
			got = flagOf(k[0], k[1]).values
		}
		if strings.Join(got, " ") != v {
			t.Errorf("%s %s: %q, want %q", k[0], k[1], got, v)
		}
	}
	if byName["version"].args.kind != compNone || byName["info"].args.kind != compFiles ||
		flagOf("split", "dir").kind != compDir || flagOf("merge", "o").kind != compFiles ||
		flagOf("version", "json").kind != compNone || flagOf("filter", "fc").kind != compValue {
		t.Error("completion kinds")
	}
}

func TestChoiceFlagKeepsBehaviour(t *testing.T) {
	var s string
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	choiceVar(fs, &s, "format", "csv", "u", "csv", "jsonl")
	if s != "csv" || fs.Lookup("format").Value.String() != "csv" || (&choiceFlag{}).String() != "" {
		t.Fatal("default")
	}
	// Values outside the set are stored; the command reports them.
	if err := fs.Parse([]string{"--format", "xml"}); err != nil || s != "xml" {
		t.Fatalf("parse: %v %q", err, s)
	}
}

func writeScript(t *testing.T, shell, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(completionScript(t, shell)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func lookShell(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed", name)
	}
	return p
}

func TestBashCompletion(t *testing.T) {
	bash := lookShell(t, "bash")
	script := writeScript(t, "bash", "mbcapctl.bash")
	if out, err := exec.Command(bash, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	// Run the completion function as bash would.
	dir := t.TempDir()
	for _, f := range []string{"a.mbcap", "b.mbcap.jsonl", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ words, want string }{
		{`mbcapctl co`, "completion convert"},
		{`mbcapctl completion ''`, "bash fish powershell zsh"},
		{`mbcapctl split --by ''`, "fc stream target"},
		{`mbcapctl convert --format = w`, "wide-csv"},
		{`mbcapctl convert -compression ''`, "deflate none"},
		{`mbcapctl version -`, "--help --json"},
		{`mbcapctl info ''`, "a.mbcap b.mbcap.jsonl sub"},
		{`mbcapctl cat -o ''`, "a.mbcap b.mbcap.jsonl sub"},
		{`mbcapctl split --dir ''`, "sub"},
		{`mbcapctl filter --fc ''`, ""},
		{`mbcapctl version ''`, ""},
	}
	// Replies are sorted.
	for _, c := range cases {
		prog := `source "$1"; COMP_WORDS=(` + c.words + `); COMP_CWORD=$((${#COMP_WORDS[@]}-1)); _mbcapctl; ` +
			`printf '%s\n' "${COMPREPLY[@]}" | sort | tr '\n' ' '`
		cmd := exec.Command(bash, "-c", prog, "bash", script)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", c.words, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != c.want {
			t.Errorf("%s: %q, want %q", c.words, got, c.want)
		}
	}
}

func TestZshCompletion(t *testing.T) {
	zsh := lookShell(t, "zsh")
	script := writeScript(t, "zsh", "_mbcapctl")
	if out, err := exec.Command(zsh, "-n", script).CombinedOutput(); err != nil {
		t.Fatalf("zsh -n: %v\n%s", err, out)
	}
	// Sourcing it registers the function with compinit.
	prog := `autoload -U compinit && compinit -u -D && source "$1" && print -r -- $_comps[mbcapctl]`
	out, err := exec.Command(zsh, "-f", "-c", prog, "zsh", script).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "_mbcapctl" {
		t.Fatalf("zsh source: %v\n%s", err, out)
	}
}

func TestFishCompletion(t *testing.T) {
	fish := lookShell(t, "fish")
	script := writeScript(t, "fish", "mbcapctl.fish")
	if out, err := exec.Command(fish, "--no-execute", script).CombinedOutput(); err != nil {
		t.Fatalf("fish --no-execute: %v\n%s", err, out)
	}
	out, err := exec.Command(fish, "--no-config", "-c", `source $argv[1]; complete -C 'mbcapctl split --by '`, script).CombinedOutput()
	if err != nil {
		t.Fatalf("fish: %v\n%s", err, out)
	}
	for _, v := range []string{"target", "stream", "fc"} {
		if !strings.Contains(string(out), v) {
			t.Errorf("fish split --by: %s", out)
		}
	}
}

func TestPowerShellCompletion(t *testing.T) {
	pwsh := lookShell(t, "pwsh")
	script := writeScript(t, "powershell", "mbcapctl.ps1")
	cases := map[string]string{
		"mbcapctl co":                 "convert completion",
		"mbcapctl split --by ":        "target stream fc",
		"mbcapctl convert --format w": "wide-csv",
		"mbcapctl completion ":        "bash zsh fish powershell",
		"mbcapctl version -":          "--help --json",
		"mbcapctl info --json ":       "",
	}
	var inputs []string
	for in := range cases {
		inputs = append(inputs, psq(in))
	}
	prog := `$p = ` + psq(script) + `; $errs = $null; [void][System.Management.Automation.Language.Parser]::ParseFile($p, [ref]$null, [ref]$errs); ` +
		`if ($errs.Count) { $errs | ForEach-Object { $_.ToString() }; exit 1 }; . $p; ` +
		`foreach ($in in @(` + strings.Join(inputs, ", ") + `)) { $r = TabExpansion2 -inputScript $in -cursorColumn $in.Length; ` +
		`$in + ' => [' + ($r.CompletionMatches.CompletionText -join ' ') + ']' }`
	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-Command", prog)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh: %v\n%s", err, out)
	}
	got := map[string]string{}
	for l := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		in, res, ok := strings.Cut(strings.TrimRight(l, "\r"), " => [")
		res, closed := strings.CutSuffix(res, "]")
		if !ok || !closed {
			t.Fatalf("pwsh output: %s", out)
		}
		got[in] = res
	}
	for in, want := range cases {
		if got[in] != want {
			t.Errorf("pwsh %q: %q, want %q", in, got[in], want)
		}
	}
}
