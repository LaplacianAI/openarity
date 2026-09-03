# sdk/agent — build and code quality targets.
#
#   make          list targets
#   make check    everything CI runs

GO            ?= go
COVER_PROFILE := coverage.out
COVER_HTML    := coverage.html

# 95 rather than 100. Every package is at 100 today and should stay there, but
# a threshold that fails the build over one uncovered line creates pressure to
# write a test that exercises code without asserting anything — which is worse
# than the line. The number to watch is the one `make cover` prints.
COVER_MIN     := 95

# Tool versions, matching apps/brain. CI runs `make tools`, so these are the
# only place a version is written down.
GOLANGCI_VERSION    := v2.12.2
GOVULNCHECK_VERSION := latest

# examples is demonstration code, not library code. Counting it measures how
# thoroughly the demos are tested, which is not a number anyone acts on — the
# same reasoning that excludes the generated client in apps/cli. The pattern
# has no anchor because the examples are now a tree: examples/gateway is a
# package too, and an anchored /examples$ would have counted it silently.
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

## boundary: fail if the core packages link a third-party dependency
#
# The whole reason sdk/agent is a separate module is that a pattern must not be
# able to reach a store, a grant or a provider SDK. Three of those the compiler
# enforces on its own — this module cannot import apps/brain/internal at all.
# The fourth it does not: nothing stops somebody importing openai-go into the
# pattern for one convenient type, and after that ModelClient is decorative.
#
# go list -deps is the real linked set, not the module graph. openai-go pulls
# the AWS and Azure SDKs into go.mod and links none of them, so counting
# requirements would fail on a dependency that costs nothing.
# Each entry is a package substring and the sentence to print when the core
# links it. Adding an adapter under models/ or tools/ means adding a line here,
# because the point of those directories is that the core cannot see into them.
FORBIDDEN := \
	openai/openai-go:the@model@client@belongs@behind@ModelClient \
	modelcontextprotocol/go-sdk:MCP@is@a@source@of@tools,@not@something@a@pattern@speaks

boundary:
	@for pkg in . ./patterns; do \
	  for rule in $(FORBIDDEN); do \
	    lib=$${rule%%:*}; why=$$(echo "$${rule#*:}" | tr '@' ' '); \
	    n=$$($(GO) list -deps $$pkg | grep -c "$$lib" || true); \
	    if [ "$$n" != "0" ]; then \
	      echo "$$pkg links $$n $$lib packages; $$why"; \
	      exit 1; \
	    fi; \
	  done; \
	done
	@echo "the core packages link no provider or protocol SDK"

## cover: run tests and fail below the coverage threshold
#
# -coverpkg so a package exercised from another package's tests is credited —
# without it the example in package agent_test, which is the only thing driving
# all three packages together, counts for nothing.
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

## example: run every example against a stub gateway, or a real one via the environment
#
#   OPENARITY_MODELS_BASE_URL=http://127.0.0.1:14000/v1 \
#   OPENARITY_MODELS_API_KEY=sk-… make example
#
# Discovered rather than listed, so a new example under examples/ is run by CI
# the moment it exists rather than the moment somebody remembers this file.
example:
	@for dir in $$($(GO) list ./examples/... | grep -v '/gateway$$'); do \
	  echo "=== $$dir ==="; \
	  $(GO) run $$dir || exit 1; \
	  echo; \
	done

## check: everything CI runs
check: tidy-check fmt-check vet lint boundary build cover vuln

# Every target here is a command, not a file. Without this, a target whose name
# matches something on disk is considered up to date and silently skipped —
# which is exactly what `tools/` did to `make tools`, leaving CI to run a
# linter it had never installed.
.PHONY: help tools tidy tidy-check fmt fmt-check vet lint test boundary cover cover-html vuln build example check
