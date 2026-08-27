# deployment — run the stack locally.
#
#   make           list targets
#   make up        dependencies only, brain on your machine
#   make staging   everything in containers, OIDC, published image, real OpenBao
#
# Every target is a docker compose invocation that is otherwise long enough to
# be retyped wrongly. The compose files hold the actual configuration; this
# file only names the combinations that make sense.

COMPOSE := docker compose -f docker-compose.yml

# Three overlays and a profile, composed by the targets below.
#
#   IMAGE runs the published image instead of building one, which is the
#   difference between testing what CI produced and testing your own tree.
#   OIDC adds authentik. BAO swaps the in-memory OpenBao for the one with file
#   storage and a seal that opens itself. OBJECTS adds MinIO and creates the
#   bucket. PROFILE opts the brain and its migrate step in — without it the
#   stack is dependencies only.
#
#   OBJECTS is left out of the everyday local targets on purpose. Attachments
#   are not built yet, and a store nothing writes to is a container to remember
#   to stop. staging includes it: OBJECTS_BACKEND=memory is refused outside
#   development, so a staging brain needs a real store to point at or it does
#   not start.
#
#   A target that omits BAO defines `openbao` in dev mode, so running one
#   while the real OpenBao is up asks compose to replace it. up, dev and image
#   are the fast local loop and use dev mode deliberately; `make bao` is the
#   same loop against the real one.
IMAGE   := -f docker-compose.image.yml
OIDC    := -f docker-compose.authentik.yml
BAO     := -f docker-compose.openbao.yml
OBJECTS := -f docker-compose.objects.yml
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

## objects: add MinIO and create the bucket — for the object store tests
#
# The live tests in internal/objects/s3 skip unless they are pointed at a
# store, which this prints the settings for.
objects:
	$(COMPOSE) $(OBJECTS) up -d minio minio-bucket
	@printf '\n%s\n%s\n%s\n%s\n\n' \
	  "export BRAIN_TEST_S3_ENDPOINT=http://127.0.0.1:19000" \
	  "export BRAIN_TEST_S3_BUCKET=$${OBJECTS_BUCKET:-openarity}" \
	  "export BRAIN_TEST_S3_ACCESS_KEY=$${OBJECTS_ACCESS_KEY:-openarity}" \
	  "export BRAIN_TEST_S3_SECRET_KEY=$${OBJECTS_SECRET_KEY:-openarity-dev-secret}"

## dev: dependencies and the brain, built from your working tree
dev:
	$(COMPOSE) $(PROFILE) up -d --build

## image: dependencies and the brain, from the image CI published
image:
	$(COMPOSE) $(IMAGE) $(PROFILE) up -d

# The only target that runs every production choice at once: the published
# image, OIDC instead of the development token, and a real OpenBao instead of
# the in-memory one. The last is not optional here — a production-like stack
# whose secret store forgets everything on restart is the same trap in a
# different place, and running this alongside a real OpenBao without $(BAO)
# tries to replace it with the dev-mode container.
#
# It refuses to start without OPENARITY_SECRETS_APPROLE_ID and _SECRET, which
# is the point. See OPENBAO.md; `make bao-approle` prints them.
## staging: published image, authentik, real OpenBao and MinIO — needs .env
staging:
	$(COMPOSE) $(OIDC) $(IMAGE) $(BAO) $(OBJECTS) $(PROFILE) up -d

## restart: recreate every running container, keeping volumes
restart:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) restart

## pull: fetch a newer published image without restarting anything
pull:
	$(COMPOSE) $(IMAGE) $(PROFILE) pull

## ps: what is running, and on which ports
ps:
	@$(COMPOSE) $(OIDC) $(OBJECTS) $(IMAGE) $(PROFILE) ps

## logs: follow the brain — `make logs service=authentik-server` for another
logs: service ?= brain
logs:
	$(COMPOSE) $(OIDC) $(OBJECTS) $(IMAGE) $(PROFILE) logs -f $(service)

## down: stop everything. Volumes survive, so data does too
down:
	$(COMPOSE) $(OIDC) $(OBJECTS) $(IMAGE) $(PROFILE) down --remove-orphans

# Named destroy rather than `clean`: it deletes the databases, authentik's
# configuration and every secret in OpenBao. There is no undo and nothing
# warns you, so the target says what it does.
## destroy: down, and delete every volume — databases, authentik, OpenBao
destroy:
	$(COMPOSE) $(OIDC) $(OBJECTS) $(IMAGE) $(PROFILE) down --remove-orphans --volumes

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
