# brain — build and code quality targets.
#
#   make          list targets
#   make check    everything CI runs

GO              ?= go
COVER_PROFILE   := coverage.out
COVER_HTML      := coverage.html
COVER_MIN       := 70
MIGRATIONS_DIR  := internal/store/migrations
GENERATED_DIR   := internal/store/db

# Tool versions. CI runs `make tools`, so these are the only place a version
# is written down — pinning one here pins it everywhere. gopls is the
# exception: it is an editor tool, nothing verifies against it, and a stale
# one is worse than a floating one.
GOLANGCI_VERSION    := v2.12.2
GOVULNCHECK_VERSION := latest
GOOSE_VERSION       := v3.27.3
SQLC_VERSION        := v1.31.1
GOPLS_VERSION       := latest

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## tools: install the tooling `make check` needs — CI runs this too
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
	$(GO) install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
	@echo "installed with $$($(GO) version)"

## tools-editor: install gopls — not needed by CI, rerun after a Go upgrade
tools-editor:
	$(GO) install golang.org/x/tools/gopls@$(GOPLS_VERSION)
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
#
# -coverpkg=./... so a package exercised from another package's tests is
# credited. Without it the generated sqlc code reports 0% while every one of
# its functions is being called, and the total is wrong by ten points. The
# per-package percentages printed become "of statements in ./..." and are no
# longer meaningful on their own; the total on the last line is the number.
cover:
	$(GO) test -race -covermode=atomic -coverpkg=./... -coverprofile=$(COVER_PROFILE) ./...
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

## run: run the server, Ctrl-C to shut it down gracefully
run:
	$(GO) run ./cmd/brain

## generate: run every code generator
#
# An umbrella, deliberately. Today sqlc is the only generator; the OpenAPI
# clients are coming, and when they do `make generate` keeps meaning "make the
# generated code match the source" without anyone having to learn a new target.
generate: generate-sql

## generate-sql: regenerate the sqlc query code from the migrations and queries
generate-sql:
	sqlc generate

## generate-check: fail if the generated code on disk is out of date
#
# Compares against the index rather than HEAD, so newly generated files that
# are staged but not yet committed pass. Against HEAD you could never get a
# green check before the first commit of a new query. CI checks out clean, so
# there anything sqlc rewrites shows up as an unstaged change either way.
generate-check: generate
	@if ! git diff --quiet -- $(GENERATED_DIR) || \
	    [ -n "$$(git ls-files --others --exclude-standard -- $(GENERATED_DIR))" ]; then \
	  echo "generated code is out of date — run 'make generate' and commit the result"; \
	  git --no-pager diff -- $(GENERATED_DIR); \
	  git ls-files --others --exclude-standard -- $(GENERATED_DIR); \
	  exit 1; \
	fi

## migration: scaffold a timestamped migration — make migration name=add_tenants
migration:
	@test -n "$(name)" || { echo "usage: make migration name=add_tenants"; exit 1; }
	@mkdir -p $(MIGRATIONS_DIR)
	goose -dir $(MIGRATIONS_DIR) create $(name) sql

## check: everything CI runs
check: tidy-check generate-check fmt-check vet lint build cover vuln

## clean: remove build and coverage artefacts
clean:
	rm -f $(COVER_PROFILE) $(COVER_HTML)
	$(GO) clean -testcache

.PHONY: help tools tools-editor tidy tidy-check fmt fmt-check vet lint test cover cover-html vuln build run generate generate-sql generate-check migration check clean
