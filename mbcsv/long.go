// SPDX-License-Identifier: MIT

// Package mbcsv reads and writes captures as CSV. Long CSV (.mbcap.csv,
// docs/FORMAT.md section 4) is lossless: one row per record, the same records
// as JSON Lines, PDUs as lowercase hex. Wide CSV (section 5) is a one-way view
// of raw register and bit values over time, sample and hold applied.
package mbcsv

import (
	"bufio"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Columns are the long CSV columns, in the order a Writer writes them.
var Columns = []string{
	"t", "at", "s", "fc", "req", "resp",
	"id", "target", "role", "label",
	"transport", "host", "port", "unit",
	"serial_device", "baud", "parity", "data_bits", "stop_bits",
	"version", "tick_ns", "wall_start", "producer", "flags", "tags",
}

const (
	colT = iota
	colAt
	colS
	colFC
	colReq
	colResp
	colID
	colTarget
	colRole
	colLabel
	colTransport
	colHost
	colPort
	colUnit
	colSerialDevice
	colBaud
	colParity
	colDataBits
	colStopBits
	colVersion
	colTickNS
	colWallStart
	colProducer
	colFlags
	colTags
	numCols
)

// MaxRecordLen is the largest CSV record (row) a Reader accepts.
const MaxRecordLen = 4 << 20

// Record types (the t column), as in JSON Lines.
const (
	typeHeader = "header"
	typeTarget = "target"
	typeStream = "stream"
	typeSample = "s"
	typeEnd    = "end"
)

// Flag names in the flags column.
const (
	flagChangeOnly = "change_only"
	flagExceptions = "exceptions"
)

// Writer writes a capture as long CSV.
// It is not safe for concurrent use; separate Writers may run concurrently.
type Writer struct {
	cw     *csv.Writer
	ck     *capture.Checker
	row    []string
	closed bool
}

// NewWriter checks h and writes the column names and the header row.
func NewWriter(w io.Writer, h capture.Header) (*Writer, error) {
	ck, err := capture.NewChecker(h)
	if err != nil {
		return nil, err
	}
	lw := &Writer{cw: csv.NewWriter(w), ck: ck, row: make([]string, numCols)}
	if err := lw.cw.Write(Columns); err != nil {
		return nil, err
	}
	r := lw.reset(typeHeader)
	r[colVersion] = fmt.Sprintf("%d.%d", capture.VersionMajor, capture.VersionMinor)
	r[colTickNS] = strconv.FormatUint(h.TickNS, 10)
	r[colWallStart] = h.WallStart.UTC().Format(time.RFC3339Nano)
	r[colProducer] = h.Producer
	var flags []string
	if h.ChangeOnly {
		flags = append(flags, flagChangeOnly)
	}
	if h.Exceptions {
		flags = append(flags, flagExceptions)
	}
	r[colFlags] = strings.Join(flags, ";")
	if r[colTags], err = encodeTags(h.Tags); err != nil {
		return nil, err
	}
	return lw, lw.flushRow()
}

func (w *Writer) reset(t string) []string {
	for i := range w.row {
		w.row[i] = ""
	}
	w.row[colT] = t
	return w.row
}

func (w *Writer) flushRow() error {
	if w.closed {
		return errors.New("mbcsv: write after Close")
	}
	return w.cw.Write(w.row)
}

func encodeTags(tags map[string]string) (string, error) {
	if len(tags) == 0 {
		return "", nil
	}
	b, err := json.Marshal(tags)
	return string(b), err
}

func uintOrEmpty(v uint64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatUint(v, 10)
}

// WriteTarget writes a target row.
func (w *Writer) WriteTarget(t capture.Target) error {
	if err := w.ck.Target(t); err != nil {
		return err
	}
	r := w.reset(typeTarget)
	r[colID] = strconv.FormatUint(uint64(t.ID), 10)
	r[colLabel] = t.Label
	r[colTransport] = t.Transport.String()
	r[colHost] = t.Host
	r[colPort] = uintOrEmpty(uint64(t.Port))
	r[colUnit] = strconv.FormatUint(uint64(t.Unit), 10)
	r[colSerialDevice] = t.Serial.Device
	r[colBaud] = uintOrEmpty(uint64(t.Serial.Baud))
	if t.Serial.Parity != capture.ParityNone {
		r[colParity] = t.Serial.Parity.String()
	}
	r[colDataBits] = uintOrEmpty(uint64(t.Serial.DataBits))
	r[colStopBits] = uintOrEmpty(uint64(t.Serial.StopBits))
	var err error
	if r[colTags], err = encodeTags(t.Tags); err != nil {
		return err
	}
	return w.flushRow()
}

// WriteStream writes a stream row.
func (w *Writer) WriteStream(s capture.Stream) error {
	if err := w.ck.Stream(s); err != nil {
		return err
	}
	r := w.reset(typeStream)
	r[colID] = strconv.FormatUint(uint64(s.ID), 10)
	r[colTarget] = strconv.FormatUint(uint64(s.Target), 10)
	r[colRole] = s.Role
	r[colLabel] = s.Label
	return w.flushRow()
}

// WriteSample writes a sample row, unless the capture does not store it (see
// capture.Writer).
func (w *Writer) WriteSample(s capture.Sample) error {
	keep, err := w.ck.Admit(s)
	if err != nil || !keep {
		return err
	}
	r := w.reset(typeSample)
	r[colAt] = strconv.FormatUint(uint64(s.At), 10)
	r[colS] = strconv.FormatUint(uint64(s.Stream), 10)
	r[colFC] = hex.EncodeToString(s.Request[:1])
	r[colReq] = hex.EncodeToString(s.Request)
	r[colResp] = hex.EncodeToString(s.Response)
	return w.flushRow()
}

// Close writes the end row when the end is known, and flushes.
func (w *Writer) Close(end capture.End) error {
	if w.closed {
		return errors.New("mbcsv: Close called twice")
	}
	if err := w.ck.End(end); err != nil {
		return err
	}
	if end.Known {
		r := w.reset(typeEnd)
		r[colAt] = strconv.FormatUint(uint64(end.At), 10)
		if err := w.flushRow(); err != nil {
			return err
		}
	}
	w.closed = true
	w.cw.Flush()
	return w.cw.Error()
}

// Reader reads a capture from long CSV.
// It is not safe for concurrent use; separate Readers may run concurrently.
type Reader struct {
	cr     *csv.Reader
	budget *budgetReader
	cols   [numCols]int // index of each known column in a row, -1 if absent
	h      capture.Header
	ck     *capture.Checker
	width  int // number of columns
	ahead  []string
	aerr   error
	line   int
	ended  bool
}

// NewReader reads the column names and the header row.
func NewReader(r io.Reader) (*Reader, error) {
	b := &budgetReader{r: r}
	b.reset()
	cr := csv.NewReader(bufio.NewReaderSize(b, 4096))
	cr.ReuseRecord = false
	lr := &Reader{cr: cr, budget: b}
	names, err := cr.Read()
	if err == io.EOF {
		return nil, fmt.Errorf("mbcsv: %w: no column names", capture.ErrTruncated)
	}
	if err != nil {
		return nil, lr.csvErr(err)
	}
	lr.width = len(names)
	lr.advance()
	if lr.aerr == io.EOF && !b.endsWithNewline() {
		return nil, fmt.Errorf("mbcsv: %w: the column names have no newline", capture.ErrTruncated)
	}
	for i := range lr.cols {
		lr.cols[i] = -1
	}
	for i, n := range names {
		for c, want := range Columns {
			if n == want {
				if lr.cols[c] >= 0 {
					return nil, fmt.Errorf("mbcsv: %w: column %q twice", capture.ErrCorrupt, n)
				}
				lr.cols[c] = i
			}
		}
	}
	if lr.cols[colT] < 0 {
		return nil, fmt.Errorf("mbcsv: %w: no t column", capture.ErrCorrupt)
	}
	row, err := lr.next()
	if err == io.EOF {
		return nil, fmt.Errorf("mbcsv: %w: no header row", capture.ErrTruncated)
	}
	if err != nil {
		return nil, err
	}
	if lr.get(row, colT) != typeHeader {
		return nil, lr.corrupt("the first row is not a header")
	}
	h, err := lr.decodeHeader(row)
	if err != nil {
		return nil, lr.wrap(err)
	}
	if lr.ck, err = capture.NewChecker(h); err != nil {
		return nil, lr.wrap(err)
	}
	lr.h = h
	return lr, nil
}

// Header returns the capture header.
func (r *Reader) Header() capture.Header { return r.h }

// advance reads the row after the current one, so that next can tell a last
// row cut short (no newline at the end of the input) from a complete one.
func (r *Reader) advance() {
	r.budget.reset()
	r.ahead, r.aerr = r.cr.Read()
	var pe *csv.ParseError
	if errors.As(r.aerr, &pe) && errors.Is(pe.Err, csv.ErrFieldCount) && len(r.ahead) < r.width {
		// A row with too few fields may be the last one, cut short by the
		// end of the input: that is a truncation, not corruption.
		r.budget.reset()
		if _, err := r.cr.Read(); err == io.EOF && !r.budget.endsWithNewline() {
			r.aerr = fmt.Errorf("mbcsv: line %d: %w: last row has no newline", pe.Line, capture.ErrTruncated)
		}
	}
}

func (r *Reader) next() ([]string, error) {
	row, err := r.ahead, r.aerr
	if err != nil {
		if err == io.EOF {
			return nil, io.EOF
		}
		return nil, r.csvErr(err)
	}
	r.line, _ = r.cr.FieldPos(0)
	r.advance()
	if r.aerr == io.EOF && !r.budget.endsWithNewline() {
		return nil, fmt.Errorf("mbcsv: line %d: %w: last row has no newline", r.line, capture.ErrTruncated)
	}
	return row, nil
}

func (r *Reader) csvErr(err error) error {
	if errors.Is(err, errRecordTooLong) {
		return fmt.Errorf("mbcsv: %w: a row is longer than %d bytes", capture.ErrCorrupt, MaxRecordLen)
	}
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		if errors.Is(pe.Err, csv.ErrQuote) && r.budget.eof {
			return fmt.Errorf("mbcsv: line %d: %w: input ends inside a quoted field", pe.Line, capture.ErrTruncated)
		}
		return fmt.Errorf("mbcsv: line %d: %w: %v", pe.Line, capture.ErrCorrupt, pe.Err)
	}
	return err
}

