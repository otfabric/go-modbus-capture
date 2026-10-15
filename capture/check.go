// SPDX-License-Identifier: MIT

package capture

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits that keep readers bounded on hostile input. Writers enforce them
// too, so that every capture written can be read back.
const (
	// MaxStringLen is the longest string (label, producer, host, tag key or
	// value) in bytes.
	MaxStringLen = 64 << 10
	// MaxTags is the largest number of tags on a header or target, and
	// MaxTagBytes the largest total size of their keys and values.
	MaxTags     = 256
	MaxTagBytes = 1 << 20
	// MaxTargets and MaxStreams bound the definitions of a capture.
	MaxTargets = 1 << 16
	MaxStreams = 1 << 16
	// MaxRequests bounds the number of distinct (stream, request) pairs whose
	// last response a change-only capture tracks.
	MaxRequests = 1 << 20
	// MaxTickNS is the largest tick size: 1 s.
	MaxTickNS = uint64(time.Second)
)

// writeFunctions are the function codes that change a device. Captures hold
// reads only.
var writeFunctions = [256]bool{0x05: true, 0x06: true, 0x0F: true, 0x10: true, 0x16: true, 0x17: true}

// IsWriteFunction reports whether fc changes the device (FC05, 06, 15, 16, 22
// and 23). Captures never hold such samples.
func IsWriteFunction(fc uint8) bool { return writeFunctions[fc] }

// readFunctions are the read functions of the Modbus specification, the only
// function codes a capture holds (REQUIREMENTS.md section 6).
var readFunctions = [256]bool{
	0x01: true, 0x02: true, 0x03: true, 0x04: true, 0x07: true, 0x08: true,
	0x0B: true, 0x0C: true, 0x11: true, 0x14: true, 0x18: true, 0x2B: true,
}

// IsReadFunction reports whether fc is a read function of the Modbus
// specification: FC01 to 04, 07, 08, 11, 12, 17, 20, 24 and 43. Vendor,
// user-defined and other function codes are not supported.
func IsReadFunction(fc uint8) bool { return readFunctions[fc] }

// readDiagnostics reports whether an FC08 sub-function only returns data:
// Return Query Data (0x00), Return Diagnostic Register (0x02) and the
// counters 0x0B to 0x12. The others change the device.
func readDiagnostics(sub uint16) bool {
	return sub == 0x00 || sub == 0x02 || sub >= 0x0B && sub <= 0x12
}

// Checker checks a stream of records against the model's invariants, in the
// order they are written or read:
//
//   - the header: a tick of 1 ns to 1 s, a wall-clock start between 1970 and
//     2262 (Unix nanoseconds in a uint64 and an int64), bounded strings and
//     tags;
//   - every string (labels, producer, host, tags): valid UTF-8 without a
//     carriage return, so that every text encoding keeps it;
//   - targets: positive unique ids, a known transport and parity;
//   - streams: positive unique ids, a target defined before;
//   - samples: a stream defined before; request and response PDUs of 1 to
//     253 bytes; a request of a read function of the specification (see
//     IsReadFunction), FC08 with a data-returning sub-function only and FC43
//     with MEI type 14 only;
//     a response with the request's function code or its exception code; an
//     exception response only when the header records exceptions; in a
//     change-only capture, no response equal to the previous response to the
//     same request of the same stream;
//   - the end: not before any sample.
//
// Sample times need not increase: samples of concurrent streams may be
// written slightly out of order.
type Checker struct {
	h       Header
	targets map[uint32]struct{}
	streams map[uint32]struct{}
	last    map[string][]byte
	maxAt   Ticks
	ended   bool
}

// NewChecker checks h and returns a Checker for the records that follow it.
func NewChecker(h Header) (*Checker, error) {
	if err := CheckHeader(&h); err != nil {
		return nil, err
	}
	c := &Checker{h: h, targets: map[uint32]struct{}{}, streams: map[uint32]struct{}{}}
	if h.ChangeOnly {
		c.last = map[string][]byte{}
	}
	return c, nil
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, args...))
}

// CheckHeader checks a header on its own.
func CheckHeader(h *Header) error {
	if h.TickNS == 0 || h.TickNS > MaxTickNS {
		return invalid("tick size %d ns, want 1 to %d", h.TickNS, MaxTickNS)
	}
	if h.WallStart.Before(time.Unix(0, 0)) || h.WallStart.After(time.Unix(0, 1<<63-1)) {
		return invalid("wall-clock start %v outside 1970..2262", h.WallStart)
	}
	if err := checkString("producer", h.Producer); err != nil {
		return err
	}
	return checkTags("header", h.Tags)
}

func checkString(what, s string) error {
	if len(s) > MaxStringLen {
		return invalid("%s is %d bytes, more than %d", what, len(s), MaxStringLen)
	}
	if !utf8.ValidString(s) {
		return invalid("%s is not valid UTF-8", what)
	}
	if strings.IndexByte(s, '\r') >= 0 {
		// CSV readers turn CR LF inside a quoted field into LF: a CR could not
		// survive every text encoding.
		return invalid("%s contains a carriage return", what)
	}
	return nil
}

func checkTags(what string, tags map[string]string) error {
	if len(tags) > MaxTags {
		return invalid("%s has %d tags, more than %d", what, len(tags), MaxTags)
	}
	total := 0
	for k, v := range tags {
		total += len(k) + len(v)
		if total > MaxTagBytes {
			return invalid("%s tags are more than %d bytes", what, MaxTagBytes)
		}
		if k == "" {
			return invalid("%s has a tag with an empty key", what)
		}
		if err := checkString(what+" tag key", k); err != nil {
			return err
		}
		if err := checkString(what+" tag "+k, v); err != nil {
			return err
		}
	}
	return nil
}

