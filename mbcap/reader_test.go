// SPDX-License-Identifier: MIT

package mbcap

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
)

// file assembles an MBCAP file from blocks, adding the index and footer for
// the data blocks it is given.
type file struct {
	b      []byte
	blocks []blockInfo
}

func newFile(t *testing.T) *file {
	t.Helper()
	f := &file{b: []byte(Magic)}
	var e enc
	e.uvarint(capture.VersionMajor)
	e.uvarint(capture.VersionMinor)
	e.uvarint(1000)
	e.uvarint(uint64(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC).UnixNano()))
	e.uvarint(0)
	e.str("test")
	e.uvarint(0)
	f.add(blockHeader, e.b)
	var tg enc
	tg.uvarint(1)
	tg.u8(1)
	tg.uvarint(1)
	for range 2 {
		tg.str("")
	}
	tg.uvarint(502)
	tg.str("")
	tg.uvarint(0)
	tg.b = append(tg.b, 0, 0, 0)
	tg.uvarint(0)
	f.add(blockTarget, tg.b)
	var st enc
	st.uvarint(1)
	st.uvarint(1)
	st.str("poll")
	st.str("")
	f.add(blockStream, st.b)
	return f
}

func (f *file) add(typ uint8, payload []byte) { f.b = frame(f.b, typ, payload) }

func (f *file) template(id uint64, pdu []byte) {
	var e enc
	e.uvarint(id)
	e.bytes(pdu)
	f.add(blockTemplate, e.b)
}

// data adds an uncompressed data block with the given sample bytes.
func (f *file) data(base uint64, count int, samples []byte, first, last uint64) {
	var body enc
	body.uvarint(base)
	body.uvarint(uint64(count))
	body.b = append(body.b, samples...)
	var e enc
	e.u8(codecNone)
	e.uvarint(uint64(len(body.b)))
	e.b = append(e.b, body.b...)
	f.blocks = append(f.blocks, blockInfo{offset: uint64(len(f.b)), first: first, last: last, streams: []uint32{1}})
	f.add(blockData, e.b)
}

func (f *file) close(end uint64) []byte {
	off := uint64(len(f.b))
	f.add(blockIndex, encodeIndex(capture.End{Known: true, At: capture.Ticks(end)}, f.blocks))
	f.add(blockFooter, append(binary.BigEndian.AppendUint64(nil, off), footerTag...))
	return f.b
}

func sampleBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestReaderInlineAndCodings(t *testing.T) {
	f := newFile(t)
	// RAW inline, then SAME and DIFF against it, then the same request through
	// a template: one key, whichever way the request is given.
	req := []byte{0x03, 0, 0, 0, 2}
	f.template(7, req)
	f.data(100, 4, sampleBytes(
		[]byte{flagInline | codingRaw, 1, 5}, req, []byte{0, 6, 0x03, 4, 0, 1, 0, 2},
		[]byte{flagInline | codingSame, 1, 5}, req, []byte{2},
		[]byte{codingDiff, 1, 7, 2, 1, 2, 0xAB, 0xCD},
		[]byte{codingSame, 1, 7, 2}, // zigzag 2 is +1
	), 100, 103)
	c, _, err := readAllBytes(f.close(200), false)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{0x03, 4, 0, 1, 0, 2}, {0x03, 4, 0, 1, 0, 2}, {0x03, 4, 0, 1, 0xAB, 0xCD}, {0x03, 4, 0, 1, 0xAB, 0xCD}}
	for i, s := range c.Samples {
		if !bytes.Equal(s.Response, want[i]) || !bytes.Equal(s.Request, req) || s.At != capture.Ticks(100+[]int{0, 1, 2, 3}[i]) {
			t.Fatalf("sample %d: %+v", i, s)
		}
	}
}

