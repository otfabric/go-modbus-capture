// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/edit"
	"github.com/otfabric/go-modbus-capture/internal/format"
	"github.com/otfabric/go-modbus-capture/mbcap"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

// writeTo opens path as a capture output in its encoding and returns an
// edit.OpenFunc for it. finish commits the file once the operation succeeded,
// or removes it.
func (e *env) writeTo(path string, inputs ...string) (open edit.OpenFunc, finish func(error) error, err error) {
	f, err := e.outputFormat(path)
	if err != nil {
		return nil, nil, err
	}
	if f == format.WideCSV {
		return nil, nil, usageErr("wide-csv is a view: write it with mbcapctl convert")
	}
	if _, err := e.binaryOptions(); err != nil {
		return nil, nil, err
	}
	out, err := e.create(path, inputs...)
	if err != nil {
		return nil, nil, err
	}
	open = func(h capture.Header) (capture.Writer, error) { return e.writerFor(out, f, h) }
	finish = func(opErr error) error {
		if opErr != nil {
			out.abort()
			return opErr
		}
		return out.commit()
	}
	return open, finish, nil
}

func runConvert(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 2 {
		return usageErr("convert takes an input and an output file")
	}
	src, dst := args[0], args[1]
	f, err := e.outputFormat(dst)
	if err != nil {
		return err
	}
	if f == format.WideCSV {
		c, err := e.readAll(src)
		if err != nil {
			return err
		}
		out, err := e.create(dst, src)
		if err != nil {
			return err
		}
		st, err := mbcsv.WriteWide(out.w, c)
		if err != nil {
			out.abort()
			return err
		}
		if st.Skipped > 0 {
			_, _ = fmt.Fprintf(e.stderr, "mbcapctl convert: left out %d exception or malformed FC01-04 samples\n", st.Skipped)
		}
		return out.commit()
	}
	in, err := e.open(src)
	if err != nil {
		return err
	}
	defer in.close()
	open, finish, err := e.writeTo(dst, src)
	if err != nil {
		return err
	}
	// Conversion is lossless: the header, producer and tags stay as they are.
	w, err := open(in.r.Header())
	if err != nil {
		return finish(err)
	}
	return finish(capture.Copy(w, in.r))
}

func mergeFlags(o *options, fs *flag.FlagSet) {
	outputFlags(o, fs)
	fs.StringVar(&o.mergeOut, "o", "", "output file (required)")
}

func runCat(e *env, _ *flag.FlagSet, args []string) error {
	return runMergeMode(e, args, edit.Concatenate)
}

func runMerge(e *env, _ *flag.FlagSet, args []string) error {
	return runMergeMode(e, args, edit.Interleave)
}

func runMergeMode(e *env, args []string, mode edit.MergeMode) error {
	if e.opt.mergeOut == "" || len(args) == 0 {
		return usageErr("need -o OUT and at least one input")
	}
	var inputs []edit.Input
	for _, path := range args {
		c, err := e.readAll(path)
		if err != nil {
			return err
		}
		inputs = append(inputs, edit.Input{Name: filepath.Base(path), Capture: c})
	}
	open, finish, err := e.writeTo(e.opt.mergeOut, args...)
	if err != nil {
		return err
	}
	_, err = edit.Merge(inputs, mode, e.step, open)
	return finish(err)
}

func splitFlags(o *options, fs *flag.FlagSet) {
	outputFlags(o, fs)
	fs.StringVar(&o.splitBy, "by", "", "split per target, stream or fc (required)")
	fs.StringVar(&o.splitDir, "dir", ".", "directory for the parts")
}

func runSplit(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 1 {
		return usageErr("split takes one input file")
	}
	var by edit.SplitBy
	switch e.opt.splitBy {
	case "target":
		by = edit.ByTarget
	case "stream":
		by = edit.ByStream
	case "fc", "function":
		by = edit.ByFunction
	default:
		return usageErr("--by must be target, stream or fc")
	}
	src := args[0]
	in, err := e.open(src)
	if err != nil {
		return err
	}
	defer in.close()

	// Parts are named after the input: capture.mbcap → capture.stream-1.mbcap.
	f := in.format
	if e.opt.out.format != "" {
		if f, err = format.Parse(e.opt.out.format); err != nil {
			return usageErr("%v", err)
		}
	}
	if f == format.WideCSV {
		return usageErr("wide-csv is a view: convert the parts afterwards")
	}
	base, ext := splitName(filepath.Base(src), f)
	outs := map[string]*output{}
	_, err = edit.Split(in.r, by, e.step, func(key string, h capture.Header) (capture.Writer, error) {
		out, err := e.create(filepath.Join(e.opt.splitDir, base+"."+key+ext), src)
		if err != nil {
			return nil, err
		}
		outs[key] = out
		return e.writerFor(out, f, h)
	})
	for _, key := range sortedKeys(outs) {
		out := outs[key]
		if err != nil {
			out.abort()
			continue
		}
		if cerr := out.commit(); cerr != nil {
			err = cerr
		}
		_, _ = fmt.Fprintln(e.stdout, out.path)
	}
	return err
}

