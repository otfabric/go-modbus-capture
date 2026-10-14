# Encodings

The same [capture model](concepts.md) can be stored in three lossless encodings and viewed in a
fourth, one-way form. Any lossless encoding converts to any other without changing a byte of a PDU, a
tick of a time or a field of metadata.

| Encoding | Extension | Lossless | Best for |
|---|---|---|---|
| [MBCAP binary](#mbcap-binary) | `.mbcap` | yes | recording, archiving, shipping: small, streamable, crash-safe, checksummed |
| [JSON Lines](#json-lines) | `.mbcap.jsonl` | yes | reading and diffing by eye, scripting, hand-written test fixtures |
| [Long CSV](#long-csv) | `.mbcap.csv` | yes | spreadsheets, pandas and other dataframe tools; editing as a table |
| [Wide CSV](#wide-csv-view) | `.wide.csv` | **no** (view) | charting raw values per address over time |

Readers detect the encoding from the content, not the file name. Convert with
`mbcapctl convert in out` ([CLI](cli.md#convert)) or `capture.Copy` ([library](library.md#converting)).

All examples on this page are the same small capture, `testdata/example.mbcap.jsonl`: one inverter,
polled four times for two holding registers, plus one identity read.

## JSON Lines

One JSON object per line. The `t` field says what the line is; PDUs are lowercase hex; times are
integer ticks since the start.

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

Reading the samples: `03 1000 0002` is FC03 (Read Holding Registers), address 0x1000 (4096), quantity
2. The response `03 04 0001 01a7` is FC03, 4 bytes, the words 0x0001 and 0x01a7. At the third sample the
second word becomes 0x01a8; under sample and hold, that is the moment a replayer starts serving the new
value. The identity sample is an FC43/14 read whose response holds the conformity level, paging fields
and three objects: `ACME`, `X1` and `1.0`.

| `t` | Line | Fields |
|---|---|---|
| `header` | first line, exactly once | `version`, `tick_ns`, `wall_start` (RFC 3339, up to ns), `producer`, `flags` (`change_only`, `exceptions`), `tags` |
| `target` | a device | `id`, `label`, `transport`, `host`, `port`, `serial` (`device`, `baud`, `parity`, `data_bits`, `stop_bits`), `unit`, `tags` |
| `stream` | an acquisition source | `id`, `target`, `role`, `label` |
| `s` | a sample | `s` (stream id), `at` (ticks), `req`, `resp` (hex PDUs) |
| `end` | last line, when the end is known | `at` |

Rules that make it safe to edit and extend:

- Definitions come before the samples that use them; `end`, if present, comes last.
- Unknown line types and unknown fields are ignored, so tools can add their own annotations.
- A second `header` line is an error. `cat file1 file2 > both.jsonl` cannot silently mix two captures;
  use `mbcapctl cat` or `merge`, which rebase the times and renumber the ids.
- The writer's output is canonical: reading a file and writing it again gives the same bytes.

## Long CSV

One row per record, the same records as JSON Lines, with a fixed set of columns. Cells that do not apply
to a record are empty.

```csv
t,at,s,fc,req,resp,id,target,role,label,transport,host,port,unit,serial_device,baud,parity,data_bits,stop_bits,version,tick_ns,wall_start,producer,flags,tags
header,,,,,,,,,,,,,,,,,,,0.1,1000,2026-10-14T13:14:24.701149Z,modbusctl 3.0.0,,
target,,,,,,1,,,Inverter A,tcp,192.168.2.201,502,21,,,,,,,,,,,
stream,,,,,,1,1,poll,holding 4096 x2,,,,,,,,,,,,,,,
stream,,,,,,2,1,identity,,,,,,,,,,,,,,,,
s,0,1,03,0310000002,0304000101a7,,,,,,,,,,,,,,,,,,,
s,1000450,1,03,0310000002,0304000101a7,,,,,,,,,,,,,,,,,,,
s,2001100,1,03,0310000002,0304000101a8,,,,,,,,,,,,,,,,,,,
s,3000900,1,03,0310000002,0304000101a8,,,,,,,,,,,,,,,,,,,
s,3500000,2,2b,2b0e0100,2b0e0101000003000441434d45010258310203312e30,,,,,,,,,,,,,,,,,,,
end,4000800,,,,,,,,,,,,,,,,,,,,,,,
```

- The `fc` column repeats the request's function code for filtering in a spreadsheet; it is ignored on
  import (and must match `req` if present).
- Readers find columns by name, accept them in any order, and ignore extra ones. Add a `note` column in a
  spreadsheet and the file still imports; the column is dropped when converting to another encoding.
- Load it in pandas with `pd.read_csv("capture.mbcap.csv", dtype=str)` and filter on `t == "s"` for the
  samples. Keep the hex columns as strings.

## Wide CSV view

A one-way view for charting: one row per sample time, one column per address, with sample and hold
applied. Columns are named `<target>/<fc>/<address>`.

```csv
at,1/03/4096,1/03/4097
0,1,423
1000450,1,423
2001100,1,424
3000900,1,424
```

It covers FC01–04 only. Values are raw: 0/1 for coils and discrete inputs, unsigned 16-bit words for
registers. A cell is empty until its address is first sampled. The view drops the request PDUs, the
other functions and the stream of each sample, so it **cannot be imported**: keep the capture.

## MBCAP binary

The binary encoding is built for recording: small, written append-only, readable after a crash, and
checksummed.

```
┌──────────────────────┐
│ magic  89 MBCAP 0D 0A │  8 bytes; catches 7-bit and text-mode corruption, like PNG
├──────────────────────┤
│ Header block          │  version, tick size, wall-clock start, flags, producer, tags
│ Target block(s)       │  one per device, written when the device is added
│ Stream block(s)       │  one per acquisition source
│ Template block(s)     │  each distinct request PDU, once
│ Data block            │  ┐
│ Data block            │  │ batches of ~256 samples, each independently decodable
│ …                     │  ┘ and optionally deflate-compressed
├──────────────────────┤
│ Index block           │  per data block: offset, time range, streams; the capture end
│ Footer                │  fixed 18 bytes: where the index is
└──────────────────────┘
every block = type (1 byte) | length (varint) | payload | CRC32C (4 bytes)
```

What makes it small:

- **Request templates.** A poller sends the same few requests thousands of times. Each request PDU is
  stored once; samples refer to it by a small number.
- **Delta-coded times.** Each sample stores its time as the difference to the previous one, a varint of
  one to three bytes at typical poll rates.
- **Response coding.** Each response is stored as **RAW** (the bytes), **SAME** (nothing: equal to the
  previous response to the same request) or **DIFF** (only the 16-bit words that changed).
- **Per-block deflate** squeezes out the rest.

On a synthetic "typical meter" (registers 60% static, 25% slow counters, 13% walking, 2% noisy), a sample
of 62 to 120 registers takes about **28 bytes**, against 192 in the old MCAP v1 format
([size model](SIZE-MODEL.md)).

What makes it safe:

- **Append-only.** The writer never seeks, so it can write to a pipe or socket, and a crash never leaves a
  half-rewritten header.
- **Independent blocks.** The "previous response" state starts empty in every data block, so any block
  decodes on its own. A crash loses at most the open block.
- **Checksums.** Every block has a CRC32C. Damage is detected; `repair` can skip a damaged block and keep
  the rest.
- **Index at the end.** On a clean close the writer appends an index and a fixed-size footer, so a tool can
  find the time range of every block without reading the data. Without it, the file still reads, and
  `repair` rebuilds it.

The byte-level layout is in the [format specification](FORMAT.md#2-binary-encoding).

## Format version

Files carry format version **0.1**: the format is implemented but still a draft. It will be frozen as
**1.0** after its coding is calibrated on real captures; 1.0 readers will not read 0.x files. Within a
major version, readers accept newer minor versions, which may only add block types and fields.

## Limits

Readers treat every file as potentially hostile: lengths and counts are capped, and readers are fuzzed.
The limits (string length, tags, targets, streams, block sizes, line length) are listed in the
[specification](FORMAT.md#1-logical-model). Ordinary captures never come near them.
