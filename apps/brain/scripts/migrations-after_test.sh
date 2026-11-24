#!/usr/bin/env bash
# The cases migrations-after.sh must get right, each on a scratch repository:
# a newer migration passes; a lower one, an equal one and a bad base fail; a
# base with no migrations fails; a file that is not a migration is ignored.
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/migrations-after.sh"
dir=apps/brain/internal/store/migrations
failed=0

repo() {
  local r
  r=$(mktemp -d)
  git -C "$r" init -q
  git -C "$r" config user.name test
  git -C "$r" config user.email test@example.test
  mkdir -p "$r/$dir"
  echo "$r"
}

commit() { git -C "$1" add -A && git -C "$1" commit -qm "$2" --allow-empty; }

expect() {
  local want=$1 name=$2 r=$3 base=$4 got=0
  (cd "$r" && "$script" "$base") >/dev/null 2>&1 || got=$?
  if [[ $got != "$want" ]]; then
    echo "FAIL $name: exit $got, want $want" >&2
    failed=1
  else
    echo "ok   $name"
  fi
}

# A base at 20261207090000, and a branch off it.
based() {
  local r
  r=$(repo)
  touch "$r/$dir/20261205090000_a.sql" "$r/$dir/20261207090000_b.sql"
  commit "$r" base
  git -C "$r" branch base
  echo "$r"
}

r=$(based); touch "$r/$dir/20261208090000_new.sql"; commit "$r" new
expect 0 "a migration after the newest passes" "$r" base

r=$(based); touch "$r/$dir/20261206090000_old.sql"; commit "$r" old
expect 1 "a migration before the newest fails" "$r" base

r=$(based); touch "$r/$dir/20261207090000_same.sql"; commit "$r" same
expect 1 "a migration at the newest's number fails" "$r" base

r=$(based); git -C "$r" mv "$dir/20261205090000_a.sql" "$dir/20261206090000_a.sql"; commit "$r" mv
expect 1 "a migration renumbered below the newest fails" "$r" base

r=$(based); touch "$r/$dir/README.md" "$r/$dir/99999999999999_x.go"; commit "$r" other
expect 0 "a file that is not a migration is ignored" "$r" base

r=$(based)
expect 128 "an unreadable base fails" "$r" deadbeef

r=$(repo); touch "$r/$dir/README.md"; commit "$r" empty; git -C "$r" branch base
touch "$r/$dir/20261208090000_new.sql"; commit "$r" new
expect 2 "a base with no migrations fails" "$r" base

exit "$failed"
