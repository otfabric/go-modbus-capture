// SPDX-License-Identifier: MIT

// Package mbcap reads and writes the MBCAP binary capture format (.mbcap) of
// docs/FORMAT.md section 2: a magic number followed by CRC32C-protected
// blocks. Samples refer to request templates, carry delta-coded times, and
// store each response raw, as "same as before" or as a sparse word diff
// against the previous response to the same request in the same block. Every
// data block is independently decodable and optionally deflate-compressed.
//
// Writers never seek. A file cut short by a crash is readable up to its last
// complete block (the reader then returns an error wrapping
// capture.ErrTruncated); Repair rewrites it as a complete file.
package mbcap

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Magic is the first 8 bytes of every MBCAP file.
const Magic = "\x89MBCAP\r\n"

// Block types.
const (
	blockHeader   = 0x01
	blockTarget   = 0x02
	blockStream   = 0x03
	blockTemplate = 0x04
	blockData     = 0x10
	blockIndex    = 0x20
	blockFooter   = 0x21
)

// Wire codes of the data block compression.
const (
	codecNone    = 0
	codecDeflate = 1
	codecZstd    = 2
)

// Response codings of a sample (flags bits 0-1).
const (
	codingRaw  = 0
	codingSame = 1
	codingDiff = 2
)

const (
	flagInline     = 1 << 2
	flagCodingMask = 0x03
	flagKnownMask  = flagCodingMask | flagInline
)

// Header flag bits.
const (
	headerChangeOnly = 1 << 0
	headerExceptions = 1 << 1
	headerKnownFlags = headerChangeOnly | headerExceptions
)

// footerTag closes the footer block.
const footerTag = "MBCE"

// FooterLen is the size of the footer block: type, length, index offset,
// tag and CRC.
const FooterLen = 1 + 1 + 8 + 4 + 4

// Limits for hostile input.
const (
	// MaxBlockLen is the largest block payload a reader accepts.
	MaxBlockLen = 32 << 20
	// MaxBlockRaw is the largest uncompressed data block body.
	MaxBlockRaw = 16 << 20
	// MaxTemplates is the largest number of request templates.
	MaxTemplates = capture.MaxRequests
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// enc appends encoded values to a byte slice.
type enc struct{ b []byte }

func (e *enc) u8(v uint8)       { e.b = append(e.b, v) }
func (e *enc) uvarint(v uint64) { e.b = binary.AppendUvarint(e.b, v) }
func (e *enc) zigzag(v int64)   { e.b = binary.AppendUvarint(e.b, uint64(v<<1)^uint64(v>>63)) }
func (e *enc) bytes(v []byte)   { e.uvarint(uint64(len(v))); e.b = append(e.b, v...) }
func (e *enc) str(v string)     { e.uvarint(uint64(len(v))); e.b = append(e.b, v...) }

func (e *enc) tags(m map[string]string) {
	keys := sortedKeys(m)
	e.uvarint(uint64(len(keys)))
	for _, k := range keys {
		e.str(k)
		e.str(m[k])
	}
}

// dec reads encoded values from a byte slice. The first error sticks; later
// reads return zero values.
type dec struct {
	b   []byte
	err error
}

func (d *dec) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s", capture.ErrCorrupt, fmt.Sprintf(format, args...))
	}
}

func (d *dec) u8() uint8 {
	if d.err != nil {
		return 0
	}
	if len(d.b) < 1 {
		d.fail("unexpected end of block")
		return 0
	}
	v := d.b[0]
	d.b = d.b[1:]
	return v
}

func (d *dec) uvarint() uint64 {
	if d.err != nil {
		return 0
	}
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail("bad varint")
		return 0
	}
	d.b = d.b[n:]
	return v
}

// uvarintMax reads a varint and checks it is at most max.
func (d *dec) uvarintMax(what string, max uint64) uint64 {
	v := d.uvarint()
	if v > max {
		d.fail("%s %d is larger than %d", what, v, max)
		return 0
	}
	return v
}

func (d *dec) zigzag() int64 {
	u := d.uvarint()
	return int64(u>>1) ^ -int64(u&1)
}

func (d *dec) take(n uint64) []byte {
	if d.err != nil {
		return nil
	}
	if n > uint64(len(d.b)) {
		d.fail("length %d exceeds the %d bytes left in the block", n, len(d.b))
		return nil
	}
	v := d.b[:n:n]
	d.b = d.b[n:]
	return v
}

func (d *dec) bytes(max int) []byte {
	n := d.uvarintMax("length", uint64(max))
	return append([]byte(nil), d.take(n)...)
}

func (d *dec) str() string {
	n := d.uvarintMax("string length", capture.MaxStringLen)
	return string(d.take(n))
}

func (d *dec) tags() map[string]string {
	n := d.uvarintMax("tag count", capture.MaxTags)
	if n == 0 || d.err != nil {
		return nil
	}
	m := make(map[string]string, n)
	for i := uint64(0); i < n && d.err == nil; i++ {
		k := d.str()
		if _, dup := m[k]; dup {
			d.fail("tag %q twice", k)
		}
		m[k] = d.str()
	}
	return m
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Insertion sort keeps this file free of a sort import for a few keys.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// frame returns a complete block: type, length, payload and CRC32C.
func frame(dst []byte, typ uint8, payload []byte) []byte {
	start := len(dst)
	dst = append(dst, typ)
	dst = binary.AppendUvarint(dst, uint64(len(payload)))
	dst = append(dst, payload...)
	return binary.BigEndian.AppendUint32(dst, crc32.Checksum(dst[start:], castagnoli))
}

// requestKey identifies a request of a stream within a data block.
func requestKey(stream uint32, req []byte) string {
	b := binary.BigEndian.AppendUint32(make([]byte, 0, 4+len(req)), stream)
	return string(append(b, req...))
}
