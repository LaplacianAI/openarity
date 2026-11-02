#!/usr/bin/env bash
#
# seed-showcase.sh - seed a "showcase" team on a running Openarity brain, so
# the dashboard's screens can be judged on data that looks like a real team's.
#
# What it creates, in one new team:
#   - seven agents of different patterns (react, plan, rewoo, reflection), two
#     with web tools, one of them the team's hub, with skills and MCP grants
#   - three MCP servers: one public server it asks for its tools, one
#     registered at an address that cannot answer (its discovery fails), and
#     one command server
#   - two skills: one imported from a public GitHub folder, one uploaded from
#     a directory this script writes to a temporary folder
#   - two custom-webhook channels with 20+ conversations (group, thread and
#     direct) from approved senders, and senders still waiting for approval
#   - runs that succeeded, failed, were cancelled, delegated work, and one
#     still running when the script ends
#
# It SPENDS REAL MODEL TOKENS: about six to ten runs, each a few thousand
# tokens; the one left running writes a long document.
#
# Prerequisites:
#   - `oa` on PATH, logged in as a super admin, its current context pointing
#     at the brain to seed (`oa whoami` should show it)
#   - openssl, curl and jq
#
# Environment (nothing else is read):
#   SEED_WEBHOOK_BASE  required. The address the brain's webhook listener is
#                      reached at from here, without /hooks. Each message is
#                      posted to $SEED_WEBHOOK_BASE/hooks/custom/<channel id>.
#   SEED_MODEL         required. The model the agents run on, as the brain's
#                      model gateway names it - the one your other teams use.
#   SEED_TEAM          the team to create. Default: showcase.
#   SEED_MEMBERS       optional, space-separated subjects of people who have
#                      already logged in. They are added to the team as
#                      members and approved senders are spread across them.
#   SEED_SKILL_SOURCE  optional, the skill to import.
#                      Default: github:anthropics/skills/skills/pdf@main
#
# Usage:
#   SEED_WEBHOOK_BASE=https://hooks.example.test SEED_MODEL=<model> \
#       apps/dashboard/scripts/seed-showcase.sh [--dry-run]
#
# --dry-run prints every oa and curl command instead of running it, with the
# signing secret shown as <secret>. Nothing is sent anywhere.
#
# The team must not exist yet. To seed again: oa teams delete <team>, or pick
# another SEED_TEAM.

set -euo pipefail

DRY_RUN=0
for arg in "$@"; do
	case "$arg" in
	--dry-run) DRY_RUN=1 ;;
	-h | --help)
		sed -n '2,/^set -euo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "unknown argument: $arg (only --dry-run is taken)" >&2
		exit 2
		;;
	esac
done

TEAM="${SEED_TEAM:-showcase}"
WEBHOOK_BASE="${SEED_WEBHOOK_BASE:-}"
MODEL="${SEED_MODEL:-}"
MEMBERS="${SEED_MEMBERS:-}"
SKILL_SOURCE="${SEED_SKILL_SOURCE:-github:anthropics/skills/skills/pdf@main}"

# How long the hub waits for new agents to reach the team's graph: they are
# taken in by a drain that ticks once a minute.
GRAPH_DRAIN_SECONDS=75
RUN_TIMEOUT_SECONDS=300

step() { printf '\n==> %s\n' "$*"; }
info() { printf '    %s\n' "$*"; }
warn() { printf '    warning: %s\n' "$*" >&2; }
die() {
	printf 'seed-showcase: %s\n' "$*" >&2
	exit 1
}

# ---------------------------------------------------------------- preflight

[ -n "$WEBHOOK_BASE" ] || die "set SEED_WEBHOOK_BASE to the brain's public webhook address (without /hooks)"
[ -n "$MODEL" ] || die "set SEED_MODEL to the model the agents should run on, as the brain's gateway names it"
WEBHOOK_BASE="${WEBHOOK_BASE%/}"

for tool in openssl curl jq; do
	command -v "$tool" >/dev/null 2>&1 || die "$tool is needed and is not on PATH"
done
if [ "$DRY_RUN" -eq 0 ]; then
	command -v oa >/dev/null 2>&1 || die "oa is needed and is not on PATH"
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/seed-showcase.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# ---------------------------------------------------------------- helpers

# show prints a command to stderr, so a dry run stays visible where the real
# command's output would be discarded.
show() {
	{
		printf '    +'
		printf ' %q' "$@"
		printf '\n'
	} >&2
}

# oa_do runs an oa command whose output is only for reading.
oa_do() {
	if [ "$DRY_RUN" -eq 1 ]; then
		show oa "$@"
		return 0
	fi
	oa "$@"
}

