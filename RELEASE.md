# go-modbus-capture Releases

## v0.0.3

**Date:** 2026-10-31
**Previous release:** v0.0.2

### Changed

- Policy 1.2: build output in `bin/` and `release/`, a per-package coverage floor, and more tests and CI. No change in behaviour.

---

## v0.0.2

**Date:** 2026-10-15
**Previous release:** v0.0.1

### Summary

Captures hold the read functions of the Modbus specification and nothing else. v0.0.1 rejected only the write function codes, so it accepted vendor and unknown function codes, which were never in scope.

### Changed

- **`capture.CheckPDUs`, and with it every reader and writer:** the request function code must be a read function of the specification: FC01–04, 07, 08, 11, 12, 17, 20, 24 or 43. FC08 is accepted only with a data-returning sub-function (0x0000, 0x0002, 0x000B–0x0012), FC43 only with MEI type 14. Anything else is `ErrCorrupt`: a writer refuses the sample and a reader reports it, never a panic. New `capture.IsReadFunction`.
- `docs/FORMAT.md`, `REQUIREMENTS.md` section 6, `docs/concepts.md` and the README say so; the "other / vendor function" row is gone. `mbcapctl info` no longer names vendor functions.
- Test data: the vendor sample is removed from `testdata/every-fc.*` (regenerated) and from the synthetic captures; tests cover every accepted and rejected case.
- `docs/HANDOFF-MODBUSCTL.md`: go-modbus v1.7.0 resolves serving FC08, FC17, FC20 and FC24 through typed handlers; no raw request send or raw handler is needed.
- `make golden` runs the golden test in `mbcap`, where it lives.

### Compatibility

A capture with a vendor or unknown function code, a state-changing FC08 sub-function or FC43 with another MEI type, which v0.0.1 accepted, is now invalid. The file layout is unchanged.

---

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
