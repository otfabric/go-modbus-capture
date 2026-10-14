# go-modbus-capture — requirements

Status: design complete enough to start phase 2. Written 2026-10-14 from an investigation of the
existing MCAP v1 record/replay code in `otfabric/modbusctl`; scope narrowed the same day to the file
format and its encodings (see section 3 and decision 12). Companion documents:

- [`docs/FORMAT.md`](docs/FORMAT.md) — MBCAP binary, JSON Lines and CSV formats (draft spec)
- [`docs/HANDOFF-MODBUSCTL.md`](docs/HANDOFF-MODBUSCTL.md) — recorder and replay design, moved out of this repo
- [`docs/MCAP-V1-ANALYSIS.md`](docs/MCAP-V1-ANALYSIS.md) — what the old format does and why it is being replaced
- [`docs/SIZE-MODEL.md`](docs/SIZE-MODEL.md) and [`docs/sizemodel/bench.py`](docs/sizemodel/bench.py) — synthetic size estimates

This file is the hand-off for Claude Code. Where it says **MUST**, **SHOULD**, **MAY** it follows RFC 2119.

## 1. Purpose

`go-modbus-capture` is a Go library (plus the `mbcapctl` command) for **capture files**: files that hold
the data a Modbus device answered, together with when it answered, so that a **virtual device** can later
host exactly that data to a Modbus client at original, slowed or fast speed.

The library defines the model, the file format and its encodings, and the operations on capture files.
It does **not** capture from devices and does **not** replay or host them: recording and replaying land in
`otfabric/modbusctl` (and a later tool on the owner's roadmap), which build on this library. Their design
is kept in [`docs/HANDOFF-MODBUSCTL.md`](docs/HANDOFF-MODBUSCTL.md).

It is split out of `modbusctl`'s record/replay so that other tools (recorders, replayers, viewers, editors,
GUIs, simulators, digital twins) can build on one shared model and file format.

- Go module: `github.com/otfabric/go-modbus-capture`
- Format name: **MBCAP** (Modbus Capture). Extensions: `.mbcap` (binary), `.mbcap.jsonl` (lossless text), `.mbcap.csv` (lossless long CSV)
- CLI: `mbcapctl`, for capture files only
- There is **no** need to read or write the old MCAP v1 format (`.mcap`, magic `"MCAP"`). Do not add a v1 reader.
  (A throwaway script to convert a few v1 files for calibration is fine and stays out of the library.)

## 2. Goals

1. Store the response to **every Modbus read function**, including FC43/14 (Read Device Identification),
   not only FC01–04.
2. Hold **several targets and streams in one file**; a consumer can untangle them and use only one.
3. Keep **target metadata** (transport, IP/port or serial settings, unit id) as advisory data that a
   consumer may override. **Register/coil/identity values and recorded request/response bytes can never be overridden.**
4. Make **timestamps** first-class: enough for a consumer to replay at original speed, slowed or fast.
5. Provide at least one **maximally compact binary encoding** and **lossless text encodings**
   (JSON Lines, long CSV), plus a one-way text view (wide CSV of raw values).
6. Be usable as a library by tooling and GUIs: read, write, inspect, verify, convert, filter, merge, split, trim, repair.
7. Be crash-safe and streamable: append-only writer that never seeks; a file truncated by a crash
   is readable up to its last valid block.

## 3. Non-goals

- **Capturing from devices.** No recorder, scanner or poller, and no Modbus client. Recorders live in
  `modbusctl` and other tools; this library gives them a writer.
- **Replaying or hosting.** No replayer, replay clock, sample-and-hold lookup, answer presets or Modbus server.
  Replayers live in `modbusctl` and other tools; this library gives them a reader.
- **Any network or serial I/O.** Nothing in this repo opens a Modbus connection. No package imports `go-modbus`.
- Recording or storing **latency**, retries, timeouts, link events or connection state.
- **Writes** (FC05, 06, 15, 16, 22, 23). Captures hold reads only. The sample record reserves a `kind`
  field so writes can be added later without a format change.
- Reading MCAP v1.
- **Decoding values.** Interpreting registers (scaling, types, enums, SunSpec or vendor maps, device profiles) is
  deliberately out of scope. The library stores raw words, bits and PDUs. Consumers bring their own decoders.
  Consequently: no decoded export of any kind and no export to third-party viewers, no port of v1's `decode`, `strings` or
  `frequencies` commands.
- Interpolating or inventing values between samples.

## 4. Data model

One logical model; every encoding stores the same four things.

