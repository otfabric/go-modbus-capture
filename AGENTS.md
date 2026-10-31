# AGENTS.md — go-modbus-capture

## What this is

MBCAP, a file format for Modbus captures, with its Go library and the `mbcapctl` tool: three lossless encodings (binary, JSON Lines, long CSV) of one model, plus editing and repair. Format version 0.1, a draft that is implemented but not frozen ([docs/FORMAT.md](docs/FORMAT.md)). Profile: **protocol-library**. What it does not do: [LIMITATIONS.md](LIMITATIONS.md).

## Organisation standards

This repository follows **otfabric/engineering policy 1.2**, profile **protocol-library** (`.otfabric/policy.yaml`, mode `enforce`). Where this file and the organisation standards disagree, this file wins here — say so in your report.

## Hard rules

1. Never lower the coverage minimum in `.github/workflows/ci.yml`, add lint suppressions, or skip or delete tests to make CI pass.
2. A layout change updates docs/FORMAT.md and the golden files (`make golden`) in the same commit.
3. This library stays on v0 or v1.

## Commands

| When you change… | Run |
|---|---|
| anything | `make check` |
| a reader or a decoder | `make fuzz` |
| a layout or the hand-written JSON Lines sources | `make golden` |

## Definition of Done

`make check` green; tests for new behaviour incl. error paths; fuzz target for new readers; docs/ and ERRORS.md updated in the same change; RELEASE.md section for user-visible changes.

## Git and releases

Branches and PRs. Do not push to `main`, tag or run the Release workflow unless the maintainer asks in the session.

Read `REQUIREMENTS.md` first, then `docs/FORMAT.md`. They are the source of truth; if you must deviate, change the
document in the same commit and say why.

## Ground rules

- Scope is the **file format, its encodings and file tooling**. No capturing from devices, no replay or hosting, no
  Modbus client or server, no network or serial I/O. Capture and replay belong to `modbusctl` (design kept in
  `docs/HANDOFF-MODBUSCTL.md`); do not add them here, not even as adapters.
- Packages (`capture`, `mbcap`, `jsonl`, `mbcsv`, `edit`, `cmd/mbcapctl`) use the **standard library only**
  (`mbcapctl` may share `modbusctl`'s CLI library). Nothing imports `go-modbus`.
- `mbcapctl` works on capture files only: inspect, verify, convert, cat/merge, split, filter, trim, repair.
  Never add `record`, `scan`, `read`, `serve` or `replay`.
- Never decode register contents (scaling, types, device profiles). Values stay raw words, bits and PDUs; decoders are out of scope on purpose. No decoded export and no export to third-party viewers.
- No MCAP v1 support. Do not port `modbusctl/internal/mcap`.
- Captures hold reads only. Never add write capture without an explicit requirement change.
- Edit operations write new files; they never rewrite recorded PDUs or relative sample times.
- Writers take sample times as arguments; nothing in the library reads the wall clock for sample times.
- Readers face hostile input: cap lengths/counts, fuzz them, never panic.
- Binary ⇄ JSON Lines ⇄ long CSV must round-trip losslessly; test it. Wide CSV is a one-way view.
- Match sibling repos (`otfabric/go-modbus`, `go-modbus-identity`, `modbusctl`) for Go version, lint config, test style and README tone.

## Order of work

Follow section 14 of `REQUIREMENTS.md`. Phase 1 (go-modbus PDU tap, otfabric/go-modbus#1) is done and serves
`modbusctl`. Start with phase 2 (core) using hand-written golden files in `testdata/`.

## Open questions to raise instead of guessing

See section 15 of `REQUIREMENTS.md` (real-capture calibration, confirmation of decisions 3–6 and 9–10, simulator overlap,
final binary and CSV layouts).
