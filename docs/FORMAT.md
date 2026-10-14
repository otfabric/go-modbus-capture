# MBCAP format specification — draft 0.1

This is the normative specification, for implementers of readers and writers. For an introduction with
examples, read [Concepts](concepts.md) and [Encodings](encodings.md) first.

Status: **implemented, not frozen.** The layouts below are the ones the library reads and writes, pinned
by golden files in `testdata/` (`mbcap/golden_test.go` holds a byte-by-byte annotated example). Files
carry format version **0.1**; major version 0 marks the draft. The format is frozen as **1.0** after the
binary coding is calibrated on real captures (`REQUIREMENTS.md` section 15); 1.0 readers will not read
0.x files. When a layout changes, change this file in the same commit and update the golden files
(`make golden`).

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
  the file; edit tools append ` | <step>`), `source` (original file names when merged).

Invariants every reader checks and every writer enforces (`capture.Checker`); a file that breaks one is
invalid:

- Header: `tick_ns` from 1 to 1 000 000 000 (1 s); `wall_start` between 1970 and 2262 (Unix
  nanoseconds fit an int64).
- Strings (labels, producer, host, serial device, roles, tag keys and values): valid UTF-8 with **no
  carriage return** (U+000D), so that every text encoding keeps them; tag keys non-empty.
- Ids: targets and streams are defined once, before use; a stream's target is defined.
- PDUs: request and response 1 to 253 bytes. The request function code is 0x01–0x7F and **not a write**
  (FC05, 06, 15, 16, 22, 23): captures hold reads only. The response function code is the request's, or
  the request's with 0x80 set (an exception response).
- Exception responses only in a capture whose header records exceptions.
- In a change-only capture, no response equals the previous response to the same request of the same
  stream (in file order).
- Sample times need not increase (concurrent streams); the capture end, when known, is not before any
  sample, and nothing follows it.

Writers drop, without error, the samples a capture does not store: exception responses when the header
does not record exceptions, and unchanged responses in a change-only capture. Any other invalid record
is an error.

Limits for hostile input, in readers and writers alike:

| Limit | Value |
|---|---|
| string length | 64 KiB |
| tags per header or target, total tag bytes | 256, 1 MiB |
| targets, streams | 65 536 each |
| request templates; distinct (stream, request) pairs in a change-only capture | 1 048 576 |
| binary block payload; uncompressed data block | 32 MiB; 16 MiB |
| JSON Lines line; CSV row | 4 MiB |

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
- After the Index only the Footer may follow (unknown block types are still skipped); nothing may
  follow the Footer.
- Writers never seek. Blocks are written in this order constraint: Header first; a Target before any
  Stream that references it; a Stream and request template before any Data block that uses them.
- A file truncated by a crash is valid up to its last complete block: the reader returns those records,
  then an error wrapping `capture.ErrTruncated` (also for a file that ends after a block but has no
  Index and Footer). A bad CRC is an error wrapping `capture.ErrCorrupt`; with `SkipCorrupt` the reader
  skips the block after its claimed length and continues (re-sync is not possible without lengths).
  `mbcap.Repair` / `mbcapctl repair` rewrite such files with a new Index and Footer.

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
version_major varint | version_minor varint      (0.1)
tick_ns       varint            (default 1000; 1..1e9)
wall_start    varint            (Unix ns, UTC, at tick 0)
flags         varint            bit0: change-only capture; bit1: exceptions recorded; other bits are an error
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
Template ids are file-global, positive, and defined once. A poller sends the same few requests thousands of
times; each is defined once. The writer gives every distinct request a template the first time it is used
(just before the data block that needs it) and writes requests inline only beyond 1 048 576 templates.