# oa_json runs an oa command for its JSON. In a dry run it prints the command
# and answers {}, so callers fall back to placeholders.
oa_json() {
	if [ "$DRY_RUN" -eq 1 ]; then
		show oa "$@" -o json
		echo '{}'
		return 0
	fi
	oa "$@" -o json
}

# field reads one field from JSON, or a placeholder when it is absent.
field() { jq -r --arg d "$2" "$1 // \$d"; }

pause() {
	if [ "$DRY_RUN" -eq 1 ]; then
		info "(would wait $1s: $2)"
		return 0
	fi
	info "waiting $1s: $2"
	sleep "$1"
}

iso_minutes_ago() {
	local at=$(($(date +%s) - $1 * 60))
	date -u -r "$at" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null ||
		date -u -d "@$at" +%Y-%m-%dT%H:%M:%SZ
}

# run_status prints a run's status, or "unknown" if it cannot be read.
run_status() {
	oa runs get "$TEAM" "$1" -o json 2>/dev/null | jq -r '.status // "unknown"' || echo unknown
}

# wait_until <run> <status...>: polls until the run reaches one of the given
# statuses or the timeout passes, and prints the last status seen.
wait_until() {
	local run="$1"
	shift
	if [ "$DRY_RUN" -eq 1 ]; then
		show oa runs get "$TEAM" "$run" -o json
		echo "<status>"
		return 0
	fi
	local deadline=$((SECONDS + RUN_TIMEOUT_SECONDS)) status want
	while :; do
		status="$(run_status "$run")"
		for want in "$@"; do
			if [ "$status" = "$want" ]; then
				echo "$status"
				return 0
			fi
		done
		if [ "$SECONDS" -ge "$deadline" ]; then
			echo "$status"
			return 0
		fi
		sleep 3
	done
}

wait_finished() { wait_until "$1" succeeded failed cancelled; }

# start_run <label> <oa runs start args...>: starts a run and prints its id.
start_run() {
	local label="$1"
	shift
	local id
	id="$(oa_json runs start "$TEAM" "$@" | field .id "<run:$label>")"
	info "started $label: $id" >&2
	echo "$id"
}

# ---------------------------------------------------------------- webhooks

# Channel bookkeeping: name, id and secret, kept only in shell variables.
CH1_NAME=eng-support
CH2_NAME=ops-alerts
CH1_ID="<channel:$CH1_NAME>"
CH2_ID="<channel:$CH2_NAME>"
CH1_SECRET=""
CH2_SECRET=""
POSTED=0
REFUSED=0

display_name() {
	case "$1" in
	u-alice) echo "Alice Moreau" ;;
	u-bilal) echo "Bilal Haddad" ;;
	u-chen) echo "Chen Wei" ;;
	u-dana) echo "Dana Okafor" ;;
	u-hiro) echo "Hiro Tanaka" ;;
	u-ines) echo "Inês Duarte" ;;
	u-erik) echo "Erik Lund" ;;
	u-farah) echo "Farah Siddiqui" ;;
	u-gwen) echo "gwen (contractor)" ;;
	u-jonas) echo "Jonas Becker" ;;
	u-kemi) echo "Kemi Adeyemi" ;;
	*) echo "$1" ;;
	esac
}

# post <channel 1|2> <sender ref> <session ref> <kind> <minutes ago> <text>
#
# One delivery to the custom adapter. The signature is HMAC-SHA256 over
# "v1:<timestamp>:<body>" keyed with the channel's signing secret, hex, and
# prefixed "v1=" - what custom.Sign produces. The timestamp header must be
# within five minutes of the brain's clock; sent_at is free to be older.
post() {
	local ch="$1" ref="$2" session="$3" kind="$4" ago="$5" text="$6"
	local id secret name prefix
	if [ "$ch" = 1 ]; then
		id="$CH1_ID" secret="$CH1_SECRET" prefix=eng
	else
		id="$CH2_ID" secret="$CH2_SECRET" prefix=ops
	fi
	POSTED=$((POSTED + 1))
	name="$(display_name "$ref")"

	local body ts url
	body="$(jq -nc \
		--arg id "$prefix-msg-$POSTED-$RUN_TAG" \
		--arg text "$text" \
		--arg sent "$(iso_minutes_ago "$ago")" \
		--arg ref "$ref" --arg name "$name" \
		--arg session "$session" --arg kind "$kind" \
		'{id: $id, text: $text, sent_at: $sent,
		  author: {ref: $ref, display_name: $name, is_bot: false},
		  session: {ref: $session, kind: $kind}}')"
	ts="$(date +%s)"
	url="$WEBHOOK_BASE/hooks/custom/$id"

	if [ "$DRY_RUN" -eq 1 ]; then
		show curl -sS -X POST "$url" \
			-H 'Content-Type: application/json' \
			-H "X-Openarity-Timestamp: $ts" \
			-H 'X-Openarity-Signature: v1=<hmac-sha256 of v1:<timestamp>:<body> keyed with <secret>>' \
			--data-binary "$body"
		return 0
	fi

	local sig code
	sig="v1=$(printf '%s' "v1:$ts:$body" | openssl dgst -sha256 -hmac "$secret" | awk '{print $NF}')"
	code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$url" \
		-H 'Content-Type: application/json' \
		-H "X-Openarity-Timestamp: $ts" \
		-H "X-Openarity-Signature: $sig" \
		--data-binary "$body")" || code="000"
	case "$code" in
	200) ;;
	403) die "the brain refused a delivery to $url (403): wrong address, or the signature did not verify" ;;
	*)
		REFUSED=$((REFUSED + 1))
		warn "delivery $POSTED to $url answered $code"
		;;
	esac
}

