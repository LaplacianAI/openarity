#!/usr/bin/env bash
#
# One command, from a bare clone to a stack you can sign in to.
#
# Every other target here assumes the setup already happened: `make staging`
# stops on its first line without a .env, and the two OpenBao keys it needs
# are gitignored, so a clone has neither. That was fine while the only people
# running it had done the setup once by hand months ago, and wrong for
# everybody else — the README listed four targets flat, with no sign that
# three of them work from a clone and one needs an afternoon.
#
# So this generates what is missing and nothing that is already there. Run it
# twice and the second run changes nothing.
#
#   make start-docker                     # asks which identity provider and gateway
#   make start-docker PROVIDER=dex        # or does not ask
#   make start-docker GATEWAY=omniroute   # omniroute | litellm | existing | none
#   make start-docker BUILD=1             # build the brain from this tree
#
set -euo pipefail

cd "$(dirname "$0")"

COMPOSE_BASE="-f docker-compose.yml"
BAO_OVERLAY="-f docker-compose.openbao.yml"
OBJECTS_OVERLAY="-f docker-compose.objects.yml"

# The dev user pinned in dex/config.yaml, as dex derives it. Committed rather
# than read back after a login, because the userID it comes from is committed
# too — see the note beside it in .env.example.
DEX_SUBJECT="CiQwZDFlOWYzYy02YTUyLTRmNWQtOGI3MS0yYzRlNmE4ZDBmMTMSBWxvY2Fs"

bold=$(tput bold 2>/dev/null || true)
dim=$(tput dim 2>/dev/null || true)
plain=$(tput sgr0 2>/dev/null || true)

say()  { printf '%s\n' "$*"; }
step() { printf '\n%s%s%s\n' "$bold" "$*" "$plain"; }
note() { printf '%s  %s%s\n' "$dim" "$*" "$plain"; }
die()  { printf '\n%s\n' "$*" >&2; exit 1; }

need() {
	command -v "$1" >/dev/null 2>&1 ||
		die "$1 is not installed, and this needs it."
}

# ------------------------------------------------------------------ the file

# A key is "set" only when it has a value. .env.example ships several of these
# empty, which is the state that has to be filled rather than left alone.
env_value() {
	[ -f .env ] || return 1
	sed -n "s/^$1=//p" .env | tail -1
}

env_is_set() {
	local value
	value=$(env_value "$1" || true)
	[ -n "$value" ]
}

# Replaces the line if the key is there, appends if it is not. The value is
# written literally, so anything containing $ must arrive already quoted —
# see the bcrypt hash below.
env_set() {
	local key=$1 value=$2
	if grep -q "^$key=" .env; then
		# A temporary file rather than sed -i: the in-place flag takes an
		# argument on BSD sed and does not on GNU, and this runs on both.
		awk -v k="$key" -v v="$value" \
			'index($0, k "=") == 1 { print k "=" v; next } { print }' \
			.env > .env.tmp
		mv .env.tmp .env
	else
		printf '%s=%s\n' "$key" "$value" >> .env
	fi
}

secret() { openssl rand -hex 24; }

ensure_env_file() {
	if [ ! -f .env ]; then
		cp .env.example .env
		chmod 600 .env
		note "wrote deployment/.env from the example"
	fi

	# Generated for both providers even when only one is being started, so
	# switching later is a re-run rather than another round of setup.
	#
	# Hex rather than base64: compose reads .env literally for interpolation
	# and a value is easier to reason about when it cannot contain a $, a
	# quote or an equals sign.
	local key
	for key in AUTHENTIK_SECRET_KEY AUTHENTIK_PG_PASS \
		AUTHENTIK_BOOTSTRAP_TOKEN OBJECTS_SECRET_KEY \
		LITELLM_MASTER_KEY LITELLM_PG_PASS LITELLM_UI_PASSWORD OMNIROUTE_PASSWORD; do
		env_is_set "$key" || { env_set "$key" "$(secret)"; note "generated $key"; }
	done

	env_is_set AUTHENTIK_BOOTSTRAP_PASSWORD || {
		AUTHENTIK_PASSWORD=$(secret)
		env_set AUTHENTIK_BOOTSTRAP_PASSWORD "$AUTHENTIK_PASSWORD"
		note "generated AUTHENTIK_BOOTSTRAP_PASSWORD"
	}
	env_is_set AUTHENTIK_BOOTSTRAP_EMAIL || env_set AUTHENTIK_BOOTSTRAP_EMAIL "admin@openarity.local"
	env_is_set OBJECTS_BUCKET || env_set OBJECTS_BUCKET "openarity"
	env_is_set OBJECTS_ACCESS_KEY || env_set OBJECTS_ACCESS_KEY "openarity"

	# What makes this a staging stack rather than the development one: both
	# backends refuse their in-process defaults once the environment is not
	# development, and the shared token is refused outright.
	env_set OPENARITY_ENVIRONMENT "staging"
	env_set OPENARITY_SECRETS_BACKEND "openbao"
	env_set OPENARITY_OBJECTS_BACKEND "s3"
	env_set OPENARITY_OIDC_ENABLED "true"
	env_set OPENARITY_DEV_TOKEN ""
}

