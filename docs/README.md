# Documentation

`go-modbus-capture` defines **MBCAP**, a file format for Modbus captures, and provides a Go library and
the `mbcapctl` CLI to work with it.

## Guides

| Page | For |
|---|---|
| [Getting started](getting-started.md) | install, look inside a capture, convert it, read it from Go |
| [Concepts](concepts.md) | the model: targets, streams, samples, time, sample and hold, what may change |
| [Encodings](encodings.md) | binary, JSON Lines, long CSV and the wide CSV view, with examples; when to use which |
| [mbcapctl](cli.md) | every command and flag |
| [Go library](library.md) | reading, writing, converting, editing; errors; binary file extras |

## Reference

| Page | For |
|---|---|
| [Format specification](FORMAT.md) | the normative byte- and field-level specification (draft 0.1) |
| [Release notes](../RELEASE.md) | what changed in each release |
| [API reference](https://pkg.go.dev/github.com/otfabric/go-modbus-capture) | Go package documentation on pkg.go.dev |

## Project internals

Background for contributors; not needed to use the library.

| Page | For |
|---|---|
| [Requirements](../REQUIREMENTS.md) | goals, non-goals, decisions and phases of the project |
| [Size model](SIZE-MODEL.md) | how small the binary encoding is, on synthetic data |
| [MCAP v1 analysis](MCAP-V1-ANALYSIS.md) | the old `modbusctl` format and why MBCAP replaces it |
| [Capture and replay hand-off](HANDOFF-MODBUSCTL.md) | the recorder and replayer design that lives in `modbusctl` |
