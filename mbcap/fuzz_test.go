// SPDX-License-Identifier: MIT

package mbcap

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/fuzzcap"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/jsonl"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

var binaryCodec = fuzzcap.Codec{
	NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
	NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) {
		return NewWriter(w, h, &Options{BlockSamples: 5})
	},
}

// The reader never panics on hostile input and does not allocate without
// bound; what it accepts is a valid capture that re-encodes to itself.
func FuzzReader(f *testing.F) {
	for _, s := range fuzzcap.Seeds(f, "*.mbcap") {
		f.Add(s)
	}
	f.Add(encode(f, synth.Generate(synth.Config{Seed: 1, Polls: 4, Mix: synth.Busy, Extras: true}), nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		got := fuzzcap.ReadAccepted(binaryCodec, data)
		if got != nil {
			fuzzcap.CheckRoundTrip(t, binaryCodec, got)
		}
		// Repair never fails on a readable header, and writes a readable file.
		var out bytes.Buffer
		if _, err := Repair(&out, bytes.NewReader(data), nil); err == nil {
			if fuzzcap.ReadAccepted(binaryCodec, out.Bytes()) == nil {
				t.Fatalf("Repair wrote a file that does not read")
			}
		}
		_, _ = ReadIndex(bytes.NewReader(data), int64(len(data)))
	})
}

// The kinds of chunk FuzzBlocks reads from its input: a block type whose
// payload is the bytes of the chunk, or a data block built around them.
const (
	chunkHeader = iota
	chunkTarget
	chunkStream
	chunkTemplate
	chunkData     // a data block payload: codec, size and body as given
	chunkBody     // the body of an uncompressed data block
	chunkDeflated // the body of a deflate data block
	chunkIndex
	chunkFooter
	chunkOther // a block of a type that no version defines
	chunkKinds
)

var chunkTypes = [chunkKinds]uint8{blockHeader, blockTarget, blockStream, blockTemplate, blockData, blockData, blockData, blockIndex, blockFooter, 0x30}

// chunk appends one chunk to a FuzzBlocks input: kind, length, bytes.
func chunk(dst []byte, kind uint8, b []byte) []byte {
	dst = append(dst, kind)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(b)))
	return append(dst, b...)
}

// inflated returns what stream inflates to, as far as it goes and at most
// MaxBlockRaw bytes.
func inflated(stream []byte) []byte {
	b, _ := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(stream)), MaxBlockRaw))
	return b
}

// frameChunks turns a FuzzBlocks input into a file: the magic number and one
// block per chunk, each with its length and a correct CRC, so that what the
// input holds reaches the block decoders. It also returns the payloads of
// the index blocks, and what reading the file may allocate in all, kept or
// not: a fixed amount for the reader and for each block, and a multiple of
// the bytes the reader is given.
//
//   - 36 times a payload: the block as it is read, and what the decoder of
//     a definition or an index keeps of it (a stream id of an index entry is
//     one byte there and four in a slice that grows).
//   - 512 times an uncompressed body: a sample of 4 bytes decodes to two
//     PDUs of up to 253 bytes and is kept in two slices that grow.
//   - 2048 times a compressed stream that the input gives as it is:
//     compress/flate builds its decoding tables again for every block of the
//     stream, about 36 KiB for a block header of about 40 bytes.
func frameChunks(data []byte) (file []byte, indexes [][]byte, budget uint64) {
	file = []byte(Magic)
	budget = 256 << 10
	for len(data) >= 3 {
		kind := data[0] % chunkKinds
		n := min(int(binary.BigEndian.Uint16(data[1:])), len(data)-3)
		b := data[3 : 3+n]
		data = data[3+n:]
		payload, body := b, 0
		switch kind {
		case chunkData:
			body = len(b)
			if len(b) > 0 {
				_, size := binary.Uvarint(b[1:])
				body += len(inflated(b[1+max(size, 0):]))
			}
			budget += 2048 * uint64(len(b))
		case chunkBody, chunkDeflated:
			var e enc
			e.u8(codecNone)
			e.uvarint(uint64(len(b)))
			e.b = append(e.b, b...)
			if kind == chunkDeflated {
				var z bytes.Buffer
				fw, _ := flate.NewWriter(&z, flate.DefaultCompression)
				_, _ = fw.Write(b)
				_ = fw.Close()
				e.b = append(e.b[:0], codecDeflate)
				e.uvarint(uint64(len(b)))
				e.b = append(e.b, z.Bytes()...)
			}
			payload, body = e.b, len(b)
		case chunkIndex:
			indexes = append(indexes, b)
		}
		file = frame(file, chunkTypes[kind], payload)
		budget += 128<<10 + 36*uint64(len(payload)) + 512*uint64(body)
	}
	return file, indexes, budget
}

