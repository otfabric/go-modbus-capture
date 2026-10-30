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

## Binary layout

A file is written append-only, in independently decodable, checksummed, deflate-compressed blocks, with an index at the end. A file cut short by a crash reads up to its last complete block, and `mbcap.Repair` rewrites it. The byte layout is in [docs/FORMAT.md](docs/FORMAT.md).

## Platforms

Pure Go with the standard library only: no cgo and no platform-specific code.
