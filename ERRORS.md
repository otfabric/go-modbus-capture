# Errors

The sentinels this module exports. Test them with `errors.Is`: readers wrap
them with the line (text encodings) or byte offset (binary) of the problem.
How to read a capture to the end: [docs/library.md](docs/library.md).

| Error | Package | Meaning | What to do |
|---|---|---|---|
| `ErrTruncated` | `capture` | The input ended before the capture did: in a record or block, or a binary file without its index. Records returned before it are valid, and the capture has no recorded end. | Use what was read. `mbcap.Repair` rewrites a binary file. |
| `ErrCorrupt` | `capture` | Not a valid encoding, a checksum mismatch, or records that break the model's invariants. | Reject the file. `mbcap.Repair` can salvage a binary file with damaged blocks. |
| `ErrNoFooter` | `mbcap` | `ReadIndex` found no valid footer, as in a file cut short by a crash. | `mbcap.Repair` rewrites such a file. |

`io.EOF` from a reader's `Next` means the capture ended normally.

`mbcapctl` exits with 0 on success, 1 on an error and 2 on a usage error.