// chunksOf returns a FuzzBlocks input for the blocks of file. With bodies,
// a data block is given by its uncompressed body, so that the fuzzer changes
// samples and not a compressed stream.
func chunksOf(file []byte, bodies bool) []byte {
	r := &Reader{br: bufio.NewReader(bytes.NewReader(file[len(Magic):]))}
	var out []byte
	for {
		typ, payload, err := r.readBlock()
		if err != nil {
			return out // the end of the file, or of what frames in a damaged one
		}
		kind := slices.Index(chunkTypes[:], typ)
		if kind < 0 {
			kind = chunkOther
		}
		if typ == blockData && bodies && len(payload) > 1 {
			_, n := binary.Uvarint(payload[1:])
			switch rest := payload[1+max(n, 0):]; payload[0] {
			case codecNone:
				kind, payload = chunkBody, rest
			case codecDeflate:
				kind, payload = chunkDeflated, inflated(rest)
			}
		}
		if len(payload) <= 0xFFFF {
			out = chunk(out, uint8(kind), payload)
		}
	}
}

// The block decoders behind the CRC: FuzzReader changes whole files, and a
// change inside a block fails its CRC before a decoder sees it. This target
// frames its input into blocks with correct lengths and CRCs (frameChunks).
// Whatever the blocks hold, the reader returns records that form a valid
// capture and re-encode to themselves, fails with ErrCorrupt or ErrTruncated
// only, and allocates within a budget set by the size of the input; an index
// payload that decodes encodes to itself; and Repair writes a file that
// reads.
func FuzzBlocks(f *testing.F) {
	for _, s := range fuzzcap.Seeds(f, "*.mbcap") {
		f.Add(chunksOf(s, false))
		f.Add(chunksOf(s, true))
	}
	busy := encode(f, synth.Generate(synth.Config{Seed: 1, Polls: 4, Mix: synth.Busy, Extras: true}), &Options{BlockSamples: 5})
	f.Add(chunksOf(busy, true))
	head := chunksOf(encode(f, &capture.Capture{Header: testHeader(), Targets: []capture.Target{{ID: 1}},
		Streams: []capture.Stream{{ID: 1, Target: 1}}}, nil), false)
	head = head[:len(head)-len(chunk(nil, chunkIndex, encodeIndex(capture.End{}, nil)))-len(chunk(nil, chunkFooter, make([]byte, 12)))]
	tmpl := chunk(bytes.Clone(head), chunkTemplate, []byte{1, 5, 0x03, 0, 0, 0, 1})
	raw := []byte{codingRaw, 1, 1, 0, 4, 0x03, 2, 0, 7}
	body := func(base uint64, samples ...[]byte) []byte {
		var e enc
		e.uvarint(base)
		e.uvarint(uint64(len(samples)))
		for _, s := range samples {
			e.b = append(e.b, s...)
		}
		return e.b
	}
	// One sample; the same with bytes after the body.
	f.Add(chunk(bytes.Clone(tmpl), chunkBody, body(0, raw)))
	f.Add(chunk(bytes.Clone(tmpl), chunkData, append([]byte{codecNone, 11}, append(body(0, raw), 0xAA, 0xBB)...)))
	// SAME and DIFF against the first sample, and times that step by the
	// largest time delta, twice forward and twice back.
	step := binary.AppendUvarint(nil, 1<<64-2) // zigzag of 2^63-1
	back := binary.AppendUvarint(nil, 1<<64-1) // zigzag of -2^63
	same := func(dt []byte) []byte { return append([]byte{codingSame, 1, 1}, dt...) }
	f.Add(chunk(bytes.Clone(tmpl), chunkDeflated, body(1, raw, same(step), same(step), []byte{codingDiff, 1, 1, 0, 1, 1, 0xAB, 0xCD}, same(back), same([]byte{1}))))
	// Two blocks whose samples are more than the largest time delta apart.
	f.Add(chunk(chunk(bytes.Clone(tmpl), chunkBody, body(0, raw)), chunkBody, body(1<<63+5, raw)))
	// A deflate size that the stream does not hold, and an index that lists
	// more data blocks than the file has.
	var short enc
	short.u8(codecDeflate)
	short.uvarint(MaxBlockRaw)
	short.b = append(short.b, deflateBytes(f, body(0, raw))...)
	f.Add(chunk(bytes.Clone(tmpl), chunkData, short.b))
	var long enc
	long.u8(0)
	long.uvarint(0)
	long.uvarint(16000)
	long.b = append(long.b, make([]byte, 4*16000)...)
	f.Add(chunk(bytes.Clone(tmpl), chunkIndex, long.b))

	f.Fuzz(func(t *testing.T, data []byte) {
		file, indexes, budget := frameChunks(data)
		var c *capture.Capture
		var err error
		if got := fuzzcap.AllocBytes(1, func() { c, _, err = readAllBytes(file, false) }); got > budget {
			t.Fatalf("reading a file of %d bytes allocated %d bytes, budget %d", len(file), got, budget)
		}
		if err != nil && !errors.Is(err, capture.ErrCorrupt) && !errors.Is(err, capture.ErrTruncated) {
			t.Fatalf("the reader failed with an error that is neither ErrCorrupt nor ErrTruncated: %v", err)
		}
		if c != nil {
			// The records before an error are valid too.
			fuzzcap.CheckRoundTrip(t, binaryCodec, c)
		}
		for _, p := range indexes {
			end, blocks, err := decodeIndex(p, -1)
			if err != nil {
				continue
			}
			again, same, err := decodeIndex(encodeIndex(end, blocks), len(blocks))
			if err != nil || again != end || !slices.EqualFunc(blocks, same, func(a, b blockInfo) bool {
				return a.offset == b.offset && a.first == b.first && a.last == b.last && slices.Equal(a.streams, b.streams)
			}) {
				t.Fatalf("an index that decodes does not encode to itself: %v", err)
			}
		}
		var out bytes.Buffer
		if rep, err := Repair(&out, bytes.NewReader(file), nil); err == nil {
			got := fuzzcap.ReadAccepted(binaryCodec, out.Bytes())
			if got == nil || len(got.Samples) != rep.Samples {
				t.Fatalf("Repair reports %d samples and wrote a file that reads as %v", rep.Samples, got)
			}
		}
	})
}

