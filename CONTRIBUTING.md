# Contributing to go-modbus-capture

Read [AGENTS.md](AGENTS.md) first: it lists the scope and the ground rules.
[REQUIREMENTS.md](REQUIREMENTS.md) and [docs/FORMAT.md](docs/FORMAT.md) are the source of truth. If you must deviate, change the document in the same commit and say why.

## Development setup

- Go 1.23+ (see [go.mod](go.mod))
- [staticcheck](https://staticcheck.dev/), [golangci-lint](https://golangci-lint.run/) and [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) for the checks
- No hardware and no network: the tests read files in `testdata/` and generate captures

```sh
make check     # format, lint, vet, vulnerability scan, race tests
make build     # ./bin/mbcapctl
make fuzz      # every fuzz target for FUZZ_TIME (default 30s)
make golden    # regenerate the derived golden files in testdata/
```

Run `make help` for every target.

## Rules

- **Standard library only** in the packages. Nothing imports `go-modbus`.
- **Hostile input:** readers cap lengths and counts, never panic, and a new reader comes with a fuzz target.
- **Lossless:** binary, JSON Lines and long CSV must round-trip; test it.

## Pull requests

- Branch from `main` and target `main`.
- `make check` passes locally.
- Add a section to [RELEASE.md](RELEASE.md) for anything a user notices.

## Security

Report vulnerabilities as [SECURITY.md](SECURITY.md) describes, not in a public issue.

## License

By contributing, you agree that your contributions are licensed under the [MIT License](LICENSE).
