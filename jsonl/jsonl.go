// SPDX-License-Identifier: MIT

// Package jsonl reads and writes captures as JSON Lines (.mbcap.jsonl), the
// lossless canonical text encoding of docs/FORMAT.md section 3: one JSON
// object per line, PDUs as lowercase hex, times as integer ticks.
package jsonl

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// MaxLineLen is the longest line a Reader accepts, newline included.
const MaxLineLen = 4 << 20

// Line types.
const (
	typeHeader = "header"
	typeTarget = "target"
	typeStream = "stream"
	typeSample = "s"
	typeEnd    = "end"
)

type flagsJSON struct {
	ChangeOnly bool `json:"change_only,omitempty"`
	Exceptions bool `json:"exceptions,omitempty"`
}

type headerJSON struct {
	T         string            `json:"t"`
	Version   *string           `json:"version"`
	TickNS    *uint64           `json:"tick_ns"`
	WallStart *string           `json:"wall_start"`
	Producer  string            `json:"producer,omitempty"`
	Flags     *flagsJSON        `json:"flags,omitempty"`
	Tags      map[string]string `json:"tags,omitempty"`
}

type serialJSON struct {
	Device   string `json:"device,omitempty"`
	Baud     uint32 `json:"baud,omitempty"`
	Parity   string `json:"parity,omitempty"`
	DataBits uint8  `json:"data_bits,omitempty"`
	StopBits uint8  `json:"stop_bits,omitempty"`
}

type targetJSON struct {
	T         string            `json:"t"`
	ID        *uint32           `json:"id"`
	Label     string            `json:"label,omitempty"`
	Transport string            `json:"transport"`
	Host      string            `json:"host,omitempty"`
	Port      uint16            `json:"port,omitempty"`
	Serial    *serialJSON       `json:"serial,omitempty"`
	Unit      uint8             `json:"unit"`
	Tags      map[string]string `json:"tags,omitempty"`
}

type streamJSON struct {
	T      string  `json:"t"`
	ID     *uint32 `json:"id"`
	Target *uint32 `json:"target"`
	Role   string  `json:"role,omitempty"`
	Label  string  `json:"label,omitempty"`
}

type sampleJSON struct {
	T    string  `json:"t"`
	S    *uint32 `json:"s"`
	At   *uint64 `json:"at"`
	Req  *string `json:"req"`
	Resp *string `json:"resp"`
}

type endJSON struct {
	T  string  `json:"t"`
	At *uint64 `json:"at"`
}

// fields names the fields of one JSON object of the format.
type fields map[string]field

// field is one field of an object: its number among the fields of the
// object and, when its value is itself an object of the format, the fields
// of that object.
type field struct {
	n     uint
	inner fields
}

func names(ns ...string) fields {
	f := fields{}
	for i, n := range ns {
		f[n] = field{n: uint(i)}
	}
	return f
}

func (f fields) with(name string, inner fields) fields {
	f[name] = field{n: f[name].n, inner: inner}
	return f
}

// The fields of each line type, as docs/FORMAT.md section 3 lists them.
var (
	typeField    = names("t")
	headerFields = names("t", "version", "tick_ns", "wall_start", "producer", "tags", "flags").
			with("flags", names("change_only", "exceptions"))
	targetFields = names("t", "id", "label", "transport", "host", "port", "unit", "tags", "serial").
			with("serial", names("device", "baud", "parity", "data_bits", "stop_bits"))
	streamFields = names("t", "id", "target", "role", "label")
	sampleFields = names("t", "s", "at", "req", "resp")
	endFields    = names("t", "at")
)

// member is one name/value pair of a JSON object: the name without its
// quotes and the value as it is written, both slices of the object's text
// (a name written with escapes is a copy, with the escapes resolved).
type member struct{ name, value []byte }

// members appends the members of the JSON object data to ms, in order. data
// must be valid JSON (json.Valid); ok is false when it is not an object.
func members(ms []member, data []byte) (_ []member, ok bool) {
	i := space(data, 0)
	if data[i] != '{' {
		return ms, false
	}
	for i = space(data, i+1); data[i] != '}'; {
		end := i + stringLen(data[i:])
		name := data[i+1 : end-1]
		if bytes.IndexByte(name, '\\') >= 0 {
			var s string
			_ = json.Unmarshal(data[i:end], &s)
			name = []byte(s)
		}
		i = space(data, space(data, end)+1) // past the colon
		end = i + valueLen(data[i:])
		ms = append(ms, member{name, data[i:end]})
		if i = space(data, end); data[i] == ',' {
			i = space(data, i+1)
		}
	}
	return ms, true
}

// space returns the index of the first byte of b at or after i that is not
// JSON white space.
func space(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
		i++
	}
	return i
}

// stringLen returns the length of the JSON string at the start of b, quotes
// included.
func stringLen(b []byte) int {
	for i := 1; ; i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
}

