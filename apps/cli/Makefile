# oa — the Openarity CLI.
#
#   make          list targets
#   make check    everything CI runs

GO           ?= go
BINARY       := oa
SPEC         := ../brain/api/openapi.yaml
GENERATED    := internal/client/client.gen.go
COVER_PROFILE := coverage.out
COVER_HTML    := coverage.html
COVER_MIN     := 70

# Two exclusions, for the same reason: neither is this module's own code.
# The generated client is ~2000 lines nobody wrote — counting it measures
# oapi-codegen, and drags the total from 84% to 35%. internal/clitest is the
# test harness; it is well covered, so counting it flatters the number rather
# than testing anything.
COVER_PKGS = $(shell $(GO) list ./... | grep -v -e '/internal/client$$' -e '/internal/clitest$$' | paste -sd, -)


# Tool versions. CI runs `make tools`, so these are the only place a version
# is written down.
GOLANGCI_VERSION     := v2.12.2
OAPI_CODEGEN_VERSION := v2.8.0
GOVULNCHECK_VERSION  := latest

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## tools: install the tooling `make check` needs — CI runs this too
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
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

## cover: run tests with coverage and fail below the minimum
cover:
	$(GO) test -race -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=$(COVER_PROFILE) ./...
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
	$(GO) build -o bin/$(BINARY) ./cmd/oa

## install: put oa on your PATH, at $(shell go env GOPATH)/bin
install:
	$(GO) install ./cmd/oa

## generate: regenerate the API client from the brain's spec
#
# The spec is the only thing that crosses the app boundary — it is a contract,
# not code — so this reads it by path rather than importing anything from
# apps/brain. A change to an endpoint becomes a compile error here.
generate:
	oapi-codegen -config oapi-codegen.yaml $(SPEC)

## generate-check: fail if the committed client is out of date
#
# Compares against the index rather than HEAD so newly generated files that
# are staged but not yet committed pass. This is what stops the CLI drifting
# from the service: the spec is regenerated in CI and the tree must be clean.
generate-check: generate
	@if ! git diff --quiet -- $(GENERATED) || \
	    [ -n "$$(git ls-files --others --exclude-standard -- $(GENERATED))" ]; then \
	  echo "$(GENERATED) is out of date — run 'make generate' and commit the result"; \
	  git --no-pager diff -- $(GENERATED); \
	  exit 1; \
	fi

## check: everything CI runs
check: tidy-check generate-check fmt-check vet lint build cover vuln

## clean: remove build artefacts
clean:
	rm -rf bin $(COVER_PROFILE) $(COVER_HTML)
	$(GO) clean -testcache

.PHONY: help tools tidy tidy-check fmt fmt-check vet lint test cover vuln build install generate generate-check check clean
