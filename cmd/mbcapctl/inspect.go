// SPDX-License-Identifier: MIT

package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/mbcap"
)

var functionNames = map[uint8]string{
	0x01: "Read Coils",
	0x02: "Read Discrete Inputs",
	0x03: "Read Holding Registers",
	0x04: "Read Input Registers",
	0x07: "Read Exception Status",
	0x08: "Diagnostics",
	0x0B: "Get Comm Event Counter",
	0x0C: "Get Comm Event Log",
	0x11: "Report Server ID",
	0x14: "Read File Record",
	0x18: "Read FIFO Queue",
	0x2B: "Encapsulated Interface (Read Device Identification)",
}

func functionName(fc uint8) string {
	if n, ok := functionNames[fc]; ok {
		return n
	}
	return "not a Modbus read function"
}

// Info is the summary info prints, also as JSON.
type Info struct {
	File        string            `json:"file"`
	Format      string            `json:"format"`
	TickNS      uint64            `json:"tick_ns"`
	WallStart   time.Time         `json:"wall_start"`
	Producer    string            `json:"producer,omitempty"`
	ChangeOnly  bool              `json:"change_only,omitempty"`
	Exceptions  bool              `json:"exceptions,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Samples     int               `json:"samples"`
	ExcSamples  int               `json:"exception_samples"`
	First       *capture.Ticks    `json:"first_at,omitempty"`
	Last        *capture.Ticks    `json:"last_at,omitempty"`
	End         *capture.Ticks    `json:"end_at,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
	Functions   map[string]int    `json:"functions"`
	Targets     []TargetInfo      `json:"targets"`
	Streams     []*StreamInfo     `json:"streams"`
	BinaryIndex *IndexInfo        `json:"binary_index,omitempty"`
}

// TargetInfo describes a target.
type TargetInfo struct {
	ID       uint32            `json:"id"`
	Label    string            `json:"label,omitempty"`
	Endpoint string            `json:"endpoint"`
	Unit     uint8             `json:"unit"`
	Tags     map[string]string `json:"tags,omitempty"`
}

// StreamInfo describes a stream and its samples.
type StreamInfo struct {
	ID         uint32         `json:"id"`
	Target     uint32         `json:"target"`
	Role       string         `json:"role,omitempty"`
	Label      string         `json:"label,omitempty"`
	Samples    int            `json:"samples"`
	ExcSamples int            `json:"exception_samples"`
	Requests   int            `json:"distinct_requests"`
	First      *capture.Ticks `json:"first_at,omitempty"`
	Last       *capture.Ticks `json:"last_at,omitempty"`
	// Cadence is the median interval between two samples of the same
	// request, in ticks.
	Cadence   *capture.Ticks `json:"median_interval,omitempty"`
	Functions map[string]int `json:"functions"`

	lastAt    map[string]capture.Ticks
	intervals []capture.Ticks
}

// IndexInfo summarises the index of a complete binary file.
type IndexInfo struct {
	DataBlocks int `json:"data_blocks"`
}

var infoJSON bool

func infoFlags(fs *flag.FlagSet) {
	infoJSON = false
	fs.BoolVar(&infoJSON, "json", false, "print the summary as JSON")
}

func endpoint(t capture.Target) string {
	switch {
	case !t.Serial.IsZero():
		s := t.Serial
		return fmt.Sprintf("%s %s %d %d%s%d", t.Transport, s.Device, s.Baud, s.DataBits, strings.ToUpper(s.Parity.String()[:1]), s.StopBits)
	case t.Host != "" || t.Port != 0:
		return fmt.Sprintf("%s %s:%d", t.Transport, t.Host, t.Port)
	}
	return t.Transport.String()
}

func fcKey(fc uint8) string { return fmt.Sprintf("%02x", fc) }