// valueLen returns the length of the JSON value at the start of b.
func valueLen(b []byte) int {
	switch b[0] {
	case '"':
		return stringLen(b)
	case '{', '[':
		for i, depth := 0, 0; ; i++ {
			switch b[i] {
			case '"':
				i += stringLen(b[i:]) - 1
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
		}
	}
	// A number or a literal ends at white space or at what ends a member.
	i := bytes.IndexAny(b, ",}] \t\r\n")
	if i < 0 {
		return len(b)
	}
	return i
}

// pick appends to dst a JSON object of the members that spec names. Names
// are compared exactly: a member with any other name, such as a defined name
// in another case, is an unknown field and left out (encoding/json alone
// would take it for the defined field). A named member that appears twice is
// an error.
func pick(dst []byte, ms []member, spec fields) ([]byte, error) {
	dst = append(dst, '{')
	var seen uint
	for _, m := range ms {
		f, named := spec[string(m.name)]
		if !named {
			continue
		}
		if seen&(1<<f.n) != 0 {
			return nil, fmt.Errorf("field %q appears twice", m.name)
		}
		if seen != 0 {
			dst = append(dst, ',')
		}
		seen |= 1 << f.n
		dst = append(dst, '"')
		dst = append(dst, m.name...)
		dst = append(dst, '"', ':')
		sub, isObject := []member(nil), false
		if f.inner != nil {
			sub, isObject = members(nil, m.value)
		}
		if !isObject {
			dst = append(dst, m.value...)
			continue
		}
		var err error
		if dst, err = pick(dst, sub, f.inner); err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}

// exact returns line with only the fields that spec names (see pick). A line
// that is not a JSON object is returned as it is, for json.Unmarshal to
// refuse.
func exact(line []byte, spec fields) ([]byte, error) {
	if !json.Valid(line) {
		return line, nil
	}
	ms, ok := members(nil, line)
	if !ok {
		return line, nil
	}
	return pick(nil, ms, spec)
}

// Writer writes a capture as JSON Lines. It keeps nothing of a record after
// the call that wrote it. Close completes the capture once: a second Close
// and a write after it return an error, and a Close that refuses its end
// leaves the Writer open. A Writer is not safe for concurrent use.
type Writer struct {
	w      *bufio.Writer
	ck     *capture.Checker
	buf    bytes.Buffer
	enc    *json.Encoder
	err    error
	closed bool
}

// NewWriter checks h and writes the header line.
func NewWriter(w io.Writer, h capture.Header) (*Writer, error) {
	ck, err := capture.NewChecker(h)
	if err != nil {
		return nil, err
	}
	jw := &Writer{w: bufio.NewWriter(w), ck: ck}
	jw.enc = json.NewEncoder(&jw.buf)
	jw.enc.SetEscapeHTML(false)
	version := fmt.Sprintf("%d.%d", capture.VersionMajor, capture.VersionMinor)
	tick := h.TickNS
	wall := h.WallStart.UTC().Format(time.RFC3339Nano)
	line := headerJSON{T: typeHeader, Version: &version, TickNS: &tick, WallStart: &wall, Producer: h.Producer, Tags: h.Tags}
	if h.ChangeOnly || h.Exceptions {
		line.Flags = &flagsJSON{ChangeOnly: h.ChangeOnly, Exceptions: h.Exceptions}
	}
	if err := jw.line(line); err != nil {
		return nil, err
	}
	return jw, nil
}

func (w *Writer) line(v any) error {
	if w.err != nil {
		return w.err
	}
	if w.closed {
		return errors.New("jsonl: write after Close")
	}
	w.buf.Reset()
	if err := w.enc.Encode(v); err != nil {
		w.err = err
		return err
	}
	if _, err := w.w.Write(w.buf.Bytes()); err != nil {
		w.err = err
		return err
	}
	return nil
}

// WriteTarget writes a target line.
func (w *Writer) WriteTarget(t capture.Target) error {
	if err := w.ck.Target(t); err != nil {
		return err
	}
	id := t.ID
	line := targetJSON{T: typeTarget, ID: &id, Label: t.Label, Transport: t.Transport.String(),
		Host: t.Host, Port: t.Port, Unit: t.Unit, Tags: t.Tags}
	if !t.Serial.IsZero() {
		line.Serial = &serialJSON{Device: t.Serial.Device, Baud: t.Serial.Baud, DataBits: t.Serial.DataBits, StopBits: t.Serial.StopBits}
		if t.Serial.Parity != capture.ParityNone {
			line.Serial.Parity = t.Serial.Parity.String()
		}
	}
	return w.line(line)
}

// WriteStream writes a stream line.
func (w *Writer) WriteStream(s capture.Stream) error {
	if err := w.ck.Stream(s); err != nil {
		return err
	}
	id, target := s.ID, s.Target
	return w.line(streamJSON{T: typeStream, ID: &id, Target: &target, Role: s.Role, Label: s.Label})
}

// WriteSample writes a sample line, unless the capture does not store it
// (see capture.Writer).
func (w *Writer) WriteSample(s capture.Sample) error {
	keep, err := w.ck.Admit(s)
	if err != nil || !keep {
		return err
	}
	stream, at := s.Stream, uint64(s.At)
	req, resp := hex.EncodeToString(s.Request), hex.EncodeToString(s.Response)
	return w.line(sampleJSON{T: typeSample, S: &stream, At: &at, Req: &req, Resp: &resp})
}

// Close writes the end line when the end is known, and flushes.
func (w *Writer) Close(end capture.End) error {
	if w.closed {
		return errors.New("jsonl: Close called twice")
	}
	if err := w.ck.End(end); err != nil {
		return err
	}
	if end.Known {
		at := uint64(end.At)
		if err := w.line(endJSON{T: typeEnd, At: &at}); err != nil {
			return err
		}
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	return w.w.Flush()
}

// Reader reads a capture from JSON Lines. Field names are matched exactly, as
// docs/FORMAT.md section 3 writes them; a field that appears twice in a line
// is an error. The records Next returns are the caller's. An error of Next
// for one line does not stick: the next call goes on with the line after it.
// A Reader is not safe for concurrent use.
type Reader struct {
	br    *bufio.Reader
	h     capture.Header
	ck    *capture.Checker
	n     int // number of the last line read
	ended bool

	// The members of the line being decoded and the object picked from
	// them, kept from line to line.
	ms  []member
	buf []byte
}

// NewReader reads and checks the header line.
func NewReader(r io.Reader) (*Reader, error) {
	jr := &Reader{br: bufio.NewReaderSize(r, 64<<10)}
	for {
		line, err := jr.readLine()
		if err == io.EOF {
			return nil, fmt.Errorf("jsonl: %w: no header line", capture.ErrTruncated)
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if line, err = exact(line, headerFields); err != nil {
			return nil, jr.corrupt("%v", err)
		}
		var hj headerJSON
		if err := json.Unmarshal(line, &hj); err != nil || hj.T != typeHeader {
			return nil, jr.corrupt("the first line is not a header")
		}
		h, err := decodeHeader(&hj)
		if err != nil {
			return nil, jr.wrap(err)
		}
		if jr.ck, err = capture.NewChecker(h); err != nil {
			return nil, jr.wrap(err)
		}
		jr.h = h
		return jr, nil
	}
}

// Header returns the capture header. The result is the caller's: its Tags
// map is a copy.
func (r *Reader) Header() capture.Header {
	h := r.h
	h.Tags = maps.Clone(h.Tags)
	return h
}

// Next returns the next record, or io.EOF after the last one.
func (r *Reader) Next() (capture.Record, error) {
	for {
		line, err := r.readLine()
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var probe struct {
			T string `json:"t"`
		}
		// A line that is not an object stays as it is, for json.Unmarshal
		// to refuse.
		r.ms = r.ms[:0]
		if json.Valid(line) {
			var isObject bool
			if r.ms, isObject = members(r.ms, line); isObject {
				if r.buf, err = pick(r.buf[:0], r.ms, typeField); err != nil {
					return nil, r.corrupt("%v", err)
				}
				line = r.buf
			}
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return nil, r.corrupt("not a JSON object: %v", err)
		}
		switch probe.T {
		case typeHeader, typeTarget, typeStream, typeSample, typeEnd:
		default:
			continue // unknown line types are ignored
		}
		if r.ended {
			return nil, r.corrupt("%q line after the end line", probe.T)
		}
		rec, err := r.decode(probe.T)
		if err != nil {
			return nil, r.wrap(err)
		}
		return rec, nil
	}
}

// decode decodes the line whose members are in r.ms as a line of type t.
func (r *Reader) decode(t string) (capture.Record, error) {
	switch t {
	case typeHeader:
		return nil, invalid("a second header line: combine captures with mbcapctl cat or merge")
	case typeTarget:
		var tj targetJSON
		if err := r.unmarshal(targetFields, &tj); err != nil {
			return nil, invalid("target: %v", err)
		}
		tg, err := decodeTarget(&tj)
		if err != nil {
			return nil, err
		}
		return tg, r.ck.Target(tg)
	case typeStream:
		var sj streamJSON
		if err := r.unmarshal(streamFields, &sj); err != nil {
			return nil, invalid("stream: %v", err)
		}
		if sj.ID == nil || sj.Target == nil {
			return nil, invalid("stream line without id or target")
		}
		s := capture.Stream{ID: *sj.ID, Target: *sj.Target, Role: sj.Role, Label: sj.Label}
		return s, r.ck.Stream(s)
	case typeSample:
		var sj sampleJSON
		if err := r.unmarshal(sampleFields, &sj); err != nil {
			return nil, invalid("sample: %v", err)
		}
		s, err := decodeSample(&sj)
		if err != nil {
			return nil, err
		}
		return s, r.ck.Sample(s)
	default: // typeEnd
		var ej endJSON
		if err := r.unmarshal(endFields, &ej); err != nil {
			return nil, invalid("end: %v", err)
		}
		if ej.At == nil {
			return nil, invalid("end line without at")
		}
		r.ended = true
		e := capture.End{Known: true, At: capture.Ticks(*ej.At)}
		return e, r.ck.End(e)
	}
}

// unmarshal decodes the fields of the line in r.ms that spec names into v.
func (r *Reader) unmarshal(spec fields, v any) error {
	var err error
	if r.buf, err = pick(r.buf[:0], r.ms, spec); err != nil {
		return err
	}
	return json.Unmarshal(r.buf, v)
}

// readLine returns the next line without its newline. A last line without a
// newline means the input was cut: ErrTruncated.
func (r *Reader) readLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.br.ReadSlice('\n')
		if len(line)+len(chunk) > MaxLineLen {
			return nil, r.corrupt("line longer than %d bytes", MaxLineLen)
		}
		switch {
		case err == nil:
			r.n++
			if line == nil {
				return chunk[:len(chunk)-1], nil
			}
			return append(line, chunk[:len(chunk)-1]...), nil
		case errors.Is(err, bufio.ErrBufferFull):
			line = append(line, chunk...)
		case err == io.EOF:
			if len(line)+len(chunk) == 0 {
				return nil, io.EOF
			}
			r.n++
			return nil, fmt.Errorf("jsonl: line %d: %w: last line has no newline", r.n, capture.ErrTruncated)
		default:
			return nil, err
		}
	}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", capture.ErrCorrupt, fmt.Sprintf(format, args...))
}

func (r *Reader) corrupt(format string, args ...any) error {
	return r.wrap(invalid(format, args...))
}

func (r *Reader) wrap(err error) error {
	return fmt.Errorf("jsonl: line %d: %w", r.n, err)
}

func decodeHeader(hj *headerJSON) (capture.Header, error) {
	if hj.Version == nil || hj.TickNS == nil || hj.WallStart == nil {
		return capture.Header{}, invalid("header line without version, tick_ns or wall_start")
	}
	if _, _, err := capture.ParseVersion(*hj.Version); err != nil {
		return capture.Header{}, err
	}
	wall, err := time.Parse(time.RFC3339Nano, *hj.WallStart)
	if err != nil {
		return capture.Header{}, invalid("wall_start: %v", err)
	}
	h := capture.Header{TickNS: *hj.TickNS, WallStart: wall.UTC(), Producer: hj.Producer, Tags: nilIfEmpty(hj.Tags)}
	if hj.Flags != nil {
		h.ChangeOnly, h.Exceptions = hj.Flags.ChangeOnly, hj.Flags.Exceptions
	}
	return h, nil
}

func decodeTarget(tj *targetJSON) (capture.Target, error) {
	if tj.ID == nil {
		return capture.Target{}, invalid("target line without id")
	}
	tr, err := capture.ParseTransport(tj.Transport)
	if err != nil {
		return capture.Target{}, invalid("%v", err)
	}
	t := capture.Target{ID: *tj.ID, Label: tj.Label, Transport: tr, Host: tj.Host, Port: tj.Port, Unit: tj.Unit, Tags: nilIfEmpty(tj.Tags)}
	if s := tj.Serial; s != nil {
		p, err := capture.ParseParity(s.Parity)
		if err != nil {
			return capture.Target{}, invalid("%v", err)
		}
		t.Serial = capture.Serial{Device: s.Device, Baud: s.Baud, Parity: p, DataBits: s.DataBits, StopBits: s.StopBits}
	}
	return t, nil
}

func decodeSample(sj *sampleJSON) (capture.Sample, error) {
	if sj.S == nil || sj.At == nil || sj.Req == nil || sj.Resp == nil {
		return capture.Sample{}, invalid("sample line without s, at, req or resp")
	}
	req, err := decodeHex("req", *sj.Req)
	if err != nil {
		return capture.Sample{}, err
	}
	resp, err := decodeHex("resp", *sj.Resp)
	if err != nil {
		return capture.Sample{}, err
	}
	return capture.Sample{Stream: *sj.S, At: capture.Ticks(*sj.At), Request: req, Response: resp}, nil
}

func decodeHex(what, s string) ([]byte, error) {
	if len(s) > 2*capture.MaxPDULen {
		return nil, invalid("%s is longer than %d bytes", what, capture.MaxPDULen)
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, invalid("%s: %v", what, err)
	}
	return b, nil
}

func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}
