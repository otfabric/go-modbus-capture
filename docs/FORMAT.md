# MBCAP format specification — DRAFT 0

Status: **draft for implementation in phase 2.** The structure, block types and coding ideas are
decided (see `REQUIREMENTS.md`); the exact field layouts below are a proposal that the implementer
should finalize, then freeze after calibrating on real captures. When a layout changes, change this
file in the same commit and update golden files.

Three lossless encodings of one model (`capture.Capture`): the compact **binary** `.mbcap`, the
canonical text **JSON Lines** `.mbcap.jsonl`, and **long CSV** `.mbcap.csv`. Conversion in any direction MUST be
lossless. A fourth, one-way **wide CSV** view is not an encoding (section 5).

Conventions: fixed-width integers are big-endian (network order, as in Modbus). "varint" is an
unsigned LEB128; "zigzag" is a signed value mapped by `(n << 1) ^ (n >> 63)` then varint-encoded.
"string" is `varint length | UTF-8 bytes`. Readers MUST cap lengths and counts to defend against hostile input.

## 1. Logical model

```
Capture: version, tick_ns, wall_start (UTC, ns resolution), end (ticks, optional), producer, tags
Target:  id, label, transport, host, port, serial{device, baud, parity, data_bits, stop_bits}, unit, tags
Stream:  id, target id, role, label
Sample:  stream id, at (ticks since capture start), request PDU, response PDU
```

- Ids are positive integers unique within the file. Targets and streams may be defined at any point
  before first use.
- `role` is a free label with conventional values: `poll`, `scan`, `identity`, `snapshot`, `other`.
- `transport` values: `tcp`, `udp`, `tls`, `rtu-over-tcp`, `rtu`, `ascii`, `unknown`. Serial settings
  apply to `rtu` and `ascii`; host/port to the others. All endpoint data is advisory.
- A Sample's request and response are PDUs (function code + data). An exception response has the
  function code with bit 0x80 set.
- `tags` are string key/value pairs. Reserved keys: `provenance` (tool and operation that produced
  the file), `source` (original file names when merged).

## 2. Binary encoding

### 2.1 File structure

```
magic   89 4D 42 43 41 50 0D 0A      ("\x89MBCAP\r\n", 8 bytes)
block*  type u8 | length varint | payload | crc32c u32
```

- The 0x89 high bit and the `0D 0A` catch 7-bit and text-mode corruption (as PNG does).
- `crc32c` (Castagnoli) is computed over `type | length | payload` bytes and stored big-endian.
- Unknown block types are skipped by length (the CRC is still checked). Minor versions only add blocks
  or append optional fields to the end of known blocks; readers MUST ignore trailing bytes in known blocks.
  A breaking change bumps the major version in the Header block (the magic stays the same).
- Writers never seek. Blocks are written in this order constraint: Header first; a Target before any
  Stream that references it; a Stream and request template before any Data block that uses them.
- A file truncated by a crash is valid up to its last block with a good CRC. A bad CRC in the middle
  makes that block unreadable; a reader MAY continue with the next block (re-sync is not possible
  without lengths, so the reader continues after the claimed length; `repair` handles the rest).

### 2.2 Block types

| Type | Name | Written | Content |
|---|---|---|---|
| 0x01 | Header | once, first | version, tick size, wall-clock start, producer, tags |
| 0x02 | Target | when a device is added | target id, transport, endpoint or serial, unit, label, tags |
| 0x03 | Stream | when a stream is added | stream id, target id, role, label |
| 0x04 | Request template | when a request is first used | template id, request PDU |
| 0x10 | Data | repeatedly | batch of samples, optionally compressed |
| 0x20 | Index | on clean close | per data block: offset, first/last time, streams present; capture end |
| 0x21 | Footer | on clean close | offset of the index block |

### 2.3 Block payloads (draft layouts)

**Header (0x01)**
```
version_major varint | version_minor varint
tick_ns       varint            (default 1000; 1..)
wall_start    varint            (Unix ns, UTC, at tick 0)
flags         varint            bit0: change-only capture; bit1: exceptions recorded
producer      string
tag_count varint | (key string, value string)*
```

**Target (0x02)**
```
id varint | transport u8 | unit varint | label string
host string | port varint                         (empty/0 when serial)
serial_device string | baud varint | parity u8 | data_bits u8 | stop_bits u8   (zero when not serial)
tag_count varint | (key string, value string)*
```
Transport codes: 0 unknown, 1 tcp, 2 udp, 3 tls, 4 rtu-over-tcp, 5 rtu, 6 ascii.
Parity codes: 0 none, 1 even, 2 odd.

**Stream (0x03)**
```
id varint | target_id varint | role string | label string
```

**Request template (0x04)**
```
id varint | pdu_len varint | pdu bytes
```
Template ids are file-global. A poller sends the same few requests thousands of times; each is defined once.