// Any valid capture survives binary, JSON Lines and long CSV unchanged.
func FuzzRoundTrip(f *testing.F) {
	f.Add([]byte{0x03, 1, 2, 3, 4, 2, 2, 0x10, 0x03, 4, 0, 0, 0, 1, 3, 2, 0, 5, 0x90, 0x05, 0xc0, 0x11, 0x22})
	f.Add([]byte{0x0f, 0, 0, 0, 0, 0, 1, 0xa1, 0x2b, 3, 0x0e, 0x01, 0x00, 0x05})
	// Six samples whose times step by 2^63 ticks, and by 127*37 ticks more
	// and less than that, forward and back.
	f.Add([]byte{0, 3, 1, 2, 0, 0, 1, 0, 1, 0, 0, 0,
		0, 2, 0, 2, 2, 1, 0x00, 0, 2, 0, 2, 2, 2, 0x80, 0, 2, 0, 2, 2, 3, 0x7f,
		0, 2, 0, 2, 2, 4, 0x7f, 0, 2, 0, 2, 2, 5, 0x81, 0, 2, 0, 2, 2, 6, 0x81})
	f.Fuzz(func(t *testing.T, data []byte) {
		c := fuzzcap.Build(data)
		if err := c.Validate(); err != nil {
			t.Fatalf("Build made an invalid capture: %v", err)
		}
		for _, codec := range []fuzzcap.Codec{
			binaryCodec,
			{NewReader: func(r io.Reader) (capture.Reader, error) { return NewReader(r) },
				NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) {
					return NewWriter(w, h, &Options{Compression: NoCompression, BlockBytes: 40})
				}},
			{NewReader: func(r io.Reader) (capture.Reader, error) { return jsonl.NewReader(r) },
				NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return jsonl.NewWriter(w, h) }},
			{NewReader: func(r io.Reader) (capture.Reader, error) { return mbcsv.NewReader(r) },
				NewWriter: func(w io.Writer, h capture.Header) (capture.Writer, error) { return mbcsv.NewWriter(w, h) }},
		} {
			var buf bytes.Buffer
			w, err := codec.NewWriter(&buf, c.Header)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.WriteTo(w); err != nil {
				t.Fatal(err)
			}
			got := fuzzcap.ReadAccepted(codec, buf.Bytes())
			if got == nil {
				t.Fatalf("an encoded valid capture does not read back")
			}
			if d := capture.Diff(c, got); d != "" {
				t.Fatalf("round trip changed the capture: %s", d)
			}
		}
	})
}
