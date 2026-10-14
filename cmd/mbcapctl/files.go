// SPDX-License-Identifier: MIT

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/format"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

// input is an open capture file.
type input struct {
	path   string
	f      *os.File
	r      capture.Reader
	format format.Format
}

func (e *env) open(path string) (*input, error) {
	var src io.Reader
	in := &input{path: path}
	if path == "-" {
		src = e.stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		in.f, src = f, f
	}
	r, ft, err := format.Open(src)
	if err != nil {
		in.close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	in.r, in.format = r, ft
	return in, nil
}

func (in *input) close() {
	if in.f != nil {
		_ = in.f.Close()
	}
}

// readAll reads a whole capture file.
func (e *env) readAll(path string) (*capture.Capture, error) {
	in, err := e.open(path)
	if err != nil {
		return nil, err
	}
	defer in.close()
	c, err := capture.ReadAll(in.r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// output is a file being written. It is written to a temporary file next to
// its destination and renamed into place by commit, so that a failed command
// leaves no half-written file and never clobbers its input.
type output struct {
	path string
	tmp  *os.File
	w    io.Writer
}

func (e *env) create(path string, inputs ...string) (*output, error) {
	if path == "-" {
		return &output{path: path, w: e.stdout}, nil
	}
	for _, in := range inputs {
		if in != "-" && samePath(in, path) {
			return nil, usageErr("the output %s is also an input", path)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return nil, err
	}
	return &output{path: path, tmp: tmp, w: tmp}, nil
}

func samePath(a, b string) bool {
	ia, errA := os.Stat(a)
	ib, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ia, ib)
	}
	pa, _ := filepath.Abs(a)
	pb, _ := filepath.Abs(b)
	return pa == pb
}

func (o *output) commit() error {
	if o.tmp == nil {
		return nil
	}
	if err := o.tmp.Close(); err != nil {
		_ = os.Remove(o.tmp.Name())
		return err
	}
	return os.Rename(o.tmp.Name(), o.path)
}

func (o *output) abort() {
	if o.tmp != nil {
		_ = o.tmp.Close()
		_ = os.Remove(o.tmp.Name())
	}
}

// outputOptions are the flags that choose how a capture is written.
type outputOptions struct {
	format      string
	compression string
	blockSize   int
}

var outOpts outputOptions

func outputFlags(fs *flag.FlagSet) {
	outOpts = outputOptions{}
	fs.StringVar(&outOpts.format, "format", "", "output encoding: mbcap, jsonl, csv or wide-csv (default: from the output file extension)")
	fs.StringVar(&outOpts.compression, "compression", "deflate", "mbcap data block compression: deflate or none")
	fs.IntVar(&outOpts.blockSize, "block-samples", mbcap.DefaultBlockSamples, "mbcap samples per data block")
}

// outputFormat resolves the encoding of an output file.
func outputFormat(path string) (format.Format, error) {
	if outOpts.format != "" {
		return format.Parse(outOpts.format)
	}
	if f := format.FromPath(path); f != format.Unknown {
		return f, nil
	}
	if path == "-" {
		return format.JSONL, nil
	}
	return format.Unknown, usageErr("cannot tell the encoding of %s from its name: use --format", path)
}

func binaryOptions() (*mbcap.Options, error) {
	o := &mbcap.Options{BlockSamples: outOpts.blockSize}
	switch outOpts.compression {
	case "deflate", "":
	case "none":
		o.Compression = mbcap.NoCompression
	default:
		return nil, usageErr("unknown compression %q (want deflate or none)", outOpts.compression)
	}
	return o, nil
}

// writerFor opens a capture writer on out in encoding f.
func writerFor(out *output, f format.Format, h capture.Header) (capture.Writer, error) {
	opts, err := binaryOptions()
	if err != nil {
		return nil, err
	}
	return format.NewWriter(f, out.w, h, opts)
}

// timeFlag is a time since the capture start: a Go duration ("90s",
// "1m30s") or a number of ticks ("1234t").
type timeFlag struct {
	set   bool
	dur   time.Duration
	ticks *capture.Ticks
}

func (t *timeFlag) String() string {
	switch {
	case !t.set:
		return ""
	case t.ticks != nil:
		return fmt.Sprintf("%dt", *t.ticks)
	}
	return t.dur.String()
}

func (t *timeFlag) Set(s string) error {
	t.set = true
	if n, ok := strings.CutSuffix(s, "t"); ok {
		v, err := strconv.ParseUint(n, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number of ticks", s)
		}
		tk := capture.Ticks(v)
		t.ticks = &tk
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	if d < 0 {
		return errors.New("a time since the capture start cannot be negative")
	}
	t.dur = d
	return nil
}

// resolve converts the flag into ticks of h, or nil when not set.
func (t *timeFlag) resolve(h capture.Header) *capture.Ticks {
	switch {
	case !t.set:
		return nil
	case t.ticks != nil:
		return t.ticks
	}
	v := h.Ticks(t.dur)
	return &v
}

// listFlag collects comma-separated unsigned numbers; it may be repeated.
// Function codes are decimal as in "FC43"; a 0x prefix makes them hex.
type listFlag struct {
	bits int
	vals []uint64
}

func (l *listFlag) String() string {
	parts := make([]string, len(l.vals))
	for i, v := range l.vals {
		parts[i] = strconv.FormatUint(v, 10)
	}
	return strings.Join(parts, ",")
}

func (l *listFlag) Set(s string) error {
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Decimal unless 0x-prefixed: a leading 0 is not octal here.
		base, digits := 10, p
		if h, ok := strings.CutPrefix(strings.ToLower(p), "0x"); ok {
			base, digits = 16, h
		}
		v, err := strconv.ParseUint(digits, base, l.bits)
		if err != nil {
			return fmt.Errorf("%q is not an unsigned %d-bit number", p, l.bits)
		}
		l.vals = append(l.vals, v)
	}
	return nil
}

func (l *listFlag) u32() []uint32 {
	out := make([]uint32, len(l.vals))
	for i, v := range l.vals {
		out[i] = uint32(v)
	}
	return out
}

func (l *listFlag) u8() []uint8 {
	out := make([]uint8, len(l.vals))
	for i, v := range l.vals {
		out[i] = uint8(v)
	}
	return out
}
