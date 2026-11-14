// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/otfabric/go-modbus-capture/capture"
)

// controls returns the characters of s that are not text: control
// characters, line breaks inside a field included, and what is not valid
// UTF-8.
func controls(s string) []rune {
	var out []rune
	for _, r := range s {
		if r == '\n' {
			continue // ends an output line
		}
		if unicode.IsControl(r) || r == unicode.ReplacementChar || r == 0x202E {
			out = append(out, r)
		}
	}
	return out
}

// Every string of a capture reaches the output of info and dump as text:
// producer, tags, labels, roles, hosts and serial devices, in every encoding.
func TestInfoAndDumpPrintStringsAsText(t *testing.T) {
	// ESC, BEL, a line break, a tab, DEL, a C1 control and a direction
	// override, each inside ordinary text.
	const odd = "a\x1b[2Jb\ac\nd\te\x7ff\u009bg\u202eh"
	c := &capture.Capture{
		Header: capture.Header{TickNS: 1000, WallStart: capture.Header{}.WallStart.AddDate(2025, 0, 0), Producer: "p " + odd,
			Tags: map[string]string{"key " + odd: "value " + odd, "plain": "text"}},
		Targets: []capture.Target{
			{ID: 1, Label: "label " + odd, Transport: capture.TransportTCP, Host: "host " + odd, Port: 502, Unit: 1,
				Tags: map[string]string{"tkey " + odd: "tvalue " + odd}},
			{ID: 2, Transport: capture.TransportRTU, Serial: capture.Serial{Device: "dev " + odd, Baud: 9600, DataBits: 8, StopBits: 1}},
		},
		Streams: []capture.Stream{{ID: 1, Target: 1, Role: "role " + odd, Label: "slabel " + odd}},
		Samples: []capture.Sample{{Stream: 1, At: 5, Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 7}}},
		End:     capture.End{Known: true, At: 9},
	}
	dir := t.TempDir()
	for _, name := range []string{"x.mbcap", "x.mbcap.jsonl", "x.mbcap.csv"} {
		path := filepath.Join(dir, name)
		writeCapture(t, path, c)
		for _, cmd := range []string{"info", "dump"} {
			r := mustRun(t, cmd, path)
			if bad := controls(r.stdout + r.stderr); len(bad) > 0 {
				t.Errorf("%s %s prints %q:\n%q", cmd, name, bad, r.stdout+r.stderr)
			}
			// Each string is there, quoted: nothing is dropped.
			parts := []string{"p ", "key ", "value ", "label ", "host ", "dev ", "role ", "slabel "}
			if cmd == "dump" {
				parts = append(parts, "tkey ", "tvalue ") // info lists no target tags
			}
			for _, part := range parts {
				if !strings.Contains(r.stdout, part+`a\x1b[2Jb\ac\nd\te\x7ff\u009bg\u202eh`) {
					t.Errorf("%s %s does not print the string %q with its escapes:\n%s", cmd, name, part, r.stdout)
				}
			}
			if !strings.Contains(r.stdout, "text") {
				t.Errorf("%s %s does not print a plain tag as it is:\n%s", cmd, name, r.stdout)
			}
		}
	}
	// As JSON too, with the strings unchanged for who decodes it.
	r := mustRun(t, "info", "--json", filepath.Join(dir, "x.mbcap"))
	if bad := controls(r.stdout); len(bad) > 0 {
		t.Errorf("info --json prints %q:\n%q", bad, r.stdout)
	}
	var info Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil {
		t.Fatalf("info --json: %v\n%s", err, r.stdout)
	}
	if info.Producer != c.Header.Producer || info.Tags["key "+odd] != "value "+odd || info.Targets[0].Label != "label "+odd ||
		info.Streams[0].Role != "role "+odd || info.Streams[0].Label != "slabel "+odd {
		t.Errorf("info --json changed a string: %+v", info)
	}
	// Strings of printable characters print as before.
	r = mustRun(t, "info", golden("multi-target.mbcap"))
	for _, want := range []string{"  modbusctl 3.0.0\n", "  site=lab\n", "rtu /dev/ttyUSB0 19200 8E1", "tcp 192.168.2.201:502", "  poll  "} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("info lacks %q:\n%s", want, r.stdout)
		}
	}
	r = mustRun(t, "dump", golden("multi-target.mbcap"))
	for _, want := range []string{" site=\"lab\"", "rtu /dev/ttyUSB0 19200 8E1 unit=3", "role=poll "} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("dump lacks %q:\n%s", want, r.stdout)
		}
	}
}