// Next returns the next record, or io.EOF after the last one.
func (r *Reader) Next() (capture.Record, error) {
	for {
		row, err := r.next()
		if err != nil {
			return nil, err
		}
		t := r.get(row, colT)
		switch t {
		case typeHeader, typeTarget, typeStream, typeSample, typeEnd:
		default:
			continue // unknown record types are ignored
		}
		if r.ended {
			return nil, r.corrupt("%q row after the end row", t)
		}
		rec, err := r.decode(t, row)
		if err != nil {
			return nil, r.wrap(err)
		}
		return rec, nil
	}
}

func (r *Reader) get(row []string, col int) string {
	if i := r.cols[col]; i >= 0 && i < len(row) {
		return row[i]
	}
	return ""
}

func (r *Reader) decode(t string, row []string) (capture.Record, error) {
	p := parser{r: r, row: row}
	switch t {
	case typeHeader:
		return nil, invalid("a second header row: combine captures with mbcapctl cat or merge")
	case typeTarget:
		tg := capture.Target{
			ID:    uint32(p.uint(colID, 32, true)),
			Label: p.str(colLabel),
			Host:  p.str(colHost),
			Port:  uint16(p.uint(colPort, 16, false)),
			Unit:  uint8(p.uint(colUnit, 8, false)),
			Tags:  p.tags(colTags),
			Serial: capture.Serial{
				Device:   p.str(colSerialDevice),
				Baud:     uint32(p.uint(colBaud, 32, false)),
				DataBits: uint8(p.uint(colDataBits, 8, false)),
				StopBits: uint8(p.uint(colStopBits, 8, false)),
			},
		}
		if p.err == nil {
			tg.Transport, p.err = capture.ParseTransport(p.str(colTransport))
		}
		if p.err == nil {
			tg.Serial.Parity, p.err = capture.ParseParity(p.str(colParity))
		}
		if p.err != nil {
			return nil, invalid("target: %v", p.err)
		}
		return tg, r.ck.Target(tg)
	case typeStream:
		s := capture.Stream{
			ID:     uint32(p.uint(colID, 32, true)),
			Target: uint32(p.uint(colTarget, 32, true)),
			Role:   p.str(colRole),
			Label:  p.str(colLabel),
		}
		if p.err != nil {
			return nil, invalid("stream: %v", p.err)
		}
		return s, r.ck.Stream(s)
	case typeSample:
		s := capture.Sample{
			At:       capture.Ticks(p.uint(colAt, 64, true)),
			Stream:   uint32(p.uint(colS, 32, true)),
			Request:  p.hex(colReq),
			Response: p.hex(colResp),
		}
		if fc := p.str(colFC); p.err == nil && fc != "" && len(s.Request) > 0 &&
			!strings.EqualFold(fc, hex.EncodeToString(s.Request[:1])) {
			p.err = fmt.Errorf("fc %s does not match the request's function code %02x", fc, s.Request[0])
		}
		if p.err != nil {
			return nil, invalid("sample: %v", p.err)
		}
		return s, r.ck.Sample(s)
	default: // typeEnd
		e := capture.End{Known: true, At: capture.Ticks(p.uint(colAt, 64, true))}
		if p.err != nil {
			return nil, invalid("end: %v", p.err)
		}
		r.ended = true
		return e, r.ck.End(e)
	}
}