func runInfo(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 1 {
		return usageErr("info takes one file")
	}
	in, err := e.open(args[0])
	if err != nil {
		return err
	}
	defer in.close()
	h := in.r.Header()
	info := &Info{File: args[0], Format: in.format.String(), TickNS: h.TickNS, WallStart: h.WallStart,
		Producer: h.Producer, ChangeOnly: h.ChangeOnly, Exceptions: h.Exceptions, Tags: h.Tags,
		Functions: map[string]int{}, Targets: []TargetInfo{}, Streams: []*StreamInfo{}}
	streams := map[uint32]*StreamInfo{}
	for {
		rec, err := in.r.Next()
		if err == io.EOF {
			break
		}
		if errors.Is(err, capture.ErrTruncated) {
			info.Truncated = true
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		switch v := rec.(type) {
		case capture.Target:
			info.Targets = append(info.Targets, TargetInfo{ID: v.ID, Label: v.Label, Endpoint: endpoint(v), Unit: v.Unit, Tags: v.Tags})
		case capture.Stream:
			si := &StreamInfo{ID: v.ID, Target: v.Target, Role: v.Role, Label: v.Label, Functions: map[string]int{}, lastAt: map[string]capture.Ticks{}}
			streams[v.ID] = si
			info.Streams = append(info.Streams, si)
		case capture.Sample:
			si := streams[v.Stream]
			info.Samples++
			si.Samples++
			info.Functions[fcKey(v.Function())]++
			si.Functions[fcKey(v.Function())]++
			if v.IsException() {
				info.ExcSamples++
				si.ExcSamples++
			}
			info.First, info.Last = minT(info.First, v.At), maxT(info.Last, v.At)
			si.First, si.Last = minT(si.First, v.At), maxT(si.Last, v.At)
			key := string(v.Request)
			if prev, ok := si.lastAt[key]; ok && v.At > prev {
				si.intervals = append(si.intervals, v.At-prev)
			}
			si.lastAt[key] = v.At
		case capture.End:
			at := v.At
			info.End = &at
		}
	}
	for _, si := range info.Streams {
		si.Requests = len(si.lastAt)
		if n := len(si.intervals); n > 0 {
			sort.Slice(si.intervals, func(i, j int) bool { return si.intervals[i] < si.intervals[j] })
			m := si.intervals[n/2]
			si.Cadence = &m
		}
	}
	if in.format.String() == "mbcap" && in.f != nil && !info.Truncated {
		if st, err := in.f.Stat(); err == nil {
			if idx, err := mbcap.ReadIndex(in.f, st.Size()); err == nil {
				info.BinaryIndex = &IndexInfo{DataBlocks: len(idx.Blocks)}
			}
		}
	}
	if infoJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	printInfo(e.stdout, info, &h)
	return nil
}

func minT(cur *capture.Ticks, v capture.Ticks) *capture.Ticks {
	if cur == nil || v < *cur {
		return &v
	}
	return cur
}

func maxT(cur *capture.Ticks, v capture.Ticks) *capture.Ticks {
	if cur == nil || v > *cur {
		return &v
	}
	return cur
}

func printInfo(w io.Writer, info *Info, h *capture.Header) {
	dur := func(t *capture.Ticks) string {
		if t == nil {
			return "-"
		}
		return h.Duration(*t).String()
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "File:\t%s (%s)\n", info.File, info.Format)
	if info.Producer != "" {
		_, _ = fmt.Fprintf(tw, "Producer:\t%s\n", info.Producer)
	}
	_, _ = fmt.Fprintf(tw, "Start:\t%s\n", info.WallStart.Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(tw, "Tick:\t%v\n", time.Duration(info.TickNS))
	var flags []string
	if info.ChangeOnly {
		flags = append(flags, "change-only")
	}
	if info.Exceptions {
		flags = append(flags, "exceptions recorded")
	}
	if len(flags) == 0 {
		flags = append(flags, "full capture")
	}
	_, _ = fmt.Fprintf(tw, "Mode:\t%s\n", strings.Join(flags, ", "))
	for _, k := range slices.Sorted(maps.Keys(info.Tags)) {
		_, _ = fmt.Fprintf(tw, "Tag:\t%s=%s\n", k, info.Tags[k])
	}
	switch {
	case info.Truncated:
		_, _ = fmt.Fprintf(tw, "End:\tunknown: the file is truncated after the sample at %s (run mbcapctl repair)\n", dur(info.Last))
	case info.End == nil:
		_, _ = fmt.Fprintf(tw, "End:\tnot recorded; last sample at %s\n", dur(info.Last))
	default:
		_, _ = fmt.Fprintf(tw, "End:\t%s\n", dur(info.End))
	}
	_, _ = fmt.Fprintf(tw, "Samples:\t%d (%d exception responses), first at %s, last at %s\n", info.Samples, info.ExcSamples, dur(info.First), dur(info.Last))
	if info.BinaryIndex != nil {
		_, _ = fmt.Fprintf(tw, "Data blocks:\t%d\n", info.BinaryIndex.DataBlocks)
	}
	_ = tw.Flush()
	if len(info.Functions) > 0 {
		_, _ = fmt.Fprintln(w, "\nFunctions:")
		for _, k := range slices.Sorted(maps.Keys(info.Functions)) {
			fc, _ := hex.DecodeString(k)
			_, _ = fmt.Fprintf(w, "  FC%02d (0x%s)  %-52s %d\n", fc[0], k, functionName(fc[0]), info.Functions[k])
		}
	}
	_, _ = fmt.Fprintln(w, "\nTargets:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, t := range info.Targets {
		_, _ = fmt.Fprintf(tw, "  %d\t%s\t%s\tunit %d\n", t.ID, quoteOrDash(t.Label), t.Endpoint, t.Unit)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintln(w, "\nStreams:")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "  id\ttarget\trole\tlabel\tsamples\trequests\tfirst\tlast\tinterval\tfunctions\n")
	for _, s := range info.Streams {
		fcs := slices.Sorted(maps.Keys(s.Functions))
		_, _ = fmt.Fprintf(tw, "  %d\t%d\t%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n", s.ID, s.Target, orDash(s.Role), quoteOrDash(s.Label),
			s.Samples, s.Requests, dur(s.First), dur(s.Last), dur(s.Cadence), strings.Join(fcs, ","))
	}
	_ = tw.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func quoteOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return fmt.Sprintf("%q", s)
}

func runVerify(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) == 0 {
		return usageErr("verify takes one or more files")
	}
	failed := 0
	for _, path := range args {
		n, end, ft, err := verifyOne(e, path)
		switch {
		case err != nil:
			failed++
			hint := ""
			if errors.Is(err, capture.ErrTruncated) {
				hint = " (run mbcapctl repair)"
			}
			_, _ = fmt.Fprintf(e.stdout, "%s: FAIL: %v%s\n", path, err, hint)
		default:
			endText := "end not recorded"
			if end.Known {
				endText = fmt.Sprintf("end at %d ticks", end.At)
			}
			_, _ = fmt.Fprintf(e.stdout, "%s: ok (%s, %d samples, %s)\n", path, ft, n, endText)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d files failed", failed, len(args))
	}
	return nil
}

func verifyOne(e *env, path string) (int, capture.End, string, error) {
	in, err := e.open(path)
	if err != nil {
		return 0, capture.End{}, "", err
	}
	defer in.close()
	n := 0
	var end capture.End
	for {
		rec, err := in.r.Next()
		if err == io.EOF {
			return n, end, in.format.String(), nil
		}
		if err != nil {
			return n, end, in.format.String(), err
		}
		switch v := rec.(type) {
		case capture.Sample:
			n++
		case capture.End:
			end = v
		}
	}
}

var dumpWall bool

func dumpFlags(fs *flag.FlagSet) {
	dumpWall = false
	fs.BoolVar(&dumpWall, "wall", false, "show sample times as wall-clock times instead of offsets")
}

func runDump(e *env, _ *flag.FlagSet, args []string) error {
	if len(args) != 1 {
		return usageErr("dump takes one file")
	}
	in, err := e.open(args[0])
	if err != nil {
		return err
	}
	defer in.close()
	h := in.r.Header()
	w := e.stdout
	_, _ = fmt.Fprintf(w, "header  format=%s tick=%v start=%s", in.format, time.Duration(h.TickNS), h.WallStart.Format(time.RFC3339Nano))
	if h.Producer != "" {
		_, _ = fmt.Fprintf(w, " producer=%q", h.Producer)
	}
	if h.ChangeOnly {
		_, _ = fmt.Fprint(w, " change-only")
	}
	if h.Exceptions {
		_, _ = fmt.Fprint(w, " exceptions")
	}
	for _, k := range slices.Sorted(maps.Keys(h.Tags)) {
		_, _ = fmt.Fprintf(w, " %s=%q", k, h.Tags[k])
	}
	_, _ = fmt.Fprintln(w)
	when := func(at capture.Ticks) string {
		if dumpWall {
			return h.Time(at).Format(time.RFC3339Nano)
		}
		return fmt.Sprintf("%12d  %14s", at, "+"+h.Duration(at).String())
	}
	for {
		rec, err := in.r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", args[0], err)
		}
		switch v := rec.(type) {
		case capture.Target:
			_, _ = fmt.Fprintf(w, "target  %d %s %s unit=%d", v.ID, quoteOrDash(v.Label), endpoint(v), v.Unit)
			for _, k := range slices.Sorted(maps.Keys(v.Tags)) {
				_, _ = fmt.Fprintf(w, " %s=%q", k, v.Tags[k])
			}
			_, _ = fmt.Fprintln(w)
		case capture.Stream:
			_, _ = fmt.Fprintf(w, "stream  %d target=%d role=%s label=%s\n", v.ID, v.Target, orDash(v.Role), quoteOrDash(v.Label))
		case capture.Sample:
			mark := ""
			if v.IsException() {
				mark = "  exception"
			}
			_, _ = fmt.Fprintf(w, "sample  %s  s%-3d %-24x -> %x%s\n", when(v.At), v.Stream, v.Request, v.Response, mark)
		case capture.End:
			_, _ = fmt.Fprintf(w, "end     %s\n", when(v.At))
		}
	}
}