# --------------------------------------------------------------- the address

# A token carries the issuer that minted it, and the brain rejects one whose
# issuer is not what it was configured with. The browser and the brain
# therefore have to reach the provider at the same address — which rules out
# loopback, because the container's is not the browser's, and
# host.docker.internal does not resolve on a macOS host.
lan_address() {
	local addr=""
	if command -v ipconfig >/dev/null 2>&1; then
		addr=$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)
	fi
	if [ -z "$addr" ] && command -v hostname >/dev/null 2>&1; then
		# `|| true` for the same reason as above: -I is a Linux flag, macOS's
		# hostname rejects it, and pipefail would carry that out of the
		# substitution and end the script under set -e.
		addr=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
	fi
	printf '%s' "$addr"
}

ensure_bind_addr() {
	if env_is_set BIND_ADDR; then
		BIND_ADDR=$(env_value BIND_ADDR)
	else
		BIND_ADDR=$(lan_address)
		[ -n "$BIND_ADDR" ] || die "Could not work out this machine's LAN address.
Set it by hand and run again:  echo 'BIND_ADDR=<address>' >> deployment/.env"
		env_set BIND_ADDR "$BIND_ADDR"
		note "BIND_ADDR=$BIND_ADDR — detected"
	fi

	# BIND_ADDR is an address to bind, and a wildcard is a perfectly good one:
	# 0.0.0.0 publishes on every interface. It is not an address anything can
	# *reach*, though — no issuer, no callback and no health poll may contain
	# it, and writing one into OPENARITY_OIDC_ISSUER is what put a brain into
	# a crash loop with "dial tcp 0.0.0.0:9000: connect: connection refused".
	#
	# So the two are separate from here down. BIND_ADDR is only ever handed to
	# compose; REACH_ADDR is what goes into a URL.
	case "$BIND_ADDR" in
		0.0.0.0|::|'[::]'|'*')
			REACH_ADDR=$(lan_address)
			[ -n "$REACH_ADDR" ] || REACH_ADDR=127.0.0.1
			note "BIND_ADDR=$BIND_ADDR publishes everywhere; addressing it as $REACH_ADDR"
			;;
		*)
			REACH_ADDR=$BIND_ADDR
			;;
	esac

	# Resolved once, here, because env_value succeeds with empty output when
	# the key is absent — so `$(env_value API_PORT || echo 21120)` never took
	# the fallback and wrote a redirect URI with no port in it at all:
	# http://192.168.1.11:/ui/callback. ${x:-default} tests the value; `||`
	# only tests the exit status.
	API_PORT=$(env_value API_PORT || true)
	API_PORT=${API_PORT:-21120}
	say ""
	say "  The identity provider and Postgres are reachable from your network"
	say "  while the stack is up. That is the trade for a browser login that"
	say "  works; on an untrusted network, stop here and read README.md."
}

# ------------------------------------------------------------------- openbao