func (r *Reader) decodeHeader(row []string) (capture.Header, error) {
	p := parser{r: r, row: row}
	version := p.str(colVersion)
	if version == "" {
		return capture.Header{}, invalid("header row without version")
	}
	if _, _, err := capture.ParseVersion(version); err != nil {
		return capture.Header{}, err
	}
	h := capture.Header{TickNS: p.uint(colTickNS, 64, true), Producer: p.str(colProducer), Tags: p.tags(colTags)}
	if p.err == nil {
		var wall time.Time
		if wall, p.err = time.Parse(time.RFC3339Nano, p.str(colWallStart)); p.err == nil {
			h.WallStart = wall.UTC()
		}
	}
	if f := p.str(colFlags); f != "" && p.err == nil {
		for _, name := range strings.Split(f, ";") {
			switch name {
			case flagChangeOnly:
				h.ChangeOnly = true
			case flagExceptions:
				h.Exceptions = true
			default:
				p.err = fmt.Errorf("unknown flag %q", name)
			}
		}
	}
	if p.err != nil {
		return capture.Header{}, invalid("header: %v", p.err)
	}
	return h, nil
}

// parser decodes the fields of one row; the first error sticks.
type parser struct {
	r   *Reader
	row []string
	err error
}

func (p *parser) str(col int) string { return p.r.get(p.row, col) }