func (c *Checker) checkOpen() error {
	if c.ended {
		return invalid("record after the capture end")
	}
	return nil
}

// Target checks a target definition.
func (c *Checker) Target(t Target) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	switch {
	case t.ID == 0:
		return invalid("target id 0")
	case !t.Transport.Valid():
		return invalid("target %d: unknown transport code %d", t.ID, uint8(t.Transport))
	case !t.Serial.Parity.Valid():
		return invalid("target %d: unknown parity code %d", t.ID, uint8(t.Serial.Parity))
	}
	if _, dup := c.targets[t.ID]; dup {
		return invalid("target %d defined twice", t.ID)
	}
	if len(c.targets) >= MaxTargets {
		return invalid("more than %d targets", MaxTargets)
	}
	what := fmt.Sprintf("target %d", t.ID)
	for _, f := range []struct{ name, v string }{
		{"label", t.Label}, {"host", t.Host}, {"serial device", t.Serial.Device},
	} {
		if err := checkString(what+" "+f.name, f.v); err != nil {
			return err
		}
	}
	if err := checkTags(what, t.Tags); err != nil {
		return err
	}
	c.targets[t.ID] = struct{}{}
	return nil
}

// Stream checks a stream definition.
func (c *Checker) Stream(s Stream) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	if s.ID == 0 {
		return invalid("stream id 0")
	}
	if _, dup := c.streams[s.ID]; dup {
		return invalid("stream %d defined twice", s.ID)
	}
	if _, ok := c.targets[s.Target]; !ok {
		return invalid("stream %d refers to undefined target %d", s.ID, s.Target)
	}
	if len(c.streams) >= MaxStreams {
		return invalid("more than %d streams", MaxStreams)
	}
	what := fmt.Sprintf("stream %d", s.ID)
	if err := checkString(what+" role", s.Role); err != nil {
		return err
	}
	if err := checkString(what+" label", s.Label); err != nil {
		return err
	}
	c.streams[s.ID] = struct{}{}
	return nil
}

// CheckPDUs checks the request and response PDUs of a sample on their own.
func CheckPDUs(req, resp []byte) error {
	switch {
	case len(req) == 0 || len(req) > MaxPDULen:
		return invalid("request PDU of %d bytes, want 1 to %d", len(req), MaxPDULen)
	case len(resp) == 0 || len(resp) > MaxPDULen:
		return invalid("response PDU of %d bytes, want 1 to %d", len(resp), MaxPDULen)
	case IsWriteFunction(req[0]):
		return invalid("request function code 0x%02x is a write; captures hold reads only", req[0])
	case !IsReadFunction(req[0]):
		return invalid("request function code 0x%02x is not a read function of the Modbus specification", req[0])
	case req[0] == 0x08 && (len(req) < 3 || !readDiagnostics(uint16(req[1])<<8|uint16(req[2]))):
		return invalid("FC08 request without a data-returning sub-function; captures hold reads only")
	case req[0] == 0x2B && (len(req) < 2 || req[1] != 0x0E):
		return invalid("FC43 request with an MEI type other than 14 (Read Device Identification)")
	case resp[0]&0x7F != req[0]:
		return invalid("response function code 0x%02x does not answer request function code 0x%02x", resp[0], req[0])
	}
	return nil
}

// Sample checks a sample read from a capture.
func (c *Checker) Sample(s Sample) error {
	keep, err := c.sample(s)
	if err != nil {
		return err
	}
	if !keep {
		if s.IsException() {
			return invalid("exception response in a capture that does not record exceptions")
		}
		return invalid("stream %d: response equal to the previous one to the same request in a change-only capture", s.Stream)
	}
	return nil
}

// Admit checks a sample to be written. It returns false, without error, for a
// sample the capture does not store: an exception response when the header
// does not record exceptions, or an unchanged response in a change-only
// capture.
func (c *Checker) Admit(s Sample) (bool, error) { return c.sample(s) }

func (c *Checker) sample(s Sample) (bool, error) {
	if err := c.checkOpen(); err != nil {
		return false, err
	}
	if _, ok := c.streams[s.Stream]; !ok {
		return false, invalid("sample refers to undefined stream %d", s.Stream)
	}
	if err := CheckPDUs(s.Request, s.Response); err != nil {
		return false, err
	}
	if s.IsException() && !c.h.Exceptions {
		return false, nil
	}
	if c.last != nil {
		key := requestKey(s.Stream, s.Request)
		prev, seen := c.last[key]
		if seen && bytes.Equal(prev, s.Response) {
			return false, nil
		}
		if !seen && len(c.last) >= MaxRequests {
			return false, invalid("more than %d distinct requests in a change-only capture", MaxRequests)
		}
		c.last[key] = append(prev[:0:0], s.Response...)
	}
	if s.At > c.maxAt {
		c.maxAt = s.At
	}
	return true, nil
}

// End checks the capture end. No record may follow it.
func (c *Checker) End(e End) error {
	if err := c.checkOpen(); err != nil {
		return err
	}
	c.ended = true
	if e.Known && e.At < c.maxAt {
		return invalid("capture end %d is before the sample at %d", e.At, c.maxAt)
	}
	if !e.Known && e.At != 0 {
		return invalid("unknown capture end with time %d", e.At)
	}
	return nil
}

// MaxAt returns the latest sample time checked so far.
func (c *Checker) MaxAt() Ticks { return c.maxAt }

// requestKey identifies a request of a stream.
func requestKey(stream uint32, req []byte) string {
	b := make([]byte, 4, 4+len(req))
	b[0], b[1], b[2], b[3] = byte(stream>>24), byte(stream>>16), byte(stream>>8), byte(stream)
	return string(append(b, req...))
}
