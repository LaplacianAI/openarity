# brain — build and code quality targets.
#
#   make          list targets
#   make check    everything CI runs

GO              ?= go
COVER_PROFILE   := coverage.out
COVER_HTML      := coverage.html
COVER_MIN       := 70

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## tools: install/refresh developer tooling — rerun after a Go upgrade
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(GO) install golang.org/x/vuln/cmd/govulncheck@latest
	$(GO) install golang.org/x/tools/gopls@latest
	@echo "installed with $$($(GO) version)"
	@echo "restart your editor's language server to pick up the new gopls"

## tidy: sync go.mod and go.sum
tidy:
	$(GO) mod tidy

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

## cover: run tests and fail below the coverage threshold
cover:
	$(GO) test -race -covermode=atomic -coverprofile=$(COVER_PROFILE) ./...
	@$(GO) tool cover -func=$(COVER_PROFILE) | tail -1
	@total=$$($(GO) tool cover -func=$(COVER_PROFILE) | tail -1 | grep -oE '[0-9]+\.[0-9]+'); \
	  awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { \
	    if (t+0 < m+0) { printf "coverage %.1f%% is below the %d%% minimum\n", t, m; exit 1 } \
	    else { printf "coverage %.1f%% meets the %d%% minimum\n", t, m } }'

## cover-html: write and open an HTML coverage report
cover-html: cover
	$(GO) tool cover -html=$(COVER_PROFILE) -o $(COVER_HTML)
	@echo "wrote $(COVER_HTML)"

## vuln: check dependencies for known vulnerabilities
vuln:
	govulncheck ./...

## build: compile everything
build:
	$(GO) build ./...

## check: everything CI runs
check: tidy-check fmt-check vet lint build test vuln

## clean: remove build and coverage artefacts
clean:
	rm -f $(COVER_PROFILE) $(COVER_HTML)
	$(GO) clean -testcache

.PHONY: help tools tidy tidy-check fmt fmt-check vet lint test cover cover-html vuln build check clean
