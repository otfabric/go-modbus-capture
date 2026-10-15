# Hand-off: capture and replay (moves to `modbusctl`)

> **Audience: `modbusctl` developers.** Design notes for recording and replaying captures, which are not part
> of this repository.

Status: moved out of `go-modbus-capture` on 2026-10-14 by owner decision. `go-modbus-capture` is a library for the
MBCAP file format and its encodings, plus `mbcapctl` for working with capture files. It does not capture from devices
and does not replay or host them. That work lands in `otfabric/modbusctl` (and a later tool on the owner's roadmap),
built on top of this library.

**Implemented in `modbusctl` 3.0.0** (`internal/capfile`: recorder; `internal/replay`: replay core and clock;
`internal/modbus/server.go`: the servers). This file is the design it follows; the modbusctl README and release notes
document the result. Since modbusctl 3.1.0 (go-modbus v1.7.0) the servers answer every recorded function (§2.7).
Still open: §1 raw request send for vendor functions. Words in **MUST**, **SHOULD**, **MAY** follow RFC 2119, as in `REQUIREMENTS.md`.

Everything here reads and writes captures only through `go-modbus-capture` (`capture`, `mbcap`, `jsonl`, `mbcsv`).

## 1. Recorder

- Poll plans: a list of requests with intervals (any read function), each a **stream**; targets are devices.
- A scan that discovers new units adds Targets/Streams mid-capture.
- Never send state-changing diagnostics unless explicitly allowed. Reads only.
- Exception responses: recorded only if the writer option asks; default off.
- Use the `mbcap` writer's timed flush for live capture; the file stays valid after every flushed block.
- On clean close the writer writes the Index and Footer.
- Sample time = the response-received instant, as monotonic ticks since capture start (`REQUIREMENTS.md` section 7).

### go-modbus dependency

- **Client PDU tap**: `Config.PDUTap` in go-modbus, opened as [otfabric/go-modbus#1](https://github.com/otfabric/go-modbus/pull/1)
  (released in [v1.6.0](https://github.com/otfabric/go-modbus/releases/tag/v1.6.0)). Once per answered request it reports unit id, request PDU, response PDU and receive time (with
  monotonic reading), plus the caller's context. It also reports exception responses and responses the calling method
  then rejects; the recorder SHOULD check the method's error to decide what to keep. Writes and probes are reported
  too, so the recorder filters for reads.
- **Raw request send**: go-modbus has no method to send an arbitrary PDU. Recording vendor or unknown read functions
  (the "other" row of `REQUIREMENTS.md` section 6) needs one; every listed read function already has a typed method.

## 2. Replay semantics

### 2.1 Replay clock

A replayer runs a virtual clock **T** (a `time.Duration` since capture start). The lookup is the same whatever the
clock does; only how T advances changes. Clock modes are consumer settings and are not stored in the file.

| Mode | How T advances |
|---|---|
| Original speed | 1 s of T per wall-clock second |
| Slowed | factor < 1 (e.g. 0.1) |
| Fast | factor > 1 (e.g. 60) |
| Stepped | each client request moves T to the next sample time (deterministic tests, no wall clock) |
| Seek and loop | start at an offset; after the end, wrap. Values jump back at the seam |

The lookup core SHOULD take `t` as an argument and not read the wall clock itself; a separate clock helper implements
the modes above.

### 2.2 Selecting what to host

A consumer picks one or more targets, one or more streams, a time window, and/or a set of function codes.
(The `edit` package and `mbcapctl filter`/`split` in `go-modbus-capture` do this at file level.)

### 2.3 Overrides

| Item | Overridable? |
|---|---|
| Listen address, port, TLS settings | Yes; the recorded endpoint is only a hint |
| Unit id | Yes (mapping such as "serve target 1 as unit 5"). Several targets may share one listener and be routed by unit |
| Transport (TCP, TLS, RTU) | Yes, as far as the server supports it |
| Response data (registers, coils, identity values) | **No** |
| Sample times | Not rewritten; the replay clock decides speed |
| Answer to unrecorded things | Consumer's choice (2.4) |

### 2.4 How a request is answered at time T

- **Bits and registers (FC01–04).** For each address in the requested range take the latest sample at or before T.
  If every address has one, build the response from them. Addresses sampled in different blocks were read a few ms
  apart; each simply holds its own latest value.
- **Other functions.** Take the latest sample for the same request PDU at or before T and return its response PDU.
- **No data.** If the capture holds nothing, the consumer decides. Presets:
  - `SpecException` (default): Illegal Data Address (02) when any address in range was never recorded;
    Illegal Function (01) for a function never recorded.
  - `ZeroFill`
  - `Silence` (no response)
- **Before the first sample.** `HoldFirst` (default: hold the first sample from the start; suits data read once, such
  as identity) or `AbsentUntilSampled`.
- **Exception responses** in the capture (response FC has bit 0x80 set) are not hosted data and are ignored for
  hosting unless the consumer opts in.

### 2.5 FC43/14 device identification

The data is the **set of objects** (vendor name, product code, revision, …) plus the conformity level, not the
paging. A full read is several transactions because the device pages its objects. Each page is stored as received;
the replayer merges pages into one object set (sample-and-hold per object), answers any read code (01 basic,
02 regular, 03 extended, 04 individual) and object id from that set, and builds the response pages itself
(respecting the max PDU size). An object never recorded is handled like any other missing data.

### 2.6 Core API sketch

```go
// Device is one target of a capture, answering from recorded samples only.
type Device interface {
    // Read returns the response PDU for req as of replay time t.
    // ok is false when the capture holds nothing for it; the caller chooses the reply.
    Read(t time.Duration, req []byte) (resp []byte, ok bool)
    // Span is the first sample time and the capture end.
    Span() (start, end time.Duration)
}
```

Implementation guidance: build per-address timelines (sorted `(tick, value)` change lists, binary-searched) for
FC01–04, and per-request timelines (sorted `(tick, resp)`) for the rest. Keep the core free of any server,
goroutine-per-connection or wall-clock dependency. For large captures, consider lazy per-block loading using the MBCAP
index (`mbcap` reader exposes it).

### 2.7 Serving every function

Resolved in go-modbus [v1.7.0](https://github.com/otfabric/go-modbus/releases/tag/v1.7.0)
([otfabric/go-modbus#3](https://github.com/otfabric/go-modbus/pull/3)): the server dispatches every function code, and
a `RawRequestHandler` receives the request PDU after the function code and returns the response PDU after it for every
function the handler does not decode (FC01–06, 15 and 16 never reach it). modbusctl 3.1.0
([otfabric/modbusctl#2](https://github.com/otfabric/modbusctl/pull/2)) serves FC01–04 per address and FC43/14 per
object, and answers every other recorded function (FC07, 08, 11, 12, 17, 20, 24, other FC43 MEI types, vendor
functions) with the response recorded for exactly that request PDU, byte for byte.

## 3. Tests the replay side needs

Sample-and-hold boundaries (T exactly on a sample time), before-first-sample modes, mixed-block address coverage,
answer presets, stepped clock, loop seam; lookup latency on a 1M-sample capture.

## 4. Decisions carried over (recommendations, not yet confirmed by the owner)

| # | Question | Decision | Reason |
|---|---|---|---|
| R1 | Exception responses in replay | Ignored unless the consumer wants them | Hints about refusals, not hosted data |
| R2 | Unrecorded requests | Consumer's choice; presets SpecException (default), ZeroFill, Silence | Per the owner's direction |
| R3 | Before first sample | Default HoldFirst; AbsentUntilSampled selectable | Data read once (identity) should exist from second 0 |

## 5. Migration in modbusctl

Move `modbusctl` `record`, `scan`, `read`, `server static` and `server replay` onto `go-modbus-capture`. The old
code is listed in `REQUIREMENTS.md` section 16 of `go-modbus-capture`. MCAP v1 is not carried over.
