.PHONY: help all test coverage cover lint lint-ci vuln fmt fmt-check vet fuzz bench build check clean golden

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
FUZZ_TIME ?= 30s
FUZZ_TARGETS = ./mbcap:FuzzReader ./mbcap:FuzzRoundTrip ./jsonl:FuzzReader ./mbcsv:FuzzReader

all: check build ## Run checks and build bin/mbcapctl

check: fmt fmt-check lint lint-ci vet vuln test ## Format, lint, vet, vuln, and test

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
