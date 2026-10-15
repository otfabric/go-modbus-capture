# Go library

```bash
go get github.com/otfabric/go-modbus-capture
```

Standard library only; Go 1.23 or later. API reference:
[pkg.go.dev/github.com/otfabric/go-modbus-capture](https://pkg.go.dev/github.com/otfabric/go-modbus-capture).

| Package | Use it to |
|---|---|
| [`capture`](https://pkg.go.dev/github.com/otfabric/go-modbus-capture/capture) | work with the model: `Header`, `Target`, `Stream`, `Sample`, `End`; the `Reader` and `Writer` interfaces; `ReadAll`, `Copy`, `Diff`; the rule `Checker` |
| [`mbcap`](https://pkg.go.dev/github.com/otfabric/go-modbus-capture/mbcap) | read and write binary `.mbcap` files; `ReadIndex`; `Repair` |
| [`jsonl`](https://pkg.go.dev/github.com/otfabric/go-modbus-capture/jsonl) | read and write JSON Lines |
| [`mbcsv`](https://pkg.go.dev/github.com/otfabric/go-modbus-capture/mbcsv) | read and write long CSV; write the wide CSV view |
| [`edit`](https://pkg.go.dev/github.com/otfabric/go-modbus-capture/edit) | filter, trim, split and merge captures |

Every encoding implements the same two interfaces, so code written against `capture.Reader` and
`capture.Writer` works with all of them:

```go
type Reader interface {
	Header() Header
	Next() (Record, error) // Target, Stream, Sample or End; io.EOF after the last one
}

type Writer interface {
	WriteTarget(t Target) error
	WriteStream(s Stream) error
	WriteSample(s Sample) error
	Close(end End) error // End{} when the end is unknown
}
```

## Writing a capture

A recorder creates the writer with the header, defines its targets and streams, writes one sample per
answered request, and closes with the capture end.

```go
start := time.Now() // wall-clock start; time.Since(start) below uses the monotonic clock
h := capture.Header{TickNS: capture.DefaultTickNS, WallStart: start, Producer: "my-recorder 1.0"}

f, err := os.Create("site.mbcap")
if err != nil {
	return err
}
defer f.Close()

w, err := mbcap.NewWriter(f, h, nil) // nil: deflate, 256 samples per data block
if err != nil {
	return err
}
if err := w.WriteTarget(capture.Target{ID: 1, Label: "Inverter A", Transport: capture.TransportTCP,
	Host: "192.168.2.201", Port: 502, Unit: 21}); err != nil {
	return err
}
if err := w.WriteStream(capture.Stream{ID: 1, Target: 1, Role: capture.RolePoll}); err != nil {
	return err
}

// For every answered request: the raw PDUs, at the time the response arrived.
err = w.WriteSample(capture.Sample{
	Stream:   1,
	At:       h.Ticks(time.Since(start)),
	Request:  []byte{0x03, 0x10, 0x00, 0x00, 0x02},
	Response: []byte{0x03, 0x04, 0x00, 0x01, 0x01, 0xa7},
})

// ...

return w.Close(capture.End{Known: true, At: h.Ticks(time.Since(start))})
```

- **Times** are ticks since the start. Use `h.Ticks(time.Since(start))`: Go's `time.Since` reads the
  monotonic clock, so wall-clock steps do not distort the capture.
- **Definitions first.** Write a target before its streams and a stream before its samples. New targets and
  streams may be added at any point, for example when a scan finds a new unit.
- **What gets dropped.** A writer silently drops exception responses unless `Header.Exceptions` is set, and
  in a change-only capture (`Header.ChangeOnly`) every response equal to the previous response to the same
  request of the same stream. Anything else that breaks a rule (a write function, an undefined stream, a
  PDU longer than 253 bytes) is an error wrapping `capture.ErrCorrupt`.
- **Live capture.** Call `w.Flush()` on a timer: it ends the open data block and flushes it, after which
  the file is readable up to there. For low-latency live capture pass
  `&mbcap.Options{Compression: mbcap.NoCompression}`.
- **Not concurrent.** A writer is not safe for concurrent use. Serialise calls, for example by funnelling
  samples from all poll goroutines through one channel. Separate writers share no state and may
  run in parallel.
- `jsonl.NewWriter(w, h)` and `mbcsv.NewWriter(w, h)` work the same way, without options.

## Reading a capture

```go
f, err := os.Open("site.mbcap")
if err != nil {
	return err
}
defer f.Close()

r, err := mbcap.NewReader(f) // jsonl.NewReader, mbcsv.NewReader for the text encodings
if err != nil {
	return err
}
h := r.Header()
for {
	rec, err := r.Next()
	if err == io.EOF {
		break
	}
	if errors.Is(err, capture.ErrTruncated) {
		break // cut short, for example by a crash: everything before is valid, the end is unknown
	}
	if err != nil {
		return err // capture.ErrCorrupt: not a valid capture
	}
	switch v := rec.(type) {
	case capture.Target:
		// a device: v.ID, v.Label, v.Transport, v.Host, v.Port, v.Serial, v.Unit
	case capture.Stream:
		// how its data was acquired: v.ID, v.Target, v.Role
	case capture.Sample:
		fmt.Printf("%v stream %d: % x -> % x\n", h.Duration(v.At), v.Stream, v.Request, v.Response)
	case capture.End:
		// the capture end: values hold until h.Duration(v.At)
	}
}
```

- Records come in file order. Definitions always precede the samples that use them; the `End`, if any, is
  last. Sample times may go back slightly (concurrent streams).
- Readers check every rule of the model as they go. A record a reader returns is valid; you do not need to
  validate it again.
- Readers bound every length and count, and are fuzzed: they are safe on untrusted files.
- `capture.ReadAll(r)` loads a whole capture into a `*capture.Capture` (header, targets, streams, samples,
  end). It returns what it read so far together with the error.
- A reader is not safe for concurrent use either; read one file per goroutine. Separate readers, and
  edit operations on separate captures, share no state and may run in parallel.

## Errors

| Error | Meaning | What to do |
|---|---|---|
| `io.EOF` from `Next` | the capture ended normally | done |
| wraps `capture.ErrTruncated` | the input ended early: inside a record or block, or a binary file without its index | use what was read; the end is unknown. `mbcap.Repair` rewrites a binary file |
| wraps `capture.ErrCorrupt` | not a valid encoding, a checksum mismatch, or a record that breaks a rule | reject the file; `mbcap.Repair` can salvage a binary file with damaged blocks |

Error messages name the line (text encodings) or byte offset (binary) where the problem is.

## Converting

`capture.Copy` streams any reader into any writer, so conversion is two constructors and one call:

```go
r, err := jsonl.NewReader(in)
if err != nil {
	return err
}
w, err := mbcap.NewWriter(out, r.Header(), nil)
if err != nil {
	return err
}
return capture.Copy(w, r) // closes w with the capture end
```

Conversion between the lossless encodings is exact. `capture.Equal(a, b)` and `capture.Diff(a, b)`
compare two loaded captures.

## Working with values

The library never decodes register contents, but it splits FC01–04 responses into raw per-address
values, which is what a consumer needs to build per-address timelines:

```go
if av, ok := capture.SplitAddresses(&sample); ok {
	for i, v := range av.Values {
		// av.Function (1-4), address int(av.Start)+i, raw value v:
		// 0/1 for coils and discrete inputs, an unsigned 16-bit word for registers
	}
}
```

It returns false for other functions, exception responses, and responses whose size does not match the
request. For a chart-ready table, `mbcsv.WriteWide(w, c)` writes the [wide CSV view](encodings.md#wide-csv-view).

## Editing captures

The `edit` package derives new captures. Each operation takes an `edit.OpenFunc`, which opens the writer
for the result once the operation knows the result's header, and a provenance step that is appended to
the header's `provenance` tag.

```go
open := func(h capture.Header) (capture.Writer, error) { return mbcap.NewWriter(out, h, nil) }

// Keep FC03 samples of target 2 between 1 h and 3 h.
from, to := h.Ticks(time.Hour), h.Ticks(3*time.Hour)
sel := edit.Selection{Targets: []uint32{2}, Functions: []uint8{0x03}, From: &from, To: &to}
stats, err := edit.Filter(r, sel, "my-tool filter", open)
```

| Function | Does |
|---|---|
| `Filter(src, sel, step, open)` | keeps a selection; ids, times and wall-clock start unchanged |
| `Trim(src, from, to, step, open)` | keeps a window and rebases it to tick 0 (the wall-clock start moves with it) |
| `Split(src, by, step, open)` | writes one capture per target (`ByTarget`), stream (`ByStream`) or function code (`ByFunction`); `open` gets the part's key |
| `Merge(inputs, mode, step, open)` | combines loaded captures, rebased exactly on their wall-clock starts, ids renumbered; `Interleave` by time or `Concatenate` |

## Binary file extras

- `mbcap.ReadIndex(r io.ReaderAt, size)` reads the index of a complete file from its end, without reading
  the data: the capture end and, per data block, its offset, time range and streams.
- `mbcap.Repair(dst, src, opts)` rewrites a truncated or damaged file with a new index and reports what it
  kept (`RepairReport`).
- `Reader.SkipCorrupt = true` makes a reader skip blocks with a bad checksum instead of failing. In a
  change-only capture it then also drops a sample that repeats the response kept before the lost block
  (`Reader.Dropped`), as a change-only writer would.
- `mbcap.Options` sets the compression (`Deflate`, `NoCompression`) and the block size (`BlockSamples`,
  `BlockBytes`).

## Testing your code

`testdata/` in the repository holds small, hand-checkable captures in all three encodings: every read
function, several targets, a paged FC43 read, exceptions, change-only, and damaged binary files (truncated,
checksum error, unknown block type). They make good fixtures for code that consumes captures.
