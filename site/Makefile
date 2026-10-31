# Zensical, pinned exactly: a 0.0.x release can change its configuration
# between versions. uvx fetches it, so nothing is installed into the system.
ZENSICAL ?= uvx --from zensical==0.0.74 zensical

.DEFAULT_GOAL := help

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## serve: the site at http://localhost:8000, rebuilding as you write
serve:
	$(ZENSICAL) serve

## build: render into public/; --strict fails on a link to a page that is not there
build:
	$(ZENSICAL) build --clean --strict

## snippets: compile the Go programs quoted in the docs
snippets:
	./scripts/check-snippets.sh

## clean: remove build output
clean:
	rm -rf public .cache

## check: everything CI runs
check: snippets build
