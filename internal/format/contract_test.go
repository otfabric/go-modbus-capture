// SPDX-License-Identifier: MIT

package format

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/mbcap"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

// Long CSV is detected by its row of column names, wherever the t column
// stands in it and however long the row is (docs/FORMAT.md sections 4 and 7).
func TestDetectCSVByColumnNames(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	for name, tc := range map[string]struct {
		in   string
		want Format
	}{
		"t first":                         {"t,at,s\n", CSV},
		"t last":                          {"at,s,t\n", CSV},
		"t last, CRLF":                    {"at,s,t\r\n", CSV},
		"t last, no line end":             {"at,s,t", CSV},
		"t alone":                         {"t\n", CSV},
		"t quoted, last":                  {"at,s,\"t\"\n", CSV},
		"t after 64 bytes":                {long(64) + ",at,t,s\n", CSV},
		"t after 100 names":               {strings.Repeat("note,", 100) + "t\nheader\n", CSV},
		"t after a name of 5000 bytes":    {long(5000) + ",t\n", CSV},
		"t after a quoted name of lines":  {"\"a\nb,\"\"c\"\"\n\",t\n", CSV},
		"empty lines first":               {"\n\nat,t\n", CSV},
		"no t column":                     {"at,s,target,tags\n", Unknown},
		"t only as part of a name":        {"at,target,tags,transport,tick_ns\n", Unknown},
		"t only in the second row":        {"at,s\nt,t\n", Unknown},
		"t in another case":               {"at,T\n", Unknown},
		"t with a space":                  {"at, t\n", Unknown},
		"a wide CSV":                      {"at,1/03/4096,1/03/4097\n0,1,2\n", Unknown},
		"text":                            {"hello", Unknown},
		"a quote that never closes":       {"at,\"t\n", Unknown},
		"JSON Lines wins over a column t": {"{\"t\":\"header\"},t\n", JSONL},
	} {
		if got := Detect(bufio.NewReaderSize(strings.NewReader(tc.in), 64<<10)); got != tc.want {
			t.Errorf("%s: Detect = %v, want %v", name, got, tc.want)
		}
	}
	// The row is looked at as far as the buffer reaches: a t column within
	// it is found, one beyond it is not, and a name cut by the end of the
	// buffer is not taken for a column.
	for name, tc := range map[string]struct {
		in   string
		want Format
	}{
		"t within the buffer":        {long(4000) + ",t," + long(200) + ",at\n", CSV},
		"t beyond the buffer":        {long(5000) + ",t\n", Unknown},
		"a name cut after its t":     {long(4094) + ",target\n", Unknown},
		"t at the end of the buffer": {long(4093) + ",t,at\n", CSV},
	} {
		if got := Detect(bufio.NewReaderSize(strings.NewReader(tc.in), 4096)); got != tc.want {
			t.Errorf("a buffer of 4096 bytes, %s: Detect = %v, want %v", name, got, tc.want)
		}
	}
	// The same whatever the size of the pieces the input comes in.
	for _, in := range []string{"at,s,t\n", "at,\"a\nb\",t\r\nheader\n", long(5000) + ",t\n", "at,s,target\n", "\n \n{\"t\":1}\n"} {
		whole := Detect(bufio.NewReaderSize(strings.NewReader(in), 64<<10))
		if got := Detect(bufio.NewReaderSize(iotest.OneByteReader(strings.NewReader(in)), 64<<10)); got != whole {
			t.Errorf("%.20q a byte at a time: Detect = %v, want %v", in, got, whole)
		}
		if got := Detect(bufio.NewReaderSize(iotest.HalfReader(strings.NewReader(in)), 64<<10)); got != whole {
			t.Errorf("%.20q in halves: Detect = %v, want %v", in, got, whole)
		}
	}
	// Detect consumes nothing, and such a file opens and reads.
	const file = "at,s,req,resp,id,target,unit,version,tick_ns,wall_start,note,t\n" +
		",,,,,,,0.1,1000,2026-10-14T13:14:24Z,,header\n" +
		",,,,1,,1,,,,,target\n,,,,1,1,,,,,,stream\n" +
		"5,1,0300000001,03020007,,,,,,,a note,s\n"
	r, f, err := Open(strings.NewReader(file))
	if err != nil || f != CSV {
		t.Fatalf("Open: %v, %v", f, err)
	}
	if c, err := capture.ReadAll(r); err != nil || len(c.Samples) != 1 {
		t.Fatalf("read %+v, %v", c, err)
	}
}

// Detection waits for no more input than the first row: a row of column
// names on a pipe that then stays open and silent is enough. (In the bubble,
// a Detect that waited for more would be reported as a deadlock.)
func TestDetectWaitsForTheFirstRowOnly(t *testing.T) {
	names := append([]string(nil), mbcsv.Columns[1:]...)
	row := strings.Join(append(names, mbcsv.Columns[0]), ",") + "\n" // t last
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		go func() { _, _ = pw.Write([]byte(row)) }()
		if got := Detect(bufio.NewReaderSize(pr, 64<<10)); got != CSV {
			t.Errorf("Detect = %v, want %v", got, CSV)
		}
		_ = pr.Close()
	})
	// A row that arrives in pieces is waited for, piece by piece.
	synctest.Test(t, func(t *testing.T) {
		pr, pw := io.Pipe()
		go func() {
			for _, piece := range []string{"at,s,target,", "\"a\nb\",", "t", "\n"} {
				_, _ = pw.Write([]byte(piece))
			}
		}()
		if got := Detect(bufio.NewReaderSize(pr, 64<<10)); got != CSV {
			t.Errorf("a row in pieces: Detect = %v, want %v", got, CSV)
		}
		_ = pr.Close()
	})
}

