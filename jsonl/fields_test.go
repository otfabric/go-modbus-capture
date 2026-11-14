// SPDX-License-Identifier: MIT

package jsonl

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

// The lines of a small capture, one of each type, with every optional field.
const (
	fullHeader = `{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z","producer":"p","flags":{"exceptions":true},"tags":{"site":"lab"}}`
	fullTarget = `{"t":"target","id":1,"label":"a","transport":"rtu","host":"h","port":502,"serial":{"device":"/dev/x","baud":9600,"parity":"even","data_bits":8,"stop_bits":1},"unit":7,"tags":{"bus":"A"}}`
	fullStream = `{"t":"stream","id":1,"target":1,"role":"poll","label":"l"}`
	fullSample = `{"t":"s","s":1,"at":5,"req":"0300000001","resp":"03020007"}`
	fullEnd    = `{"t":"end","at":9}`
)

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

// with returns line with more members put in front of its closing brace.
func with(line, members string) string { return line[:len(line)-1] + "," + members + "}" }

// Field names are matched exactly (docs/FORMAT.md section 3). A field whose
// name differs from a defined one only in case is an unknown field: the line
// reads as it does without it.
func TestFieldNamesAreExact(t *testing.T) {
	plain := []string{fullHeader, fullTarget, fullStream, fullSample, fullEnd}
	want, err := read(lines(plain...))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		line    int // index in plain
		members string
	}{
		{"header version", 0, `"Version":"9.9"`},
		{"header tick_ns", 0, `"TICK_NS":5`},
		{"header wall_start", 0, `"Wall_Start":"1999-01-01T00:00:00Z"`},
		{"header producer", 0, `"Producer":"other"`},
		{"header flags", 0, `"Flags":{"change_only":true}`},
		{"header tags", 0, `"TAGS":{"site":"other","more":"x"}`},
		{"target id", 1, `"ID":9`},
		{"target label", 1, `"Label":"other"`},
		{"target transport", 1, `"Transport":"tcp"`},
		{"target host", 1, `"HOST":"other"`},
		{"target port", 1, `"Port":1`},
		{"target serial", 1, `"Serial":{"device":"/dev/other"}`},
		{"target unit", 1, `"Unit":9`},
		{"target tags", 1, `"Tags":{"bus":"B"}`},
		{"stream id", 2, `"Id":9`},
		{"stream target", 2, `"Target":9`},
		{"stream role", 2, `"ROLE":"scan"`},
		{"stream label", 2, `"lAbel":"other"`},
		{"sample s", 3, `"S":9`},
		{"sample at", 3, `"AT":99,"At":98,"aT":97`},
		{"sample req", 3, `"REQ":"0400000001"`},
		{"sample resp", 3, `"Resp":"0302ffff"`},
		{"end at", 4, `"AT":99999`},
		{"a field of the wrong shape in another case", 3, `"AT":"not a number","S":[1],"REQ":{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]string(nil), plain...)
			in[tc.line] = with(in[tc.line], tc.members)
			got, err := read(lines(in...))
			if err != nil {
				t.Fatal(err)
			}
			if d := capture.Diff(want, got); d != "" {
				t.Fatalf("the field changed the capture: %s", d)
			}
		})
	}

	// The same inside the two objects of the format.
	nested := []string{
		strings.Replace(fullHeader, `{"exceptions":true}`, `{"exceptions":true,"Change_Only":true,"EXCEPTIONS":false}`, 1),
		strings.Replace(fullTarget, `"stop_bits":1}`, `"stop_bits":1,"Device":"/dev/other","BAUD":1200,"Parity":"odd","Data_Bits":7,"Stop_Bits":2}`, 1),
		fullStream, fullSample, fullEnd,
	}
	got, err := read(lines(nested...))
	if err != nil {
		t.Fatal(err)
	}
	if d := capture.Diff(want, got); d != "" {
		t.Fatalf("a field of flags or serial in another case changed the capture: %s", d)
	}

	// The line type too: a line whose t is unknown, or that has no t, stays
	// an unknown line whatever a "T" says.
	for _, line := range []string{
		with(`{"t":"note"`+fullSample[8:len(fullSample)-1]+"}", `"T":"s"`),
		`{"T":"s","s":1,"at":6,"req":"0300000001","resp":"03020008"}`,
		`{"T":"target","id":2,"unit":1}`,
		`{"T":"end","at":7}`,
	} {
		got, err := read(lines(fullHeader, fullTarget, fullStream, fullSample, line, fullEnd))
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if d := capture.Diff(want, got); d != "" {
			t.Fatalf("%s: the line was not ignored: %s", line, d)
		}
	}
	// A first line that says header only in another case is not a header.
	if _, err := read(lines(strings.Replace(fullHeader, `"t":"header"`, `"T":"header"`, 1))); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("a first line without t: %v", err)
	}
}

// A defined field that appears twice in a line, or twice in its flags or
// serial object, is an error. Fields that are not defined for the line may
// repeat: they are ignored.
func TestRepeatedFields(t *testing.T) {
	plain := []string{fullHeader, fullTarget, fullStream, fullSample, fullEnd}
	for _, tc := range []struct {
		name    string
		line    int
		members string
		field   string
	}{
		{"header t", 0, `"t":"header"`, "t"},
		{"header version", 0, `"version":"0.1"`, "version"},
		{"header tick_ns", 0, `"tick_ns":1000`, "tick_ns"},
		{"header wall_start", 0, `"wall_start":"2026-10-14T13:14:24Z"`, "wall_start"},
		{"header producer", 0, `"producer":"p"`, "producer"},
		{"header flags", 0, `"flags":{}`, "flags"},
		{"header tags", 0, `"tags":{}`, "tags"},
		{"target t", 1, `"t":"target"`, "t"},
		{"target id", 1, `"id":1`, "id"},
		{"target label", 1, `"label":"a"`, "label"},
		{"target transport", 1, `"transport":"rtu"`, "transport"},
		{"target host", 1, `"host":"h"`, "host"},
		{"target port", 1, `"port":502`, "port"},
		{"target serial", 1, `"serial":{}`, "serial"},
		{"target unit", 1, `"unit":7`, "unit"},
		{"target tags", 1, `"tags":null`, "tags"},
		{"stream id", 2, `"id":1`, "id"},
		{"stream target", 2, `"target":1`, "target"},
		{"stream role", 2, `"role":"poll"`, "role"},
		{"stream label", 2, `"label":"l"`, "label"},
		{"sample t", 3, `"t":"s"`, "t"},
		{"sample s", 3, `"s":1`, "s"},
		{"sample at", 3, `"at":5`, "at"},
		{"sample at with another value", 3, `"at":99`, "at"},
		{"sample req", 3, `"req":"0300000001"`, "req"},
		{"sample resp", 3, `"resp":"03020007"`, "resp"},
		{"end t", 4, `"t":"end"`, "t"},
		{"end at", 4, `"at":9`, "at"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]string(nil), plain...)
			in[tc.line] = with(in[tc.line], tc.members)
			_, err := read(lines(in...))
			if !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), `field "`+tc.field+`" appears twice`) {
				t.Fatalf("err = %v, want ErrCorrupt naming the field %q", err, tc.field)
			}
			if want := "line " + string(rune('1'+tc.line)); !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want it to name %s", err, want)
			}
		})
	}
	for name, in := range map[string]string{
		"flags change_only": strings.Replace(fullHeader, `{"exceptions":true}`, `{"change_only":false,"exceptions":true,"change_only":true}`, 1),
		"flags exceptions":  strings.Replace(fullHeader, `{"exceptions":true}`, `{"exceptions":true,"exceptions":true}`, 1),
		"serial device":     lines(fullHeader, strings.Replace(fullTarget, `"baud":9600`, `"baud":9600,"device":"/dev/y"`, 1)),
		"serial baud":       lines(fullHeader, strings.Replace(fullTarget, `"baud":9600`, `"baud":9600,"baud":1200`, 1)),
		"serial parity":     lines(fullHeader, strings.Replace(fullTarget, `"baud":9600`, `"baud":9600,"parity":"odd"`, 1)),
		"serial data_bits":  lines(fullHeader, strings.Replace(fullTarget, `"baud":9600`, `"baud":9600,"data_bits":7`, 1)),
		"serial stop_bits":  lines(fullHeader, strings.Replace(fullTarget, `"baud":9600`, `"baud":9600,"stop_bits":2`, 1)),
	} {
		if !strings.HasSuffix(in, "\n") {
			in += "\n"
		}
		if _, err := read(in); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "appears twice") {
			t.Errorf("%s twice: err = %v, want ErrCorrupt", name, err)
		}
	}

	want, err := read(lines(plain...))
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string][]string{
		"an unknown field":                     {fullHeader, fullTarget, fullStream, with(fullSample, `"note":1,"note":2`), fullEnd},
		"a field of another line type":         {fullHeader, with(fullTarget, `"at":1,"at":2`), fullStream, with(fullSample, `"id":1,"id":2`), fullEnd},
		"a field of a line of an unknown type": {fullHeader, fullTarget, fullStream, fullSample, `{"t":"note","at":1,"at":2,"id":3,"id":4}`, fullEnd},
		"a field in another case":              {fullHeader, fullTarget, fullStream, with(fullSample, `"AT":1,"AT":2`), fullEnd},
		"an unknown field of serial":           {fullHeader, strings.Replace(fullTarget, `"baud":9600`, `"baud":9600,"x":1,"x":2`, 1), fullStream, fullSample, fullEnd},
	} {
		got, err := read(lines(in...))
		if err != nil {
			t.Errorf("%s twice: %v", name, err)
			continue
		}
		if d := capture.Diff(want, got); d != "" {
			t.Errorf("%s twice changed the capture: %s", name, d)
		}
	}
	// The t of a line of an unknown type is still its t.
	if _, err := read(lines(fullHeader, `{"t":"note","t":"s"}`)); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("t twice on a line of an unknown type: %v", err)
	}
}

// Lines that are not JSON objects are refused as before, except null, which
// has no type and is ignored like a line of an unknown type.
func TestLinesThatAreNotObjects(t *testing.T) {
	for _, line := range []string{`[1,2]`, `"t"`, `5`, `true`, `{"t":5}`, `{"t":"s"`, `{"t":"s"} x`, `{"t":"s",}`} {
		if _, err := read(lines(fullHeader, line)); !errors.Is(err, capture.ErrCorrupt) {
			t.Errorf("%s: err = %v, want ErrCorrupt", line, err)
		}
	}
	if c, err := read(lines(fullHeader, `null`, ` {"t" : "note"} `)); err != nil || len(c.Samples) != 0 {
		t.Fatalf("null and a padded line: %v", err)
	}
	for _, first := range []string{`[1,2]`, `null`, `5`, `hello`, `{"t":"header"`} {
		if _, err := read(lines(first)); !errors.Is(err, capture.ErrCorrupt) {
			t.Errorf("first line %s: err = %v, want ErrCorrupt", first, err)
		}
	}
}

// A Close that refuses its end leaves the writer open: a valid Close
// afterwards completes the capture.
func TestWriterCloseAfterRefusedEnd(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, header())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(capture.Target{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 1}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteSample(capture.Sample{Stream: 1, At: 50, Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 7}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(capture.End{Known: true, At: 10}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("end before the sample: %v", err)
	}
	if err := w.Close(capture.End{Known: true, At: 60}); err != nil {
		t.Fatalf("a valid Close after the refused one: %v", err)
	}
	c, err := read(buf.String())
	if err != nil || len(c.Samples) != 1 || c.End != (capture.End{Known: true, At: 60}) {
		t.Fatalf("read back %+v, err %v", c, err)
	}
}

// What the reader returns is the caller's, and the writer keeps nothing of
// the caller's.
func TestOwnership(t *testing.T) {
	fuzzcap.CheckOwnership(t, codec)
}

// A line longer than MaxLineLen is refused, and reading it costs memory in
// proportion to the limit, not to the line.
func TestLineLimit(t *testing.T) {
	note := func(n int) string {
		return `{"t":"note","x":"` + strings.Repeat("a", n-len(`{"t":"note","x":""}`)-1) + `"}` + "\n"
	}
	if _, err := read(fullHeader + "\n" + note(MaxLineLen)); err != nil {
		t.Fatalf("a line of MaxLineLen bytes: %v", err)
	}
	if _, err := read(fullHeader + "\n" + note(MaxLineLen+1)); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("a line of MaxLineLen+1 bytes: err = %v, want ErrCorrupt", err)
	}
	long := fullHeader + "\n" + note(16*MaxLineLen)
	const budget = 8 * MaxLineLen
	if got := fuzzcap.AllocBytes(2, func() { _, _ = read(long) }); got > budget {
		t.Fatalf("a line of %d bytes allocated %d bytes, budget %d", 16*MaxLineLen, got, budget)
	}
}