| Entity | What it is | Key fields |
|---|---|---|
| **Capture** | The file | format version, wall-clock start, tick size, capture end, producer, free tags |
| **Target** | One device: an endpoint plus a unit id | id, transport, host+port or serial settings, unit id, label, tags |
| **Stream** | One acquisition source for a target ("poll plan A", "scan", "identity probe") | id, target id, role, label |
| **Sample** | One answered request | stream id, time (ticks), request PDU, response PDU |

A target is the device a consumer would host; a stream is how its data was acquired. A device polled every
second plus every minute, plus a one-off FC43 read, is **one target, three streams**. A simple
file has one target and one stream, and the writer creates both by default.

PDU = function code byte plus data. Transport framing (MBAP header, RTU CRC, ASCII LRC) is **not**
stored; a replayer regenerates it.

## 5. Rules the format and library follow

1. **Data first.** The file holds what the device answered and when. Nothing else about the exchange.
2. **Sample and hold** is the meaning of the data: a value is valid from its sample until the next sample of the
   same address (or same request). Whatever happened in between is unknown. Consumers that replay MUST read it so;
   tools in this repo that derive views (wide CSV) apply it.
3. **Only what was recorded exists.** An address, request or function with no sample is absent.
   The library MUST NOT invent data.
4. **PDU-level storage.** Raw request and response PDUs. Register contents are raw, uninterpreted
   words and bits. The library never decodes them (see non-goals).
5. **Metadata is advisory, values are not.** Only target metadata may be overridden by a consumer.
   Sample times are not rewritten either; a replayer's clock controls speed.
6. **Append-only and self-describing.** Writers never seek. Targets and streams can be added mid-capture.
   Readers skip unknown block types.
7. **One model, several encodings.** Binary, JSON Lines and long CSV convert to each other **without loss**
   (byte-for-byte identical PDUs, identical tick times, identical metadata).
8. Tools that edit a capture (trim, filter, merge, split) write a **new file** and record their
   provenance in header tags. Changing values is an editing operation producing a new file, never an override.

## 6. Read function coverage

Every sample is stored as raw PDUs, so one record type covers all functions. The "keyed per" column says how a
consumer associates samples over time (sample and hold); it matters to this repo for the wide CSV view and for
`info` statistics, and to replayers (see the hand-off).

| FC | Function | Keyed per | Notes |
|---|---|---|---|
| 01 | Read Coils | address | one bit per address |
| 02 | Read Discrete Inputs | address | one bit per address |
| 03 | Read Holding Registers | address | one 16-bit word per address |
| 04 | Read Input Registers | address | one 16-bit word per address |
| 07 | Read Exception Status | request | one byte |
| 08 | Diagnostics (data-returning sub-functions) | request | echo, diagnostic register, counters 0x0B–0x12. State-changing sub-functions (0x01 restart, 0x04 listen-only, 0x0A clear counters) are not reads; a recorder MUST NOT send them unless explicitly allowed |
| 11 | Get Comm Event Counter | request | status + event count |
| 12 | Get Comm Event Log | request | status, counts, up to 64 event bytes |
| 17 | Report Server ID | request | device-defined bytes kept verbatim |
| 20 | Read File Record | request | key is the full sub-request list |
| 24 | Read FIFO Queue | request | each read is a snapshot of the queue |
| 43/14 | Read Device Identification | objects | each page stored as received; consumers merge pages into one object set |
| other | unknown / vendor function | request | stored as opaque PDUs; nothing is dropped |

FC23 (Read/Write Multiple Registers) is excluded because it changes the device.

"Keyed per address" means the request range and response data split into one value per address, so values
recorded by different requests combine. "Keyed per request" means the exact request PDU bytes are the key.

## 7. Timestamps

- One time per sample: the instant the **response was received**, in integer **ticks since capture start**.
  (The device read the value somewhere between request and answer; that gap is not modelled.)
- Writers take ticks from the **monotonic clock** of the recorder, so a wall-clock step during a long capture does
  not distort intervals. The writer API takes the sample time as an argument; it does not read a clock itself.
- The header holds the **tick size** (default 1 µs, selectable down to 1 ns) and the **wall-clock start**.
- The file records the **capture end** time so a consumer knows how long the last value holds
  (binary: in the index; JSONL and CSV: the `end` record). A crash-truncated file has no recorded end; the
  reader SHOULD use the last sample time and mark the end as unknown.
- Merging captures from several hosts rebases streams on their wall-clock starts; alignment is only as good as clock sync.
- Sampling rate sets fidelity. A change between two samples shows at the later sample's time.
  Nothing is interpolated.

## 8. Encodings

Detailed in [`docs/FORMAT.md`](docs/FORMAT.md). Summary:

- **MBCAP binary** (`.mbcap`): magic `89 'M' 'B' 'C' 'A' 'P' 0D 0A`, then CRC32C-protected blocks
  (Header, Target, Stream, Request template, Data, Index, Footer). LEB128 varints. Samples use
  request-template references, delta-coded times, and response coding RAW / SAME / DIFF (sparse
  16-bit word diff against the previous response to the same request in the same block). Each data
  block is independently decodable and optionally compressed (none, deflate; zstd optional).
- **JSON Lines** (`.mbcap.jsonl`): lossless canonical text; PDUs as lowercase hex; times as integer ticks.
  Concatenating files with `cat` must not produce silently wrong results (see FORMAT.md on multi-header files; `mbcapctl cat` is the supported way).
- **Long CSV** (`.mbcap.csv`): lossless, importable; one row per record (header, target, stream, sample, end) with a fixed
  column set; PDUs as lowercase hex. For spreadsheets and pandas, and for editing a capture as text.
- **Wide CSV** (one-way view): one row per sample time, one column per address, sample and hold applied, raw unsigned
  words / bits (never decoded). Not importable: it drops request PDUs and per-sample identity.

Capture modes (writer options): **full** (every sample stored; keeps poll cadence; default) and **change-only**
(skip a sample whose response is identical to the previous sample of the same request; loses only
the poll cadence; the capture end is still recorded).

## 9. Library layout

`go-modbus-capture` MUST be split so that a viewer can import the model without the encoders it does not need.
No package imports `go-modbus` or does network or serial I/O.

| Package | Responsibility |
|---|---|
| `capture` | Model types: Capture, Target, Stream, Sample, and model invariants (validation). No I/O |
| `mbcap` | Binary writer and reader, index, repair |
| `jsonl` | JSON Lines writer and reader |
| `mbcsv` | Long CSV writer and reader (lossless); wide CSV view writer (one-way) |
| `edit` | Operations producing new captures: filter (time window, targets, streams, function codes), split, merge/cat (rebase on wall-clock start, renumber ids), trim; provenance tags |
| `cmd/mbcapctl` | Command-line tool over capture files (section 11) |

Conventions: match the `otfabric` repositories (see `go-modbus`, `go-modbus-identity`, `modbusctl`):
same Go version as `go-modbus`'s `go.mod`, standard library only (zstd, if added, lives behind a build tag or
sub-module; `mbcapctl` may use the same CLI library as `modbusctl`), table-driven tests, lint config copied from sibling repos.

## 10. Writer requirements (for recorders built on the library)

- Targets and streams can be added at any point of a capture (a scan that discovers new units).
- Options: change-only mode, record-exceptions flag (decision 6), compression, block size.
- Blocks are written as they fill; an optional timed flush serves live capture; the file stays valid after every
  flushed block.
- On clean close the writer writes the Index and Footer.
- The writer never seeks, so it can write to a pipe or socket.

## 11. Command-line tool (`mbcapctl`)

`mbcapctl` works on capture files only: it reads, inspects, edits and converts them. It MUST NOT open a Modbus
connection. There is no `record`, `scan`, `read`, `serve` or `replay` command; those belong to `modbusctl`.

| Command | Does |
|---|---|
| `info` / `stat` | Targets, streams, spans, sample counts, function mix, exception counts, per-stream cadence |
| `verify` | CRC, structure and model invariants |
| `dump` | Human-readable listing of records |
| `convert` | Binary ⇄ JSON Lines ⇄ long CSV (lossless, any direction); wide CSV view (one-way) |
| `cat` / `merge` | Combine files; rebase on wall-clock start; renumber ids |
| `split` | One file per target, per stream or per function code |
| `filter` | Keep a time window, targets, streams and/or function codes |
| `trim` | Cut to a time window, rebasing ticks so the window starts at 0 (the wall-clock start moves with it) |
| `repair` | Rebuild the index and footer of a truncated file |

Edit commands (`cat`/`merge`, `split`, `filter`, `trim`) write new files with provenance tags; they never change
values, PDUs or the relative times of samples. All of them are thin wrappers over the `edit` package.

## 12. Quality requirements

- Round-trip tests binary ⇄ JSONL ⇄ long CSV ⇄ binary: model equality, byte-identical PDUs.
- **Fuzz tests** for every reader (no panics, no unbounded allocation on hostile input; cap lengths).
- Golden files in `testdata/` (small, hand-checkable), including: multi-target multi-stream, FC43 paged read,
  every FC in section 6, exception response, truncated file, CRC-corrupted block, unknown block type, change-only capture.
- Edit operation tests: filter/split/merge/trim preserve PDUs and relative times; provenance tags written; merge
  rebasing and id renumbering; split by function code keeps every sample exactly once.
