# Errors

The sentinels this module exports. Test them with `errors.Is`: readers wrap
them with the line (text encodings) or byte offset (binary) of the problem.
How to read a capture to the end: [docs/library.md](docs/library.md).

| Error | Package | Meaning | What to do |
|---|---|---|---|
| `ErrTruncated` | `capture` | The input ended before the capture did: in a record or block, or a binary file without its index. Records returned before it are valid, and the capture has no recorded end. | Use what was read. `mbcap.Repair` rewrites a binary file. |
| `ErrCorrupt` | `capture` | From a reader: not a valid encoding, a checksum mismatch, a value beyond a limit of the format, or records that break the model's invariants. From a writer: a record it does not write, because it breaks an invariant or a limit. | Reject the file. `mbcap.Repair` can salvage a binary file with damaged blocks. A writer stays usable: see below. |
| `ErrNoFooter` | `mbcap` | `ReadIndex` found no valid footer, as in a file cut short by a crash. | `mbcap.Repair` rewrites such a file. |

`io.EOF` from a reader's `Next` means the capture ended normally.

## After an error

| Call | State afterwards |
|---|---|
| `Next` of the binary reader | The error sticks: every later `Next` returns it. |
| `Next` of the JSON Lines and long CSV readers | The next call goes on with the line or row after the one in error. Stop at the first error unless skipping bad lines is what you want. |
| A write that returns `ErrCorrupt` | The record was not written and the writer goes on. This includes `mbcap.Writer.WriteSample` for a sample whose data block the index has no room for ([LIMITATIONS.md](LIMITATIONS.md)): close the file and continue in a new one. |
| `Close` that returns `ErrCorrupt` for its end (an end before a sample) | The writer is still open: call `Close` again with a valid end, or `capture.End{}`. |
| A write or `Close` that fails on the underlying `io.Writer` | The error sticks. |
| A second `Close`, a write after `Close` | An error. |
| `edit.Split` with more than 4 096 parts | A plain error for the sample that would need one more part; the parts opened so far are incomplete. |
| Any `edit` operation that returns an error | The writers it opened are not closed: discard their output. |

`mbcapctl` exits with 0 on success, 1 on an error and 2 on a usage error.