**Data (0x10)**
```
compression u8          0 none, 1 deflate (stdlib), 2 zstd (optional)
raw_len     varint      length of the uncompressed body
body        (compressed with the chosen codec when compression != 0)

body = base_time varint | sample_count varint | sample*
```
`base_time` is the absolute tick time that the first sample's `dt` is relative to (so a block is
independently decodable and seekable); the writer uses the first sample's time, so its `dt` is 0.
`sample_count` is at least 1. With deflate, the body is a raw DEFLATE stream (RFC 1951,
`compress/flate`) of exactly `raw_len` bytes; the writer stores a block uncompressed when deflate does
not make it smaller.

**Sample**
```
flags    u8       bits 0-1 response coding: 0 RAW, 1 SAME, 2 DIFF (3 reserved)
                  bit 2    request inline (else template id)
                  bits 3-4 kind: 0 read sample (others reserved: writes, events; an error for this version)
                  bits 5-7 reserved, MUST be 0 (an error otherwise)
stream   varint
request  varint                if inline: pdu_len varint | pdu bytes; else template id
dt       zigzag varint         ticks since the previous sample in this block (first: since base_time)
response by coding:
  RAW   pdu_len varint | response PDU bytes
  SAME  (nothing)  identical to the previous response to this (stream, request) in this block
  DIFF  count varint | (word_index_delta varint | 2 bytes)*count
        applies to the previous response of the same length; the unit is a 2-byte word of the PDU
        (an odd trailing byte is padded with 0 and truncated on decode); word_index_delta is relative
        to the previous changed word's index (the first is relative to 0, the others are at least 1);
        every index is within the response
```
Rules:
- "Previous response" state is **empty at the start of every data block**. The first sample of each
  (stream, request) in a block MUST be RAW. This is what makes a block independently decodable.
- A writer picks the smallest of RAW / SAME / DIFF per sample: SAME when the response equals the previous
  one, DIFF when it has the same length and the diff is smaller than RAW, RAW otherwise. DIFF is only valid
  when the previous response has the same length.
- Times stay within 0..2^64-1; a `dt` that leaves that range is an error.
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
`first_time` and `last_time` are the earliest and latest sample times in the block (not those of its
first and last sample: times may go back); stream ids are in increasing order. A streaming reader checks
the index against the data blocks it read and treats a mismatch as corruption (not after skipping a
corrupt block). `mbcap.ReadIndex` reads it from the end of the file without reading the data.

**Footer (0x21)** — fixed size so a reader can find it from the end of the file:
```
type 0x21 | length 0x0C | index_offset u64 | tag "MBCE" (4 bytes) | crc32c u32     (18 bytes total)
```
The CRC covers the first 14 bytes. A file without a valid footer is truncated: read it as far as it goes,
or rewrite it with `repair`, which marks the capture end as unknown unless the old index survived.

### 2.4 Compression

Per-block, inside the Data block. Default deflate for file captures; none for live low-latency capture
(a recorder calls the writer's `Flush` on a timer: it ends the open block and flushes, after which the
file is readable up to there). zstd is optional and MUST live outside the stdlib-only core
(build tag or sub-module). Readers that meet an unsupported codec MUST fail with a clear error naming the codec.

### 2.5 Block size

The writer ends a Data block after 256 samples or once its uncompressed body reaches 64 KiB, whichever
comes first (`mbcap.Options`; to be calibrated).
Smaller blocks cost keyframe overhead, larger blocks cost seek granularity and recovery loss.

## 3. JSON Lines encoding (`.mbcap.jsonl`)

