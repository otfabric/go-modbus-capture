# MCAP v1 analysis (the format being replaced)

Source: `otfabric/modbusctl`, read 2026-10-14. MCAP v1 is **not** supported by this library; this file
records why the new format exists and where the old code lives.

## Where it lives

`internal/mcap/codec.go` (`WriteHeader`, `AppendRecord`, `ReadHeader`, `ReadRecord`; constants `mcapMagic = "MCAP"`,
`mcapVersion = 1`), `internal/mcap/load.go`, `export_info.go`, `export_json.go`, `export_csv.go`, `export_blocks.go`,
`decode_profile.go`, `internal/format/mcap_derived.go`, `internal/types/capture.go`
(`CaptureHeader{IP, Port, Unit, Function, StartTime}`, `CaptureRecord{Iteration, RequestTimestamp, ResponseTimestamp,
StartAddress, RegisterCount, Data}`), `internal/modbus/client.go` (`performRead` FC1–4 only, `readRegisters`,
`RecordAndWriteMCAP`, `ScanAndWriteMCAP`), `internal/modbus/server.go` (`MemoryStore`, `LoadMCAPAndServeStatic/Replay`),
`cmd/client/record.go`, `cmd/server/replay.go`.

## Layout (all big-endian)

```
"MCAP" | version u8 (=1) | header_len u16 | header JSON | record*
record = iteration u32 | request_ts i64 | response_ts i64 | start_addr u16 | count u16 | data_len u16 | data
```

Header: `ip`, `port`, `unit`, `function`, `start_time`. `CaptureHeader` has no JSON tags, so the keys on disk are the Go names
(`IP`, `Port`, …); only `mcap convert` output uses lowercase. Timestamps are wall-clock Unix nanoseconds. 26 bytes of fixed
overhead per record before data.

Writers: `client read`, `client scan`, `client record`. Readers: `server static`, `server replay`, and the `mcap`
subcommands (`convert`, `info`, `decode`, `extract`, `strings`, `frequencies`).

## What v1 cannot keep

| Area | Behaviour in v1 | Consequence |
|---|---|---|
| Function codes | One FC per file (header). `performRead` accepts only FC01–04 | FC43, 17, 07, 11, 12, 20, 24 cannot be recorded |
| Failures | `record` skips a failed block; `scan` writes successes only | Exception responses are not kept |
| Devices | One ip, port, unit per file | No multi-device or multi-stream files |
| Transport metadata | Serial devices put the device path in `ip` with port 0; baud, parity, TCP/UDP/RTU-over-TCP not stored | Replayer cannot tell the original link |
| Request/response | Neither PDU stored; request implied by start+count, response is data bytes only | Nothing beyond FC01–04 fits |
| Timestamps | Two absolute wall-clock values per record (16 B); replay uses only the first request time of each iteration | Spacing between blocks approximate; a wall-clock step breaks intervals |
| Robustness | Writer seeks to start (header) and to end (every record). No checksum, end marker or index. `LoadRecordsFromMCAP` loads the whole file | Cannot write to a pipe or socket, cannot seek by time, cannot skip a damaged record |
| Extensibility | One version byte, no flags, header ≤ 64 KB, no skippable sections | Any new field needs a new version |

Replay limits: the server answers only the header's function code and unit (one function of one unit per file);
speed is a fixed pause between iterations, not a factor of the original; `--loops` restarts at iteration 0 so values jump back
at the loop point.

## Name collision

`.mcap` is already used by an unrelated, established format. That is why the new format is named MBCAP/`.mbcap`.

## Related code worth knowing

- `go-modbus` (v1.5.0 when checked): `modbus.go`, `client_device_id.go` (FC43), `client_diagnostics.go`, `metrics.go`
  (`ClientMetrics`, `AttemptMetrics`: no PDU bytes), `server.go`, `server_transport.go` (dispatch cases),
  `internal/protocol/fc.go`, `internal/adu/types.go`, `client_exec.go`.
- `modbus-simulator`: `REQUIREMENTS.md`, `internal/simulator/rawpdu.go`, `trace.go` (raw PDU handling and JSONL traces;
  overlaps with this model, see REQUIREMENTS decision 11).
- `go-modbus-identity`: FC43 profile handling.
