# Testing go-modbus-capture

Tests need no hardware, network or privileges.

## Tiers

| Tier | Command | In CI |
|---|---|---|
| Unit, golden files | `make test` | yes, Go 1.23 to 1.26, with `-race` |
| End-to-end | `make e2e` | yes, as part of the package tests |
| Fuzz | `make fuzz` (`FUZZ_TIME`, default 30s) | yes, 10s per target; nightly, 5 minutes per target |
| Benchmarks | `make bench` | no |
| Cross-compile | `make cross` | no |

`e2e/` builds `mbcapctl` (with the race detector where cgo allows) and runs it on real files: every encoding and back, pipes, a truncated file and its repair, split, merge and filter, and the exit codes of failures. The public readers check what the binary wrote.

There is no interop tier, and `make interop` fails rather than report a pass: no independent MBCAP implementation exists ([INTEROPERABILITY.md](INTEROPERABILITY.md), TEST-009 in [.otfabric/exceptions.yaml](.otfabric/exceptions.yaml)). The closest thing is the replay test of go-modbus-trace, which runs in that repository.

## Golden files

`mbcap/golden_test.go` pins the binary layout, with a byte-by-byte annotated example. When a layout changes, change [docs/FORMAT.md](docs/FORMAT.md) in the same commit and regenerate with `make golden`.

## Coverage

`make coverage` writes `coverage.out` over `./...`, the CLI included. `make coverage-check` fails below `COVER_MIN` (90), the same minimum as `coverage-min` in `.github/workflows/ci.yml`. `make coverage-pkg-check` fails when one package is below `COVER_PKG_MIN`; CI runs it in the `coverage-packages` job. No production package is excluded.

## Fuzz regressions

Targets: `FuzzReader` and `FuzzRoundTrip` (`mbcap`), `FuzzReader` (`jsonl`, `mbcsv`). A failing input is written to `testdata/fuzz/<target>/` of the package. Commit it with the fix: `go test` replays it from then on.
