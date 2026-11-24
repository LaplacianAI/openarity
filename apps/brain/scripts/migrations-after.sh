#!/usr/bin/env bash
# Fails when a migration a branch adds is numbered at or before the newest one
# on the base it merges into.
#
# goose refuses to apply a migration numbered below one a database has already
# applied. Two branches written side by side can each add one, and whichever
# merges second may carry the lower number: it passes every test, then every
# deploy to a database the first one reached is refused. Renumbering before
# the merge is the fix, and this says so while that is still cheap.
#
# It fails closed: an unreadable base, or a base with no migrations, is an
# error rather than a pass.
#
#   apps/brain/scripts/migrations-after.sh <base-ref>
#
# CI passes HEAD^1, the base side of the pull request's merge commit, so the
# comparison is with the base as the merge would find it.
set -euo pipefail
export LC_ALL=C

base=${1:?usage: migrations-after.sh <base-ref>}
dir=apps/brain/internal/store/migrations
cd "$(git rev-parse --show-toplevel)"

# goose's files only, so a README or an embed.go can neither set nor fail it.
pat='^[0-9]{14}_.*\.sql$'

version() { basename "$1" | cut -d_ -f1; }

# Read into variables first: set -e does not see a failure inside a process
# substitution, and a bad ref must fail here, not pass.
base_files=$(git ls-tree --name-only "$base" -- "$dir/")
added_files=$(git diff --no-renames --name-only --diff-filter=A "$base" HEAD -- "$dir")

newest=""
while read -r f; do
  [[ $(basename "${f:-}") =~ $pat ]] || continue
  v=$(version "$f")
  if [[ $v > $newest ]]; then newest=$v; fi
done <<<"$base_files"

if [[ -z $newest ]]; then
  echo "no migrations in $dir on $base: refusing to pass." >&2
  exit 2
fi

bad=0
# --no-renames: a migration moved to another number is a new one to check.
while read -r f; do
  [[ $(basename "${f:-}") =~ $pat ]] || continue
  v=$(version "$f")
  if [[ ! $v > $newest ]]; then
    echo "$f is numbered $v, at or before $newest, the newest migration on $base." >&2
    echo "  A database that already applied $newest refuses it. Renumber it after $newest." >&2
    bad=1
  fi
done <<<"$added_files"

exit "$bad"
