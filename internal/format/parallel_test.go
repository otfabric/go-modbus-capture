// SPDX-License-Identifier: MIT

package format

import (
	"bytes"
	"fmt"
	"sync"
	"testing"

	"github.com/otfabric/go-modbus-capture/capture"
	"github.com/otfabric/go-modbus-capture/edit"
	"github.com/otfabric/go-modbus-capture/internal/synth"
	"github.com/otfabric/go-modbus-capture/mbcap"
	"github.com/otfabric/go-modbus-capture/mbcsv"
)

// pipeline runs writers, readers, converters and every edit operation on
// one capture and returns everything they wrote. It must give the same
// bytes however many pipelines run at once: run under -race, this catches
// state shared between Writers, Readers or operations (pools, tables,
// package variables).
func pipeline(seed int64) ([]byte, error) {
	c := synth.Generate(synth.Config{Seed: seed, Polls: 20, Mix: synth.Busy, Extras: true, ChangeOnly: seed%2 == 0})
	var all bytes.Buffer
	emit := func(name string, b []byte) {
		_, _ = fmt.Fprintf(&all, "== %s %d\n", name, len(b))
		_, _ = all.Write(b)
	}

	// Convert along a chain of encodings; every step reads the previous one.
	cur := c
	for _, f := range []Format{Binary, JSONL, CSV, Binary} {
		var buf bytes.Buffer
		w, err := NewWriter(f, &buf, cur.Header, &mbcap.Options{BlockSamples: 1 + int(seed%17)})
		if err != nil {
			return nil, err
		}
		if err := cur.WriteTo(w); err != nil {
			return nil, err
		}
		emit(f.String(), buf.Bytes())
		r, got, err := Open(bytes.NewReader(buf.Bytes()))
		if err != nil {
			return nil, err
		}
		if got != f {
			return nil, fmt.Errorf("detected %v, wrote %v", got, f)
		}
		if cur, err = capture.ReadAll(r); err != nil {
			return nil, err
		}
		if d := capture.Diff(c, cur); d != "" {
			return nil, fmt.Errorf("after %v: %s", f, d)
		}
	}
	var wide bytes.Buffer
	if _, err := mbcsv.WriteWide(&wide, c); err != nil {
		return nil, err
	}
	emit("wide", wide.Bytes())

	// Edit operations, each writing JSON Lines to memory.
	open := func(name string) edit.OpenFunc {
		return func(h capture.Header) (capture.Writer, error) {
			buf := &bytes.Buffer{}
			w, err := NewWriter(JSONL, buf, h, nil)
			return &emitWriter{Writer: w, done: func() { emit(name, buf.Bytes()) }}, err
		}
	}
	reader := func() capture.Reader {
		var buf bytes.Buffer
		w, _ := NewWriter(Binary, &buf, c.Header, nil)
		_ = c.WriteTo(w)
		r, _ := mbcap.NewReader(&buf)
		return r
	}
	from, to := capture.Ticks(2_000_000), capture.Ticks(9_000_000)
	if _, err := edit.Filter(reader(), edit.Selection{Targets: []uint32{2}, Functions: []uint8{0x01, 0x2B}}, "filter", open("filter")); err != nil {
		return nil, err
	}
	if _, err := edit.Trim(reader(), &from, &to, "trim", open("trim")); err != nil {
		return nil, err
	}
	parts := map[string]*bytes.Buffer{}
	if _, err := edit.Split(reader(), edit.ByStream, "split", func(key string, h capture.Header) (capture.Writer, error) {
		parts[key] = &bytes.Buffer{}
		return NewWriter(CSV, parts[key], h, nil)
	}); err != nil {
		return nil, err
	}
	var inputs []edit.Input
	for _, key := range []string{"stream-1", "stream-2", "stream-3"} {
		emit("split "+key, parts[key].Bytes())
		r, _, err := Open(bytes.NewReader(parts[key].Bytes()))
		if err != nil {
			return nil, err
		}
		pc, err := capture.ReadAll(r)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, edit.Input{Name: key, Capture: pc})
	}
	if _, err := edit.Merge(inputs, edit.Interleave, "merge", open("merge")); err != nil {
		return nil, err
	}
	return all.Bytes(), nil
}

// emitWriter calls done once the capture is closed.
type emitWriter struct {
	capture.Writer
	done func()
}

func (w *emitWriter) Close(end capture.End) error {
	err := w.Writer.Close(end)
	w.done()
	return err
}

// Independent writers, readers, converters and edit operations running at
// once on separate data give the same bytes as one at a time.
func TestParallel(t *testing.T) {
	const seeds, copies = 6, 4
	want := make([][]byte, seeds)
	for s := range seeds {
		b, err := pipeline(int64(s))
		if err != nil {
			t.Fatalf("seed %d: %v", s, err)
		}
		want[s] = b
	}
	var wg sync.WaitGroup
	errs := make(chan error, seeds*copies)
	for s := range seeds {
		for range copies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				b, err := pipeline(int64(s))
				switch {
				case err != nil:
					errs <- fmt.Errorf("seed %d: %w", s, err)
				case !bytes.Equal(b, want[s]):
					errs <- fmt.Errorf("seed %d: the output differs when pipelines run concurrently", s)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
