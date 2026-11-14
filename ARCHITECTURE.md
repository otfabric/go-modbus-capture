# Architecture

go-modbus-capture is a file format and its codecs. It has no network or serial I/O and starts no goroutines. The format is specified in [docs/FORMAT.md](docs/FORMAT.md); the library is described in [docs/library.md](docs/library.md).

## Layers

Matches the import graph (`go list -f '{{.ImportPath}} {{.Imports}}' ./...`):

```
cmd/mbcapctl
   │
   ├── internal/format    picks an encoding from the file name
   │      │
   ├── edit      mbcap      jsonl      mbcsv
   │      │        │          │          │
   └──────┴────────┴──────────┴──────────┘
                   │
                capture       the model, Reader, Writer, the invariants
```

`capture` imports no other package of this module. Every encoding imports only `capture`, so code written against `capture.Reader` and `capture.Writer` works with all of them. `internal/synth` and `internal/fuzzcap` generate captures for tests.

## Ownership

Readers and writers work on an `io.Reader` or `io.Writer` passed in by the caller. A writer's `Close` ends the capture (for binary: the open block, index and footer); it is not a close of the caller's stream. A reader returns records in file order through `Next`; `io.EOF` ends a complete capture.

Data has one owner at a time. What a reader returns is the caller's: the header and every record are copies, with their own PDUs and tag maps. A writer copies what it keeps of a record (the binary writer keeps the last response to each request of the open data block), so the caller may reuse its buffers after a call, and a closed binary writer holds none of its buffers. `edit.Merge` copies nothing: the samples it passes to the writer share their PDUs with its inputs, which it does not change. The `edit` operations do not close the writers they opened when they fail; the caller discards that output.

No reader or writer is safe for concurrent use. The module keeps no state outside its values, except one package-level variable in `mbcap` that only tests change (the size limit of the index).

## Binary layout

A file is written append-only, in independently decodable, checksummed, deflate-compressed blocks, with an index at the end. A file cut short by a crash reads up to its last complete block, and `mbcap.Repair` rewrites it. The byte layout is in [docs/FORMAT.md](docs/FORMAT.md).

## Platforms

Pure Go with the standard library only: no cgo and no platform-specific code.
