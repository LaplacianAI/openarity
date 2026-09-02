#!/usr/bin/env bash
#
# Compile every fenced Go block that is a whole program, in each file below,
# against the working tree rather than the published module.
#
# The pages claim their snippets are what runs. Nothing else checks that: a
# rename in sdk/agent breaks every page quoting the old name, and Hugo will
# happily build and publish all of them.
#
# Blocks that do not start with `package main` are excerpts — an interface, a
# struct, a few lines of a larger function — and are skipped rather than
# failed, because there is no honest way to compile a fragment.
set -euo pipefail

root=$(git rev-parse --show-toplevel)

files=(
  "$root/sdk/agent/README.md"
  "$root/site/content/docs/agent-sdk/install.md"
  "$root/site/content/docs/agent-sdk/custom-patterns.md"
)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# GOWORK=off because the temporary module is outside the repository and so is
# not in go.work; leaving the workspace on makes go refuse to build it at all.
export GOWORK=off

compiled=0

for file in "${files[@]}"; do
  blocks="$tmp/blocks-$compiled"
  mkdir -p "$blocks"

  # Every ```go block to its own file. Splitting first and filtering second
  # keeps the numbering stable, so a failure names the block a reader can count
  # to on the page.
  awk -v out="$blocks" '
    /^```go$/ { n++; inside = 1; next }
    /^```$/   { inside = 0; next }
    inside    { print > (out "/" n ".go") }
  ' "$file"

  for block in "$blocks"/*.go; do
    [ -e "$block" ] || continue
    head -1 "$block" | grep -q '^package main$' || continue

    work="$tmp/work-$compiled"
    mkdir -p "$work"
    cp "$block" "$work/main.go"

    (
      cd "$work"
      go mod init example.com/snippet > /dev/null 2>&1
      go mod edit -replace "github.com/LaplacianAI/openarity/sdk/agent=$root/sdk/agent"
      go mod tidy > /dev/null 2>&1
      go build ./...
    ) || {
      echo "FAIL ${file#"$root"/} block $(basename "$block" .go)" >&2
      exit 1
    }

    echo "ok  ${file#"$root"/} block $(basename "$block" .go)"
    compiled=$((compiled + 1))
  done
done

if [ "$compiled" -eq 0 ]; then
  echo "no whole programs were found; the extraction is broken" >&2
  exit 1
fi