- Wide CSV tests: sample and hold applied per address, never interpolated, absent addresses empty.
- Benchmarks: encode/decode MB/s for each encoding.
- A synthetic generator (like `docs/sizemodel/bench.py`, in Go under `internal/`) for tests and benchmarks.

## 13. Decisions taken (change only with reason)

| # | Question | Decision | Reason |
|---|---|---|---|
| 1 | Names | Format MBCAP, `.mbcap`, own magic, version starts at 1. Repo `go-modbus-capture`, CLI `mbcapctl`. MCAP v1 not supported | `.mcap` is already used by an unrelated, established format; two unrelated formats on one extension confuse file managers and tools |
| 2 | Container | Custom, purpose-built container | Needs per-sample request templates, delta-coded times and response diff coding; the sketch spends ~4–6 B per sample on framing+timing |
| 3 | Meaning of a sample's time | Response-received instant, monotonic ticks from capture start; wall-clock start in header | One value suffices for sample-and-hold |
| 4 | Default tick | 1 µs, selectable to 1 ns | ~2 bytes/sample cheaper than ns; far finer than sampling interval |
| 5 | Capture mode | Full default; change-only optional | Full keeps cadence; change-only is far smaller for static devices |
| 6 | Exception responses | Stored only if the writer is asked to (header flag); default off | Hints about refusals, not data |
| 9 | Scope of samples | Reads only | Capturing must not change the device |
| 10 | Compression | Per-block deflate default (off for live low-latency capture); zstd optional | Standard library only |
| 11 | `modbus-simulator` | Decide later; its JSON fixtures/JSONL traces overlap | A capture→fixture converter is the likely first bridge |
| 12 | Repository scope | File format, encodings and file tooling only. No capture, no replay, no Modbus I/O, no go-modbus dependency. Capture and replay land in `modbusctl` and a later tool | Owner's direction, 2026-10-14 |
| 13 | Text formats | JSON Lines (canonical) and long CSV are both lossless and importable; wide CSV is a one-way view | Owner's direction, 2026-10-14: export/import to text-based formats |

Decisions 7 and 8 (unrecorded requests, before first sample) were replay decisions; they moved to the hand-off
as R2 and R3. Numbers are kept so references stay valid.

Decisions 3–6 and 9–10 are the author's recommendations and are not yet confirmed line by line by the owner;
the owner's explicit requirements are in sections 2, 3 and 5 and decisions 12–13. Raise a question rather than
silently deviate.

## 14. Phases

1. **go-modbus: client PDU tap** — done as [otfabric/go-modbus#1](https://github.com/otfabric/go-modbus/pull/1)
   (released in [v1.6.0](https://github.com/otfabric/go-modbus/releases/tag/v1.6.0)). It serves the recorder in `modbusctl`; this repo does not depend on it.
2. **Core:** `capture`, `mbcap`, `jsonl`, `mbcapctl verify|convert|info|dump`. Round-trip and fuzz tests.
   **Calibrate the binary coding on real captures before freezing it** (see section 15).
3. **File tooling:** `mbcsv` (long CSV both ways, wide CSV view), `edit`, `mbcapctl cat|merge|split|filter|trim|repair`.
4. **Consumers (outside this repo):** `modbusctl` moves `record`, `scan`, `read`, `server static` and `server replay`
   onto the library (see the hand-off); viewers/GUIs.

## 15. Open items

- **Real captures for calibration.** The size numbers in `docs/SIZE-MODEL.md` are from synthetic data.
  The owner has real v1 captures (e.g. the 192.168.2.201 sample in the `modbusctl` README). Convert a few
  with a throwaway script, measure, then freeze the DIFF/block-size parameters before declaring the format stable.
- Confirm decisions 3–6 and 9–10 (section 13) with the owner.
- Exact varint/field layouts in `docs/FORMAT.md`, and the long CSV columns, are a **draft**: finalize during phases 2–3,
  write them back to the spec, and add golden files. Bump the draft marker once frozen.
- Relationship to `modbus-simulator` (decision 11).

## 16. Where the old code lives (reference only)

`otfabric/modbusctl`: `internal/mcap/` (codec, load, exports), `internal/types/capture.go`,
`internal/modbus/client.go` (`RecordAndWriteMCAP`, `ScanAndWriteMCAP`), `internal/modbus/server.go`
(`LoadMCAPAndServeStatic/Replay`), `cmd/client/record.go`, `cmd/server/replay.go`. The record and replay parts are
for the `modbusctl` migration (hand-off), not for this repo.
`otfabric/modbus-simulator`: `REQUIREMENTS.md`, `internal/simulator/rawpdu.go`, `trace.go` (raw PDU handling and JSONL traces worth reusing as ideas).
