# oa — the Openarity CLI.
#
#   make          list targets
#   make check    everything CI runs

GO           ?= go
BINARY       := oa
SPEC         := ../brain/api/openapi.yaml
GENERATED    := internal/client/client.gen.go

# Tool versions. CI runs `make tools`, so these are the only place a version
# is written down.
GOLANGCI_VERSION     := v2.12.2
OAPI_CODEGEN_VERSION := v2.8.0

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## tools: install the tooling `make check` needs — CI runs this too
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)
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
check: tidy-check generate-check fmt-check vet lint build test

## clean: remove build artefacts
clean:
	rm -rf bin
	$(GO) clean -testcache

.PHONY: help tools tidy tidy-check fmt fmt-check vet lint test build install generate generate-check check clean