# A tag that keeps message ids unique if the same channel ever sees a rerun.
RUN_TAG="$(date +%s)"

# ================================================================ the seed

step "who is seeding"
ME_JSON="$(oa_json whoami)"
ME_ID="$(printf '%s' "$ME_JSON" | field .id "<your-user-id>")"
ME_SUBJECT="$(printf '%s' "$ME_JSON" | field .subject "<your-subject>")"
info "you are $ME_SUBJECT ($ME_ID)"

step "checking that team \"$TEAM\" does not exist"
if [ "$DRY_RUN" -eq 1 ]; then
	show oa teams list --limit 100 -o json
else
	cursor=""
	while :; do
		if [ -n "$cursor" ]; then
			page="$(oa teams list --limit 100 --cursor "$cursor" -o json)"
		else
			page="$(oa teams list --limit 100 -o json)"
		fi
		if printf '%s' "$page" | jq -e --arg t "$TEAM" \
			'.items[] | select((.name | ascii_downcase) == ($t | ascii_downcase))' >/dev/null; then
			die "a team named \"$TEAM\" already exists. Delete it first (oa teams delete $TEAM), or set SEED_TEAM to another name."
		fi
		cursor="$(printf '%s' "$page" | jq -r '.next_cursor // empty')"
		[ -n "$cursor" ] || break
	done
	info "it does not"
fi

step "creating team \"$TEAM\""
oa_do teams create "$TEAM" >/dev/null
oa_do teams members add "$TEAM" "$ME_ID" --role admin >/dev/null
info "added you as admin"

# Approved senders are spread over these users.
APPROVERS="$ME_ID"
for subject in $MEMBERS; do
	oa_do teams members add "$TEAM" "$subject" --role member >/dev/null
	APPROVERS="$APPROVERS $subject"
	info "added $subject as member"
done

oa_do teams settings set "$TEAM" --model "$MODEL" --max-tokens 2048 >/dev/null
info "team model set to $MODEL"

# ---------------------------------------------------------------- MCP servers

step "registering MCP servers"
oa_do mcp-servers create "$TEAM" deepwiki --url https://mcp.deepwiki.com/mcp >/dev/null
info "deepwiki: a public server with no credential"
if [ "$DRY_RUN" -eq 1 ]; then
	oa_do mcp-servers discover "$TEAM" deepwiki
elif oa mcp-servers discover "$TEAM" deepwiki >/dev/null; then
	info "deepwiki: discovered"
else
	warn "deepwiki could not be discovered from the brain; it stays registered with no tools"
fi

# .invalid is reserved (RFC 6761) and never resolves, so this server can
# never answer: its discovery fails, and a run granted it fails to start.
oa_do mcp-servers create "$TEAM" statuspage --url https://statuspage.mcp.invalid/mcp >/dev/null
if [ "$DRY_RUN" -eq 1 ]; then
	oa_do mcp-servers discover "$TEAM" statuspage
elif oa mcp-servers discover "$TEAM" statuspage >/dev/null 2>&1; then
	warn "statuspage was discovered, which should not be possible"
else
	info "statuspage: discovery failed, as intended"
fi

oa_do mcp-servers create "$TEAM" runbooks-fs -- npx -y @modelcontextprotocol/server-filesystem /srv/runbooks >/dev/null
info "runbooks-fs: a command server (not discovered; the brain does not start those)"

# ---------------------------------------------------------------- skills

step "adding skills"
SKILL_DIR="$WORK/incident-postmortem"
mkdir -p "$SKILL_DIR"
cat >"$SKILL_DIR/SKILL.md" <<'EOF'
---
name: incident-postmortem
description: Write a blameless incident postmortem from a timeline, logs and chat. Use when someone asks for a postmortem, an incident review or a write-up of an outage.
---

# Incident postmortem

1. Build the timeline first, in UTC, one line per event.
2. Separate the trigger from the root cause. There is usually more than one
   contributing factor; list them all.
3. Impact in numbers: duration, error rate, customers affected.
4. Action items have an owner and a date, or they are wishes.

