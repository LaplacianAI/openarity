#!/usr/bin/env bash
#
# The only thing CI may run on the staging server.
#
# It is installed as an SSH forced command (see README.md), so the key CI
# holds cannot open a shell, forward a port or run anything but this file.
# What CI asked for arrives in SSH_ORIGINAL_COMMAND as plain words; nothing
# here passes them to a shell, and each is checked against a pattern before
# it is used.
#
#   deploy main  sha-<40 hex>  sha256:<64 hex>    the newest main; takes the
#                                                 slot only if main holds it
#   deploy pr-<n> pr-<n>       sha256:<64 hex>    a labelled pull request;
#                                                 takes the slot
#   release <n>                                   the pull request closed: its
#                                                 database goes, and the slot
#                                                 returns to main if it held it
#   status                                        what holds the slot
#
# Staging is one slot because the server is small: authentik, OpenBao,
# SeaweedFS, Postgres and FalkorDB run once, and only migrate, brain and
# worker are replaced. Each pull request gets a database of its own on the
# one Postgres, so migrations from two branches never meet, and a pull
# request's data survives being swapped out and back in.
#
# The image is always pinned by digest. A tag can be moved after CI read it;
# a digest cannot.
#
# It can also be run by hand on the server, with the same words as arguments.
#
# Over SSH, the caller is a CI job, and a public repository publishes its
# log to anyone. So what compose, curl and the brain print - addresses,
# container names, migration output - goes to deploy.log beside the state,
# and the caller is told only the outcome. By hand, everything is shown.

set -euo pipefail

REPO_DIR=${OPENARITY_STAGING_REPO:-$HOME/openarity}
STATE_DIR=${OPENARITY_STAGING_STATE:-$HOME/.openarity-staging}
DEPLOYMENT=$REPO_DIR/deployment

# Until the output is redirected, 3 and 4 are simply stdout and stderr.
exec 3>&1 4>&2
quiet=0

say() { printf '%s\n' "$*"; }
# The outcome, which the caller sees as well as the log.
tell() {
	say "$*"
	if [ "$quiet" = 1 ]; then say "$*" >&3; fi
}
refuse() {
	say "refused: $*" >&2
	if [ "$quiet" = 1 ]; then say "refused: $*; deploy.log on the server has the output" >&4; fi
	exit 2
}
# Anything set -e stops on, which refuse did not already explain.
on_exit() {
	local code=$?
	if [ "$quiet" = 1 ] && [ "$code" -ne 0 ] && [ "$code" -ne 2 ]; then
		say "failed with exit $code; deploy.log on the server has the output" >&4
	fi
}
trap on_exit EXIT

if [ -n "${SSH_ORIGINAL_COMMAND:-}" ]; then
	read -r -a words <<<"$SSH_ORIGINAL_COMMAND"
else
	words=("$@")
fi
[ "${#words[@]}" -ge 1 ] || refuse "no command"

# The files `make staging` composes, in the same order, so a deploy runs the
# stack an operator would get by hand.
compose() {
	docker compose --project-directory "$DEPLOYMENT" \
		-f "$DEPLOYMENT/docker-compose.yml" \
		-f "$DEPLOYMENT/docker-compose.authentik.yml" \
		-f "$DEPLOYMENT/docker-compose.image.yml" \
		-f "$DEPLOYMENT/docker-compose.openbao.yml" \
		-f "$DEPLOYMENT/docker-compose.objects.yml" \
		--profile brain "$@"
}

psql_postgres() {
	compose exec -T postgres psql -U postgres -d postgres -v ON_ERROR_STOP=1 -tAc "$1"
}

database_for() {
	case "$1" in
	main) say openarity ;;
	pr-*) say "openarity_pr_${1#pr-}" ;;
	esac
}

ensure_database() {
	if [ "$(psql_postgres "SELECT 1 FROM pg_database WHERE datname = '$1'")" != 1 ]; then
		psql_postgres "CREATE DATABASE $1" >/dev/null
		say "created database $1"
	fi
}

# The API's address as compose published it, so this works whatever BIND_ADDR
# and API_PORT the server's .env chose.
wait_healthy() {
	local address
	address=$(compose port brain 21120)
	address=${address/0.0.0.0/127.0.0.1}
	for _ in $(seq 1 60); do
		if curl -fsS -o /dev/null "http://$address/healthz"; then
			return 0
		fi
		sleep 2
	done
	compose logs --tail 40 brain >&2
	refuse "the brain did not answer /healthz within two minutes"
}

