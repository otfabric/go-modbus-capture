# go-modbus-capture

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-modbus-capture.svg)](https://pkg.go.dev/github.com/otfabric/go-modbus-capture)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-modbus-capture/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-modbus-capture/actions/workflows/ci.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-modbus-capture/graph/badge.svg)](https://codecov.io/gh/otfabric/go-modbus-capture)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/go-modbus-capture?label=release)](https://github.com/otfabric/go-modbus-capture/releases)

**MBCAP is a file format for Modbus captures**: what a device answered, and when, stored so that a
virtual device can later serve exactly that data to a Modbus client, at the original speed, slowed down
or sped up. `go-modbus-capture` is its Go library and the `mbcapctl` command-line tool.

Record a site once, then test SCADA screens, gateways, historians or analytics against it as often as
you like, without touching the real devices.

- **Every read function**: coils, registers, diagnostics, event logs, file records, FIFO queues, device
  identification (FC43) and vendor functions, all stored as raw request and response PDUs.
- **Whole sites in one file**: several devices, each with several acquisition streams, that a consumer can
  untangle or split.
- **Compact and safe**: about 28 bytes per sample for a typical meter, append-only, checksummed, readable
  after a crash.
- **Lossless text twins**: JSON Lines and CSV versions that convert back to the identical file.
- **No decoding, no surprises**: values stay raw; nothing is interpolated or invented.

This repository owns the format and the tooling for capture files. Recording from devices and replaying
captures are done by [`modbusctl`](https://github.com/otfabric/modbusctl), built on this library.

## How a capture works

A capture holds four kinds of records:

| Record | Is | Example |
|---|---|---|
| **Header** | the capture: tick size, wall-clock start, producer, flags, tags, end | recorded on 2026-10-14 at 13:14:24 UTC, 1 µs ticks |
| **Target** | a device: an endpoint plus a unit id | Inverter A at 192.168.2.201:502, unit 21 |
| **Stream** | one way a target's data was acquired | "poll every second", "identity probe" |
| **Sample** | one answered request: stream, time, request PDU, response PDU | at 1.00045 s, `03 1000 0002` → `03 04 0001 01a7` |

Here is a complete capture, in its JSON Lines encoding:

```jsonl
{"t":"header","version":"0.1","tick_ns":1000,"wall_start":"2026-10-14T13:14:24.701149Z","producer":"modbusctl 3.0.0"}
{"t":"target","id":1,"label":"Inverter A","transport":"tcp","host":"192.168.2.201","port":502,"unit":21}
{"t":"stream","id":1,"target":1,"role":"poll","label":"holding 4096 x2"}
{"t":"stream","id":2,"target":1,"role":"identity"}
{"t":"s","s":1,"at":0,"req":"0310000002","resp":"0304000101a7"}
{"t":"s","s":1,"at":1000450,"req":"0310000002","resp":"0304000101a7"}
{"t":"s","s":1,"at":2001100,"req":"0310000002","resp":"0304000101a8"}
{"t":"s","s":1,"at":3000900,"req":"0310000002","resp":"0304000101a8"}
{"t":"s","s":2,"at":3500000,"req":"2b0e0100","resp":"2b0e0101000003000441434d45010258310203312e30"}
{"t":"end","at":4000800}
```

The poll stream reads two holding registers at address 4096 (`03 1000 0002`) once a second. The device
answers `0x0001, 0x01a7`, and from the third poll `0x0001, 0x01a8`. The identity stream holds one
Read Device Identification answer: vendor `ACME`, product `X1`, revision `1.0`.

Three rules give the data its meaning:

1. **Sample and hold.** A value holds from its sample until the next sample of the same address or
   request. Register 4097 is `0x01a7` from 0 s and `0x01a8` from 2.0011 s until the end at 4.0008 s.
   Nothing is interpolated.
2. **Only what was recorded exists.** An address or function without a sample is absent; a replayer
   decides how to answer it.
3. **Metadata is advisory, values are not.** A replayer may serve the inverter on another address or
   unit id. It may never change a recorded value or time; edits produce a new file.

Times are integer **ticks** since the start (here 1 µs), taken from a monotonic clock, so a capture
replays at exactly its original pace, or at any multiple of it. More in [Concepts](docs/concepts.md).

## Encodings

| Encoding | Extension | Lossless | For |
|---|---|---|---|
| **MBCAP binary** | `.mbcap` | yes | recording and archiving: compact, append-only, crash-safe, checksummed |
| **JSON Lines** | `.mbcap.jsonl` | yes | reading, diffing, scripting, test fixtures |
| **Long CSV** | `.mbcap.csv` | yes | spreadsheets and dataframes |
| **Wide CSV** | `.wide.csv` | view only | charting raw values per address over time |

The binary encoding stores each distinct request once and lets samples refer to it, codes times as
small deltas, and stores each response raw, as "same as before", or as only the 16-bit words that
changed. Samples are grouped in independently decodable, checksummed, deflate-compressed blocks, with an
index at the end:

```
magic │ header │ targets │ streams │ request templates │ data block │ data block │ … │ index │ footer
```

On a synthetic typical meter that is about 28 bytes per sample, against 192 for the format it replaces.
A file cut short by a crash reads up to its last complete block. More in
[Encodings](docs/encodings.md) and the [format specification](docs/FORMAT.md).

## Quick start

Install the CLI:

```bash
go install github.com/otfabric/go-modbus-capture/cmd/mbcapctl@latest
```

Look inside a capture:

```bash
mbcapctl info capture.mbcap
```

Convert it, losslessly:

```bash
mbcapctl convert capture.mbcap capture.mbcap.jsonl
```

Keep only the FC03 samples of the first ten minutes:

```bash
mbcapctl filter capture.mbcap first-ten.mbcap --fc 3 --to 10m
```

`mbcapctl` also verifies, dumps, merges, splits, trims and repairs captures; see the
[CLI reference](docs/cli.md). It never opens a Modbus connection.

Read a capture in Go:

```go
r, err := mbcap.NewReader(f) // or jsonl.NewReader, mbcsv.NewReader
if err != nil {
	return err
}
for {
	rec, err := r.Next()
	if err == io.EOF {
		break
	}
	if err != nil {
		return err
	}
	if s, ok := rec.(capture.Sample); ok {
		fmt.Printf("%v: % x -> % x\n", r.Header().Duration(s.At), s.Request, s.Response)
	}
}
```

Write one with `mbcap.NewWriter`, convert with `capture.Copy`, edit with the `edit` package: see the
[library guide](docs/library.md).

## Packages

| Package | Responsibility |
|---|---|
| `capture` | the model, the `Reader` and `Writer` interfaces, and the rules every file follows |
| `mbcap` | binary reader and writer, index access, repair |
| `jsonl` | JSON Lines reader and writer |
| `mbcsv` | long CSV reader and writer, wide CSV view |
| `edit` | filter, trim, split and merge, each writing a new capture with provenance |
| `cmd/mbcapctl` | the CLI |

Standard library only, no dependencies; Go 1.23 or later. Every reader checks every rule and is fuzzed
against hostile input.

## Status

The format is **draft 0.1**: implemented, tested and pinned by golden files, but not frozen. It becomes
**1.0** once its coding is calibrated on real captures; files written now will not be readable by 1.0.
See the [release notes](RELEASE.md).

## Documentation

| Page | |
|---|---|
| [Getting started](docs/getting-started.md) | install, inspect, convert, read from Go |
| [Concepts](docs/concepts.md) | targets, streams, samples, time, sample and hold |
| [Encodings](docs/encodings.md) | binary, JSON Lines, CSV, with examples |
| [mbcapctl](docs/cli.md) | every command and flag |
| [Go library](docs/library.md) | reading, writing, converting, editing |
| [Format specification](docs/FORMAT.md) | the normative specification |
| [All documentation](docs/README.md) | including project internals |

## Development

```bash
make check
```

`make test` runs the tests with the race detector, `make fuzz` every fuzz target, `make bench` the
benchmarks, and `make golden` regenerates the derived golden files in `testdata/` from their hand-written
JSON Lines sources.

## License

MIT, see [LICENSE](LICENSE).
