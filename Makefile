.PHONY: help all test coverage cover lint lint-ci vuln fmt fmt-check vet fuzz bench build check clean golden coverage-check cross e2e interop coverage-pkg-check

# Prefer module-local dependency resolution when a sibling go.work exists.
export GOWORK := off

help: ## Show targets and descriptions
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help

APP_NAME   = mbcapctl
CMD_PKG    = ./cmd/mbcapctl
BIN_DIR    = bin
BINARY     = $(BIN_DIR)/$(APP_NAME)

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.0.0-dev")
TAG        ?= $(shell git describe --tags --exact-match 2>/dev/null || echo "")
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -ldflags "-s -w -X 'main.version=$(VERSION)' -X 'main.tag=$(TAG)' -X 'main.commit=$(COMMIT)' -X 'main.buildDate=$(BUILD_DATE)'"

# CI formats with the Go version from go.mod; gofmt output can differ between releases.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

# Fuzz targets and the time each gets in `make fuzz`.
# Same minimum as coverage-min in ci.yml.
COVER_MIN := 90
# Per-package floor (TEST-012): what the weakest package measures now, rounded down.
COVER_PKG_MIN := 90
FUZZ_TIME ?= 30s
FUZZ_TARGETS = ./mbcap:FuzzReader ./mbcap:FuzzRoundTrip ./jsonl:FuzzReader ./mbcsv:FuzzReader

all: check build ## Run checks and build bin/mbcapctl

coverage-check: coverage ## Fail when total coverage is below COVER_MIN
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%","",$$3); print $$3}'); \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { if (t+0 < m+0) { printf "coverage %.1f%% < %s%%\n", t, m; exit 1 } else printf "coverage %.1f%% >= %s%%\n", t, m }'

CROSS_PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/arm64 windows/amd64

cross: ## Cross-compile without cgo for the supported platforms
	@for p in $(CROSS_PLATFORMS); do \
		echo "Building for $$p"; \
		CGO_ENABLED=0 GOOS=$${p%/*} GOARCH=$${p#*/} go build ./... || exit 1; \
	done

coverage-pkg-check: coverage ## Fail when a package is below COVER_PKG_MIN
	@awk -v m="$(COVER_PKG_MIN)" 'NR > 1 { n[$$1] = $$2; if ($$3 > 0) h[$$1] = 1 } \
	END { for (k in n) { p = k; sub(/\/[^\/]*$$/, "", p); s[p] += n[k]; if (k in h) c[p] += n[k] } \
	for (p in s) { v = 100 * c[p] / s[p]; if (v < m) { printf "%s: %.1f%% < %s%%\n", p, v, m; bad = 1 } } exit bad }' coverage.out

check: fmt fmt-check lint lint-ci vet vuln test coverage-pkg-check ## Format, lint, vet, vuln, and test

test: ## Run unit tests (race detector)
	@echo "Running go test -race ./..."
	@go test -count=1 -race ./...

coverage: ## Write coverage.out (atomic mode)
	@echo "Running tests with coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic ./...

cover: coverage ## Open HTML coverage report
	@go tool cover -html=coverage.out

fuzz: ## Run every fuzz target for FUZZ_TIME
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t#*:}; \
		echo "Fuzzing $$name in $$pkg for $(FUZZ_TIME)"; \
		go test -run '^$$' -fuzz "^$$name$$" -fuzztime $(FUZZ_TIME) $$pkg || exit 1; \
	done

e2e: ## Run the built mbcapctl over real files; the library reads what it wrote
	@go test -count=1 -race ./e2e/...

interop: ## No suite: no independent MBCAP implementation exists, so this fails rather than report a pass (see INTEROPERABILITY.md)
	@echo "no independent MBCAP implementation exists; see INTEROPERABILITY.md"
	@exit 1

bench: ## Run benchmarks
	@go test -run '^$$' -bench . -benchmem ./...

golden: ## Regenerate derived golden files in testdata/ from the hand-written JSON Lines sources
	@go test ./mbcap -run TestGolden -update

lint: ## Run staticcheck
	@echo "Running staticcheck"
	@staticcheck ./...

lint-ci: ## Run golangci-lint (.golangci.yml)
	@echo "Running golangci-lint"
	@golangci-lint run ./...

fmt: ## gofmt all Go sources with the go.mod Go version
	@echo "Running gofmt (go$(GO_MOD_VERSION))"
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if the go.mod-version gofmt or the local gofmt would change any file
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then echo "Not formatted:"; echo "$$ci $$loc"; exit 1; fi

vet: ## go vet
	@go vet ./...

vuln: ## Run govulncheck
	@govulncheck ./...

build: ## Build $(BINARY)
	@mkdir -p $(BIN_DIR)
	@go build $(LDFLAGS) -o $(BINARY) $(CMD_PKG)

clean: ## Remove bin/ and coverage.out
	@rm -rf $(BIN_DIR) coverage.out
