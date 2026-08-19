# deployment — run the stack locally.
#
#   make           list targets
#   make up        dependencies only, brain on your machine
#   make staging   everything in containers, OIDC, published image
#
# Every target is a docker compose invocation that is otherwise long enough to
# be retyped wrongly. The compose files hold the actual configuration; this
# file only names the combinations that make sense.

COMPOSE := docker compose -f docker-compose.yml

# Three overlays, composed by the targets below.
#
#   IMAGE runs the published image instead of building one, which is the
#   difference between testing what CI produced and testing your own tree.
#   OIDC adds authentik. PROFILE opts the brain and its migrate step in —
#   without it the stack is dependencies only.
IMAGE   := -f docker-compose.image.yml
OIDC    := -f docker-compose.authentik.yml
BAO     := -f docker-compose.openbao.yml
PROFILE := --profile brain

# The bootstrap has to reach a running OpenBao before its AppRole exists, and
# the overlay's `:?` guards are interpolated for the whole file before compose
# looks at which service was asked for. These get past interpolation; nothing
# below ever starts the brain with them.
# via env, not a bare assignment prefix: BAO_EXEC is expanded inside a shell
# variable in bao-approle, and there a leading VAR=value is read as a command
# name rather than an assignment.
BOOTSTRAP := env OPENARITY_SECRETS_APPROLE_ID=bootstrap OPENARITY_SECRETS_APPROLE_SECRET=bootstrap

# Stops short of the service name on purpose: docker compose exec takes its
# flags before it, so a -e appended after "openbao" becomes part of the
# command the container runs rather than an environment variable.
BAO_EXEC := $(BOOTSTRAP) $(COMPOSE) $(BAO) exec -T -e BAO_ADDR=http://127.0.0.1:8200
BAO_KEYS := openbao/keys/init-keys.json

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## up: dependencies only — Postgres, Redis, FalkorDB, OpenBao in dev mode
up:
	$(COMPOSE) up -d

## dev: dependencies and the brain, built from your working tree
dev:
	$(COMPOSE) $(PROFILE) up -d --build

## image: dependencies and the brain, from the image CI published
image:
	$(COMPOSE) $(IMAGE) $(PROFILE) up -d

## staging: image plus authentik — OIDC only, no development token
staging:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) up -d

## restart: recreate every running container, keeping volumes
restart:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) restart

## pull: fetch a newer published image without restarting anything
pull:
	$(COMPOSE) $(IMAGE) $(PROFILE) pull

## ps: what is running, and on which ports
ps:
	@$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) ps

## logs: follow the brain — `make logs service=authentik-server` for another
logs: service ?= brain
logs:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) logs -f $(service)

## down: stop everything. Volumes survive, so data does too
down:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) down --remove-orphans

# Named destroy rather than `clean`: it deletes the databases, authentik's
# configuration and every secret in OpenBao. There is no undo and nothing
# warns you, so the target says what it does.
## destroy: down, and delete every volume — databases, authentik, OpenBao
destroy:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) down --remove-orphans --volumes

# ---------------------------------------------------------------------------
# A real OpenBao. See OPENBAO.md — these three run once, in this order, and
# then never again.

## bao-up: start the real OpenBao (file storage, auto-unsealing)
bao-up:
	$(BOOTSTRAP) $(COMPOSE) $(BAO) up -d openbao

## bao-init: initialise a fresh OpenBao and save its recovery keys
bao-init:
	@test -f openbao/keys/unseal.key || { \
	  echo "openbao/keys/unseal.key is missing. Create it first:"; \
	  echo "  openssl rand -out deployment/openbao/keys/unseal.key 32"; \
	  exit 1; }
	@test $$(wc -c < openbao/keys/unseal.key) -eq 32 || { \
	  echo "openbao/keys/unseal.key must be exactly 32 bytes — the static seal"; \
	  echo "takes an AES-256 key and rejects any other length."; exit 1; }
	@test ! -f $(BAO_KEYS) || { \
	  echo "$(BAO_KEYS) exists — refusing to re-init."; \
	  echo "Re-initialising a store that holds secrets orphans every one of them."; \
	  exit 1; }
	@# -recovery-shares, not -key-shares. Under any auto-unseal OpenBao rejects
	@# secret_shares outright and returns recovery keys instead; unseal_keys_b64
	@# comes back as [], so anything grepping for it gets null.
	@# Via a temporary file. A redirection straight to $(BAO_KEYS) creates it
	@# before the command runs, so a failed init leaves an empty file behind
	@# and the refusal above then blocks every retry.
	@$(BAO_EXEC) openbao bao operator init -recovery-shares=1 -recovery-threshold=1 \
	  -format=json > $(BAO_KEYS).tmp || { rm -f $(BAO_KEYS).tmp; exit 1; }
	@chmod 600 $(BAO_KEYS).tmp
	@mv $(BAO_KEYS).tmp $(BAO_KEYS)
	@echo "wrote deployment/$(BAO_KEYS) — recovery keys and the root token."
	@echo "Back it up somewhere that is not a backup of the data volume."

## bao-status: is OpenBao initialised, and is it sealed?
bao-status:
	@$(BAO_EXEC) openbao bao status || true

## bao-approle: enable KV v2 and mint the brain's AppRole — prints two .env lines
bao-approle:
	@test -f $(BAO_KEYS) || { echo "run make bao-init first"; exit 1; }
	@ROOT=$$(jq -r .root_token $(BAO_KEYS)); \
	  BAO="$(BAO_EXEC) -e BAO_TOKEN=$$ROOT openbao bao"; \
	  $$BAO secrets enable -path=secret kv-v2 >/dev/null 2>&1 || true; \
	  $$BAO auth enable approle >/dev/null 2>&1 || true; \
	  $$BAO policy write brain /openbao/config/policy-brain.hcl >/dev/null; \
	  $$BAO write auth/approle/role/brain token_policies=brain \
	    token_ttl=1h token_max_ttl=4h >/dev/null; \
	  echo "# Add these to deployment/.env — they are credentials, not config."; \
	  echo "OPENARITY_SECRETS_APPROLE_ID=$$($$BAO read -field=role_id auth/approle/role/brain/role-id)"; \
	  echo "OPENARITY_SECRETS_APPROLE_SECRET=$$($$BAO write -f -field=secret_id auth/approle/role/brain/secret-id)"

## bao: dependencies and the brain against the real OpenBao
bao:
	$(COMPOSE) $(BAO) $(PROFILE) up -d --build

.PHONY: help up dev image staging restart pull ps logs down destroy \
        bao-up bao-init bao-status bao-approle bao
