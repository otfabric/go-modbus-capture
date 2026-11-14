// SPDX-License-Identifier: MIT

package mbcsv

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
)

// A row holds at most maxColumns fields, the row of column names included.
// A row with more ends the reading before the CSV reader has taken it in, so
// that it costs memory in proportion to the limit, not to the row.
func TestColumnLimit(t *testing.T) {
	const known = "t,version,tick_ns,wall_start,at"
	const row = "header,0.1,1000,2026-10-14T13:14:24Z,"
	const end = "end,,,,7"
	// file has n columns: the five above and n-5 that the format does not
	// know, with a header row and an end row of as many fields.
	file := func(n int) string {
		pad := strings.Repeat(",", n-5)
		return known + pad + "\n" + row + pad + "\n" + end + pad + "\n"
	}
	for _, n := range []int{5, 6, maxColumns - 1, maxColumns} {
		if c, err := read(file(n)); err != nil || c.End != (capture.End{Known: true, At: 7}) {
			t.Errorf("%d columns: %+v, %v", n, c, err)
		}
	}
	for _, n := range []int{maxColumns + 1, maxColumns + 2, 100_000} {
		in := file(n)
		if _, err := read(in); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "columns") {
			t.Errorf("%d columns: err = %v, want ErrCorrupt naming the columns", n, err)
		}
		const budget = 1 << 20
		if got := fuzzcap.AllocBytes(4, func() { _, _ = read(in) }); got > budget {
			t.Errorf("a column row of %d names allocated %d bytes, budget %d", n, got, budget)
		}
	}

	// The same when the input comes a byte at a time.
	if _, err := NewReader(iotest.OneByteReader(strings.NewReader(file(maxColumns)))); err != nil {
		t.Errorf("%d columns, a byte at a time: %v", maxColumns, err)
	}
	if _, err := NewReader(iotest.OneByteReader(strings.NewReader(file(maxColumns + 1)))); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "columns") {
		t.Errorf("%d columns, a byte at a time: err = %v, want ErrCorrupt naming the columns", maxColumns+1, err)
	}

	// A later row with more fields than the limit, in a file of few columns.
	wide := known + "\n" + row + "\n" + "note" + strings.Repeat(",", 100_000) + "\n"
	if _, err := read(wide); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "columns") {
		t.Errorf("a row of 100001 fields: err = %v, want ErrCorrupt naming the columns", err)
	}
	const budget = 1 << 20
	if got := fuzzcap.AllocBytes(4, func() { _, _ = read(wide) }); got > budget {
		t.Errorf("a row of 100001 fields allocated %d bytes, budget %d", got, budget)
	}
	// The rows before it are returned first.
	r, err := NewReader(strings.NewReader(known + "\n" + row + "\n" + end + "\n" + "note" + strings.Repeat(",", 100_000) + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := r.Next(); err != nil || rec != (capture.End{Known: true, At: 7}) {
		t.Fatalf("the row before the long one: %v, %v", rec, err)
	}
	if _, err := r.Next(); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("the long row: %v", err)
	}

	// Separators inside a quoted field are text, not fields: one name of
	// many commas is one column, also when it holds quotes and line breaks.
	name := `"` + strings.Repeat(",", 2*maxColumns) + `""` + "\n" + strings.Repeat(",", 2*maxColumns) + `"`
	quoted := known + "," + name + "\n" + row + ",\n"
	if _, err := read(quoted); err != nil {
		t.Errorf("a quoted column name of %d commas: %v", 4*maxColumns, err)
	}
	// The count starts again with every row.
	var many strings.Builder
	many.WriteString(known + strings.Repeat(",", 95) + "\n" + row + strings.Repeat(",", 95) + "\n")
	for range 100 {
		many.WriteString("note" + strings.Repeat(",", 99) + "\n")
	}
	if _, err := read(many.String()); err != nil {
		t.Errorf("100 rows of 100 fields: %v", err)
	}
}

// A row longer than MaxRecordLen is refused, and reading it costs memory in
// proportion to the limit, not to the row.
func TestRecordLimit(t *testing.T) {
	note := func(n int) string { return "note," + strings.Repeat("a", n-len("note,,,,,,,,,,,\n")) + ",,,,,,,,,,\n" }
	if _, err := read(cols + head + note(MaxRecordLen)); err != nil {
		t.Fatalf("a row of MaxRecordLen bytes: %v", err)
	}
	long := cols + head + note(16*MaxRecordLen)
	if _, err := read(long); !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), "longer than") {
		t.Fatalf("a row of %d bytes: err = %v, want ErrCorrupt", 16*MaxRecordLen, err)
	}
	const budget = 12 * MaxRecordLen
	if got := fuzzcap.AllocBytes(2, func() { _, _ = read(long) }); got > budget {
		t.Fatalf("a row of %d bytes allocated %d bytes, budget %d", 16*MaxRecordLen, got, budget)
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
