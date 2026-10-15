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

// Writer writes a capture as JSON Lines.
// It is not safe for concurrent use; separate Writers may run concurrently.
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

// Reader reads a capture from JSON Lines.
// It is not safe for concurrent use; separate Readers may run concurrently.
type Reader struct {
	br    *bufio.Reader
	h     capture.Header
	ck    *capture.Checker
	n     int // number of the last line read
	ended bool
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

// Header returns the capture header.
func (r *Reader) Header() capture.Header { return r.h }

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
		rec, err := r.decode(probe.T, line)
		if err != nil {
			return nil, r.wrap(err)
		}
		return rec, nil
	}
}

func (r *Reader) decode(t string, line []byte) (capture.Record, error) {
	switch t {
	case typeHeader:
		return nil, invalid("a second header line: combine captures with mbcapctl cat or merge")
	case typeTarget:
		var tj targetJSON
		if err := json.Unmarshal(line, &tj); err != nil {
			return nil, invalid("target: %v", err)
		}
		tg, err := decodeTarget(&tj)
		if err != nil {
			return nil, err
		}
		return tg, r.ck.Target(tg)
	case typeStream:
		var sj streamJSON
		if err := json.Unmarshal(line, &sj); err != nil {
			return nil, invalid("stream: %v", err)
		}
		if sj.ID == nil || sj.Target == nil {
			return nil, invalid("stream line without id or target")
		}
		s := capture.Stream{ID: *sj.ID, Target: *sj.Target, Role: sj.Role, Label: sj.Label}
		return s, r.ck.Stream(s)
	case typeSample:
		var sj sampleJSON
		if err := json.Unmarshal(line, &sj); err != nil {
			return nil, invalid("sample: %v", err)
		}
		s, err := decodeSample(&sj)
		if err != nil {
			return nil, err
		}
		return s, r.ck.Sample(s)
	default: // typeEnd
		var ej endJSON
		if err := json.Unmarshal(line, &ej); err != nil {
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
