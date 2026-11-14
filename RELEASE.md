# go-modbus-capture Releases

## v0.2.2

**Date:** 2026-11-14
**Previous release:** v0.2.1

### Fixed

- **`mbcap` writer, time steps:** a sample 2^63 ticks or more from the one before it, in either direction, starts a new data block, so that every time delta fits its field and the file reads back.
- **`mbcap` writer, long files:** the index is one block of at most 32 MiB, room for about 1.7 million data blocks. `WriteSample` returns an error wrapping `capture.ErrCorrupt` for the sample whose data block the index could not list. The sample is not written and the writer stays usable: `Close` completes the file, and a recorder continues in a new one. `mbcap.Repair` stops at such a sample and reports it in `RepairReport.Stopped`.
- **`mbcap` reader:** a block of an unknown type between the index and the footer is skipped, and bytes after the body of an uncompressed data block are ignored, as docs/FORMAT.md section 2.1 says. A minor version above 2^31-1 is `ErrCorrupt`: `Reader.Version` returns it as an `int` on every platform.
- **`jsonl` reader:** field names are matched exactly, as JSON names are. A field whose name differs from a defined one only in case is an unknown field and is ignored, as the specification says of unknown fields. A defined field that appears twice in a line, or twice in its `flags` or `serial` object, is `ErrCorrupt`.
- **Every writer:** a `Close` that refuses its end, because the end is before a sample, leaves the writer open, and a `Close` with a valid end then completes the capture (`capture.Checker.End` marks the capture ended only for an end it accepts).
- **`edit.Merge`:** an input without a capture, or with a header that `capture.CheckHeader` refuses, is an error wrapping `ErrCorrupt`.
- **`mbcapctl`, encoding detection:** a long CSV file is recognised by its row of column names wherever the `t` column stands and however long the row is, as docs/FORMAT.md sections 4 and 7 say.
- **Documents:** docs/FEATURE-STATUS.md and INTEROPERABILITY.md name the current releases, and the README says what a conversion keeps: the capture, not the bytes of a binary file.

### Changed

- **Samples per data block:** a binary data block holds at most 65 536 samples. The reader refuses a block that declares more, and the writer ends a block there whatever `Options.BlockSamples` asks for. A reader decodes one data block at a time, so this bounds what a block takes in memory, whatever its size in the file.
- **Fields per CSV row:** a long CSV row holds at most 4 096 fields, the row of column names included.
- **Parts of a split:** `edit.Split` and `mbcapctl split` write at most 4 096 parts and return an error for the sample that would need one more.
- **Memory:** readers allocate for the bytes they have read, not for a length or count that the file declares. A binary block or compressed body of more than 64 KiB is read into a buffer that grows as the bytes arrive, the block count of an index is compared with the data blocks read before an entry is decoded, and a CSV row ends at its field limit before it is taken in. The binary reader allocates less per sample. A closed `mbcap.Writer` holds none of its buffers.
- **`mbcapctl info` and `dump`** print the strings of a capture as text: a producer, tag, role, host or serial device with a control character, a line break or another character that is not printable is printed as a quoted Go string with escapes, as labels already were. Strings of printable characters print as before. Error messages of every command are printed on one line with the same escapes, and `info --json` writes such characters as `\u` escapes.
- **`Reader.Header`** of every encoding returns a header with its own `Tags` map.
- **Doc comments and documents** state what was true and unwritten: what a reader returns is the caller's, a writer copies what it keeps, no reader or writer is safe for concurrent use, the first error of the binary reader sticks while the text readers go on with the next line, a second `Close` is an error, and `edit` operations that fail leave the writers they opened to the caller. docs/FORMAT.md section 1 lists the new limits, LIMITATIONS.md has a section on memory, ERRORS.md says what state an error leaves, and TESTING.md has the lifecycle matrix.
- **`make fuzz`** runs every fuzz target of every package, discovered with `go test -list`. The nightly workflow also runs the tests on linux/386.

### Tests

- `FuzzBlocks` (`mbcap`) frames its input into blocks with correct lengths and CRCs, so that fuzzing reaches the block decoders; `FuzzReader` (`jsonl`) compares what it reads with a reading from the defined fields alone; `FuzzRoundTrip` covers time steps of about 2^63 ticks.
- Every limit of docs/FORMAT.md section 1 has a test at the limit and beyond it, and where a file declares a length or a count, a test that measures what the reader allocates for it.
- Ownership tests for every encoding, and one test of the reader and writer contracts across the three encodings.

### Compatibility

No exported identifier changes. What this release reads differently from v0.2.1:

- A binary file with more than 65 536 samples in one data block is refused. A writer of this library makes such blocks only with `Options.BlockSamples` and `Options.BlockBytes` both raised; `mbcapctl` does not, because its blocks end at 64 KiB.
- In a JSON Lines file, a field in another case than the specification's is ignored, and a line with a defined field twice is refused. No writer of this library writes either.
- A long CSV file with more than 4 096 columns is refused, and so is a split into more than 4 096 parts.
- Now read: a file with a block of an unknown type between the index and the footer, a file with bytes after the body of an uncompressed data block, and a long CSV file whose `t` column is not among the first.

---

## v0.2.1

**Date:** 2026-11-08
**Previous release:** v0.2.0

### Changed

- `mbcapctl` is built with the latest Go 1.26 patch release; v0.2.0 was built with go1.26.0.

---

## v0.2.0

**Date:** 2026-11-07
**Previous release:** v0.1.0

### Added

- `mbcapctl version --json`: the version as one line of JSON (`name`, `version`, `tag`, `commit`, `built`, `libraries`, `go`, `platform`).
- `mbcapctl completion bash|zsh|fish|powershell`: shell completion for commands, flags, the values of `--format`, `--compression` and `--by`, and capture files (`.mbcap`, `.jsonl`, `.csv`) for file arguments and `-o`. Generated with the standard library from the command table; no new dependency (REQUIREMENTS.md decision 16).

### Changed

- `mbcapctl version` prints the aligned OT Fabric line, `mbcapctl 0.2.0 (tag v0.2.0, commit <full sha>, built <date>, go1.26.x, linux/amd64)`; the tag is left out when there is none (it used to print `tag none`). A build that is not a release (`go build`, `go run`, pseudo-versions) reports `dev` and the commit from the Go build information, with `-dirty` for uncommitted changes; `go install ...@vX.Y.Z` reports X.Y.Z. The provenance tag uses the same version.
- `make build` injects the version and tag only when HEAD is exactly on a tag, and the full commit SHA.

---

## v0.1.0

**Date:** 2026-11-05
**Previous release:** v0.0.3

### Changed

- Go 1.26 or later is required (was 1.23); CI runs Go 1.26 and 1.27. `go fix` modernised the code; no change in behaviour or API.

---

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
