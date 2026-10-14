# go-modbus-capture Releases

## v0.0.1

**Date:** 2026-10-14
**Previous release:** none (initial tagged release)

### Summary

First tagged release of the Modbus capture file library and the `mbcapctl` CLI: the MBCAP model, a
compact binary encoding, two lossless text encodings, a wide CSV view, edit operations, and file
tooling. Recording from devices and replaying captures are not part of this repository; they land in
`modbusctl`, built on this library. Standard library only, no dependencies; Go floor is **1.23**.

The file format is **draft 0.1**: the layouts are implemented and pinned by golden files, but they are
frozen as 1.0 only after calibration on real captures. Files written by this release will not be
readable by 1.0, and the APIs may still change.

### Added

#### Library

- **`capture`** — the model (`Header`, `Target`, `Stream`, `Sample`, `End`), the streaming `Reader` and
  `Writer` interfaces, `ReadAll`, `Copy`, `Equal`/`Diff`, and `Checker`, which every reader and writer
  uses to enforce the model's invariants (reads only, PDUs of 1 to 253 bytes, responses that answer
  their request, definitions before use, change-only and exception rules, bounded strings and tags).
  Sentinels `ErrTruncated` and `ErrCorrupt`. `SplitAddresses` splits FC01–04 responses into raw
  per-address values.
- **`mbcap`** — binary writer and reader: CRC32C-protected blocks, request templates, delta-coded
  times, RAW / SAME / DIFF response coding, per-block deflate, index and footer. Append-only (writes
  to pipes), `Flush` for live capture, readable up to the last complete block after a crash.
  `ReadIndex` reads the index from the end of a file; `Repair` rewrites a truncated or damaged file.
- **`jsonl`** — JSON Lines (`.mbcap.jsonl`), the canonical lossless text encoding.
- **`mbcsv`** — long CSV (`.mbcap.csv`), lossless and importable; `WriteWide`, a one-way view of raw
  values per address over time with sample and hold.
- **`edit`** — `Filter` (time window, targets, streams, function codes), `Trim` (window rebased to
  0), `Split` (per target, stream or function code) and `Merge` (interleaved or concatenated, rebased
  exactly on wall-clock starts, ids renumbered). Each records itself in the `provenance` header tag.

#### CLI (`mbcapctl`)

- `info` / `stat` (with `--json`), `verify`, `dump`, `convert`, `cat`, `merge`, `split`, `filter`,
  `trim`, `repair`. Works on capture files only; it never opens a Modbus connection.
- Encodings are detected from the content; outputs follow the file extension or `--format`; `-` is
  standard input or output. Outputs are written to a temporary file and renamed into place, and never
  overwrite an input.

#### Quality

- Golden files in `testdata/` (every read function, multi-target, paged FC43, exceptions, change-only,
  truncated, CRC-corrupted, unknown block type) and a byte-pinned layout test.
- Lossless round trips between all encodings; fuzz targets for every reader and the round trip;
  benchmarks; a synthetic capture generator.

### Notes

- Captures hold reads only: write function codes (FC05, 06, 15, 16, 22, 23) are rejected.
- Strings in a capture cannot contain carriage returns, so that long CSV stays lossless.
- zstd compression is not supported; a reader names the codec when it meets one.
