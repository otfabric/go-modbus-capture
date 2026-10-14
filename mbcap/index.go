// SPDX-License-Identifier: MIT

package mbcap

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"github.com/otfabric/go-modbus-capture/capture"
)

// Index describes the data blocks of a complete file, as its index block
// lists them.
type Index struct {
	// End is the capture end.
	End capture.End
	// Blocks lists the data blocks in file order.
	Blocks []BlockInfo
}

// BlockInfo locates one data block.
type BlockInfo struct {
	// Offset is the file offset of the block's type byte.
	Offset uint64
	// First and Last are the earliest and latest sample times in the block.
	First, Last capture.Ticks
	// Streams are the ids of the streams with samples in the block, in order.
	Streams []uint32
}

// ErrNoFooter is returned by ReadIndex for a file without a valid footer,
// such as one cut short by a crash. Repair rewrites such a file.
var ErrNoFooter = errors.New("mbcap: no valid footer")

// ReadIndex reads the index of a complete file from its end, without reading
// the data blocks. It checks the footer and the index block's CRC, not the
// rest of the file.
func ReadIndex(r io.ReaderAt, size int64) (*Index, error) {
	if size < int64(len(Magic))+FooterLen {
		return nil, ErrNoFooter
	}
	var foot [FooterLen]byte
	if _, err := r.ReadAt(foot[:], size-FooterLen); err != nil {
		return nil, err
	}
	if foot[0] != blockFooter || foot[1] != 12 || string(foot[10:14]) != footerTag ||
		crc32.Checksum(foot[:14], castagnoli) != binary.BigEndian.Uint32(foot[14:]) {
		return nil, ErrNoFooter
	}
	off := binary.BigEndian.Uint64(foot[2:10])
	if off < uint64(len(Magic)) || off >= uint64(size-FooterLen) {
		return nil, fmt.Errorf("mbcap: %w: footer points outside the file", capture.ErrCorrupt)
	}
	sec := io.NewSectionReader(r, int64(off), size-FooterLen-int64(off))
	ir := &Reader{br: bufio.NewReader(sec)}
	typ, payload, err := ir.readBlock()
	if err != nil {
		return nil, fmt.Errorf("mbcap: index: %w", err)
	}
	if typ != blockIndex {
		return nil, fmt.Errorf("mbcap: %w: footer points to a block of type 0x%02x", capture.ErrCorrupt, typ)
	}
	end, blocks, err := decodeIndex(payload)
	if err != nil {
		return nil, fmt.Errorf("mbcap: index: %w", err)
	}
	idx := &Index{End: end}
	for _, b := range blocks {
		idx.Blocks = append(idx.Blocks, BlockInfo{Offset: b.offset, First: capture.Ticks(b.first), Last: capture.Ticks(b.last), Streams: b.streams})
	}
	return idx, nil
}

// RepairReport says what Repair kept.
type RepairReport struct {
	// Samples is the number of samples written.
	Samples int
	// SkippedBlocks is the number of blocks skipped for a bad CRC.
	SkippedBlocks int
	// Truncated is true when the input ended before its index and footer.
	Truncated bool
	// Stopped is the error that ended the copy early, if any; records after
	// it are lost.
	Stopped error
	// End is the capture end written: the recorded one if the input still
	// had it, otherwise unknown.
	End capture.End
}

// Repair reads src as far as it can and writes a complete file to dst, with
// a new index and footer. It skips blocks with a bad CRC and stops at a
// truncation or any other error, keeping everything before it. The end is
// unknown unless src still holds its index. It fails only when the header
// cannot be read or dst cannot be written.
func Repair(dst io.Writer, src io.Reader, opts *Options) (RepairReport, error) {
	var rep RepairReport
	r, err := NewReader(src)
	if err != nil {
		return rep, err
	}
	r.SkipCorrupt = true
	w, err := NewWriter(dst, r.Header(), opts)
	if err != nil {
		return rep, err
	}
	for {
		rec, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			rep.Truncated = errors.Is(err, capture.ErrTruncated)
			if !rep.Truncated {
				rep.Stopped = err
			}
			break
		}
		if e, ok := rec.(capture.End); ok {
			rep.End = e
			continue
		}
		if err := capture.Write(w, rec); err != nil {
			return rep, err
		}
		if _, ok := rec.(capture.Sample); ok {
			rep.Samples++
		}
	}
	rep.SkippedBlocks = r.Skipped()
	if rep.End.Known && rep.End.At < w.ck.MaxAt() {
		rep.End = capture.End{}
	}
	return rep, w.Close(rep.End)
}