# The compose command is a parameter because the file set is not constant.
# Compose interpolates every `:?` guard in every file it was handed *before*
# it looks at which service the command names — so while DEX_PASSWORD_HASH is
# still empty, any call carrying the dex overlay fails, whatever service it
# was about. The OpenBao phase therefore talks to compose through $BAO_COMPOSE,
# which carries the base file and the OpenBao overlay and nothing else.
wait_healthy() {
	local service=$1 compose=${2:-$COMPOSE} id deadline
	deadline=$(( $(date +%s) + 120 ))
	while :; do
		id=$($compose ps -q "$service" 2>/dev/null || true)
		if [ -n "$id" ] &&
			[ "$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null)" = "healthy" ]; then
			return 0
		fi
		[ "$(date +%s)" -lt "$deadline" ] ||
			die "$service did not become healthy within two minutes.
Look at it with:  docker compose $COMPOSE_FILES logs $service"
		sleep 2
	done
}

# An OpenBao that has never been initialised answers 501 on /v1/sys/health,
# and the overlay's healthcheck is a wget that fails on any non-2xx — so it is
# *never* healthy until `bao init` has run. Waiting for health before
# initialising therefore waits forever, on exactly the machine this target
# exists for: one that has never run this before. `bao status` answers as soon
# as the server is listening, whatever state it is in, so that is what says it
# is ready to be initialised.
wait_bao_answering() {
	local deadline said
	deadline=$(( $(date +%s) + 120 ))
	while :; do
		# Captured and then matched, rather than piped into grep. `bao status`
		# exits 2 whenever the store is sealed — which a fresh one always is —
		# and this script runs with pipefail, so the pipeline would carry that
		# 2 out even though grep matched. The test never passed and the wait
		# ran to its deadline while the answer was on stdout the whole time.
		said=$($BAO_COMPOSE exec -T -e BAO_ADDR=http://127.0.0.1:8200 openbao \
			bao status 2>/dev/null || true)
		case "$said" in
			*Initialized*) return 0 ;;
		esac
		[ "$(date +%s)" -lt "$deadline" ] ||
			die "OpenBao did not start within two minutes.
Look at it with:  docker compose $COMPOSE_BASE $BAO_OVERLAY logs openbao"
		sleep 2
	done
}

ensure_openbao() {
	mkdir -p openbao/keys

	if [ ! -f openbao/keys/unseal.key ]; then
		# Exactly 32 bytes: the static seal takes an AES-256 key and rejects
		# any other length.
		openssl rand -out openbao/keys/unseal.key 32
		chmod 600 openbao/keys/unseal.key
		note "generated openbao/keys/unseal.key"
	fi

	make bao-up >/dev/null
	wait_bao_answering

	if [ ! -f openbao/keys/init-keys.json ]; then
		make bao-init >/dev/null
		note "initialised OpenBao — recovery keys in openbao/keys/init-keys.json"
	fi

	# Only now can it be: an initialised, self-unsealing store answers 200.
	wait_healthy openbao "$BAO_COMPOSE"

	# Always, rather than only when the AppRole lines are empty. A secret_id
	# is a credential with a lease; minting a fresh one costs a round trip and
	# means a stack whose store was rebuilt still starts. The role and policy
	# are written idempotently, so nothing accumulates.
	local minted
	minted=$(make bao-approle 2>/dev/null | grep '^OPENARITY_SECRETS_APPROLE_')
	[ -n "$minted" ] || die "Could not mint the brain's AppRole. Try: make bao-approle"

	env_set OPENARITY_SECRETS_APPROLE_ID "$(printf '%s' "$minted" |
		sed -n 's/^OPENARITY_SECRETS_APPROLE_ID=//p')"
	env_set OPENARITY_SECRETS_APPROLE_SECRET "$(printf '%s' "$minted" |
		sed -n 's/^OPENARITY_SECRETS_APPROLE_SECRET=//p')"
	note "minted the brain's AppRole"
}

# ----------------------------------------------------------------------- dex

