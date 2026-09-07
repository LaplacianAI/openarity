TRIPLE  := $(shell rustc -Vv | awk '/^host:/ {print $$2}')
SIDECAR := src-tauri/binaries/oa-$(TRIPLE)

.DEFAULT_GOAL := help

## help: list targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## deps: install the frontend dependencies
deps:
	npm ci

## icons: regenerate the icon set from assets/logo.svg
icons:
	@test -f src-tauri/icons/icon.png || { \
	  echo "src-tauri/icons/icon.png is missing — render assets/logo.svg to a 1024px PNG first"; \
	  exit 1; \
	}
	npx tauri icon src-tauri/icons/icon.png

## binaries: put the oa the bundle ships into place
binaries:
	@mkdir -p src-tauri/binaries
	@test -n "$(OA)" || { echo "OA=/path/to/oa is required"; exit 1; }
	cp "$(OA)" "$(SIDECAR)"
	@echo "staged $(SIDECAR)"

## fmt-check: fail if anything is unformatted
fmt-check:
	npx tsc --noEmit
	cd src-tauri && cargo fmt --check

## test: the reducer, which is the whole flow without a window
test:
	npx vitest run

## build: the frontend and the Rust binary
build:
	npx vite build
	cd src-tauri && cargo build --release

## bundle: the installable artifact — .dmg, .msi or .deb
bundle:
	@test -f "$(SIDECAR)" || { echo "no sidecar at $(SIDECAR) — run make binaries OA=..."; exit 1; }
	npx tauri build

## dev: the window, against a live frontend
dev:
	npx tauri dev

## check: everything CI runs
check: fmt-check test build

.PHONY: help deps icons binaries fmt-check test build bundle dev check