Use `template.md` for the layout. Never name a person as the cause.
EOF
cat >"$SKILL_DIR/template.md" <<'EOF'
# <Service>: <one-line summary> (<date>)

## Summary
## Impact
## Timeline (UTC)
## Root cause and contributing factors
## What went well
## What went badly
## Action items
| Action | Owner | Due |
| ------ | ----- | --- |
EOF
oa_do skills upload "$TEAM" "$SKILL_DIR" >/dev/null
info "uploaded incident-postmortem"

IMPORTED_SKILL="$(oa_json skills import "$TEAM" "$SKILL_SOURCE" | field .name "<imported-skill>")" ||
	IMPORTED_SKILL=""
if [ -n "$IMPORTED_SKILL" ]; then
	info "imported $IMPORTED_SKILL from $SKILL_SOURCE"
else
	warn "importing $SKILL_SOURCE failed; runbook-writer gets the uploaded skill only"
fi

# ---------------------------------------------------------------- agents

step "defining agents"
agent_file() { printf '%s/%s.yaml' "$WORK" "$1"; }

cat >"$(agent_file log-reader)" <<EOF
name: log-reader
description: Reads stack traces, log excerpts and metrics, and says what failed and where to look next
instructions: |
  You are an SRE who reads logs for a living. Given an excerpt, name the
  failing component, the most likely cause and the next two things to check.
  Quote the line that proves it. If the excerpt is not enough, say what is
  missing instead of guessing.
model: {name: $MODEL, max_tokens: 1024}
pattern: react
max_steps: 6
EOF

cat >"$(agent_file runbook-writer)" <<EOF
name: runbook-writer
description: Turns incidents and tribal knowledge into step-by-step runbooks and postmortems
instructions: |
  You write operational runbooks: numbered steps, the exact command, what
  good output looks like, and when to escalate. Prefer short sentences.
  Every step must be safe to run twice.
model: {name: $MODEL, max_tokens: 4096}
pattern: plan
max_steps: 20
EOF

cat >"$(agent_file release-captain)" <<EOF
name: release-captain
description: Plans releases - change lists, rollout order, feature flags and rollback steps
instructions: |
  You run release planning for a platform team. Break a release into stages,
  name the risk in each, and give the rollback for every stage.
model: {name: $MODEL, max_tokens: 2048}
pattern: rewoo
max_steps: 16
EOF

cat >"$(agent_file docs-scout)" <<EOF
name: docs-scout
description: Researches libraries and public repositories and answers with sources
instructions: |
  Answer questions about third-party libraries and services. Cite the page
  or repository you used. Check your draft once against the question before
  answering.
model: {name: $MODEL, max_tokens: 2048}
pattern: reflection
max_steps: 8
web: true
EOF

cat >"$(agent_file statuspage-sync)" <<EOF
name: statuspage-sync
description: Keeps the public status page in step with open incidents
instructions: |
  Read the open incidents and update the matching status page components.
  Never mark a component operational while its incident is open.
model: {name: $MODEL, max_tokens: 1024}
pattern: react
max_steps: 6
EOF

cat >"$(agent_file changelog-digest)" <<EOF
name: changelog-digest
description: Summarises a week of merged changes into a digest for the support team
instructions: |
  Turn merged pull requests into a digest support can read: what changed for
  customers, what they might ask about, and anything that needs a macro.
model: {name: $MODEL, max_tokens: 1024}
pattern: react
max_steps: 4
token_budget: 250
EOF

cat >"$(agent_file ops-lead)" <<EOF
name: ops-lead
description: The team's front door for operations - triages a request and hands it to the right specialist
instructions: |
  You coordinate the platform team's agents. Hand log analysis to
  log-reader, runbooks and postmortems to runbook-writer, and release plans
  to release-captain. Keep your own answers to five lines.
model: {name: $MODEL, max_tokens: 2048}
pattern: react
max_steps: 12
delegation: listed
delegates: [log-reader, runbook-writer, release-captain]
web: true
EOF

oa_do agents create "$TEAM" -f "$(agent_file log-reader)" \
	--mcp-server runbooks-fs --skill incident-postmortem >/dev/null
info "log-reader (react, runbooks-fs, incident-postmortem)"

if [ -n "$IMPORTED_SKILL" ]; then
	oa_do agents create "$TEAM" -f "$(agent_file runbook-writer)" \
		--skill incident-postmortem --skill "$IMPORTED_SKILL" >/dev/null
	info "runbook-writer (plan, incident-postmortem, $IMPORTED_SKILL)"
else
	oa_do agents create "$TEAM" -f "$(agent_file runbook-writer)" \
		--skill incident-postmortem >/dev/null
	info "runbook-writer (plan, incident-postmortem)"
fi

