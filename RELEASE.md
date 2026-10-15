# go-modbus-capture Releases

## v0.0.2

**Date:** 2026-10-15
**Previous release:** v0.0.1

### Summary

Captures hold the read functions of the Modbus specification and nothing else. v0.0.1 rejected only the write function codes, so it accepted vendor and unknown function codes, which were never in scope. The release also adds chaos, parallel and end-to-end tests, fixes the bugs they found, and removes the documentation of the format MBCAP replaced.

### Changed

- **`capture.CheckPDUs`, and with it every reader and writer:** the request function code must be a read function of the specification: FC01–04, 07, 08, 11, 12, 17, 20, 24 or 43. FC08 is accepted only with a data-returning sub-function (0x0000, 0x0002, 0x000B–0x0012), FC43 only with MEI type 14. Anything else is `ErrCorrupt`: a writer refuses the sample and a reader reports it, never a panic. New `capture.IsReadFunction`.
- `docs/FORMAT.md`, `REQUIREMENTS.md` section 6, `docs/concepts.md` and the README say so; the "other / vendor function" row is gone. `mbcapctl info` no longer names vendor functions.
- Test data: the vendor sample is removed from `testdata/every-fc.*` (regenerated) and from the synthetic captures; tests cover every accepted and rejected case.
- `docs/HANDOFF-MODBUSCTL.md`: go-modbus v1.7.0 resolves serving FC08, FC17, FC20 and FC24 through typed handlers; no raw request send or raw handler is needed.
- `make golden` runs the golden test in `mbcap`, where it lives.
- The documents no longer describe the format MBCAP replaced: `docs/MCAP-V1-ANALYSIS.md` is gone, and `REQUIREMENTS.md`, `CLAUDE.md`, the README, `docs/encodings.md`, `docs/SIZE-MODEL.md` (with `docs/sizemodel/bench.py`) and `docs/HANDOFF-MODBUSCTL.md` compare against raw PDUs or say only that MBCAP is the only capture format.
- The documentation states that a single Reader, Writer or `capture.Checker` is not safe for concurrent use, and that separate ones, and edit operations on separate captures, share no state.

### Added

- **Chaos tests** (`mbcap`, `jsonl`, `mbcsv`, helpers in `internal/fuzzcap`): valid synthetic captures (extras, change-only, exceptions, unknown end, fuzz-built captures) are damaged at random — bit flips, overwritten, inserted and deleted bytes, cuts at every block or line boundary and at random offsets, blocks or lines dropped, duplicated, swapped, moved and garbled. They check that readers never panic or hang, that errors wrap `ErrCorrupt` or `ErrTruncated` (a cut always `ErrTruncated`), that the samples before the damage come out intact and nothing is made up, and that `mbcap.Repair` writes a file that verifies and holds only original samples. `go test` runs 24 seeds per encoding, `-short` 4; `make chaos` runs `CHAOS_SEEDS` (500) under the race detector (`MBCAP_CHAOS_SEEDS`).
- **Parallel tests** under `-race`: writers, readers, converters and every edit operation (`internal/format`), and the `mbcapctl` command function with different flags (`cmd/mbcapctl`), running concurrently on separate data give the same bytes as one at a time.
- **End-to-end CLI test**: a two-target capture converted to JSON Lines and CSV and back (byte-identical binary), split per stream and per target, merged back to the original, filtered, trimmed, and checked with `info`, `verify` and `dump`; a JSON Lines file with a vendor function code fails every command with exit code 1 and a clear message, and leaves no output.
- `mbcap.Reader.Dropped` and `mbcap.RepairReport.Dropped`.

### Fixed

- **`mbcap.Repair` stopped after a damaged block in a change-only capture** (found by the chaos tests). With the block skipped, the next response could equal the last one kept; the reader reported that as a broken change-only rule and Repair lost every later block. A reader with `SkipCorrupt` now drops such a sample, as a change-only writer would, and Repair keeps the rest (`mbcapctl repair` reports the count).
- **Long CSV reported a cut file as corrupt** (found by the chaos tests). A last row cut short, with fewer fields than the column names, was `ErrCorrupt` ("wrong number of fields"), as was a cut inside the column names row (which could read as a column named twice). Both are now `ErrTruncated`, as `docs/FORMAT.md` section 4 requires.
- **`mbcapctl` kept its flags in package variables** (found by the parallel test), so two command lines run in one process raced; they now live in the run's own state.
- `mbcapctl split` wrote its `--dir` into the `provenance` tag; output directories are now left out, like output file names.

### Compatibility

A capture with a vendor or unknown function code, a state-changing FC08 sub-function or FC43 with another MEI type, which v0.0.1 accepted, is now invalid. The file layout is unchanged.

A long CSV file cut short is now `ErrTruncated` where v0.0.1 could say `ErrCorrupt`. `mbcap.Repair` and `SkipCorrupt` readers keep more of a damaged change-only capture than before.

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