ensure_dex() {
	if ! env_is_set DEX_PASSWORD_HASH; then
		DEX_PASSPHRASE=$(openssl rand -base64 15 | tr -d '/+=' | cut -c1-16)

		local hash
		hash=$(docker run --rm httpd:2.4-alpine \
			htpasswd -nbBC 10 '' "$DEX_PASSPHRASE" 2>/dev/null | cut -d: -f2 | tr -d '\r\n')
		[ -n "$hash" ] || die "Could not hash the passphrase. Is Docker running?"

		# Single-quoted, and that is not tidiness: a bcrypt hash contains $,
		# and compose reads an unquoted one as a variable reference — $2y$10$abc
		# becomes y0abc, and dex then refuses every password silently.
		env_set DEX_PASSWORD_HASH "'$hash'"

		# Printed here, not in the summary at the end. By this line the hash
		# is written and the passphrase exists nowhere else, so any later step
		# that fails — and several can, they start containers — would take it
		# with it. Both earlier runs of this script did exactly that.
		say ""
		say "  sign in as   dev@openarity.local"
		say "  passphrase   ${bold}$DEX_PASSPHRASE${plain}"
		say ""
		say "  Write it down. Only its hash is kept, in deployment/.env."
	fi

	env_set OPENARITY_OIDC_ISSUER "http://$REACH_ADDR:5556"
	env_set OPENARITY_OIDC_AUDIENCE "openarity"
	env_set OPENARITY_SUPER_ADMINS "$DEX_SUBJECT"
}

# ----------------------------------------------------------------- authentik

# Everything dex has in a committed file, authentik keeps in a database and
# creates over an API — so this is the long half, and the brittle one. The
# out-of-box setup flow already 404s in current versions, which is why the
# admin is bootstrapped from the environment instead.
ensure_authentik() {
	$COMPOSE up -d authentik-postgresql authentik-server authentik-worker >/dev/null
	wait_healthy authentik-server

	local token
	token=$(env_value AUTHENTIK_BOOTSTRAP_TOKEN)

	local client_id
	client_id=$(AUTHENTIK_URL="http://$REACH_ADDR:9000" AUTHENTIK_TOKEN="$token" \
		DASHBOARD_ORIGIN="http://$REACH_ADDR:$API_PORT" \
		LOOPBACK_ORIGINS="http://localhost:$API_PORT,http://127.0.0.1:$API_PORT" \
		python3 authentik-provision.py) ||
		die "Could not provision authentik. Its log may say why:
  docker compose $COMPOSE_FILES logs authentik-server"

	env_set OPENARITY_OIDC_ISSUER "http://$REACH_ADDR:9000/application/o/openarity/"
	env_set OPENARITY_OIDC_AUDIENCE "$client_id"
	env_set OPENARITY_SUPER_ADMINS "akadmin"
	note "authentik provider ready"
}

# ------------------------------------------------------------------- gateway

wait_answering() {
	local url=$1 deadline code
	deadline=$(( $(date +%s) + 180 ))
	while :; do
		code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$url" 2>/dev/null || true)
		case "$code" in
			000) ;;
			*) return 0 ;;
		esac
		[ "$(date +%s)" -lt "$deadline" ] ||
			die "The gateway did not answer $url within three minutes.
It will say why:  docker compose $COMPOSE_FILES logs $GATEWAY"
		sleep 2
	done
}

ensure_litellm() {
	LITELLM_PORT=$(env_value LITELLM_PORT || true)
	LITELLM_PORT=${LITELLM_PORT:-14000}

	$COMPOSE up -d litellm-postgresql litellm >/dev/null
	wait_answering "http://$REACH_ADDR:$LITELLM_PORT/health/liveliness"

	GATEWAY_URL="http://$REACH_ADDR:$LITELLM_PORT/v1"
	GATEWAY_KEY=$(env_value LITELLM_MASTER_KEY)
	GATEWAY_DASHBOARD="http://$REACH_ADDR:$LITELLM_PORT/ui"

	env_set OPENARITY_MODELS_BASE_URL "$GATEWAY_URL"
	env_set OPENARITY_MODELS_API_KEY "$GATEWAY_KEY"
	note "LiteLLM answering on $LITELLM_PORT"
}

ensure_omniroute() {
	OMNIROUTE_PORT=$(env_value OMNIROUTE_PORT || true)
	OMNIROUTE_PORT=${OMNIROUTE_PORT:-20128}

	$COMPOSE up -d omniroute >/dev/null
	wait_answering "http://$REACH_ADDR:$OMNIROUTE_PORT/v1/models"

	GATEWAY_URL="http://$REACH_ADDR:$OMNIROUTE_PORT/v1"
	GATEWAY_KEY=$(env_value OMNIROUTE_API_KEY || true)
	GATEWAY_DASHBOARD="http://$REACH_ADDR:$OMNIROUTE_PORT/dashboard"

	env_set OPENARITY_MODELS_BASE_URL "$GATEWAY_URL"
	[ -n "$GATEWAY_KEY" ] && env_set OPENARITY_MODELS_API_KEY "$GATEWAY_KEY"
	note "OmniRoute answering on $OMNIROUTE_PORT"
}