oa_do agents create "$TEAM" -f "$(agent_file release-captain)" >/dev/null
info "release-captain (rewoo)"

oa_do agents create "$TEAM" -f "$(agent_file docs-scout)" \
	--mcp-server deepwiki >/dev/null
info "docs-scout (reflection, web, deepwiki)"

oa_do agents create "$TEAM" -f "$(agent_file statuspage-sync)" \
	--mcp-server statuspage >/dev/null
info "statuspage-sync (react, granted the server that never answers)"

oa_do agents create "$TEAM" -f "$(agent_file changelog-digest)" >/dev/null
info "changelog-digest (react, a 250-token budget per run)"

oa_do agents create "$TEAM" -f "$(agent_file ops-lead)" \
	--skill incident-postmortem >/dev/null
info "ops-lead (react, web, delegates to log-reader, runbook-writer, release-captain)"
AGENTS_AT=$SECONDS

oa_do teams settings set "$TEAM" --hub ops-lead >/dev/null
info "ops-lead is the team's hub"

# ---------------------------------------------------------------- channels

step "connecting channels"
CH1_SECRET="$(openssl rand -hex 32)"
CH2_SECRET="$(openssl rand -hex 32)"
if [ "$DRY_RUN" -eq 1 ]; then
	printf '    + printf %%s <secret> | oa channels create %q %q --provider custom --secret-stdin -o json\n' "$TEAM" "$CH1_NAME" >&2
	printf '    + printf %%s <secret> | oa channels create %q %q --provider custom --secret-stdin -o json\n' "$TEAM" "$CH2_NAME" >&2
else
	CH1_ID="$(printf '%s' "$CH1_SECRET" |
		oa channels create "$TEAM" "$CH1_NAME" --provider custom --secret-stdin -o json | jq -r .id)"
	CH2_ID="$(printf '%s' "$CH2_SECRET" |
		oa channels create "$TEAM" "$CH2_NAME" --provider custom --secret-stdin -o json | jq -r .id)"
fi
info "$CH1_NAME: $CH1_ID"
info "$CH2_NAME: $CH2_ID"

# ---------------------------------------------------------------- senders

step "first messages: every sender is unknown, so each lands in the pending queue"
# Nothing from an unapproved sender is stored; only who they are is.
post 1 u-alice eng-support group 2900 "hi all - is this where we ask the agents about deploys?"
post 1 u-bilal eng-support group 2880 "testing the new channel, ignore"
post 1 u-chen eng-support group 2870 "hello from the payments squad"
post 1 u-dana eng-support group 2860 "can someone add me here?"
post 1 u-erik eng-support group 2600 "Is the staging DB down? Getting connection resets"
post 1 u-farah eng-support group 1500 "hey, I'm new on the data team - who owns the ingestion runbooks?"
post 1 u-gwen eng-support group 300 "pls give me access to prod logs, deadline today"
post 1 u-farah eng-support group 120 "bumping my question about ingestion runbooks"
post 2 u-hiro ops-alerts group 2890 "hooking ops-alerts up, first message"
post 2 u-ines ops-alerts group 2885 "olá, checking this works"
post 2 u-alice ops-alerts group 2880 "alice here, on-call this week"
post 2 u-jonas ops-alerts group 900 "who do I page for the CDN?"
post 2 u-kemi ops-alerts group 60 "is ops-alerts the right place for vendor outages?"

step "approving senders (the rest stay pending)"
n=0
approve() {
	local channel="$1" ref="$2" user
	user="$(printf '%s\n' "$APPROVERS" | awk -v i="$n" '{ print $((i % NF) + 1) }')"
	n=$((n + 1))
	oa_do channels senders approve "$TEAM" "$channel" "$ref" "$user" >/dev/null
	info "$channel: $ref -> $user"
}
approve "$CH1_NAME" u-alice
approve "$CH1_NAME" u-bilal
approve "$CH1_NAME" u-chen
approve "$CH1_NAME" u-dana
approve "$CH2_NAME" u-hiro
approve "$CH2_NAME" u-ines
approve "$CH2_NAME" u-alice
info "pending: u-erik, u-farah, u-gwen on $CH1_NAME; u-jonas, u-kemi on $CH2_NAME"

# ---------------------------------------------------------------- conversations

step "conversations on $CH1_NAME"
# The room itself.
post 1 u-alice eng-support group 2700 "Morning. Deploy freeze starts Thursday 18:00 UTC, anything risky should be out by Wednesday."
post 1 u-bilal eng-support group 2690 "@ops-lead what's left on the release checklist for 4.12?"
post 1 u-chen eng-support group 1440 "FYI payments-api is on the new connection pool settings as of this morning"
post 1 u-dana eng-support group 200 "Is anyone else seeing slow CI? The integration suite went from 9 to 23 minutes"

