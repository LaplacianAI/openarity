#!/usr/bin/env bash
#
# Compile the first Go block of each file below as a whole program, against the
# working tree rather than the published module.
#
# The pages claim the snippet is what runs. Nothing else checks that: a rename
# in sdk/agent breaks every page quoting the old name, and Hugo will happily
# build and publish all of them.
set -euo pipefail

root=$(git rev-parse --show-toplevel)

files=(
  "$root/sdk/agent/README.md"
  "$root/site/content/docs/agent-sdk/install.md"
)

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# GOWORK=off because the temporary module is outside the repository and so is
# not in go.work; leaving the workspace on makes go refuse to build it at all.
export GOWORK=off

for file in "${files[@]}"; do
  work="$tmp/$(basename "$(dirname "$file")")"
  mkdir -p "$work"

  awk '/^```go$/ { inside = 1; next } /^```$/ { if (inside) exit } inside' \
    "$file" > "$work/main.go"

  if ! head -1 "$work/main.go" | grep -q '^package main$'; then
    echo "$file: the first Go block is not a whole program" >&2
    exit 1
  fi

  (
    cd "$work"
    go mod init example.com/snippet > /dev/null 2>&1
    go mod edit -replace "github.com/LaplacianAI/openarity/sdk/agent=$root/sdk/agent"
    go mod tidy > /dev/null 2>&1
    go build ./...
  )

  echo "ok  ${file#"$root"/}"
done