ensure_existing() {
	GATEWAY_URL=${MODELS_BASE_URL:-}
	GATEWAY_KEY=${MODELS_API_KEY:-}
	GATEWAY_DASHBOARD=""

	if [ -z "$GATEWAY_URL" ]; then
		[ -t 0 ] || die "GATEWAY=existing needs MODELS_BASE_URL, and there is no terminal to ask at.
  MODELS_BASE_URL=http://host:port/v1 MODELS_API_KEY=… make start-docker GATEWAY=existing"
		printf '  base URL (ending in /v1): '
		read -r GATEWAY_URL
	fi
	[ -n "$GATEWAY_URL" ] || die "No base URL given."

	if [ -z "$GATEWAY_KEY" ] && [ -t 0 ]; then
		printf '  API key (blank if it needs none): '
		read -r GATEWAY_KEY
	fi

	env_set OPENARITY_MODELS_BASE_URL "$GATEWAY_URL"
	env_set OPENARITY_MODELS_API_KEY "$GATEWAY_KEY"

	local code
	code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 \
		-H "Authorization: Bearer $GATEWAY_KEY" "$GATEWAY_URL/models" 2>/dev/null || true)
	case "$code" in
		000) note "nothing answered $GATEWAY_URL/models — recorded anyway" ;;
		200) note "$GATEWAY_URL answered and accepted the key" ;;
		*)   note "$GATEWAY_URL answered $code — recorded, but check the key" ;;
	esac
}

# --------------------------------------------------------------------- start

# At $BIND_ADDR, not loopback. BIND_ADDR moves every published port, so on a
# machine where it is the LAN address nothing is listening on 127.0.0.1 at all
# — polling there waits out the deadline while the stack is up and answering.
# It is also the only address the browser may use: dex's redirect URI is
# DASHBOARD_ORIGIN/ui/callback, built from this same value, so a sign-in
# started at loopback fails at the callback.
wait_ready() {
	local url="http://$REACH_ADDR:$API_PORT/readyz" deadline
	deadline=$(( $(date +%s) + 180 ))
	while :; do
		if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$url" 2>/dev/null)" = "200" ]; then
			return 0
		fi
		[ "$(date +%s)" -lt "$deadline" ] || die "The brain did not answer $url within three minutes.
It will say why:  docker compose $COMPOSE_FILES logs brain"
		sleep 2
	done
}

# ---------------------------------------------------------------------- main

need docker
need openssl
need python3
need curl
docker info >/dev/null 2>&1 || die "Docker is not running."

PROVIDER=${PROVIDER:-}
if [ -z "$PROVIDER" ]; then
	if [ -t 0 ]; then
		say ""
		say "${bold}Which identity provider?${plain}"
		say "  1) dex        one container, configuration committed to this repo"
		say "  2) authentik  three containers, an admin UI, federates Google and the rest"
		say ""
		printf '  [1] '
		read -r answer
		case "${answer:-1}" in
			1|dex) PROVIDER=dex ;;
			2|authentik) PROVIDER=authentik ;;
			*) die "Answer 1 or 2." ;;
		esac
	else
		PROVIDER=dex
		note "not a terminal — taking dex, the default"
	fi
fi

case "$PROVIDER" in
	dex)       PROVIDER_OVERLAY="-f docker-compose.dex.yml" ;;
	authentik) PROVIDER_OVERLAY="-f docker-compose.authentik.yml" ;;
	*) die "PROVIDER must be dex or authentik, not $PROVIDER." ;;
esac

GATEWAY=${GATEWAY:-}
if [ -z "$GATEWAY" ]; then
	if [ -t 0 ]; then
		say ""
		say "${bold}Which model gateway?${plain}"
		say "  1) none       nothing in the brain calls one yet"
		say "  2) omniroute  a dashboard for providers and routing, 4.1GB image, port 20128"
		say "  3) litellm    1.2GB and its own Postgres, port 14000"
		say "  4) existing   an OpenAI-compatible endpoint you already run"
		say ""
		printf '  [1] '
		read -r answer
		case "${answer:-1}" in
			1|none) GATEWAY=none ;;
			2|omniroute) GATEWAY=omniroute ;;
			3|litellm) GATEWAY=litellm ;;
			4|existing) GATEWAY=existing ;;
			*) die "Answer 1, 2, 3 or 4." ;;
		esac
	else
		GATEWAY=none
		note "not a terminal — starting no gateway"
	fi
