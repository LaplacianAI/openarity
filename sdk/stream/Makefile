# sdk/stream — build and code quality targets.
#
#   make          list targets
#   make check    everything CI runs

GO            ?= go
COVER_PROFILE := coverage.out
COVER_HTML    := coverage.html

# As sdk/agent: 95, so one uncovered line is a conversation rather than a
# reason to write a test that asserts nothing.
COVER_MIN     := 95

# Tool versions, matching sdk/agent and apps/brain.
GOLANGCI_VERSION    := v2.15.0
GOVULNCHECK_VERSION := latest

COVER_PKGS = $(shell $(GO) list ./... | grep -v '/examples' | paste -sd, -)

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## tools: install the tooling `make check` needs — CI runs this too
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@echo "installed with $$($(GO) version)"

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

## boundary: fail if the stream package links anything outside the standard library
#
# A server, a CLI and a browser bridge all read this package; none of them
# should get the agent SDK, or anything else, by importing it. agentstream is
# the one place sdk/agent is linked, and it is a package of its own for that.
boundary:
	@extra=$$($(GO) list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' . | grep -v '^github.com/LaplacianAI/openarity/sdk/stream$$' || true); \
	  if [ -n "$$extra" ]; then echo "the stream package links $$extra; only the standard library is allowed"; exit 1; fi
	@echo "the stream package links only the standard library"

## cover: run tests and fail below the coverage threshold
cover:
	$(GO) test -race -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=$(COVER_PROFILE) ./...
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

## example: run every example
example:
	@for dir in $$($(GO) list ./examples/...); do \
	  echo "=== $$dir ==="; \
	  $(GO) run $$dir || exit 1; \
	  echo; \
	done

## check: everything CI runs
check: tidy-check fmt-check vet lint boundary build cover example vuln

.PHONY: help tools tidy tidy-check fmt fmt-check vet lint test boundary cover cover-html vuln build example check