# Threads.
post 1 u-bilal eng-support:t-1001 thread 2650 "checkout-api p99 went from 180ms to 2.4s right after the 14:05 deploy. Rollback or dig?"
post 1 u-alice eng-support:t-1001 thread 2645 "Dig for 10 minutes first. What changed in the deploy besides the pool size?"
post 1 u-bilal eng-support:t-1001 thread 2640 "Only the pool size and a log level change. Grabbing traces now."

post 1 u-chen eng-support:t-1002 thread 2400 "Can an agent turn last Friday's Redis failover into a postmortem? Timeline is in the incident doc."
post 1 u-chen eng-support:t-1002 thread 2395 "Key bits: 16:02 primary OOM, 16:04 sentinel promotes replica, 16:11 clients reconnect, 16:20 queue drained."

post 1 u-dana eng-support:t-1003 thread 2200 "What's the safe way to rotate the Postgres replication password without dropping the replica?"
post 1 u-alice eng-support:t-1003 thread 2190 "There's an old runbook but it predates the move to managed Postgres. Would love a fresh one."

post 1 u-bilal eng-support:t-1004 thread 1900 "Plan for rolling out the new rate limiter: canary in eu-west first, then everywhere?"
post 1 u-chen eng-support:t-1004 thread 1890 "Needs a kill switch flag before canary. Last time we had to redeploy to turn it off."
post 1 u-bilal eng-support:t-1004 thread 1880 "Agreed, adding rate_limiter_v2_enabled. Who reviews the rollback steps?"

post 1 u-alice eng-support:t-1005 thread 1300 "Does anyone know if pgx v5 pools reset prepared statements on reconnect?"
post 1 u-dana eng-support:t-1005 thread 1290 "I think it depends on the statement cache mode, but don't quote me."

post 1 u-chen eng-support:t-1006 thread 700 "Nightly export job failed again: 'context deadline exceeded' after 30m. Third night in a row."
post 1 u-chen eng-support:t-1006 thread 695 "Logs: export: write chunk 412/980: s3 PutObject: RequestTimeout: Your socket connection to the server was not read from or written to within the timeout period."
post 1 u-dana eng-support:t-1006 thread 690 "That bucket moved to another region last week, could be cross-region latency."

post 1 u-dana eng-support:t-1007 thread 180 "The integration suite slowdown: it started with the commit that enabled race detection on all packages."
post 1 u-bilal eng-support:t-1007 thread 170 "Race detector costs 2-10x, so that tracks. Maybe only on the packages with goroutines?"

# Direct messages.
post 1 u-alice eng-support:dm-u-alice direct 2500 "Can you remind me what our on-call handover checklist covers?"
post 1 u-alice eng-support:dm-u-alice direct 2490 "Specifically the part about open incidents."
post 1 u-bilal eng-support:dm-u-bilal direct 1600 "Draft me a message to the support team about the checkout slowness yesterday, nothing too technical."
post 1 u-chen eng-support:dm-u-chen direct 1100 "How many retries does our payments client do before giving up? I keep forgetting."
post 1 u-dana eng-support:dm-u-dana direct 90 "Quick one: what's the command to drain a node in our cluster without killing the ingress pods?"

step "conversations on $CH2_NAME"
post 2 u-hiro ops-alerts group 2600 "[FIRING] HighErrorRate checkout-api 5xx > 2% for 5m (eu-west-1)"
post 2 u-alice ops-alerts group 2598 "ack, looking. Probably the pool change, see eng-support thread"
post 2 u-ines ops-alerts group 2550 "[RESOLVED] HighErrorRate checkout-api"
post 2 u-hiro ops-alerts group 400 "Reminder: certificate for api.internal expires in 9 days"

post 2 u-hiro ops-alerts:t-2001 thread 2300 "Disk on db-replica-2 at 91%. WAL archiving fell behind overnight."
post 2 u-ines ops-alerts:t-2001 thread 2295 "archive_command is failing on permissions since the bucket policy change."
post 2 u-hiro ops-alerts:t-2001 thread 2290 "Fixed the policy. Replica catching up, 84% now."

post 2 u-alice ops-alerts:t-2002 thread 1700 "CDN error rate up in APAC, vendor status page says all green."
post 2 u-hiro ops-alerts:t-2002 thread 1690 "Their status page is always green. Open a ticket with the trace ids."

post 2 u-ines ops-alerts:t-2003 thread 1200 "Can we get a weekly digest of what changed for the support folks? They keep getting surprised."
post 2 u-alice ops-alerts:t-2003 thread 1190 "+1, they found out about the new export limits from a customer."

post 2 u-hiro ops-alerts:t-2004 thread 800 "Kafka consumer lag on billing-events at 1.2M and climbing."
post 2 u-ines ops-alerts:t-2004 thread 795 "One partition is stuck. The consumer for partition 7 keeps rebalancing."
post 2 u-hiro ops-alerts:t-2004 thread 780 "Restarted the consumer group, lag dropping. Need a runbook for this, third time this month."

