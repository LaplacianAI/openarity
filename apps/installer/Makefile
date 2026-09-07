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

## binaries: put the three the bundle ships into place
binaries:
	@mkdir -p src-tauri/binaries
	@test -n "$(OA)" || { echo "OA=/path/to/oa is required"; exit 1; }
	@test -n "$(BRAIN)" || { echo "BRAIN=/path/to/brain is required"; exit 1; }
	@test -n "$(DEX)" || { echo "DEX=/path/to/dex is required"; exit 1; }
	cp "$(OA)" "src-tauri/binaries/oa-$(TRIPLE)"
	cp "$(BRAIN)" "src-tauri/binaries/brain-$(TRIPLE)"
	cp "$(DEX)" "src-tauri/binaries/dex-$(TRIPLE)"
	@echo "staged oa, brain and dex for $(TRIPLE)"

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
	@test -f "$(SIDECAR)" || { echo "no sidecars for $(TRIPLE) — run make binaries OA=... BRAIN=... DEX=..."; exit 1; }
	npx tauri build

## dev: the window, against a live frontend
dev:
	npx tauri dev

## check: everything CI runs
check: fmt-check test build

.PHONY: help deps icons binaries fmt-check test build bundle dev check
