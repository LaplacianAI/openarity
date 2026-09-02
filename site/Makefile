HUGO ?= hugo

.DEFAULT_GOAL := help

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## serve: the site at http://localhost:1313, rebuilding as you write
serve:
	$(HUGO) server --buildDrafts --navigateToChanged

## build: render into public/
#
# GOWORK=off because Hugo fetches its theme as a Go module, and this directory
# is deliberately not in the repository's go.work — a documentation theme has
# no business in the workspace that builds the services.
build:
	GOWORK=off $(HUGO) --gc --minify

## snippets: compile the Go programs quoted in the docs
snippets:
	./scripts/check-snippets.sh

## upgrade: take the latest theme
upgrade:
	GOWORK=off $(HUGO) mod get -u ./...
	GOWORK=off $(HUGO) mod tidy

## clean: remove build output
clean:
	rm -rf public resources .hugo_build.lock

## check: everything CI runs
check: snippets build
