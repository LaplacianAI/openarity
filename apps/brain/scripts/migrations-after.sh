#!/usr/bin/env bash
# Fails when a migration this branch adds is numbered at or before the newest
# one on the base it merges into.
#
# goose refuses to apply a migration numbered below one a database has already
# applied. Two branches written side by side can each add one, and whichever
# merges second may carry the lower number: it passes CI, then every deploy to
# a database the first one reached fails. That is how #295 blocked staging
# after #294. Renumbering the newer branch's migration before it merges is the
# fix, and this says so while that is still cheap.
#
#   scripts/migrations-after.sh <base-ref>     e.g. origin/main
set -euo pipefail

base=${1:?usage: migrations-after.sh <base-ref>}
dir=apps/brain/internal/store/migrations
cd "$(git rev-parse --show-toplevel)"

version() { basename "$1" | cut -d_ -f1; }

newest=""
while read -r f; do
  v=$(version "$f")
  [[ $v > $newest ]] && newest=$v
done < <(git ls-tree --name-only "$base" -- "$dir/")

bad=0
# --no-renames: a migration moved to another number is a new one to check.
while read -r f; do
  [[ -z $f ]] && continue
  v=$(version "$f")
  if [[ ! $v > $newest ]]; then
    echo "$f is numbered $v, at or before $newest, the newest migration on $base." >&2
    echo "  A database that already applied $newest refuses it. Renumber it after $newest." >&2
    bad=1
  fi
done < <(git diff --no-renames --name-only --diff-filter=A "$base" HEAD -- "$dir")

exit "$bad"
