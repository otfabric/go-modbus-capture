// SPDX-License-Identifier: MIT

// Package format detects capture encodings and opens readers and writers for
// them by name.
package format

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/jsonl"
	"github.com/otfabric/go-modbus-capture/mbcap"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

// Format is a capture encoding.
type Format int

// Formats. Wide CSV can only be written.
const (
	Unknown Format = iota
	Binary
	JSONL
	CSV
	WideCSV
)

var names = map[Format]string{Binary: "mbcap", JSONL: "jsonl", CSV: "csv", WideCSV: "wide-csv"}

func (f Format) String() string {
	if n, ok := names[f]; ok {
		return n
	}
	return "unknown"
}

// Parse parses a format name: mbcap (or binary), jsonl, csv, wide-csv.
func Parse(s string) (Format, error) {
	switch strings.ToLower(s) {
	case "mbcap", "binary":
		return Binary, nil
	case "jsonl":
		return JSONL, nil
	case "csv":
		return CSV, nil
	case "wide-csv", "wide":
		return WideCSV, nil
	}
	return Unknown, fmt.Errorf("unknown format %q (want mbcap, jsonl, csv or wide-csv)", s)
}

// FromPath guesses the format from a file name: .mbcap, .jsonl, .csv
// (.wide.csv for wide CSV).
func FromPath(path string) Format {
	p := strings.ToLower(path)
	switch {
	case strings.HasSuffix(p, ".mbcap"):
		return Binary
	case strings.HasSuffix(p, ".jsonl"):
		return JSONL
	case strings.HasSuffix(p, ".wide.csv"):
		return WideCSV
	case strings.HasSuffix(p, ".csv"):
		return CSV
	}
	return Unknown
}

// Detect looks at the start of br without consuming it: the magic number of
// the binary encoding, a leading "{" for JSON Lines, or a CSV row of column
// names with a column t, wherever it stands in the row (docs/FORMAT.md
// sections 4 and 7). It looks no further than the first row, and at most as
// far as the buffer of br reaches; it waits for more input only while that
// row is not complete.
func Detect(br *bufio.Reader) Format {
	head, _ := br.Peek(len(mbcap.Magic))
	if string(head) == mbcap.Magic {
		return Binary
	}
	for n, seen := len(head), 0; ; n = len(head) + 1 {
		_, err := br.Peek(min(n, br.Size()))
		head, _ = br.Peek(br.Buffered()) // all that is there
		if bytes.HasPrefix(bytes.TrimLeft(head, " \t\r\n"), []byte("{")) {
			return JSONL
		}
		// A row ends with a line end: the row is parsed again only when
		// one has arrived, or when no more input will be looked at.
		last := err != nil || len(head) == br.Size()
		arrived := head[seen:]
		if seen = len(head); !last && bytes.IndexByte(arrived, '\n') < 0 {
			continue
		}
		names, whole := columnNames(head)
		switch {
		case whole || err != nil:
			// The whole row, or all the input holds of it.
		case last:
			// The row goes on beyond the buffer: its last name may be cut.
			names = names[:max(len(names)-1, 0)]
		default:
			continue
		}
		if slices.Contains(names, "t") {
			return CSV
		}
		return Unknown
	}
}

// columnNames parses the first CSV row of head. whole is false when the row
// may go on beyond head.
func columnNames(head []byte) (names []string, whole bool) {
	cr := csv.NewReader(bytes.NewReader(head))
	names, err := cr.Read()
	return names, err == nil && (cr.InputOffset() < int64(len(head)) || bytes.HasSuffix(head, []byte("\n")))
}

// Open detects the encoding of r and returns a reader for it.
func Open(r io.Reader) (capture.Reader, Format, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	f := Detect(br)
	cr, err := NewReader(f, br)
	return cr, f, err
}

// NewReader returns a reader for f.
func NewReader(f Format, r io.Reader) (capture.Reader, error) {
	switch f {
	case Binary:
		return mbcap.NewReader(r)
	case JSONL:
		return jsonl.NewReader(r)
	case CSV:
		return mbcsv.NewReader(r)
	default:
		return nil, fmt.Errorf("%w: not a capture in a known format", capture.ErrCorrupt)
	}
}

// NewWriter returns a writer for f. opts applies to Binary only. Wide CSV is
// not a streaming encoding; it has no Writer (see mbcsv.WriteWide).
func NewWriter(f Format, w io.Writer, h capture.Header, opts *mbcap.Options) (capture.Writer, error) {
	switch f {
	case Binary:
		return mbcap.NewWriter(w, h, opts)
	case JSONL:
		return jsonl.NewWriter(w, h)
	case CSV:
		return mbcsv.NewWriter(w, h)
	default:
		return nil, fmt.Errorf("format %v cannot be written as a capture", f)
	}
}
