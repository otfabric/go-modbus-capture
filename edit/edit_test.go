// SPDX-License-Identifier: MIT

package edit

import (
	"bytes"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/jsonl"
)

// mem is an OpenFunc target that keeps the result in memory, going through a
// real encoder so that the writer's checks apply.
type mem struct {
	buf bytes.Buffer
	t   *testing.T
}

func (m *mem) open(h capture.Header) (capture.Writer, error) { return jsonl.NewWriter(&m.buf, h) }

func (m *mem) capture() *capture.Capture {
	m.t.Helper()
	r, err := jsonl.NewReader(bytes.NewReader(m.buf.Bytes()))
	if err != nil {
		m.t.Fatalf("result: %v", err)
	}
	c, err := capture.ReadAll(r)
	if err != nil {
		m.t.Fatalf("result: %v", err)
	}
	return c
}

// reader returns c as a capture.Reader.
func reader(t *testing.T, c *capture.Capture) capture.Reader {
	t.Helper()
	var buf bytes.Buffer
	w, err := jsonl.NewWriter(&buf, c.Header)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WriteTo(w); err != nil {
		t.Fatal(err)
	}
	r, err := jsonl.NewReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ticks(v capture.Ticks) *capture.Ticks { return &v }

func extras() *capture.Capture {
	return synth.Generate(synth.Config{Seed: 7, Polls: 30, Mix: synth.Busy, Extras: true})
}

// samplesOf returns the samples of src that keep satisfies, as they are.
func samplesOf(src *capture.Capture, keep func(capture.Sample) bool) []capture.Sample {
	var out []capture.Sample
	for _, s := range src.Samples {
		if keep(s) {
			out = append(out, s)
		}
	}
	return out
}

func sameSamples(t *testing.T, got, want []capture.Sample) {
	t.Helper()
	a := &capture.Capture{Samples: got}
	b := &capture.Capture{Samples: want}
	if d := capture.Diff(a, b); d != "" {
		t.Fatalf("samples differ: %s", d)
	}
}

func TestFilter(t *testing.T) {
	src := extras()
	targetOf := map[uint32]uint32{}
	for _, s := range src.Streams {
		targetOf[s.ID] = s.Target
	}
	for _, tc := range []struct {
		name      string
		sel       Selection
		keep      func(capture.Sample) bool
		targets   []uint32
		streams   []uint32
		wantEndAt *capture.Ticks
	}{
		{"everything", Selection{}, func(capture.Sample) bool { return true }, []uint32{1, 2}, []uint32{1, 2, 3}, nil},
		{"one target", Selection{Targets: []uint32{2}}, func(s capture.Sample) bool { return targetOf[s.Stream] == 2 }, []uint32{2}, []uint32{2, 3}, nil},
		{"one stream defines only its target", Selection{Streams: []uint32{3}}, func(s capture.Sample) bool { return s.Stream == 3 }, []uint32{2}, []uint32{3}, nil},
		{"function codes", Selection{Functions: []uint8{0x03, 0x2B}}, func(s capture.Sample) bool { return s.Function() == 0x03 || s.Function() == 0x2B }, []uint32{1, 2}, []uint32{1, 2, 3}, nil},
		{"time window", Selection{From: ticks(5_000_000), To: ticks(9_000_000)}, func(s capture.Sample) bool { return s.At >= 5_000_000 && s.At < 9_000_000 }, []uint32{1, 2}, []uint32{1, 2, 3}, ticks(9_000_000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &mem{t: t}
			st, err := Filter(reader(t, src), tc.sel, "test filter", m.open)
			if err != nil {
				t.Fatal(err)
			}
			got := m.capture()
			want := samplesOf(src, tc.keep)
			sameSamples(t, got.Samples, want)
			if st.SamplesIn != len(src.Samples) || st.SamplesOut != len(want) {
				t.Errorf("stats %+v", st)
			}
			var ts, ss []uint32
			for _, x := range got.Targets {
				ts = append(ts, x.ID)
			}
			for _, x := range got.Streams {
				ss = append(ss, x.ID)
			}
			if !slices.Equal(ts, tc.targets) || !slices.Equal(ss, tc.streams) {
				t.Errorf("targets %v streams %v, want %v %v", ts, ss, tc.targets, tc.streams)
			}
			if !got.Header.WallStart.Equal(src.Header.WallStart) {
				t.Errorf("filter moved the wall-clock start")
			}
			if p := got.Header.Tags[capture.TagProvenance]; p != "test filter" {
				t.Errorf("provenance %q", p)
			}
			if got.Header.Tags["site"] != "lab" {
				t.Errorf("filter dropped a header tag")
			}
			wantEnd := src.End
			if tc.wantEndAt != nil {
				wantEnd.At = *tc.wantEndAt
			}
			if got.End != wantEnd {
				t.Errorf("end %+v, want %+v", got.End, wantEnd)
			}
		})
	}
	if _, err := Filter(reader(t, src), Selection{From: ticks(5), To: ticks(4)}, "", (&mem{t: t}).open); err == nil {
		t.Error("an inverted window was accepted")
	}
}

func TestTrim(t *testing.T) {
	src := extras()
	from, to := capture.Ticks(4_000_000), capture.Ticks(8_000_000)
	m := &mem{t: t}
	if _, err := Trim(reader(t, src), &from, &to, "test trim", m.open); err != nil {
		t.Fatal(err)
	}
	got := m.capture()
	want := samplesOf(src, func(s capture.Sample) bool { return s.At >= from && s.At < to })
	for i := range want {
		want[i].At -= from
	}
	sameSamples(t, got.Samples, want)
	if wantStart := src.Header.WallStart.Add(4 * time.Second); !got.Header.WallStart.Equal(wantStart) {
		t.Errorf("wall start %v, want %v", got.Header.WallStart, wantStart)
	}
	// Every sample keeps its wall-clock time.
	for i, s := range got.Samples {
		if !got.Header.Time(s.At).Equal(src.Header.Time(want[i].At + from)) {
			t.Fatalf("sample %d moved in wall-clock time", i)
		}
	}
	if got.End != (capture.End{Known: true, At: to - from}) {
		t.Errorf("end %+v", got.End)
	}
}

func TestSplit(t *testing.T) {
	src := extras()
	for _, by := range []SplitBy{ByTarget, ByStream, ByFunction} {
		parts := map[string]*mem{}
		counts, err := Split(reader(t, src), by, "test split", func(key string, h capture.Header) (capture.Writer, error) {
			m := &mem{t: t}
			parts[key] = m
			return m.open(h)
		})
		if err != nil {
			t.Fatal(err)
		}
		total := 0
		for key, m := range parts {
			c := m.capture()
			total += len(c.Samples)
			if counts[key] != len(c.Samples) {
				t.Errorf("%s: counted %d, holds %d", key, counts[key], len(c.Samples))
			}
			if !strings.HasSuffix(c.Header.Tags[capture.TagProvenance], "("+key+")") {
				t.Errorf("%s: provenance %q", key, c.Header.Tags[capture.TagProvenance])
			}
			if c.End != src.End {
				t.Errorf("%s: end %+v", key, c.End)
			}
			for _, s := range c.Samples {
				var target uint32
				for _, st := range c.Streams {
					if st.ID == s.Stream {
						target = st.Target
					}
				}
				if SplitKey(by, target, s.Stream, s.Function()) != key {
					t.Fatalf("%s holds a sample of %d/%d/%02x", key, target, s.Stream, s.Function())
				}
			}
		}
		// Every sample went to exactly one part.
		if total != len(src.Samples) {
			t.Errorf("by %d: %d samples in the parts, %d in the source", by, total, len(src.Samples))
		}
	}
}

func TestMerge(t *testing.T) {
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	req := []byte{0x03, 0, 0, 0, 1}
	one := func(tick uint64, wall time.Time, ats []capture.Ticks, end capture.End, tags map[string]string) *capture.Capture {
		c := &capture.Capture{
			Header:  capture.Header{TickNS: tick, WallStart: wall, Producer: "rec", Tags: tags},
			Targets: []capture.Target{{ID: 9, Label: "dev"}},
			Streams: []capture.Stream{{ID: 4, Target: 9}},
			End:     end,
		}
		for i, at := range ats {
			c.Samples = append(c.Samples, capture.Sample{Stream: 4, At: at, Request: req, Response: []byte{0x03, 2, 0, byte(i)}})
		}
		return c
	}
	// A: 1 µs ticks from 12:00:00. B: 1 ms ticks from 12:00:00.0015 (1.5 ms later).
	a := one(1000, start, []capture.Ticks{0, 2000, 5000}, capture.End{Known: true, At: 6000}, map[string]string{"site": "lab", "host": "a"})
	b := one(1_000_000, start.Add(1500*time.Microsecond), []capture.Ticks{0, 2}, capture.End{Known: true, At: 9}, map[string]string{"site": "lab", "host": "b"})
	inputs := []Input{{Name: "a.mbcap", Capture: a}, {Name: "b.mbcap", Capture: b}}

	wallOf := func(c *capture.Capture) []time.Time {
		var out []time.Time
		for _, s := range c.Samples {
			out = append(out, c.Header.Time(s.At))
		}
		return out
	}

	m := &mem{t: t}
	if _, err := Merge(inputs, Interleave, "test merge", m.open); err != nil {
		t.Fatal(err)
	}
	got := m.capture()
	// gcd(1000 ns, 1e6 ns, 1.5e6 ns offset) = 1000 ns.
	if got.Header.TickNS != 1000 || !got.Header.WallStart.Equal(start) {
		t.Fatalf("header %+v", got.Header)
	}
	want := []time.Time{start, start.Add(1500 * time.Microsecond), start.Add(2 * time.Millisecond),
		start.Add(3500 * time.Microsecond), start.Add(5 * time.Millisecond)}
	if !slices.EqualFunc(wallOf(got), want, time.Time.Equal) {
		t.Fatalf("interleaved times %v, want %v", wallOf(got), want)
	}
	if len(got.Targets) != 2 || got.Targets[0].ID != 1 || got.Targets[1].ID != 2 ||
		got.Streams[0] != (capture.Stream{ID: 1, Target: 1}) || got.Streams[1] != (capture.Stream{ID: 2, Target: 2}) {
		t.Fatalf("not renumbered: %+v %+v", got.Targets, got.Streams)
	}
	wantTags := map[string]string{"site": "lab", capture.TagSource: "a.mbcap, b.mbcap", capture.TagProvenance: "test merge"}
	if !maps.Equal(got.Header.Tags, wantTags) || got.Header.Producer != "rec" {
		t.Fatalf("tags %v producer %q", got.Header.Tags, got.Header.Producer)
	}
	// B ends at 9 ms after its start: 10.5 ms.
	if got.End != (capture.End{Known: true, At: 10500}) {
		t.Fatalf("end %+v", got.End)
	}

	m = &mem{t: t}
	if _, err := Merge(inputs, Concatenate, "", m.open); err != nil {
		t.Fatal(err)
	}
	cat := m.capture()
	wantCat := []time.Time{start, start.Add(2 * time.Millisecond), start.Add(5 * time.Millisecond),
		start.Add(1500 * time.Microsecond), start.Add(3500 * time.Microsecond)}
	if !slices.EqualFunc(wallOf(cat), wantCat, time.Time.Equal) {
		t.Fatalf("concatenated times %v, want %v", wallOf(cat), wantCat)
	}

	// An unknown end makes the merged end unknown; an odd offset lowers the tick.
	b.End = capture.End{}
	b.Header.WallStart = start.Add(1500*time.Microsecond + 7)
	m = &mem{t: t}
	if _, err := Merge(inputs, Interleave, "", m.open); err != nil {
		t.Fatal(err)
	}
	got = m.capture()
	if got.End.Known || got.Header.TickNS != 1 {
		t.Fatalf("end %+v tick %d, want unknown and 1 ns", got.End, got.Header.TickNS)
	}
	if !got.Header.Time(got.Samples[1].At).Equal(b.Header.WallStart) {
		t.Fatalf("a 1 ns offset was not kept exactly")
	}
}

func TestMergeFlags(t *testing.T) {
	co := synth.Generate(synth.Config{Seed: 1, Polls: 10, Mix: synth.MostlyStatic, ChangeOnly: true})
	full := synth.Generate(synth.Config{Seed: 2, Polls: 10, Mix: synth.MostlyStatic, Extras: true})
	m := &mem{t: t}
	if _, err := Merge([]Input{{Capture: co}, {Capture: co}}, Interleave, "", m.open); err != nil {
		t.Fatal(err)
	}
	if h := m.capture().Header; !h.ChangeOnly || h.Exceptions {
		t.Fatalf("two change-only inputs: %+v", h)
	}
	m = &mem{t: t}
	st, err := Merge([]Input{{Capture: co}, {Capture: full}}, Interleave, "", m.open)
	if err != nil {
		t.Fatal(err)
	}
	if h := m.capture().Header; h.ChangeOnly || !h.Exceptions {
		t.Fatalf("mixed inputs: %+v", h)
	}
	if st.SamplesOut != len(co.Samples)+len(full.Samples) {
		t.Fatalf("merge lost samples: %+v", st)
	}
}

func TestWithProvenance(t *testing.T) {
	h := capture.Header{Tags: map[string]string{capture.TagProvenance: "recorder"}}
	got := WithProvenance(h, "mbcapctl filter")
	if got.Tags[capture.TagProvenance] != "recorder | mbcapctl filter" {
		t.Fatalf("%q", got.Tags[capture.TagProvenance])
	}
	if h.Tags[capture.TagProvenance] != "recorder" {
		t.Fatal("WithProvenance changed its input")
	}
}