func TestReaderRejects(t *testing.T) {
	req := []byte{0x03, 0, 0, 0, 1}
	raw := sampleBytes([]byte{codingRaw, 1, 1, 0, 4}, []byte{0x03, 2, 0, 7})
	good := func(t *testing.T) *file {
		f := newFile(t)
		f.template(1, req)
		return f
	}
	for _, tc := range []struct {
		name string
		make func(t *testing.T) []byte
		want string
	}{
		{"bad magic", func(t *testing.T) []byte { return []byte("\x89MBCAX\r\n") }, "magic"},
		{"zstd", func(t *testing.T) []byte {
			f := good(t)
			f.add(blockData, []byte{codecZstd, 1, 0})
			return f.close(0)
		}, "zstd"},
		{"unknown codec", func(t *testing.T) []byte {
			f := good(t)
			f.add(blockData, []byte{9, 1, 0})
			return f.close(0)
		}, "codec 9"},
		{"reserved coding", func(t *testing.T) []byte {
			f := good(t)
			f.data(0, 1, []byte{3, 1, 1, 0}, 0, 0)
			return f.close(0)
		}, "reserved response coding"},
		{"sample kind", func(t *testing.T) []byte {
			f := good(t)
			f.data(0, 1, sampleBytes([]byte{1 << 3}, raw[1:]), 0, 0)
			return f.close(0)
		}, "sample kind"},
		{"SAME first", func(t *testing.T) []byte {
			f := good(t)
			f.data(0, 1, []byte{codingSame, 1, 1, 0}, 0, 0)
			return f.close(0)
		}, "SAME without"},
		{"DIFF beyond the response", func(t *testing.T) []byte {
			f := good(t)
			f.data(0, 2, sampleBytes(raw, []byte{codingDiff, 1, 1, 0, 1, 9, 0, 0}), 0, 0)
			return f.close(0)
		}, "beyond"},
		{"undefined template", func(t *testing.T) []byte {
			f := good(t)
			f.data(0, 1, sampleBytes([]byte{codingRaw, 1, 2}, raw[3:]), 0, 0)
			return f.close(0)
		}, "undefined template"},
		{"template twice", func(t *testing.T) []byte {
			f := good(t)
			f.template(1, req)
			return f.close(0)
		}, "defined twice"},
		{"index does not match", func(t *testing.T) []byte {
			f := good(t)
			f.data(0, 1, raw, 0, 0)
			f.blocks[0].last = 99
			return f.close(0)
		}, "index entry"},
		{"end before a sample", func(t *testing.T) []byte {
			f := good(t)
			f.data(50, 1, raw, 50, 50)
			return f.close(10)
		}, "before the sample"},
		{"data after the footer", func(t *testing.T) []byte {
			return append(good(t).close(0), 0)
		}, "after the footer"},
		{"a block after the index", func(t *testing.T) []byte {
			f := good(t)
			b := f.close(0)
			// Move the footer behind another target block.
			body := b[:len(b)-FooterLen]
			off := binary.BigEndian.Uint64(b[len(b)-FooterLen+2:])
			body = frame(body, blockTarget, []byte{2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0})
			return frame(body, blockFooter, append(binary.BigEndian.AppendUint64(nil, off), footerTag...))
		}, "after the index"},
		{"footer pointing elsewhere", func(t *testing.T) []byte {
			b := good(t).close(0)
			body := b[:len(b)-FooterLen]
			return frame(body, blockFooter, append(binary.BigEndian.AppendUint64(nil, 8), footerTag...))
		}, "footer points"},
		{"a header flag from the future", func(t *testing.T) []byte {
			f := &file{b: []byte(Magic)}
			f.add(blockHeader, []byte{0, 1, 0xe8, 7, 0, 4, 0, 0})
			return f.close(0)
		}, "unknown header flags"},
		{"block too large", func(t *testing.T) []byte {
			f := good(t)
			f.b = append(f.b, blockData)
			f.b = binary.AppendUvarint(f.b, MaxBlockLen+1)
			return f.b
		}, "more than"},
		{"deflate bomb beyond its size", func(t *testing.T) []byte {
			f := good(t)
			f.add(blockData, []byte{codecDeflate, 1, 0x01, 0x05, 0x00, 0xfa, 0xff, 1, 2, 3, 4, 5})
			return f.close(0)
		}, "deflate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := readAllBytes(tc.make(t), false)
			if !errors.Is(err, capture.ErrCorrupt) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want ErrCorrupt mentioning %q", err, tc.want)
			}
		})
	}
}

// Writes after Close fail; a writer reports invalid records instead of writing them.
func TestWriterMisuse(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf, capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteStream(capture.Stream{ID: 1, Target: 1}); !errors.Is(err, capture.ErrCorrupt) {
		t.Fatalf("stream of an undefined target: %v", err)
	}
	if err := w.Close(capture.End{}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteTarget(capture.Target{ID: 1}); err == nil {
		t.Fatal("write after Close succeeded")
	}
	if _, err := NewWriter(&buf, capture.Header{}, nil); err == nil {
		t.Fatal("NewWriter took a header with tick 0")
	}
}

// Flush makes everything written so far readable, as a live recorder needs.
func TestFlushIsReadable(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, capture.Header{TickNS: 1000, WallStart: time.Unix(0, 0)}, &Options{Compression: NoCompression})
	_ = w.WriteTarget(capture.Target{ID: 1})
	_ = w.WriteStream(capture.Stream{ID: 1, Target: 1})
	for i := range 3 {
		if err := w.WriteSample(capture.Sample{Stream: 1, At: capture.Ticks(i), Request: []byte{0x07}, Response: []byte{0x07, byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	c, _, err := readAllBytes(buf.Bytes(), false)
	if !errors.Is(err, capture.ErrTruncated) || len(c.Samples) != 3 {
		t.Fatalf("after Flush: %d samples, err %v; want 3 and ErrTruncated (no index yet)", len(c.Samples), err)
	}
}
