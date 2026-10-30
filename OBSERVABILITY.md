# Observability

The library logs nothing, has no metrics hooks and no stats type: it runs inside a caller's read or write call, and everything it has to report is a returned value.

| What | Where |
|---|---|
| Why a read failed, with the line (text encodings) or byte offset (binary) | the returned error ([ERRORS.md](ERRORS.md)) |
| A capture that ended early | `capture.ErrTruncated`; records already returned are valid |
| Exit status of `mbcapctl` | 0 success, 1 error, 2 usage error |