**Data (0x10)**
```
compression u8          0 none, 1 deflate (stdlib), 2 zstd (optional)
raw_len     varint      length of the uncompressed body
body        (compressed with the chosen codec when compression != 0)

body = base_time varint | sample_count varint | sample*
```
`base_time` is the absolute tick time that the first sample's `dt` is relative to (so a block is
independently decodable and seekable).

**Sample**
```
flags    u8       bits 0-1 response coding: 0 RAW, 1 SAME, 2 DIFF (3 reserved)
                  bit 2    request inline (else template id)
                  bits 3-4 kind: 0 read sample (others reserved: writes, events)
                  bits 5-7 reserved, MUST be 0
stream   varint
request  varint                if inline: pdu_len varint | pdu bytes; else template id
dt       zigzag varint         ticks since the previous sample in this block (first: since base_time)
response by coding:
  RAW   pdu_len varint | response PDU bytes
  SAME  (nothing)  identical to the previous response to this (stream, request) in this block
  DIFF  count varint | (word_index_delta varint | 2 bytes)*count
        applies to the previous response of the same length; the unit is a 2-byte word of the PDU
        (an odd trailing byte is padded with 0 and truncated on decode); word_index_delta is relative
        to the previous changed word's index (the first is relative to 0)
```
Rules:
- "Previous response" state is **empty at the start of every data block**. The first sample of each
  (stream, request) in a block MUST be RAW. This is what makes a block independently decodable.
- A writer picks the smallest of RAW / SAME / DIFF per sample. DIFF is only valid when the previous response has the same length.
- `dt` may be negative (samples from concurrent streams can be written slightly out of order); the
  reader MUST NOT assume global monotonicity and MUST sort by `at` (stable) when building timelines.
- Change-only captures (header flag bit0) omit samples whose response equals the previous one for the same
  (stream, request); they never write SAME as a way to record "unchanged".

**Index (0x20)**
```
end_known u8              1 if capture_end is meaningful
capture_end varint        ticks since capture start (0 when end_known is 0)
block_count varint
per data block: offset varint (file offset of the block's type byte) | first_time varint | last_time varint
                | stream_count varint | stream ids varint*
```

**Footer (0x21)** — fixed size so a reader can find it from the end of the file:
```
type 0x21 | length 0x0C | index_offset u64 | tag "MBCE" (4 bytes) | crc32c u32     (18 bytes total)
```
A reader that does not find a valid footer rebuilds the index by scanning, and marks the capture end as unknown.

### 2.4 Compression

Per-block, inside the Data block. Default deflate for file captures; none for live low-latency capture
(writers flush blocks on a timer). zstd is optional and MUST live outside the stdlib-only core
(build tag or sub-module). Readers that meet an unsupported codec MUST fail with a clear error naming the codec.

### 2.5 Block size

Target about 256 samples or 64 KiB uncompressed per Data block, whichever first (tunable; calibrate).
Smaller blocks cost keyframe overhead, larger blocks cost seek granularity and recovery loss.

## 3. JSON Lines encoding (`.mbcap.jsonl`)

