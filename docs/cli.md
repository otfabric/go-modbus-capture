# mbcapctl

`mbcapctl` works with capture files: it inspects, verifies, converts, combines, splits, filters, trims
and repairs them. It **never opens a Modbus connection**. Recording from devices and replaying captures
are done by other tools, such as [`modbusctl`](https://github.com/otfabric/modbusctl).

## Install

```bash
go install github.com/otfabric/go-modbus-capture/cmd/mbcapctl@latest
```

Prebuilt binaries for Linux, macOS and Windows are attached to each
[release](https://github.com/otfabric/go-modbus-capture/releases).

## Common behaviour

- **Any encoding in.** Inputs are detected from their content: `.mbcap`, `.mbcap.jsonl` and `.mbcap.csv`
  all work, whatever their name. `-` reads standard input.
- **Encoding out by name.** The output encoding follows the file extension (`.mbcap`, `.jsonl`, `.csv`,
  `.wide.csv`), or `--format mbcap|jsonl|csv|wide-csv`. `-` writes standard output (JSON Lines unless
  `--format` says otherwise).
- **Safe outputs.** Outputs are written to a temporary file next to the destination and renamed into place
  only when the command succeeds. An output that is also an input is refused.
- **Provenance.** `cat`, `merge`, `split`, `filter` and `trim` add their command and flags (not file or
  directory names) to the `provenance` header tag, for example `mbcapctl 0.0.1 filter --fc=3 --to=2s`.
  `convert` and `repair` copy the capture as it is.
- **Flags anywhere.** Flags may come before or after the file arguments.
- **Times** are offsets from the capture start: a Go duration (`90s`, `1m30s`, `250ms`) or an exact number
  of ticks with a `t` suffix (`1234t`).
- **Function codes** are decimal, as in "FC43" (`--fc 3,4,43`), or hex with `0x` (`--fc 0x2b`).
- **Exit codes**: 0 success, 1 failure (for example an invalid file), 2 usage error.

Options for binary output, on every command that writes files:

| Flag | Default | Meaning |
|---|---|---|
| `--format` | from the extension | `mbcap`, `jsonl`, `csv` or `wide-csv` |
| `--compression` | `deflate` | data block compression: `deflate` or `none` |
| `--block-samples` | `256` | samples per data block |

## info

```
mbcapctl info [--json] FILE
```

Summarises a capture: header, mode, end, sample counts, function mix, targets, and per stream the sample
count, distinct requests, time span, median interval between two samples of the same request, and
functions. `stat` is the same command.

```
$ mbcapctl info example.mbcap
File:         example.mbcap (mbcap)
Producer:     modbusctl 3.0.0
Start:        2026-10-14T13:14:24.701149Z
Tick:         1µs
Mode:         full capture
End:          4.0008s
Samples:      5 (0 exception responses), first at 0s, last at 3.5s
Data blocks:  1

Functions:
  FC03 (0x03)  Read Holding Registers                               4
  FC43 (0x2b)  Encapsulated Interface (Read Device Identification)  1

Targets:
  1  "Inverter A"  tcp 192.168.2.201:502  unit 21

Streams:
  id  target  role      label              samples  requests  first  last     interval  functions
  1   1       poll      "holding 4096 x2"  4        1         0s     3.0009s  1.00045s  03
  2   1       identity  -                  1        1         3.5s   3.5s     -         2b
```

`--json` prints the same summary as JSON, for scripts and GUIs (times in ticks). A truncated file is
summarised up to where it ends, and `info` says so.

## verify

```
mbcapctl verify FILE...
```

Reads every file completely and checks block checksums, structure, the index, and every rule of the model.
Prints one line per file and fails if any file fails.

```
$ mbcapctl verify example.mbcap truncated.mbcap
example.mbcap: ok (mbcap, 5 samples, end at 4000800 ticks)
truncated.mbcap: FAIL: mbcap: offset 220: capture: truncated: input ends inside a block (run mbcapctl repair)
mbcapctl verify: 1 of 2 files failed
```

## dump

```
mbcapctl dump [--wall] FILE
```

Lists every record. Sample times are shown as ticks and as an offset from the start, or with `--wall` as
wall-clock times.

```
$ mbcapctl dump example.mbcap
header  format=mbcap tick=1µs start=2026-10-14T13:14:24.701149Z producer="modbusctl 3.0.0"
target  1 "Inverter A" tcp 192.168.2.201:502 unit=21
stream  1 target=1 role=poll label="holding 4096 x2"
stream  2 target=1 role=identity label=-
sample             0             +0s  s1   0310000002               -> 0304000101a7
sample       1000450       +1.00045s  s1   0310000002               -> 0304000101a7
sample       2001100        +2.0011s  s1   0310000002               -> 0304000101a8
sample       3000900        +3.0009s  s1   0310000002               -> 0304000101a8
sample       3500000           +3.5s  s2   2b0e0100                 -> 2b0e0101000003000441434d45010258310203312e30
end          4000800        +4.0008s
```

## convert

```
mbcapctl convert [--format F] [--compression C] [--block-samples N] IN OUT
```

Converts between the lossless encodings, in any direction, or writes the wide CSV view.

```bash
mbcapctl convert capture.mbcap capture.mbcap.jsonl
```

```bash
mbcapctl convert capture.mbcap.csv capture.mbcap
```

```bash
mbcapctl convert capture.mbcap registers.wide.csv
```

The wide view covers FC01–04; `convert` reports how many exception or malformed samples it left out.

## cat and merge

```
mbcapctl cat   -o OUT IN...
mbcapctl merge -o OUT IN...
```

Combine captures into one. Both rebase every input on the earliest wall-clock start (the output's tick
is chosen so that every time stays exact) and renumber targets and streams from 1 in input order.
`cat` keeps each input's samples together, in input order; `merge` interleaves all samples by time.

The output keeps the producer and header tags that all inputs share, lists the input file names in the
`source` tag, is change-only only if every input is, and records exceptions if any input does. Its end is
the latest input end, or unknown if any input has none.

```bash
mbcapctl merge -o site.mbcap inverter.mbcap meter.mbcap gateway.mbcap.jsonl
```

## split

```
mbcapctl split --by target|stream|fc [--dir DIR] [--format F] IN
```

Writes one capture per target, per stream, or per function code, named after the input:
`capture.target-1.mbcap`, `capture.stream-3.mbcap`, `capture.fc-03.mbcap`. Parts keep the input's ids,
header and end; each holds only the definitions its samples need. Prints the files it wrote.

```bash
mbcapctl split --by stream --dir parts capture.mbcap
```

## filter

```
mbcapctl filter [--from T] [--to T] [--target IDS] [--stream IDS] [--fc FCS] IN OUT
```

Keeps part of a capture without changing anything that remains: same ids, same times, same wall-clock
start. All conditions must hold.

| Flag | Keeps |
|---|---|
| `--from T`, `--to T` | samples with `from ≤ at < to`; with `--to`, a later recorded end moves to `to` |
| `--target IDS` | these targets and their streams |
| `--stream IDS` | these streams, and only the targets they belong to |
| `--fc FCS` | samples of these function codes |

Lists are comma-separated and the flags may be repeated.

```bash
mbcapctl filter capture.mbcap identity.mbcap --fc 43
```

```bash
mbcapctl filter capture.mbcap morning.mbcap --target 2 --from 1h --to 3h
```

## trim

```
mbcapctl trim [--from T] [--to T] IN OUT
```

Cuts a capture to a time window and rebases it so that `--from` becomes time 0; the wall-clock start moves
with it, so every sample keeps its wall-clock time. Values sampled before the window are not carried into
it.

```bash
mbcapctl trim --from 10m --to 20m capture.mbcap ten-minutes.mbcap
```

## repair

```
mbcapctl repair IN OUT
```

Rewrites a truncated or damaged binary file as a complete one, with a new index and footer. It keeps every
complete block, skips blocks whose checksum fails, and stops at the point where the file is cut. In a
change-only capture it also drops a sample that, with a skipped block lost, repeats the response before
it. The end is kept if the old index survived, and unknown otherwise. The output is always `.mbcap`;
convert it afterwards if needed.

```
$ mbcapctl repair crashed.mbcap repaired.mbcap
repaired.mbcap: kept 4 samples, input was truncated, capture end unknown
```

## version

```
mbcapctl version
```

Prints the version, tag, commit and build date.
