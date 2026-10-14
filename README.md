# go-modbus-capture

The file format for Modbus captures: what a device answered, and when, so that a virtual device can
later host exactly that data at original, slowed or fast speed.

This repository is the format, its encodings and the tooling for capture files. Recording from devices
and replaying captures live in [`modbusctl`](https://github.com/otfabric/modbusctl), built on this library.

**Status: design phase.** No code yet. Start with [`REQUIREMENTS.md`](REQUIREMENTS.md).

- Format: **MBCAP** (`.mbcap` compact binary; `.mbcap.jsonl` and `.mbcap.csv` lossless text)
- Command-line tool: `mbcapctl`, for capture files: inspect, verify, convert, merge, split, filter, trim, repair
- Module: `github.com/otfabric/go-modbus-capture`

## Idea in four lines

1. A **sample** is one answered request: stream, time, raw request PDU, raw response PDU.
2. A file holds several **targets** (device = endpoint + unit id) and **streams** (how a target's data was acquired), so a consumer can use just one.
3. The data means **sample and hold**: a value holds from its sample until the next one; nothing is interpolated and nothing unrecorded exists.
4. Target metadata (IP, port, unit id, serial settings) is advisory and overridable by consumers; recorded values and times never are.

## Documents

| File | Content |
|---|---|
| [`REQUIREMENTS.md`](REQUIREMENTS.md) | Goals, non-goals, data model, encodings, library layout, `mbcapctl`, quality bar, decisions, phases |
| [`docs/FORMAT.md`](docs/FORMAT.md) | MBCAP binary, JSON Lines and CSV specification (draft) |
| [`docs/HANDOFF-MODBUSCTL.md`](docs/HANDOFF-MODBUSCTL.md) | Recorder and replay design, moved out of this repo for `modbusctl` |
| [`docs/MCAP-V1-ANALYSIS.md`](docs/MCAP-V1-ANALYSIS.md) | The old `modbusctl` MCAP v1 format and why it is replaced (not supported here) |
| [`docs/SIZE-MODEL.md`](docs/SIZE-MODEL.md) | Synthetic size estimates and the script behind them |