One JSON object per line, UTF-8, `\n` terminated (a last line without `\n` means the file was cut:
`ErrTruncated`). The first line MUST be the header. Blank lines are ignored. The `t` field
selects the line type. PDUs are **lowercase hex strings**. Times are **integer ticks** since capture
start (using the header's `tick_ns`), so the text loses nothing relative to the binary. A reader that
wants RFC 3339 times computes them from `wall_start` and `tick_ns`. Readers accept hex in either case.

Writers leave out fields at their zero value except `transport`, `unit` and the required ones: `producer`,
`flags`, `tags`, `label`, `host`, `port`, `serial` (and parity `none` inside it), `role`. Readers take a
missing optional field as zero. Required: `version`, `tick_ns`, `wall_start` (header); `id` (target);
`id`, `target` (stream); `s`, `at`, `req`, `resp` (sample); `at` (end). Numbers are JSON integers; a
fraction, a negative value or one beyond the field's range is an error.

| `t` | Meaning | Fields |
|---|---|---|
| `header` | Capture header (first line) | `version` ("0.1"), `tick_ns`, `wall_start` (RFC 3339 with up to ns), `producer`, `flags` (optional: `change_only`, `exceptions`), `tags` (object, optional) |
| `target` | Target definition | `id`, `label`, `transport`, `host`, `port`, `serial` (object: `device`, `baud`, `parity`, `data_bits`, `stop_bits`), `unit`, `tags` |
| `stream` | Stream definition | `id`, `target`, `role`, `label` |
| `s` | Sample | `s` (stream id), `at` (ticks), `req` (hex), `resp` (hex) |
| `end` | Capture end (last line) | `at` (ticks) |

Rules:
- Order: header, then definitions before first use, samples, finally `end`. A missing `end` line means the end
  is unknown. After `end` only lines of unknown types may follow.
- Unknown line types and unknown fields MUST be ignored, so other tools may add their own annotations to lines.
- Exactly one `header` line. A reader that meets a second `header` line MUST return an error rather than silently
  mixing two captures; combining files is done with `mbcapctl cat`/`merge`, which rebases times and renumbers ids.
- JSON Lines is the canonical text encoding. Long CSV (section 4) carries the same records in tabular form for
  spreadsheet and dataframe tools; there is no third text syntax.

### Example

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

One target with two streams. The poll stream reads two holding registers (FC03, address 0x1000, quantity 2)
four times; the second register word changes from `01a7` to `01a8` at the third sample, which is the time a replayer
starts returning the new value. The identity stream holds an FC43/14 read: the response is the full PDU with
conformity level, paging fields and three objects (`ACME`, `X1`, `1.0`). It is not captured from a device; it is
the golden file `testdata/example.mbcap.jsonl`, and its binary and CSV encodings are `example.mbcap` and
`example.mbcap.csv`.

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
- Numbers are plain decimal integers. An empty optional number is 0 and an empty parity is `none`; writers leave
  such fields empty, except `transport` and `unit`, which they always write. Required fields are those of JSON
  Lines. Hex is lowercase on write and accepted in either case on read.
- Free text (labels, producer, tags) is quoted per RFC 4180 when needed; readers MUST NOT trim whitespace. Text
  holds no carriage return (section 1): RFC 4180 readers turn CR LF inside a quoted field into LF.
- Every row has as many fields as the column row. Readers accept `\r\n` line ends.
- The input ends with a line end; a last row without one, or input that ends inside a quoted field, is
  `ErrTruncated`. Readers cap rows at 4 MiB.

Example: records of the JSON Lines example of section 3 (two of its four samples; the whole capture is
`testdata/example.mbcap.csv`):

```csv
t,at,s,fc,req,resp,id,target,role,label,transport,host,port,unit,serial_device,baud,parity,data_bits,stop_bits,version,tick_ns,wall_start,producer,flags,tags
header,,,,,,,,,,,,,,,,,,,0.1,1000,2026-10-14T13:14:24.701149Z,modbusctl 3.0.0,,
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
Column names: `at`, then `<target>/<fc>/<address>` (for example `1/03/4096`, the function code as two hex digits),
ordered by target, function code and address. Rows are in time order (samples at the same time share a row);
each cell is the latest value sampled at or before the row's time, or empty before the first sample of its
address. Nothing is interpolated. Exception responses and responses whose size does not match the request are
left out (`mbcsv.WriteWide` counts them). Values are 0/1 for coils and discrete inputs (LSB of the first data
byte is the first address) and unsigned 16-bit words for registers.

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
- JSON Lines and long CSV `version` mirror the binary header version ("0.1" for the draft).
- Readers detect the encoding from the content: the magic number, a leading `{`, or CSV column names.