post 2 u-alice ops-alerts:t-2005 thread 240 "Status page still shows 'degraded' for exports though the incident is closed."
post 2 u-ines ops-alerts:t-2005 thread 235 "The sync job should have flipped it. Can someone check why it didn't?"

post 2 u-hiro ops-alerts:dm-u-hiro direct 2000 "What's our SLO for the export API again, 99.5 or 99.9?"
post 2 u-ines ops-alerts:dm-u-ines direct 1000 "Can you give me a checklist for the quarterly DR drill?"
post 2 u-alice ops-alerts:dm-u-alice direct 150 "Summarise what happened on ops-alerts while I was off yesterday."

# A pending sender tries again: their count goes up, nothing is stored.
post 1 u-gwen eng-support group 30 "still need prod log access!!"

info "posted $POSTED deliveries ($REFUSED not accepted)"

session_id() {
	local channel="$1" ref="$2"
	oa_json sessions list "$TEAM" --channel "$channel" --limit 100 |
		jq -r --arg r "$ref" --arg d "<session:$ref>" \
			'([.items[]? | select(.provider_ref == $r) | .id] | first) // $d'
}

# ---------------------------------------------------------------- runs

step "runs that succeed"
S_T1001="$(session_id "$CH1_NAME" eng-support:t-1001)"
RUN_OK1="$(start_run "log-reader in thread t-1001" log-reader \
	"From the checkout-api thread: p99 went from 180ms to 2.4s right after a deploy that changed only the DB pool size (max_conns 50 -> 8) and a log level. Trace excerpt: 'acquire conn: waited 2.1s; pool stats: total=8 idle=0 waiting=37'. What failed and what should we check next?" \
	--session "$S_T1001")"
S_DM_INES="$(session_id "$CH2_NAME" ops-alerts:dm-u-ines)"
RUN_OK2="$(start_run "runbook-writer in Ines's direct session" runbook-writer \
	"Give me a short checklist (max 10 items) for our quarterly disaster-recovery drill: restoring Postgres from backup into a fresh region and pointing one service at it." \
	--session "$S_DM_INES")"

step "a run that fails before it calls the model: its MCP server cannot be reached"
S_T2005="$(session_id "$CH2_NAME" ops-alerts:t-2005)"
RUN_FAIL_MCP="$(start_run "statuspage-sync in thread t-2005" statuspage-sync \
	"The exports incident is closed but the status page still shows exports as degraded. Bring it in line." \
	--session "$S_T2005")"

step "a run that fails on its token budget"
S_T2003="$(session_id "$CH2_NAME" ops-alerts:t-2003)"
RUN_FAIL_BUDGET="$(start_run "changelog-digest in thread t-2003" changelog-digest \
	"Write this week's support digest from these merged changes: export API limit raised to 50k rows; new rate limiter behind rate_limiter_v2_enabled; payments client retries reduced from 5 to 3; dashboard sessions page shows thread names; CSV exports now UTF-8 with BOM; SSO login now remembers the last workspace. For each: what changed, what customers may ask, and a suggested reply." \
	--session "$S_T2003")"

ST_OK1="$(wait_finished "$RUN_OK1")"
info "$RUN_OK1: $ST_OK1"
ST_OK2="$(wait_finished "$RUN_OK2")"
info "$RUN_OK2: $ST_OK2"
ST_FAIL_MCP="$(wait_finished "$RUN_FAIL_MCP")"
info "$RUN_FAIL_MCP: $ST_FAIL_MCP"
ST_FAIL_BUDGET="$(wait_finished "$RUN_FAIL_BUDGET")"
info "$RUN_FAIL_BUDGET: $ST_FAIL_BUDGET"

step "a run that is cancelled while it works"
S_T1004="$(session_id "$CH1_NAME" eng-support:t-1004)"
RUN_CANCEL="$(start_run "release-captain in thread t-1004" release-captain \
	"Plan the rollout of the new rate limiter across 14 services in 3 regions: canary order, the flag rate_limiter_v2_enabled per stage, the metrics that gate each stage, and the rollback for every stage. Be thorough." \
	--session "$S_T1004")"
info "$RUN_CANCEL: $(wait_until "$RUN_CANCEL" running succeeded failed cancelled)"
oa_do runs cancel "$TEAM" "$RUN_CANCEL" >/dev/null || warn "the cancel was refused; the run may have ended first"
ST_CANCEL="$(wait_finished "$RUN_CANCEL")"
info "$RUN_CANCEL: $ST_CANCEL"

