# Testing go-modbus-capture

Tests need no hardware, network or privileges.

## Tiers

| Tier | Command | In CI |
|---|---|---|
| Unit, golden files | `make test` | yes, Go 1.23 to 1.26, with `-race` |
| Fuzz | `make fuzz` (`FUZZ_TIME`, default 30s) | yes, 10s per target |
| Benchmarks | `make bench` | no |
| Cross-compile | `make cross` | no |

There is no end-to-end tier and no interop tier: this module does no I/O of its own, and no independent MBCAP implementation exists ([INTEROPERABILITY.md](INTEROPERABILITY.md)). The closest thing is the replay test of go-modbus-trace, which runs in that repository.

## Golden files

`mbcap/golden_test.go` pins the binary layout, with a byte-by-byte annotated example. When a layout changes, change [docs/FORMAT.md](docs/FORMAT.md) in the same commit and regenerate with `make golden`.

## Coverage

`make coverage` writes `coverage.out` over `./...`, the CLI included. `make coverage-check` fails below `COVER_MIN` (90), the same minimum as `coverage-min` in `.github/workflows/ci.yml`. No production package is excluded.

## Fuzz regressions

Targets: `FuzzReader` and `FuzzRoundTrip` (`mbcap`), `FuzzReader` (`jsonl`, `mbcsv`). A failing input is written to `testdata/fuzz/<target>/` of the package. Commit it with the fix: `go test` replays it from then on.
