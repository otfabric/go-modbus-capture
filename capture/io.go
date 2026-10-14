// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
)

// ErrTruncated is wrapped by the error a reader returns when the input ends
// before the capture does: in the middle of a record or block, or (binary)
// without an index. Records returned before it are valid. A capture read
// that far has no recorded end.
var ErrTruncated = errors.New("capture: truncated")

// ErrCorrupt is wrapped by the error a reader returns for input that is not
// a valid encoding, or whose records break the model's invariants.
var ErrCorrupt = errors.New("capture: corrupt")

// A Reader returns the records of a capture in order. Definitions come before
// the samples that use them; the End, when the capture has one, comes last.
type Reader interface {
	// Header returns the capture header.
	Header() Header
	// Next returns the next record. After the last one it returns io.EOF.
	Next() (Record, error)
}

// A Writer writes a capture. Definitions must be written before the samples
// that use them. Writers check every record (see Checker) and return an error
// for an invalid one. A sample that a change-only capture does not store, or
// an exception response in a capture without Exceptions, is dropped without
// error.
type Writer interface {
	WriteTarget(t Target) error
	WriteStream(s Stream) error
	WriteSample(s Sample) error
	// Close completes the capture with its end and flushes it. It does not
	// close the underlying io.Writer. Use End{} when the end is unknown.
	Close(end End) error
}

// Capture is a whole capture in memory.
type Capture struct {
	Header  Header
	Targets []Target
	Streams []Stream
	Samples []Sample
	End     End
}

// ReadAll reads every record of r.
func ReadAll(r Reader) (*Capture, error) {
	c := &Capture{Header: r.Header()}
	for {
		rec, err := r.Next()
		if err == io.EOF {
			return c, nil
		}
		if err != nil {
			return c, err
		}
		c.add(rec)
	}
}

func (c *Capture) add(rec Record) {
	switch v := rec.(type) {
	case Target:
		c.Targets = append(c.Targets, v)
	case Stream:
		c.Streams = append(c.Streams, v)
	case Sample:
		c.Samples = append(c.Samples, v)
	case End:
		c.End = v
	}
}

// WriteTo writes the capture to w, definitions first, and closes w with the
// capture end.
func (c *Capture) WriteTo(w Writer) error {
	for _, t := range c.Targets {
		if err := w.WriteTarget(t); err != nil {
			return err
		}
	}
	for _, s := range c.Streams {
		if err := w.WriteStream(s); err != nil {
			return err
		}
	}
	for _, s := range c.Samples {
		if err := w.WriteSample(s); err != nil {
			return err
		}
	}
	return w.Close(c.End)
}

// Validate checks the capture against the model's invariants.
func (c *Capture) Validate() error {
	ck, err := NewChecker(c.Header)
	if err != nil {
		return err
	}
	for _, t := range c.Targets {
		if err := ck.Target(t); err != nil {
			return err
		}
	}
	for _, s := range c.Streams {
		if err := ck.Stream(s); err != nil {
			return err
		}
	}
	for i, s := range c.Samples {
		if err := ck.Sample(s); err != nil {
			return fmt.Errorf("sample %d: %w", i, err)
		}
	}
	return ck.End(c.End)
}

// Copy writes every record of src to dst and closes dst with the end of src
// (End{} when src has none). It stops at the first error and does not close
// dst then.
func Copy(dst Writer, src Reader) error {
	end := End{}
	for {
		rec, err := src.Next()
		if err == io.EOF {
			return dst.Close(end)
		}
		if err != nil {
			return err
		}
		if err := Write(dst, rec); err != nil {
			return err
		}
		if e, ok := rec.(End); ok {
			end = e
		}
	}
}

// Write writes one record to w. An End is not written: it is passed to Close.
func Write(w Writer, rec Record) error {
	switch v := rec.(type) {
	case Target:
		return w.WriteTarget(v)
	case Stream:
		return w.WriteStream(v)
	case Sample:
		return w.WriteSample(v)
	case End:
		return nil
	default:
		return fmt.Errorf("capture: unknown record type %T", rec)
	}
}

// Equal reports whether a and b hold the same capture: the same header, the
// same definitions in the same order, the same samples in the same order with
// byte-identical PDUs, and the same end. Empty and nil maps are equal.
func Equal(a, b *Capture) bool { return Diff(a, b) == "" }

// Diff describes the first difference between a and b, or returns "" when
// they are equal (see Equal).
func Diff(a, b *Capture) string {
	if d := diffHeader(&a.Header, &b.Header); d != "" {
		return "header: " + d
	}
	if len(a.Targets) != len(b.Targets) {
		return fmt.Sprintf("%d targets, %d targets", len(a.Targets), len(b.Targets))
	}
	for i := range a.Targets {
		x, y := a.Targets[i], b.Targets[i]
		if !maps.Equal(x.Tags, y.Tags) {
			return fmt.Sprintf("target %d tags: %v, %v", i, x.Tags, y.Tags)
		}
		if targetFields(x) != targetFields(y) {
			return fmt.Sprintf("target %d: %+v, %+v", i, x, y)
		}
	}
	if len(a.Streams) != len(b.Streams) {
		return fmt.Sprintf("%d streams, %d streams", len(a.Streams), len(b.Streams))
	}
	for i := range a.Streams {
		if a.Streams[i] != b.Streams[i] {
			return fmt.Sprintf("stream %d: %+v, %+v", i, a.Streams[i], b.Streams[i])
		}
	}
	if len(a.Samples) != len(b.Samples) {
		return fmt.Sprintf("%d samples, %d samples", len(a.Samples), len(b.Samples))
	}
	for i := range a.Samples {
		x, y := &a.Samples[i], &b.Samples[i]
		if x.Stream != y.Stream || x.At != y.At || !bytes.Equal(x.Request, y.Request) || !bytes.Equal(x.Response, y.Response) {
			return fmt.Sprintf("sample %d: {s=%d at=%d req=%x resp=%x}, {s=%d at=%d req=%x resp=%x}",
				i, x.Stream, x.At, x.Request, x.Response, y.Stream, y.At, y.Request, y.Response)
		}
	}
	if a.End != b.End {
		return fmt.Sprintf("end: %+v, %+v", a.End, b.End)
	}
	return ""
}

func diffHeader(a, b *Header) string {
	switch {
	case a.TickNS != b.TickNS:
		return fmt.Sprintf("tick %d ns, %d ns", a.TickNS, b.TickNS)
	case !a.WallStart.Equal(b.WallStart):
		return fmt.Sprintf("wall start %v, %v", a.WallStart, b.WallStart)
	case a.ChangeOnly != b.ChangeOnly || a.Exceptions != b.Exceptions:
		return fmt.Sprintf("flags change-only=%v exceptions=%v, change-only=%v exceptions=%v",
			a.ChangeOnly, a.Exceptions, b.ChangeOnly, b.Exceptions)
	case a.Producer != b.Producer:
		return fmt.Sprintf("producer %q, %q", a.Producer, b.Producer)
	case !maps.Equal(a.Tags, b.Tags):
		return fmt.Sprintf("tags %v, %v", a.Tags, b.Tags)
	}
	return ""
}

// targetKey is a Target without its tags, so that it can be compared.
type targetKey struct {
	ID        uint32
	Label     string
	Transport Transport
	Host      string
	Port      uint16
	Serial    Serial
	Unit      uint8
}

func targetFields(t Target) targetKey {
	return targetKey{t.ID, t.Label, t.Transport, t.Host, t.Port, t.Serial, t.Unit}
}