// An error that quotes a string of the file is printed as one line of text.
func TestErrorsPrintStringsAsText(t *testing.T) {
	dir := t.TempDir()
	// A tag whose key holds control characters and whose value is refused
	// (a carriage return): the error names the key.
	bad := filepath.Join(dir, "bad.mbcap.jsonl")
	line := `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z","tags":{"k\u001b[2J\u0007\nsecond line":"a\rb"}}` + "\n"
	if err := os.WriteFile(bad, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	// A long CSV whose fc column does not match the request.
	csv := filepath.Join(dir, "bad.mbcap.csv")
	rows := "t,at,s,fc,req,resp,id,target,unit,version,tick_ns,wall_start\n" +
		"header,,,,,,,,,0.1,1000,2026-10-14T13:14:24Z\ntarget,,,,,,1,,1,,,\nstream,,,,,,1,1,,,,\n" +
		"s,5,1,\"\x1b[2J\a\nx\",0300000001,03020007,,,,,,\n"
	if err := os.WriteFile(csv, []byte(rows), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.mbcap")
	for _, args := range [][]string{
		{"verify", bad}, {"info", bad}, {"dump", bad}, {"convert", bad, out},
		{"verify", csv}, {"info", csv}, {"dump", csv}, {"convert", csv, out}, {"filter", csv, out},
		{"merge", "-o", out, csv}, {"split", "--by", "fc", "--dir", dir, csv},
	} {
		r := mbcapctl(t, args...)
		if r.code != exitError {
			t.Errorf("%v: exit %d", args, r.code)
		}
		text := r.stdout + r.stderr
		if bad := controls(text); len(bad) > 0 {
			t.Errorf("%v prints %q:\n%q", args, bad, text)
		}
		// One line for the error; verify adds its count of failed files,
		// and dump has printed the records before the one in error.
		want := 1
		if args[0] == "verify" {
			want = 2
		}
		if n := strings.Count(text, "\n"); n != want && args[0] != "dump" {
			t.Errorf("%v prints %d lines, want %d:\n%s", args, n, want, text)
		}
		if !strings.Contains(text, `\x1b[2J\a\n`) {
			t.Errorf("%v does not print the string of the file with its escapes:\n%s", args, text)
		}
	}
}

func TestJSONText(t *testing.T) {
	for in, want := range map[string]string{
		"{\n  \"a\": \"ünï 中 \\u001b\"\n}\n": "{\n  \"a\": \"ünï 中 \\u001b\"\n}\n",
		"\"\x7f\u009b\u202e\u00a0\"":         `"\u007f\u009b\u202e\u00a0"`,
		"\"\U000E0001\"":                     `"\udb40\udc01"`, // a tag character, beyond the basic plane
		"\"\U0001F600\"":                     "\"\U0001F600\"",
	} {
		got := jsonText(in)
		if got != want {
			t.Errorf("jsonText(%q) = %s, want %s", in, got, want)
		}
		var a, b any
		if json.Unmarshal([]byte(in), &a) != nil || json.Unmarshal([]byte(got), &b) != nil || !reflect.DeepEqual(a, b) {
			t.Errorf("jsonText(%q) = %s decodes to another value", in, got)
		}
	}
}

func TestShownAndPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"":                   "",
		"plain text, ünï 中":  "plain text, ünï 中",
		`a "quoted" \ word`:  `a "quoted" \ word`,
		"a\x1bb":             `"a\x1bb"`,
		"line\nbreak":        `"line\nbreak"`,
		"tab\t":              `"tab\t"`,
		"\x7f":               `"\x7f"`,
		"c1 \u009b":          `"c1 \u009b"`,
		"rlo \u202e":         `"rlo \u202e"`,
		"nbsp\u00a0":         `"nbsp\u00a0"`,
		"not UTF-8 \xff\x9b": `"not UTF-8 \xff\x9b"`,
		"a\x1b\"b":           `"a\x1b\"b"`,
	} {
		if got := shown(in); got != want {
			t.Errorf("shown(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"":                             "",
		"jsonl: line 3: plain, ünï 中":  "jsonl: line 3: plain, ünï 中",
		`tag "k" twice \ `:             `tag "k" twice \ `,
		"a\x1b[2Jb\ac":                 `a\x1b[2Jb\ac`,
		"two\nlines\r\n":               `two\nlines\r\n`,
		"\x7f\u009b\u202e\u00a0":       `\x7f\u009b\u202e\u00a0`,
		"not UTF-8 \xff\x9b, then \t.": `not UTF-8 \xff\x9b, then \t.`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %s, want %s", in, got, want)
		}
	}
}
