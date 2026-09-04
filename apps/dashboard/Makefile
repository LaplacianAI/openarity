# apps/dashboard — the web dashboard the brain embeds and serves at /ui.
#
#   make check     everything CI runs
#   make dev       hot reload against a brain on 127.0.0.1:21120
#
# The build output is copied into the brain by `make ui` over there, because
# //go:embed cannot reach outside the directory of the package that embeds it.
# Nothing in this file knows where that is.

NPM := npm

# npm ci, never npm install: it installs exactly the lockfile and fails if
# package.json and the lockfile disagree, which is the difference between a
# reproducible build and one that drifted on somebody's laptop.
INSTALL := $(NPM) ci --no-audit --no-fund

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## deps: install exactly what the lockfile pins — CI runs this too
deps:
	$(INSTALL)

## dev: serve with hot reload, proxying the API to a local brain
dev:
	$(NPM) run dev

## fmt: apply formatting and the fixable lint rules
fmt:
	npx biome check --write .

## fmt-check: fail if anything is unformatted or a fixable rule is broken
fmt-check:
	npx biome check .

## lint: the lint rules only, without touching formatting
lint:
	npx biome lint .

## typecheck: tsc with no emit — the build runs this too, this target is for CI
typecheck:
	npx tsc --noEmit

## test: run the tests once
test:
	npx vitest run

## test-watch: run the tests and stay running
test-watch:
	npx vitest

## cover: run the tests with coverage
cover:
	npx vitest run --coverage

## build: type-check and emit the static bundle into dist/
build:
	$(NPM) run build

## check: everything CI runs, in the order that fails fastest
#
# CI calls these five targets as five named steps rather than calling this
# one, so that a failure names the check in the step list instead of only
# inside the log. That means the list here and the steps in
# .github/workflows/ci.yml have to be kept in step by hand — adding a target
# to this line without adding a step there leaves it running locally and
# nowhere else.
check: fmt-check lint typecheck test build

## clean: remove the build output and the installed dependencies
clean:
	rm -rf dist node_modules

.PHONY: help deps dev fmt fmt-check lint typecheck test test-watch cover build check clean