step "a hub run that delegates"
waited=$((SECONDS - AGENTS_AT))
if [ "$waited" -lt "$GRAPH_DRAIN_SECONDS" ]; then
	pause $((GRAPH_DRAIN_SECONDS - waited)) "new agents reach the team's graph within a minute"
fi
S_GROUP1="$(session_id "$CH1_NAME" eng-support)"
RUN_HUB="$(start_run "the hub (ops-lead) in the $CH1_NAME room" \
	"Nightly export job has failed three nights running: 'export: write chunk 412/980: s3 PutObject: RequestTimeout' after 30 minutes. The bucket moved to another region last week. Have log-reader diagnose the error, then reply with the cause and the fix in five lines." \
	--session "$S_GROUP1")"
info "$RUN_HUB: $(wait_finished "$RUN_HUB")"

delegated() {
	[ "$DRY_RUN" -eq 1 ] && return 0
	oa runs events "$TEAM" "$1" -o json 2>/dev/null |
		jq -e -s 'any(.[]; .event == "tool_call" and .data.name == "delegate")' >/dev/null
}
DELEGATED_RUN=""
if delegated "$RUN_HUB"; then
	DELEGATED_RUN="$RUN_HUB"
	info "the hub delegated"
else
	# As the hub, delegation needs the team's graph; run as itself, ops-lead
	# delegates to the agents it lists, which needs nothing else.
	warn "the hub did not delegate (no graph on this brain, or the model chose not to); asking ops-lead directly"
	RUN_DELEGATE="$(start_run "ops-lead as itself" ops-lead \
		"Delegate this to log-reader and report its answer in three lines: 'export: write chunk 412/980: s3 PutObject: RequestTimeout' after 30 minutes, three nights running, since the bucket moved to another region." \
		--session "$S_GROUP1")"
	info "$RUN_DELEGATE: $(wait_finished "$RUN_DELEGATE")"
	if delegated "$RUN_DELEGATE"; then
		DELEGATED_RUN="$RUN_DELEGATE"
		info "ops-lead delegated"
	else
		warn "ops-lead did not delegate either; see: oa runs events $TEAM $RUN_DELEGATE"
	fi
fi

step "a run left running"
S_T2004="$(session_id "$CH2_NAME" ops-alerts:t-2004)"
RUN_LIVE="$(start_run "runbook-writer in thread t-2004" runbook-writer \
	"Write a complete runbook for a Kafka consumer group stuck rebalancing on one partition (billing-events, partition 7, lag over 1M). Cover: detection, the five most likely causes each with how to confirm it, safe remediation for each, when to escalate, and a post-incident checklist. Use the incident-postmortem skill for the follow-up section. Be thorough; this replaces three outdated wiki pages." \
	--session "$S_T2004")"
LIVE_STATUS="$(wait_until "$RUN_LIVE" running succeeded failed cancelled)"
info "$RUN_LIVE: $LIVE_STATUS"

# ---------------------------------------------------------------- summary

step "summary"
count_sessions() {
	oa_json sessions list "$TEAM" --channel "$1" --limit 100 | jq '[.items[]?] | length'
}
info "team:          $TEAM"
info "agents:        ops-lead (hub), log-reader, runbook-writer, release-captain, docs-scout, statuspage-sync, changelog-digest"
info "mcp servers:   deepwiki (discovered unless the brain cannot reach it), statuspage (discovery failed), runbooks-fs (command)"
info "skills:        incident-postmortem (uploaded)${IMPORTED_SKILL:+, $IMPORTED_SKILL (imported)}"
info "channels:      $CH1_NAME ($(count_sessions "$CH1_NAME") sessions), $CH2_NAME ($(count_sessions "$CH2_NAME") sessions)"
info "pending:       u-erik, u-farah, u-gwen on $CH1_NAME; u-jonas, u-kemi on $CH2_NAME"
info "deliveries:    $POSTED posted, $REFUSED not accepted"
info "runs:"
info "  meant to succeed  $RUN_OK1 (log-reader): $ST_OK1"
info "  meant to succeed  $RUN_OK2 (runbook-writer): $ST_OK2"
info "  meant to fail     $RUN_FAIL_MCP (statuspage-sync, server unreachable): $ST_FAIL_MCP"
info "  meant to fail     $RUN_FAIL_BUDGET (changelog-digest, token budget): $ST_FAIL_BUDGET"
info "  cancelled         $RUN_CANCEL (release-captain): $ST_CANCEL"
info "  delegated         ${DELEGATED_RUN:-none - see the warnings above}"
info "  left running      $RUN_LIVE (runbook-writer), status when this script ended: $LIVE_STATUS"
info ""
info "check them with: oa runs get $TEAM <run>   and   oa channels senders pending $TEAM $CH1_NAME"
if [ "$DRY_RUN" -eq 1 ]; then
	info "(dry run: nothing was sent)"
fi
