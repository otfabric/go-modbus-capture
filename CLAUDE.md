# CLAUDE.md — go-modbus-capture

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
- MBCAP is the only capture format; do not add readers or writers for other or legacy formats.
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