One JSON object per line, UTF-8, `\n` terminated. The first line MUST be the header. The `t` field
selects the line type. PDUs are **lowercase hex strings**. Times are **integer ticks** since capture
start (using the header's `tick_ns`), so the text loses nothing relative to the binary. A reader that
wants RFC 3339 times computes them from `wall_start` and `tick_ns`.

| `t` | Meaning | Fields |
|---|---|---|
| `header` | Capture header (first line) | `version` ("1.0"), `tick_ns`, `wall_start` (RFC 3339 with up to ns), `producer`, `flags` (optional: `change_only`, `exceptions`), `tags` (object, optional) |
| `target` | Target definition | `id`, `label`, `transport`, `host`, `port`, `serial` (object: `device`, `baud`, `parity`, `data_bits`, `stop_bits`), `unit`, `tags` |
| `stream` | Stream definition | `id`, `target`, `role`, `label` |
| `s` | Sample | `s` (stream id), `at` (ticks), `req` (hex), `resp` (hex) |
| `end` | Capture end (last line) | `at` (ticks) |

Rules:
- Order: header, then definitions before first use, samples, finally `end`. A missing `end` line means the end is unknown.
- Unknown line types and unknown fields MUST be ignored, so other tools may add their own annotations to lines.
- Exactly one `header` line. A reader that meets a second `header` line MUST return an error rather than silently
  mixing two captures; combining files is done with `mbcapctl cat`/`merge`, which rebases times and renumbers ids.
- JSON Lines is the canonical text encoding. Long CSV (section 4) carries the same records in tabular form for
  spreadsheet and dataframe tools; there is no third text syntax.

### Example

```jsonl
{"t":"header","version":"1.0","tick_ns":1000,"wall_start":"2026-10-14T13:14:24.701149Z","producer":"modbusctl 3.0.0"}
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

One target with two streams. The poll stream reads two holding registers (FC03, address 0x1000, quantity 2)
four times; the second register word changes from `01a7` to `01a8` at the third sample, which is the time a replayer
starts returning the new value. The identity stream holds an FC43/14 read: the response is the full PDU with
conformity level, paging fields and three objects (`ACME`, `X1`, `1.0`). The example is generated and checked to
parse as JSON; it is not captured from a device. (Use it as the first golden file; recompute the hex yourself with
the codecs and fix any mistake rather than trusting the example.)

## 4. Long CSV encoding (`.mbcap.csv`) — lossless

RFC 4180 CSV, UTF-8, `\n` line ends, a header row with the column names below, in this order. One row per record,
in the same order and with the same rules as JSON Lines (section 3): the `t` column selects the record type and uses
the same values (`header`, `target`, `stream`, `s`, `end`). A column that does not apply to a record type is empty.

| Column | Used by | Content |
|---|---|---|
| `t` | all | record type |
| `at` | `s`, `end` | ticks since capture start |
| `s` | `s` | stream id |
| `fc` | `s` | function code of `req`, two lowercase hex digits. **Informative**: derived from `req`, ignored on import, MUST match `req` when present |
| `req` | `s` | request PDU, lowercase hex |
| `resp` | `s` | response PDU, lowercase hex |
| `id` | `target`, `stream` | target or stream id |
| `target` | `stream` | target id of the stream |
| `role` | `stream` | stream role |
| `label` | `target`, `stream` | label |
| `transport` | `target` | transport name (section 1) |
| `host`, `port` | `target` | endpoint |
| `unit` | `target` | unit id |
| `serial_device`, `baud`, `parity`, `data_bits`, `stop_bits` | `target` | serial settings; `parity` is `none`, `even` or `odd` |
| `version`, `tick_ns`, `wall_start`, `producer`, `flags` | `header` | as in JSON Lines; `flags` is a `;`-separated list (`change_only;exceptions`) |
| `tags` | `header`, `target` | JSON object, compact, as in JSON Lines (empty when there are none) |

Rules:
- Exactly one `header` row, first after the column names. A second one is an error, as in JSON Lines.
- Readers MUST locate columns by the header row, ignore unknown columns and unknown record types, and accept the
  columns in any order. Writers write the order above. Extra columns let tools annotate rows; they are dropped on
  conversion to the other encodings.
- Numbers are plain decimal integers. Hex is lowercase on write and accepted in either case on read.
- Free text (labels, producer, tags) is quoted per RFC 4180 when needed; readers MUST NOT trim whitespace.
- Readers cap field and row lengths as for JSON Lines.

Example: records of the JSON Lines example of section 3 (two of its four samples):

```csv
t,at,s,fc,req,resp,id,target,role,label,transport,host,port,unit,serial_device,baud,parity,data_bits,stop_bits,version,tick_ns,wall_start,producer,flags,tags
header,,,,,,,,,,,,,,,,,,,1.0,1000,2026-10-14T13:14:24.701149Z,modbusctl 3.0.0,,
target,,,,,,1,,,Inverter A,tcp,192.168.2.201,502,21,,,,,,,,,,,
stream,,,,,,1,1,poll,holding 4096 x2,,,,,,,,,,,,,,,
stream,,,,,,2,1,identity,,,,,,,,,,,,,,,,
s,0,1,03,0310000002,0304000101a7,,,,,,,,,,,,,,,,,,,
s,2001100,1,03,0310000002,0304000101a8,,,,,,,,,,,,,,,,,,,
end,4000800,,,,,,,,,,,,,,,,,,,,,,,
```

## 5. Wide CSV view (one-way, not importable)

One row per sample time, one column per address (sample and hold applied), raw unsigned words / bits, for charting
raw values. It covers FC01–04 only and drops request PDUs and per-sample identity, so it cannot be imported.
Column names: `at`, then `<target>/<fc>/<address>` (for example `1/03/4096`). An address not yet sampled at a row's
time is empty; nothing is interpolated.

## 6. Derived structures (informative)

What a reader can derive from the model; the library builds only what its own tools need (`info`, the wide CSV view).
Replayers build the rest themselves (see `docs/HANDOFF-MODBUSCTL.md`).

- Per target: per-address values for FC01–04 (split the request `(start, quantity)` and the response data into
  addresses). Coil/discrete-input bit order: LSB of the first data byte is the first address of the request.
- Per target: per-request values for all other functions, keyed by the exact request PDU bytes.
- Per target: FC43/14 object set, merged across pages and across samples (sample-and-hold per object id),
  plus the latest conformity level.
- Capture span: first sample time to capture end.

## 7. Versioning

- Header carries `version_major.version_minor`. Draft 0 corresponds to nothing frozen; the first frozen
  release is 1.0. Readers accept the same major and any minor; unknown major is an error.
- JSON Lines and long CSV `version` mirror the binary header version.