// What the capture.Reader and capture.Writer comments say of the encodings:
// a second Close and a write after Close are errors in every encoding, the
// first error of the binary reader sticks, and the text readers go on with
// the line or row after the one in error.
func TestReaderAndWriterContracts(t *testing.T) {
	h := capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}
	good := capture.Sample{Stream: 1, At: 5, Request: []byte{3, 0, 0, 0, 1}, Response: []byte{3, 2, 0, 7}}
	for _, f := range []Format{Binary, JSONL, CSV} {
		var buf bytes.Buffer
		w, err := NewWriter(f, &buf, h, &mbcap.Options{BlockSamples: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(w.WriteTarget(capture.Target{ID: 1}), w.WriteStream(capture.Stream{ID: 1, Target: 1}), w.WriteSample(good)); err != nil {
			t.Fatalf("%v: %v", f, err)
		}
		if err := w.Close(capture.End{Known: true, At: 1}); !errors.Is(err, capture.ErrCorrupt) {
			t.Fatalf("%v: Close with an end before the sample: %v", f, err)
		}
		if err := w.Close(capture.End{Known: true, At: 9}); err != nil {
			t.Fatalf("%v: Close after a refused end: %v", f, err)
		}
		if err := w.Close(capture.End{Known: true, At: 9}); err == nil {
			t.Errorf("%v: a second Close succeeded", f)
		}
		if err := errors.Join(w.WriteTarget(capture.Target{ID: 2})); err == nil {
			t.Errorf("%v: WriteTarget after Close succeeded", f)
		}
		if err := w.WriteStream(capture.Stream{ID: 2, Target: 1}); err == nil {
			t.Errorf("%v: WriteStream after Close succeeded", f)
		}
		if err := w.WriteSample(good); err == nil {
			t.Errorf("%v: WriteSample after Close succeeded", f)
		}
		if c := decode(t, f, buf.Bytes()); len(c.Samples) != 1 || c.End != (capture.End{Known: true, At: 9}) {
			t.Errorf("%v: read back %+v", f, c)
		}
	}

	// A capture whose second record does not decode, with records after it.
	binary := func() []byte {
		var buf bytes.Buffer
		w, err := mbcap.NewWriter(&buf, h, &mbcap.Options{BlockSamples: 1, Compression: mbcap.NoCompression})
		if err != nil {
			t.Fatal(err)
		}
		if err := errors.Join(w.WriteTarget(capture.Target{ID: 1}), w.WriteStream(capture.Stream{ID: 1, Target: 1}), w.Flush()); err != nil {
			t.Fatal(err)
		}
		cut := buf.Len()
		if err := errors.Join(w.WriteTarget(capture.Target{ID: 2}), w.WriteTarget(capture.Target{ID: 3}), w.Close(capture.End{})); err != nil {
			t.Fatal(err)
		}
		b := buf.Bytes()
		b[cut+2] = 0 // the id of target 2: 0 is not a target id; the CRC no longer matches either
		return b
	}()
	for _, tc := range []struct {
		f      Format
		in     []byte
		sticky bool
	}{
		{Binary, binary, true},
		{JSONL, []byte(`{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24Z"}` + "\n" +
			`{"t":"target","id":1,"unit":1}` + "\n" + `{"t":"target","id":0,"unit":1}` + "\n" + `{"t":"target","id":3,"unit":1}` + "\n"), false},
		{CSV, []byte("t,id,unit,version,tick_ns,wall_start\nheader,,,0.1,1000,2026-10-14T13:14:24Z\ntarget,1,1,,,\ntarget,0,1,,,\ntarget,3,1,,,\n"), false},
	} {
		r, err := NewReader(tc.f, bytes.NewReader(tc.in))
		if err != nil {
			t.Fatalf("%v: %v", tc.f, err)
		}
		if rec, err := r.Next(); err != nil || rec.(capture.Target).ID != 1 {
			t.Fatalf("%v: first record %v, %v", tc.f, rec, err)
		}
		if tc.f == Binary {
			if _, err := r.Next(); err != nil { // the stream
				t.Fatal(err)
			}
		}
		_, first := r.Next()
		if !errors.Is(first, capture.ErrCorrupt) {
			t.Fatalf("%v: the record in error: %v", tc.f, first)
		}
		rec, again := r.Next()
		switch {
		case tc.sticky && (again == nil || again.Error() != first.Error()):
			t.Errorf("%v: Next after an error: %v, %v; want the same error %v", tc.f, rec, again, first)
		case !tc.sticky && (again != nil || rec.(capture.Target).ID != 3):
			t.Errorf("%v: Next after an error: %v, %v; want the record after the one in error", tc.f, rec, again)
		}
	}
}
