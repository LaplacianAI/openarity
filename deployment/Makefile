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
PROFILE := --profile brain

.DEFAULT_GOAL := help

## help: list available targets
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'

## up: dependencies only — Postgres, Redis, FalkorDB, Vault
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
# configuration and every secret written to the dev Vault. There is no undo
# and nothing warns you, so the target says what it does.
## destroy: down, and delete every volume — databases, authentik, Vault
destroy:
	$(COMPOSE) $(OIDC) $(IMAGE) $(PROFILE) down --remove-orphans --volumes

.PHONY: help up dev image staging restart pull ps logs down destroy
