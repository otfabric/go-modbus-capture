// SPDX-License-Identifier: MIT

// Package edit derives new captures from existing ones: Filter keeps a part,
// Trim cuts a time window and rebases it to 0, Split writes one capture per
// target, stream or function code, and Merge combines captures on their
// wall-clock starts. Every operation writes a new capture and records itself
// in the TagProvenance header tag. None of them changes a value, a PDU or the
// relative times of samples.
//
// Operations take an OpenFunc instead of a Writer, because the header of the
// result is only known once the operation has derived it.
package edit

import (
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/otfabric/go-modbus-capture/capture"
)

// OpenFunc opens the writer for a result capture with header h.
type OpenFunc func(h capture.Header) (capture.Writer, error)

// WithProvenance returns a copy of h whose TagProvenance tag ends with step.
// An earlier provenance is kept, separated by " | ".
func WithProvenance(h capture.Header, step string) capture.Header {
	h.Tags = maps.Clone(h.Tags)
	if step == "" {
		return h
	}
	if h.Tags == nil {
		h.Tags = map[string]string{}
	}
	if old := h.Tags[capture.TagProvenance]; old != "" {
		step = old + " | " + step
	}
	h.Tags[capture.TagProvenance] = step
	return h
}

// Selection picks part of a capture. Empty lists select everything; a nil
// bound is open.
type Selection struct {
	Targets   []uint32
	Streams   []uint32
	Functions []uint8
	// From and To select samples with From <= At < To.
	From, To *capture.Ticks
}

func (s *Selection) inWindow(at capture.Ticks) bool {
	return (s.From == nil || at >= *s.From) && (s.To == nil || at < *s.To)
}

// Stats counts what an operation read and wrote.
type Stats struct {
	SamplesIn, SamplesOut int
}

// Filter writes the selected part of src. A target is kept when it is
// selected and, if streams are selected, when one of them belongs to it; a
// stream when it and its target are selected. Ids are not renumbered. With a
// To bound, a known capture end moves to To if that is earlier.
func Filter(src capture.Reader, sel Selection, provenance string, open OpenFunc) (Stats, error) {
	return filter(src, sel, false, provenance, open)
}

// Trim writes the samples with From <= At < To (either bound may be nil)
// with times rebased so that From is tick 0; the wall-clock start moves by
// From. Values sampled before From are not carried into the window. A known
// capture end is cut to To and rebased too.
func Trim(src capture.Reader, from, to *capture.Ticks, provenance string, open OpenFunc) (Stats, error) {
	return filter(src, Selection{From: from, To: to}, true, provenance, open)
}

func filter(src capture.Reader, sel Selection, rebase bool, provenance string, open OpenFunc) (Stats, error) {
	var st Stats
	h := WithProvenance(src.Header(), provenance)
	var shift capture.Ticks
	if rebase && sel.From != nil {
		shift = *sel.From
		h.WallStart = h.Time(shift)
	}
	if sel.From != nil && sel.To != nil && *sel.To < *sel.From {
		return st, fmt.Errorf("edit: the window ends (%d) before it starts (%d)", *sel.To, *sel.From)
	}
	w, err := open(h)
	if err != nil {
		return st, err
	}
	targets := set(sel.Targets)
	streams := set(sel.Streams)
	fcs := set(sel.Functions)
	pending := map[uint32]capture.Target{} // targets waiting for a selected stream
	kept := map[uint32]bool{}              // kept streams
	end := capture.End{}
	for {
		rec, err := src.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return st, err
		}
		switch v := rec.(type) {
		case capture.Target:
			if targets != nil && !targets[v.ID] {
				continue
			}
			if streams != nil {
				pending[v.ID] = v
				continue
			}
			if err := w.WriteTarget(v); err != nil {
				return st, err
			}
		case capture.Stream:
			if streams != nil && !streams[v.ID] || targets != nil && !targets[v.Target] {
				continue
			}
			if t, ok := pending[v.Target]; ok {
				if err := w.WriteTarget(t); err != nil {
					return st, err
				}
				delete(pending, v.Target)
			}
			if err := w.WriteStream(v); err != nil {
				return st, err
			}
			kept[v.ID] = true
		case capture.Sample:
			st.SamplesIn++
			if !kept[v.Stream] || fcs != nil && !fcs[v.Function()] || !sel.inWindow(v.At) {
				continue
			}
			v.At -= shift
			if err := w.WriteSample(v); err != nil {
				return st, err
			}
			st.SamplesOut++
		case capture.End:
			end = v
		}
	}
	if end.Known && sel.To != nil && *sel.To < end.At {
		end.At = *sel.To
	}
	if end.Known {
		end.At -= min(shift, end.At)
	}
	return st, w.Close(end)
}