fi

case "$GATEWAY" in
	none)      GATEWAY_OVERLAY="" ;;
	existing)  GATEWAY_OVERLAY="" ;;
	omniroute) GATEWAY_OVERLAY="-f docker-compose.omniroute.yml" ;;
	litellm)   GATEWAY_OVERLAY="-f docker-compose.litellm.yml" ;;
	*) die "GATEWAY must be none, omniroute, litellm or existing, not $GATEWAY." ;;
esac

# The published image unless asked otherwise. A clone that wants its own tree
# built passes BUILD=1 and waits for the compile instead of the pull.
if [ -n "${BUILD:-}" ]; then
	IMAGE_OVERLAY=""
	BUILD_FLAG="--build"
else
	IMAGE_OVERLAY="-f docker-compose.image.yml"
	BUILD_FLAG=""
fi

COMPOSE_FILES="$COMPOSE_BASE $PROVIDER_OVERLAY $GATEWAY_OVERLAY $IMAGE_OVERLAY $BAO_OVERLAY $OBJECTS_OVERLAY"
COMPOSE="docker compose $COMPOSE_FILES"
BAO_COMPOSE="docker compose $COMPOSE_BASE $BAO_OVERLAY"

step "Settings"
ensure_env_file
ensure_bind_addr

step "Secret store"
ensure_openbao

step "Identity provider — $PROVIDER"
case "$PROVIDER" in
	dex)       ensure_dex ;;
	authentik) ensure_authentik ;;
esac

GATEWAY_URL=""
GATEWAY_KEY=""
GATEWAY_DASHBOARD=""
if [ "$GATEWAY" != none ]; then
	step "Model gateway — $GATEWAY"
	case "$GATEWAY" in
		omniroute) ensure_omniroute ;;
		litellm)   ensure_litellm ;;
		existing)  ensure_existing ;;
	esac
fi

step "Starting"
# shellcheck disable=SC2086 # every one of these is a flag we wrote
$COMPOSE --profile brain up -d $BUILD_FLAG
wait_ready

say ""
say "${bold}Openarity is running.${plain}"
say ""
say "  dashboard    http://$REACH_ADDR:$API_PORT/ui"
if [ "$PROVIDER" = dex ]; then
	say "  sign in as   dev@openarity.local"
	if [ -n "${DEX_PASSPHRASE:-}" ]; then
		say "  passphrase   $DEX_PASSPHRASE"
		say ""
		say "Write it down. Only its hash is kept, in deployment/.env."
	else
		say "  passphrase   the one from the first run — deployment/.env has only its hash"
	fi
else
	say "  authentik    http://$REACH_ADDR:9000"
	say "  sign in as   akadmin"
	say "  passphrase   ${AUTHENTIK_PASSWORD:-AUTHENTIK_BOOTSTRAP_PASSWORD in deployment/.env}"
fi

if [ -n "$GATEWAY_URL" ]; then
	say ""
	say "  gateway      $GATEWAY_URL"
	[ -n "$GATEWAY_DASHBOARD" ] && say "  its console  $GATEWAY_DASHBOARD"
	case "$GATEWAY" in
		omniroute)
			say ""
			say "  It routes nothing until you add a provider and mint a key in"
			say "  that console, then put the key in deployment/.env as"
			say "  OMNIROUTE_API_KEY and run this again."
			;;
		litellm)
			say ""
			say "  It routes nothing until a provider key is in deployment/.env"
			say "  — ANTHROPIC_API_KEY or OPENAI_API_KEY — and litellm restarts."
			;;
	esac
	say ""
	say "${dim}  export OPENARITY_MODELS_BASE_URL=$GATEWAY_URL${plain}"
	say "${dim}  export OPENARITY_MODELS_API_KEY=…   # in deployment/.env${plain}"
fi

say ""
say "${dim}  oa context create local --server http://$REACH_ADDR:$API_PORT${plain}"
say "${dim}  oa login${plain}"
say ""
