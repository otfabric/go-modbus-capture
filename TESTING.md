# Testing go-modbus-capture

Tests need no hardware, network or privileges.

## Tiers

| Tier | Command | In CI |
|---|---|---|
| Unit, golden files | `make test` | yes, Go 1.26 and 1.27, with `-race` |
| End-to-end | `make e2e` | yes, as part of the package tests |
| Fuzz | `make fuzz` (`FUZZ_TIME`, default 30s): every fuzz target of every package, discovered with `go test -list '^Fuzz'` | yes, 10s per target; nightly, 5 minutes per target |
| 32-bit | `GOARCH=386 go test -count=1 ./...` (needs a host that runs 386 binaries) | nightly, on linux/386 |
| Benchmarks | `make bench` | no |
| Cross-compile | `make cross` | no |

`e2e/` builds `mbcapctl` (with the race detector where cgo allows) and runs it on real files: every encoding and back, pipes, a truncated file and its repair, split, merge and filter, and the exit codes of failures. The public readers check what the binary wrote.

There is no interop tier, and `make interop` fails rather than report a pass: no independent MBCAP implementation exists ([INTEROPERABILITY.md](INTEROPERABILITY.md), TEST-009 in [.otfabric/exceptions.yaml](.otfabric/exceptions.yaml)). The closest thing is the replay test of go-modbus-trace, which runs in that repository.

## Golden files

`mbcap/golden_test.go` pins the binary layout, with a byte-by-byte annotated example. When a layout changes, change [docs/FORMAT.md](docs/FORMAT.md) in the same commit and regenerate with `make golden`.

## Coverage

`make coverage` writes `coverage.out` over `./...`, the CLI included. `make coverage-check` fails below `COVER_MIN` (90), the same minimum as `coverage-min` in `.github/workflows/ci.yml`. `make coverage-pkg-check` fails when one package is below `COVER_PKG_MIN`; CI runs it in the `coverage-packages` job. No production package is excluded.

## Fuzz targets and regressions

| Target | Package | What it checks |
|---|---|---|
| `FuzzReader` | `mbcap` | Whole files: the reader fails only with an error; what it accepts is a valid capture that re-encodes to itself; `Repair` writes a file that reads. A change inside a block fails the block's CRC, so this target reaches the framing, not the block decoders. |
| `FuzzBlocks` | `mbcap` | The block decoders: the input is framed into blocks with correct lengths and CRCs (any block type, and data blocks built around a fuzzed body, compressed or not). The records read form a valid capture that re-encodes to itself, errors are `ErrCorrupt` or `ErrTruncated`, an index payload that decodes encodes to itself, and reading stays within an allocation budget set by the size of the input. Seeded with the blocks of the golden files. |
| `FuzzRoundTrip` | `mbcap` | A capture built from the input (`internal/fuzzcap.Build`: flags, tags, repeats, times out of order and about 2^63 ticks apart) survives binary, JSON Lines and long CSV unchanged. |
| `FuzzReader` | `jsonl` | As for `mbcap`; the capture is the same when every field that the format does not define by its exact name is removed first; and the reader splits every line that is valid JSON into the members that `encoding/json` reads. |
| `FuzzReader` | `mbcsv` | As for `mbcap`, and the wide view of what was read can be written. |

A failing input is written to `testdata/fuzz/<target>/` of the package. Commit it with the fix: `go test` replays it from then on.

## Limits and allocation

Every limit of [docs/FORMAT.md](docs/FORMAT.md) section 1 has a test at the limit and one beyond it, and where a file declares a length or a count, a test that measures what the reader allocates for it (`internal/fuzzcap.AllocBytes`, total bytes allocated) against a budget set by the limit or by the size of the input, not by the declaration.