// splitName returns the base name of src without its capture extension, and
// the extension of the parts.
func splitName(src string, f format.Format) (string, string) {
	base := src
	for _, ext := range []string{".mbcap.jsonl", ".mbcap.csv", ".mbcap", ".jsonl", ".csv"} {
		if b, ok := strings.CutSuffix(strings.ToLower(base), ext); ok {
			base = base[:len(b)]
			break
		}
	}
	switch f {
	case format.JSONL:
		return base, ".mbcap.jsonl"
	case format.CSV:
		return base, ".mbcap.csv"
	default:
		return base, ".mbcap"
	}
}

func filterFlags(o *options, fs *flag.FlagSet) {
	outputFlags(o, fs)
	o.filter.targets = listFlag{bits: 32}
	o.filter.streams = listFlag{bits: 32}
	o.filter.functions = listFlag{bits: 8}
	fs.Var(&o.filter.from, "from", "keep samples at or after this time since the start (duration such as 90s, or ticks such as 1234t)")
	fs.Var(&o.filter.to, "to", "keep samples before this time since the start")
	fs.Var(&o.filter.targets, "target", "keep these target ids (comma-separated, repeatable)")
	fs.Var(&o.filter.streams, "stream", "keep these stream ids (comma-separated, repeatable)")
	fs.Var(&o.filter.functions, "fc", "keep these function codes, decimal as in FC43, or 0x-prefixed hex (comma-separated, repeatable)")
}

func runFilter(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 2 {
		return usageErr("filter takes an input and an output file")
	}
	in, err := e.open(args[0])
	if err != nil {
		return err
	}
	defer in.close()
	h := in.r.Header()
	sel := edit.Selection{
		Targets:   e.opt.filter.targets.u32(),
		Streams:   e.opt.filter.streams.u32(),
		Functions: e.opt.filter.functions.u8(),
		From:      e.opt.filter.from.resolve(h),
		To:        e.opt.filter.to.resolve(h),
	}
	open, finish, err := e.writeTo(args[1], args[0])
	if err != nil {
		return err
	}
	st, err := edit.Filter(in.r, sel, e.step, open)
	if err == nil {
		_, _ = fmt.Fprintf(e.stderr, "mbcapctl filter: kept %d of %d samples\n", st.SamplesOut, st.SamplesIn)
	}
	return finish(err)
}

func trimFlags(o *options, fs *flag.FlagSet) {
	outputFlags(o, fs)
	fs.Var(&o.trim.from, "from", "start of the window, which becomes time 0 (duration such as 90s, or ticks such as 1234t)")
	fs.Var(&o.trim.to, "to", "end of the window (exclusive)")
}

func runTrim(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 2 {
		return usageErr("trim takes an input and an output file")
	}
	if !e.opt.trim.from.set && !e.opt.trim.to.set {
		return usageErr("trim needs --from and/or --to")
	}
	in, err := e.open(args[0])
	if err != nil {
		return err
	}
	defer in.close()
	h := in.r.Header()
	open, finish, err := e.writeTo(args[1], args[0])
	if err != nil {
		return err
	}
	_, err = edit.Trim(in.r, e.opt.trim.from.resolve(h), e.opt.trim.to.resolve(h), e.step, open)
	return finish(err)
}

func runRepair(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 2 {
		return usageErr("repair takes an input and an output file")
	}
	src, dst := args[0], args[1]
	f, err := e.outputFormat(dst)
	if err != nil {
		return err
	}
	if f != format.Binary {
		return usageErr("repair writes mbcap; convert the repaired file afterwards")
	}
	opts, err := e.binaryOptions()
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := e.create(dst, src)
	if err != nil {
		return err
	}
	rep, err := mbcap.Repair(out.w, in, opts)
	if err != nil {
		out.abort()
		return fmt.Errorf("%s: %w", src, err)
	}
	if err := out.commit(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(e.stdout, "%s: kept %d samples", dst, rep.Samples)
	if rep.SkippedBlocks > 0 {
		_, _ = fmt.Fprintf(e.stdout, ", skipped %d damaged blocks", rep.SkippedBlocks)
	}
	if rep.Dropped > 0 {
		_, _ = fmt.Fprintf(e.stdout, ", dropped %d samples that repeat the one before (change-only)", rep.Dropped)
	}
	if rep.Truncated {
		_, _ = fmt.Fprint(e.stdout, ", input was truncated")
	}
	if rep.Stopped != nil {
		_, _ = fmt.Fprintf(e.stdout, ", stopped at: %v", rep.Stopped)
	}
	if rep.End.Known {
		_, _ = fmt.Fprintln(e.stdout, ", capture end kept")
	} else {
		_, _ = fmt.Fprintln(e.stdout, ", capture end unknown")
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