func (p *parser) uint(col, bits int, required bool) uint64 {
	s := p.str(col)
	if p.err != nil {
		return 0
	}
	if s == "" {
		if required {
			p.err = fmt.Errorf("column %s is empty", Columns[col])
		}
		return 0
	}
	v, err := strconv.ParseUint(s, 10, bits)
	if err != nil {
		p.err = fmt.Errorf("column %s: %q is not an unsigned %d-bit integer", Columns[col], s, bits)
	}
	return v
}

func (p *parser) hex(col int) []byte {
	s := p.str(col)
	if p.err != nil {
		return nil
	}
	if len(s) > 2*capture.MaxPDULen {
		p.err = fmt.Errorf("column %s is longer than %d bytes", Columns[col], capture.MaxPDULen)
		return nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		p.err = fmt.Errorf("column %s: %v", Columns[col], err)
	}
	return b
}

func (p *parser) tags(col int) map[string]string {
	s := p.str(col)
	if p.err != nil || s == "" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		p.err = fmt.Errorf("column tags: %v", err)
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", capture.ErrCorrupt, fmt.Sprintf(format, args...))
}

func (r *Reader) corrupt(format string, args ...any) error { return r.wrap(invalid(format, args...)) }

func (r *Reader) wrap(err error) error { return fmt.Errorf("mbcsv: line %d: %w", r.line, err) }

var errRecordTooLong = errors.New("mbcsv: record too long")

// budgetReader limits how much the CSV reader may read for one record, and
// remembers whether the input ended with a newline.
type budgetReader struct {
	r    io.Reader
	left int
	last byte
	eof  bool
}

func (b *budgetReader) reset() { b.left = MaxRecordLen + 2*4096 }

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errRecordTooLong
	}
	if len(p) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= n
	if n > 0 {
		b.last = p[n-1]
	}
	if err == io.EOF {
		b.eof = true
	}
	return n, err
}

func (b *budgetReader) endsWithNewline() bool { return b.last == '\n' }