| Limit | Test |
|---|---|
| string length, tags, tag bytes, targets, streams | `capture`: `TestCheckHeaderLimits`, `TestCheckerTargetAndStream`, `TestCheckerLimits`; `mbcap`: `TestDeclaredValuesBeyondLimits` |
| distinct requests of a change-only capture | `capture`: `TestCheckerRequestLimit` |
| request templates | `mbcap`: `TestTemplateLimit` |
| block payload, uncompressed data block | `mbcap`: `TestDeclaredValuesBeyondLimits`, `TestSizesAtTheLimits`, `TestDeclaredSizeBeyondInput`, `TestReadN` |
| samples in a data block | `mbcap`: `TestDataBlockSampleLimit`, `TestWriterBlockSampleLimit` |
| data blocks in a file (size of the index) | `mbcap`: `TestWriterIndexLimit`, `TestWriterIndexLimitSweep`, `TestIndexCountCheckedFirst` |
| JSON Lines line | `jsonl`: `TestLineLimit` |
| CSV row, fields in a row | `mbcsv`: `TestRecordLimit`, `TestColumnLimit` |
| parts of a split | `edit`: `TestSplitPartLimit`, `TestSplitHoldsLittlePerClosedPart` |

## Ownership and contracts

`internal/fuzzcap.CheckOwnership` runs for every encoding (`TestOwnership` in `mbcap`, `jsonl`, `mbcsv`): a capture is written while the caller changes every PDU and tag map it passed, and read while the caller changes the header tags and every record it got; both times it reads as it was written. `internal/format` `TestReaderAndWriterContracts` pins what the `capture.Reader` and `capture.Writer` comments say of the three encodings: a second `Close` and a write after `Close` are errors, a `Close` that refuses its end can be repeated, the first error of the binary reader sticks, and the text readers go on after a bad line.

## Lifecycle contract matrix

The module has no goroutines, timers, network or serial I/O. Its long-lived types are stream codecs over an `io.Reader` or `io.Writer` of the caller (the readers and writers of `mbcap`, `jsonl` and `mbcsv`) and the `edit` operations with their open functions. Rows L1 to L16 of the organisation's matrix:

| Row | Applies | Test, or why not |
|---|---|---|
| L1 Close twice | yes | `TestReaderAndWriterContracts` (`internal/format`), `TestWriterMisuse` (`mbcap`, `jsonl`, `mbcsv`), `TestCloseReleasesBuffers` (`mbcap`): a second `Close` is an error; `TestWriterCloseAfterRejectedEnd` (`mbcap`), `TestWriterCloseAfterRefusedEnd` (`jsonl`, `mbcsv`): a `Close` that refuses its end can be repeated |
| L2 Close concurrently | no | Single-goroutine codecs: no type is safe for concurrent use, and the doc comments say so |
| L3 Close during a blocked read | no | No background reader: `Next` blocks only inside the caller's `io.Reader` |
| L4 Close during a write | no | The library owns no shared stream: `Close` and `Flush` flush a private buffer |
| L5 Close during handshake | no | No handshake |
| L6 to L8 cancel, late response, reconnect | no | No requests and no connections |
| L9 Callback calls a public method | yes | The open functions of `edit` run on the calling goroutine with no lock held: the `edit` tests and `e2e` open writers and write from them |
| L10 Callback calls Close | no | `edit` has no `Close` |
| L11, L12 slow callback, queue at capacity | no | No queues |
| L13 Concurrent sessions | no | Every reader and writer owns its state. The one package-level variable that is written, the index size limit of `mbcap`, is changed by tests only |
| L14 Failure during construction | yes | `NewReader` and `NewWriter` acquire only memory. `TestSplitPropagatesErrors` and `TestSplitPartLimit` (`edit`): `Split` returns the error of every failing step and leaves the parts it opened to the caller, as its comment says |
| L15, L16 Shutdown, Close racing reconnect | no | Nothing to shut down or reconnect |

## When CI runs

| Event | Workflow | What runs |
|---|---|---|
| Pull request | `pr.yml` | `docs.yml` (docsync, otfabric-policy) always; `ci.yml` unless every changed file is documentation; then `OT Fabric Required Checks` |
| Push to a branch | none | nothing; dispatch `ci.yml` to test a branch without a pull request |
| Push to `main` | `docs.yml` | the light checks; the merged commit was tested as the pull request's merge commit |
| Schedule | `nightly.yml` | the scheduled runs, on `main` |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
