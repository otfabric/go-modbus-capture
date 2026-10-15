// SPDX-License-Identifier: MIT

package mbcsv

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

const cols = "t,at,s,fc,req,resp,id,target,unit,version,tick_ns,wall_start\n"
const head = "header,,,,,,,,,0.1,1000,2026-10-14T13:14:24Z\n"
const defs = "target,,,,,,1,,1,,,\nstream,,,,,,1,1,,,,\n"

func read(s string) (*capture.Capture, error) {
	r, err := NewReader(strings.NewReader(s))
	if err != nil {
		return nil, err
	}
	return capture.ReadAll(r)
}

func TestReaderRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		samples int
		err     error
	}{
		{"a subset of the columns", cols + head + defs + "s,5,1,03,0300000001,03020007,,,,,,\n", 1, nil},
		{"columns in another order, and an extra one",
			"note,resp,req,s,at,t,id,target,unit,tick_ns,version,wall_start\n" +
				"x,,,,,header,,,,1000,0.1,2026-10-14T13:14:24Z\n" +
				"x,,,,,target,1,,1,,,\nx,,,,,stream,1,1,,,,\n" +
				"x,03020007,0300000001,1,5,s,,,,,,\n", 1, nil},
		{"fc may be left empty", cols + head + defs + "s,5,1,,0300000001,03020007,,,,,,\n", 1, nil},
		{"unknown record types are ignored", cols + head + "comment,,,,,,,,,,,\n" + defs, 0, nil},
		{"CRLF line ends", strings.ReplaceAll(cols+head+defs, "\n", "\r\n"), 0, nil},
		{"fc does not match", cols + head + defs + "s,5,1,04,0300000001,03020007,,,,,,\n", 0, capture.ErrCorrupt},
		{"no t column", "at,s\n5,1\n", 0, capture.ErrCorrupt},
		{"a column twice", "t,t\nheader,header\n", 0, capture.ErrCorrupt},
		{"first row not a header", cols + defs, 0, capture.ErrCorrupt},
		{"a second header", cols + head + head, 0, capture.ErrCorrupt},
		{"a row with too few fields", cols + head + "target,,,\n", 0, capture.ErrCorrupt},
		{"unknown flag", "t,version,tick_ns,wall_start,flags\nheader,0.1,1,2026-10-14T13:14:24Z,turbo\n", 0, capture.ErrCorrupt},
		{"bad number", cols + head + "target,,,,,,one,,1,,,\n", 0, capture.ErrCorrupt},
		{"last row without newline", cols + head + defs + "s,5,1,03,0300000001,03020007,,,,,,", 0, capture.ErrTruncated},
		{"cut inside a quoted field", cols + head + `target,,,,,,1,,1,,,"abc`, 0, capture.ErrTruncated},
		// Regressions (found by TestChaos): a cut that leaves the last row or the
		// column names short was reported as corruption.
		{"last row cut to too few fields", cols + head + defs + "s,5,1,03", 0, capture.ErrTruncated},
		{"header row cut to too few fields", cols + "header,,,", 0, capture.ErrTruncated},
		{"column names cut", "t,at,s,fc,req,resp,id,target,unit,version,tick_ns,wall_start,t", 0, capture.ErrTruncated},
		{"a short row before others", cols + head + "target,,,\n" + defs, 0, capture.ErrCorrupt},
		{"empty input", "", 0, capture.ErrTruncated},
		{"only column names", cols, 0, capture.ErrTruncated},
		{"too long a row", cols + head + "note," + strings.Repeat("a", MaxRecordLen+10) + "\n", 0, capture.ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := read(tc.in)
			if tc.err == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if len(c.Samples) != tc.samples {
					t.Fatalf("%d samples, want %d", len(c.Samples), tc.samples)
				}
				return
			}
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

func TestWide(t *testing.T) {
	regs := func(vals ...byte) []byte { return append([]byte{0x03, byte(len(vals))}, vals...) }
	c := &capture.Capture{
		Header:  capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0).UTC(), Exceptions: true},
		Targets: []capture.Target{{ID: 1}, {ID: 2}},
		Streams: []capture.Stream{{ID: 1, Target: 1}, {ID: 2, Target: 2}, {ID: 3, Target: 1}},
		Samples: []capture.Sample{
			{Stream: 1, At: 10, Request: []byte{0x03, 0, 100, 0, 2}, Response: regs(0, 1, 0, 2)},
			{Stream: 2, At: 10, Request: []byte{0x01, 0, 0, 0, 3}, Response: []byte{0x01, 1, 0b101}},
			// Another stream of target 1 reads an overlapping range later.
			{Stream: 3, At: 30, Request: []byte{0x03, 0, 101, 0, 2}, Response: regs(0, 9, 0xff, 0xff)},
			{Stream: 1, At: 20, Request: []byte{0x03, 0, 100, 0, 2}, Response: regs(0, 5, 0, 2)},
			{Stream: 1, At: 40, Request: []byte{0x03, 0, 100, 0, 1}, Response: []byte{0x83, 2}},
			{Stream: 1, At: 50, Request: []byte{0x07}, Response: []byte{0x07, 1}},
		},
		End: capture.End{Known: true, At: 60},
	}
	var buf bytes.Buffer
	st, err := WriteWide(&buf, c)
	if err != nil {
		t.Fatal(err)
	}
	want := "at,1/03/100,1/03/101,1/03/102,2/01/0,2/01/1,2/01/2\n" +
		"10,1,2,,1,0,1\n" +
		"20,5,2,,1,0,1\n" +
		"30,5,9,65535,1,0,1\n"
	if buf.String() != want {
		t.Fatalf("got\n%swant\n%s", buf.String(), want)
	}
	if st.Rows != 3 || st.Columns != 6 || st.Skipped != 1 {
		t.Fatalf("stats %+v", st)
	}
}
