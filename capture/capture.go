// SPDX-License-Identifier: MIT

// Package capture holds the model of a Modbus capture: what a device answered,
// and when. It defines the types every encoding stores (Header, Target, Stream,
// Sample, End), the streaming Reader and Writer interfaces the encodings
// implement, and the invariants a capture satisfies (Checker).
//
// The package does no I/O. Values are raw request and response PDUs; nothing
// is decoded.
package capture

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Format version written by this library. Readers accept files with the same
// major version and any minor version. Major version 0 marks the draft format
// (see docs/FORMAT.md): files written now are not readable once the format is
// frozen as 1.0.
const (
	VersionMajor = 0
	VersionMinor = 1
)

// DefaultTickNS is the default tick size: 1 µs.
const DefaultTickNS = 1000

// MaxPDULen is the largest Modbus PDU: 253 bytes (function code and data).
const MaxPDULen = 253

// Ticks is a time in ticks since the capture start. The tick size is in the
// Header.
type Ticks uint64

// Header describes a capture as a whole.
type Header struct {
	// TickNS is the size of a tick in nanoseconds (1 to 1e9; default 1000).
	TickNS uint64
	// WallStart is the wall-clock time at tick 0, at nanosecond resolution.
	// It is stored in UTC.
	WallStart time.Time
	// ChangeOnly marks a change-only capture: a sample whose response equals
	// the previous response to the same request of the same stream is not
	// stored.
	ChangeOnly bool
	// Exceptions marks a capture that records exception responses. Without
	// it a capture holds none.
	Exceptions bool
	// Producer names the tool that wrote the capture.
	Producer string
	// Tags are free key/value pairs. Reserved keys: TagProvenance, TagSource.
	Tags map[string]string
}

// Reserved tag keys.
const (
	// TagProvenance records the tools and operations that produced a capture.
	TagProvenance = "provenance"
	// TagSource records the original file names of a merged capture.
	TagSource = "source"
)

// Time returns the wall-clock time of t.
func (h Header) Time(t Ticks) time.Time {
	return h.WallStart.Add(h.Duration(t))
}

// Duration returns t as a duration since the capture start. It saturates at
// the largest duration.
func (h Header) Duration(t Ticks) time.Duration {
	const maxDur = time.Duration(1<<63 - 1)
	if h.TickNS == 0 {
		return 0
	}
	if uint64(t) > uint64(maxDur)/h.TickNS {
		return maxDur
	}
	return time.Duration(uint64(t) * h.TickNS)
}

// Ticks returns d in ticks, rounded down. A negative d is 0.
func (h Header) Ticks(d time.Duration) Ticks {
	if d <= 0 || h.TickNS == 0 {
		return 0
	}
	return Ticks(uint64(d) / h.TickNS)
}

// Transport is the advisory link a target was reached over.
type Transport uint8

// Transports, with their numeric codes in the binary encoding.
const (
	TransportUnknown    Transport = 0
	TransportTCP        Transport = 1
	TransportUDP        Transport = 2
	TransportTLS        Transport = 3
	TransportRTUOverTCP Transport = 4
	TransportRTU        Transport = 5
	TransportASCII      Transport = 6
)

var transportNames = [...]string{"unknown", "tcp", "udp", "tls", "rtu-over-tcp", "rtu", "ascii"}

// String returns the name used by the text encodings.
func (t Transport) String() string {
	if int(t) < len(transportNames) {
		return transportNames[t]
	}
	return fmt.Sprintf("transport(%d)", uint8(t))
}

// Valid reports whether t is a known transport.
func (t Transport) Valid() bool { return int(t) < len(transportNames) }

// ParseTransport parses a transport name. The empty string is
// TransportUnknown.
func ParseTransport(s string) (Transport, error) {
	if s == "" {
		return TransportUnknown, nil
	}
	for i, n := range transportNames {
		if n == s {
			return Transport(i), nil
		}
	}
	return 0, fmt.Errorf("capture: unknown transport %q", s)
}

// Parity is the parity of a serial link.
type Parity uint8

// Parities, with their numeric codes in the binary encoding.
const (
	ParityNone Parity = 0
	ParityEven Parity = 1
	ParityOdd  Parity = 2
)

var parityNames = [...]string{"none", "even", "odd"}

// String returns the name used by the text encodings.
func (p Parity) String() string {
	if int(p) < len(parityNames) {
		return parityNames[p]
	}
	return fmt.Sprintf("parity(%d)", uint8(p))
}

// Valid reports whether p is a known parity.
func (p Parity) Valid() bool { return int(p) < len(parityNames) }

// ParseParity parses a parity name. The empty string is ParityNone.
func ParseParity(s string) (Parity, error) {
	if s == "" {
		return ParityNone, nil
	}
	for i, n := range parityNames {
		if n == s {
			return Parity(i), nil
		}
	}
	return 0, fmt.Errorf("capture: unknown parity %q", s)
}

// Serial holds the settings of a serial link. The zero value means none.
type Serial struct {
	Device   string
	Baud     uint32
	Parity   Parity
	DataBits uint8
	StopBits uint8
}

// IsZero reports whether s holds no settings.
func (s Serial) IsZero() bool { return s == Serial{} }

// Target is one device: an endpoint plus a unit id. Its endpoint data is
// advisory; a consumer may override it.
type Target struct {
	// ID is positive and unique among the targets of a capture.
	ID        uint32
	Label     string
	Transport Transport
	Host      string
	Port      uint16
	Serial    Serial
	Unit      uint8
	Tags      map[string]string
}

// Stream is one acquisition source of a target, such as a poll plan, a scan
// or an identity probe.
type Stream struct {
	// ID is positive and unique among the streams of a capture.
	ID     uint32
	Target uint32
	// Role is a free label; conventional values are RolePoll, RoleScan,
	// RoleIdentity, RoleSnapshot and RoleOther.
	Role  string
	Label string
}

// Conventional stream roles.
const (
	RolePoll     = "poll"
	RoleScan     = "scan"
	RoleIdentity = "identity"
	RoleSnapshot = "snapshot"
	RoleOther    = "other"
)

// Sample is one answered request: the request PDU as sent and the response
// PDU as received, at the time the response was received.
type Sample struct {
	Stream   uint32
	At       Ticks
	Request  []byte
	Response []byte
}

// Function returns the function code of the request.
func (s *Sample) Function() uint8 {
	if len(s.Request) == 0 {
		return 0
	}
	return s.Request[0]
}

// IsException reports whether the response is an exception response.
func (s *Sample) IsException() bool {
	return len(s.Response) > 0 && s.Response[0]&0x80 != 0
}

// End is the capture end. A capture whose end was not recorded (for example
// one truncated by a crash) has Known false.
type End struct {
	Known bool
	At    Ticks
}

// Record is one record of a capture stream: a Target, a Stream, a Sample or
// an End.
type Record interface{ isRecord() }

func (Target) isRecord() {}
func (Stream) isRecord() {}
func (Sample) isRecord() {}
func (End) isRecord()    {}

// ParseVersion parses a text version "major.minor" and checks that this
// library reads it: the major version must be VersionMajor.
func ParseVersion(s string) (major, minor int, err error) {
	a, b, ok := strings.Cut(s, ".")
	if ok {
		major, err = strconv.Atoi(a)
		if err == nil {
			minor, err = strconv.Atoi(b)
		}
	}
	if !ok || err != nil || major < 0 || minor < 0 {
		return 0, 0, invalid("version %q is not major.minor", s)
	}
	if major != VersionMajor {
		return 0, 0, invalid("format version %s: this reader reads major version %d", s, VersionMajor)
	}
	return major, minor, nil
}