# Migrate this database with this image, then replace brain and worker.
#
# --no-deps on both, because nothing else is to be touched: recreating
# Postgres, authentik or OpenBao for a deploy would drop every session and
# make a brain swap a restart of the whole server. A failed migration stops
# here, with the old brain still serving.
switch() {
	local slot=$1 tag=$2 digest=$3 database
	database=$(database_for "$slot")
	ensure_database "$database"

	export BRAIN_TAG="$tag@$digest" OPENARITY_DATABASE="$database"
	compose pull migrate brain worker
	compose run --rm --no-deps migrate || refuse "the migration of $database failed; the brain was not replaced"
	compose up -d --no-deps --force-recreate brain worker
	wait_healthy

	printf '%s %s %s\n' "$slot" "$tag" "$digest" >"$STATE_DIR/current"
	docker image prune -f >/dev/null
	tell "deployed $slot $digest on database $database"
}

current_slot() {
	if [ -f "$STATE_DIR/current" ]; then
		cut -d' ' -f1 "$STATE_DIR/current"
	fi
}

mkdir -p "$STATE_DIR"
# One change at a time: a merge to main and a label can land together.
exec 9>"$STATE_DIR/lock"
flock -w 900 9 || refuse "another deploy held the lock for fifteen minutes"

if [ -n "${SSH_ORIGINAL_COMMAND:-}" ]; then
	log=$STATE_DIR/deploy.log
	if [ -f "$log" ] && [ "$(wc -c <"$log")" -gt 1048576 ]; then
		mv "$log" "$log.1"
	fi
	exec >>"$log" 2>&1
	quiet=1
	say "== $(date -u +%FT%TZ) $SSH_ORIGINAL_COMMAND"
fi

case "${words[0]}" in
deploy)
	[ "${#words[@]}" -eq 4 ] || refuse "deploy takes a slot, a tag and a digest"
	slot=${words[1]} tag=${words[2]} digest=${words[3]}
	[[ $digest =~ ^sha256:[0-9a-f]{64}$ ]] || refuse "the digest is not sha256:<64 hex>"
	case "$slot" in
	main)
		[[ $tag =~ ^sha-[0-9a-f]{40}$ ]] || refuse "main deploys a sha-<commit> tag"
		printf '%s %s\n' "$tag" "$digest" >"$STATE_DIR/main"
		held=$(current_slot)
		if [ -n "$held" ] && [ "$held" != main ]; then
			tell "recorded main $digest; $held holds the slot and keeps it until it is released"
			exit 0
		fi
		switch main "$tag" "$digest"
		;;
	pr-*)
		[[ $slot =~ ^pr-[0-9]{1,7}$ ]] || refuse "a pull request slot is pr-<number>"
		[ "$tag" = "$slot" ] || refuse "a pull request deploys its own pr-<number> tag"
		switch "$slot" "$tag" "$digest"
		;;
	*) refuse "the slot is main or pr-<number>" ;;
	esac
	;;
release)
	[ "${#words[@]}" -eq 2 ] && [[ ${words[1]} =~ ^[0-9]{1,7}$ ]] || refuse "release takes a pull request number"
	slot=pr-${words[1]}
	if [ "$(current_slot)" = "$slot" ]; then
		if [ -f "$STATE_DIR/main" ]; then
			read -r main_tag main_digest <"$STATE_DIR/main"
			switch main "$main_tag" "$main_digest"
		else
			compose stop brain worker
			rm -f "$STATE_DIR/current"
			tell "stopped $slot; no main deploy is recorded to return to"
		fi
	fi
	database=$(database_for "$slot")
	psql_postgres "DROP DATABASE IF EXISTS $database WITH (FORCE)" >/dev/null
	tell "released $slot and dropped $database"
	;;
status)
	if [ -f "$STATE_DIR/current" ]; then
		tell "holding the slot: $(cat "$STATE_DIR/current")"
	else
		tell "nothing deployed"
	fi
	if [ -f "$STATE_DIR/main" ]; then
		tell "newest main: $(cat "$STATE_DIR/main")"
	fi
	;;
*) refuse "unknown command ${words[0]}" ;;
esac
