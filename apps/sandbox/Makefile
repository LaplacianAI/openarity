# sandbox — the optional self-hosted sandbox provider, and the shim every
# sandbox runs a command MCP server behind.
#
#   make          list targets
#   make check    everything CI runs

GO            ?= go
BINARY        := sandbox
COVER_PROFILE := coverage.out
COVER_HTML    := coverage.html
COVER_MIN     := 80

# Tool versions. CI runs `make tools`, so these are the only place a version
# is written down. The same as apps/brain and apps/cli.
GOLANGCI_VERSION    := v2.15.0
GOVULNCHECK_VERSION := latest

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## tools: install the tooling `make check` needs — CI runs this too
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@echo "installed with $$($(GO) version)"

## tidy-check: fail if go.mod or go.sum is out of date
tidy-check:
	$(GO) mod tidy -diff

## fmt: apply gofumpt and fix import order
fmt:
	golangci-lint fmt

## fmt-check: fail if anything is unformatted or imports are out of order
fmt-check:
	golangci-lint fmt --diff

## vet: go vet
vet:
	$(GO) vet ./...

## lint: run all linters
lint:
	golangci-lint run

## test: run tests with the race detector
test:
	$(GO) test -race ./...

## cover: run tests with coverage and fail below the minimum
cover:
	$(GO) test -race -covermode=atomic -coverpkg=./... -coverprofile=$(COVER_PROFILE) ./...
	@$(GO) tool cover -func=$(COVER_PROFILE) | tail -1
	@total=$$($(GO) tool cover -func=$(COVER_PROFILE) | tail -1 | grep -oE '[0-9]+\.[0-9]+'); \
	  awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { \
	    if (t+0 < m+0) { printf "coverage %.1f%% is below the %d%% minimum\n", t, m; exit 1 } \
	  }'

## vuln: check dependencies against the Go vulnerability database
vuln:
	govulncheck ./...

## build: compile the binary into ./bin
build:
	$(GO) build -o bin/$(BINARY) ./cmd/sandbox

## check: everything CI runs
check: tidy-check fmt-check vet lint build cover vuln

## clean: remove build artefacts
clean:
	rm -rf bin $(COVER_PROFILE) $(COVER_HTML)
	$(GO) clean -testcache

.PHONY: help tools tidy-check fmt fmt-check vet lint test cover vuln build check clean