func set[T comparable](xs []T) map[T]bool {
	if len(xs) == 0 {
		return nil
	}
	m := make(map[T]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// SplitBy says how Split divides a capture.
type SplitBy int

// Ways to split.
const (
	ByTarget SplitBy = iota
	ByStream
	ByFunction
)

// SplitKey names the part of a split that a sample of stream (of target)
// with function code fc goes to: "target-<id>", "stream-<id>" or "fc-<hex>".
func SplitKey(by SplitBy, target, stream uint32, fc uint8) string {
	switch by {
	case ByTarget:
		return fmt.Sprintf("target-%d", target)
	case ByStream:
		return fmt.Sprintf("stream-%d", stream)
	default:
		return fmt.Sprintf("fc-%02x", fc)
	}
}

// SplitOpenFunc opens the writer of the part named key.
type SplitOpenFunc func(key string, h capture.Header) (capture.Writer, error)

// Split writes every sample of src to the part its SplitKey names, opened on
// first use, with the definitions it needs. Each part keeps the header (plus
// provenance naming the part), the ids and the capture end of src. A target
// or stream without samples goes to no part. It returns the number of
// samples per part.
func Split(src capture.Reader, by SplitBy, provenance string, open SplitOpenFunc) (map[string]int, error) {
	type part struct {
		w       capture.Writer
		targets map[uint32]bool
		streams map[uint32]bool
	}
	parts := map[string]*part{}
	counts := map[string]int{}
	targets := map[uint32]capture.Target{}
	streams := map[uint32]capture.Stream{}
	end := capture.End{}
	for {
		rec, err := src.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return counts, err
		}
		switch v := rec.(type) {
		case capture.Target:
			targets[v.ID] = v
		case capture.Stream:
			streams[v.ID] = v
		case capture.Sample:
			s, ok := streams[v.Stream]
			if !ok {
				return counts, fmt.Errorf("edit: sample of undefined stream %d", v.Stream)
			}
			key := SplitKey(by, s.Target, s.ID, v.Function())
			p := parts[key]
			if p == nil {
				step := provenance
				if step != "" {
					step += " (" + key + ")"
				}
				w, err := open(key, WithProvenance(src.Header(), step))
				if err != nil {
					return counts, err
				}
				p = &part{w: w, targets: map[uint32]bool{}, streams: map[uint32]bool{}}
				parts[key] = p
			}
			if !p.targets[s.Target] {
				if err := p.w.WriteTarget(targets[s.Target]); err != nil {
					return counts, err
				}
				p.targets[s.Target] = true
			}
			if !p.streams[s.ID] {
				if err := p.w.WriteStream(s); err != nil {
					return counts, err
				}
				p.streams[s.ID] = true
			}
			if err := p.w.WriteSample(v); err != nil {
				return counts, err
			}
			counts[key]++
		case capture.End:
			end = v
		}
	}
	for _, key := range slices.Sorted(maps.Keys(parts)) {
		if err := parts[key].w.Close(end); err != nil {
			return counts, fmt.Errorf("edit: closing part %s: %w", key, err)
		}
	}
	return counts, nil
}

// Input is one capture to merge, with the name recorded in TagSource.
type Input struct {
	Name    string
	Capture *capture.Capture
}

// MergeMode says how Merge orders samples.
type MergeMode int

// Merge modes.
const (
	// Interleave orders all samples by their wall-clock time (stable: ties
	// keep input order).
	Interleave MergeMode = iota
	// Concatenate keeps the samples of each input in order, inputs one
	// after the other.
	Concatenate
)

// Merge combines captures into one. Times are rebased on the earliest
// wall-clock start, exactly: the result's tick is the greatest common divisor
// of every tick size and start offset (in nanoseconds). Targets and streams
// are renumbered from 1 in input order. The header keeps the producer and
// tags that all inputs share, records the input names in TagSource, is
// change-only when every input is, and records exceptions when any input
// does. The end is the latest rebased end when every input has one, and
// unknown otherwise. Alignment between inputs is only as good as the clocks
// of the hosts that recorded them.
func Merge(inputs []Input, mode MergeMode, provenance string, open OpenFunc) (Stats, error) {
	var st Stats
	if len(inputs) == 0 {
		return st, fmt.Errorf("edit: nothing to merge")
	}
	wallMin := inputs[0].Capture.Header.WallStart
	for _, in := range inputs[1:] {
		if in.Capture.Header.WallStart.Before(wallMin) {
			wallMin = in.Capture.Header.WallStart
		}
	}
	tick := uint64(0)
	offsets := make([]uint64, len(inputs))
	for i, in := range inputs {
		offsets[i] = uint64(in.Capture.Header.WallStart.Sub(wallMin))
		tick = gcd(gcd(tick, in.Capture.Header.TickNS), offsets[i])
	}
	// tick divides every input tick, so it is at most capture.MaxTickNS.

	h := capture.Header{TickNS: tick, WallStart: wallMin, ChangeOnly: true}
	h.Producer = inputs[0].Capture.Header.Producer
	h.Tags = maps.Clone(inputs[0].Capture.Header.Tags)
	delete(h.Tags, capture.TagProvenance)
	delete(h.Tags, capture.TagSource)
	var names []string
	for _, in := range inputs {
		ih := &in.Capture.Header
		h.ChangeOnly = h.ChangeOnly && ih.ChangeOnly
		h.Exceptions = h.Exceptions || ih.Exceptions
		if ih.Producer != h.Producer {
			h.Producer = ""
		}
		for k, v := range h.Tags {
			if ih.Tags[k] != v {
				delete(h.Tags, k)
			}
		}
		if in.Name != "" {
			names = append(names, in.Name)
		}
	}
	if len(names) > 0 {
		if h.Tags == nil {
			h.Tags = map[string]string{}
		}
		h.Tags[capture.TagSource] = joinNames(names)
	}
	h = WithProvenance(h, provenance)

	var out capture.Capture
	out.Header = h
	endKnown := true
	var end capture.Ticks
	var nextTarget, nextStream uint32
	for i, in := range inputs {
		c := in.Capture
		scale := c.Header.TickNS / tick
		off := offsets[i] / tick
		rebase := func(at capture.Ticks) (capture.Ticks, error) {
			if scale != 0 && uint64(at) > (^uint64(0)-off)/scale {
				return 0, fmt.Errorf("edit: input %d: time %d overflows when rebased", i, at)
			}
			return capture.Ticks(off + uint64(at)*scale), nil
		}
		tmap := map[uint32]uint32{}
		for _, t := range c.Targets {
			nextTarget++
			tmap[t.ID] = nextTarget
			t.ID = nextTarget
			t.Tags = maps.Clone(t.Tags)
			out.Targets = append(out.Targets, t)
		}
		smap := map[uint32]uint32{}
		for _, s := range c.Streams {
			nextStream++
			smap[s.ID] = nextStream
			s.ID, s.Target = nextStream, tmap[s.Target]
			out.Streams = append(out.Streams, s)
		}
		for _, s := range c.Samples {
			at, err := rebase(s.At)
			if err != nil {
				return st, err
			}
			s.Stream, s.At = smap[s.Stream], at
			out.Samples = append(out.Samples, s)
			end = max(end, at)
		}
		st.SamplesIn += len(c.Samples)
		if c.End.Known {
			at, err := rebase(c.End.At)
			if err != nil {
				return st, err
			}
			end = max(end, at)
		} else {
			endKnown = false
		}
	}
	if mode == Interleave {
		slices.SortStableFunc(out.Samples, func(a, b capture.Sample) int {
			switch {
			case a.At < b.At:
				return -1
			case a.At > b.At:
				return 1
			}
			return 0
		})
	}
	if endKnown {
		out.End = capture.End{Known: true, At: end}
	}
	w, err := open(out.Header)
	if err != nil {
		return st, err
	}
	st.SamplesOut = len(out.Samples)
	return st, out.WriteTo(w)
}

func gcd(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func joinNames(names []string) string {
	s := ""
	for i, n := range names {
		if i > 0 {
			s += ", "
		}
		s += n
	}
	return s
}
