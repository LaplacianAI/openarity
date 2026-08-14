# Deployment

Nothing here is required to develop the brain — `go run ./cmd/brain` against a
local Postgres is enough. These files exist so the dependencies are reproducible
and so a deployment is a starting point rather than a blank page.

## Local development

```sh
docker compose -f deployment/docker-compose.yml up -d
cd apps/brain
go run ./cmd/brain migrate up
OPENARITY_DEV_TOKEN=letmein OPENARITY_SUPER_ADMINS=dev go run ./cmd/brain
```

Ports match the defaults in `internal/config`, so nothing else needs setting.
FalkorDB, Redis and Vault are running but unused — they are here so the day one
of them is needed, it is configuration rather than archaeology.

If you already run Postgres or Redis on your machine, the published ports
collide. Every one of them can be moved without editing the file:

```sh
POSTGRES_PORT=15432 REDIS_PORT=16379 docker compose -f deployment/docker-compose.yml up -d
```

`POSTGRES_PORT`, `REDIS_PORT`, `FALKORDB_PORT`, `VAULT_PORT`, `API_PORT` and
`WEBHOOK_PORT` all work this way. Only the host side moves — the containers
still talk to each other on the standard ports.

To run the brain in a container too:

```sh
docker compose -f deployment/docker-compose.yml --profile brain up -d --build
```

That profile also runs a `migrate` service to completion first, the same shape
as the init container in `k8s/deployment.yaml`. Without it the brain comes up
healthy against an empty schema and every route returns 500.

## Authentication

The brain speaks OIDC to exactly one provider. That is deliberate: an
organisation wanting Google *and* GitHub *and* LDAP puts an identity provider in
front and lets it federate, rather than teaching every service about every
provider.

Authentik is the reference, not a dependency. Keycloak, Okta, Entra, Auth0 and
Dex all work — the brain only needs discovery at
`<issuer>/.well-known/openid-configuration`.

### Step 1 — pick an address before you start

A token carries the issuer that minted it, and the brain rejects one whose
issuer is not the value it was configured with. Everything that talks to
authentik — your browser, and the brain — must therefore reach it at the *same*
address.

On a laptop that rules out `127.0.0.1`: the browser's loopback is not the
container's. `host.docker.internal` does not resolve on macOS hosts either. Use
the machine's LAN address:

```sh
ipconfig getifaddr en0     # macOS, for example 192.168.1.4
hostname -I | cut -d' ' -f1  # Linux
```

Put it in `deployment/.env`. Every published port moves with it, and the
compose files still default to loopback for anyone who does not set it:

```sh
BIND_ADDR=192.168.1.4
```

This does expose authentik and Postgres to your network for as long as the
stack is up. That is the trade for a browser login that works; on an untrusted
network, run the brain on the host instead and leave `BIND_ADDR` alone.

### Step 2 — start authentik

```sh
cd deployment
cp .env.example .env                       # then fill in both secrets
docker compose -f docker-compose.yml -f docker-compose.authentik.yml up -d
```

The out-of-box setup flow (`/if/flow/initial-setup/`) **returns 404 in current
versions**. Bootstrap the admin instead, before the first start — these are
read once, when authentik initialises its database:

```sh
cat >> .env <<'EOF'
AUTHENTIK_BOOTSTRAP_PASSWORD=choose-something
AUTHENTIK_BOOTSTRAP_TOKEN=a-long-random-string
AUTHENTIK_BOOTSTRAP_EMAIL=you@example.com
EOF
```

That creates `akadmin` with that password, and an API token you can use
immediately. If authentik has already initialised, delete its volume and start
again — `docker volume rm openarity_authentik-database`.

### Step 3 — create the provider and application

The UI works, but three fields decide whether login succeeds and two of them
are easy to miss. Doing it over the API makes them explicit:

```sh
TOKEN=<AUTHENTIK_BOOTSTRAP_TOKEN>
HOST=http://192.168.1.4:9000

# The flows and the signing certificate authentik ships with.
curl -s -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/v3/flows/instances/?designation=authorization"
curl -s -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/v3/flows/instances/?designation=invalidation"
curl -s -H "Authorization: Bearer $TOKEN" \
  "$HOST/api/v3/crypto/certificatekeypairs/"

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/providers/oauth2/" -d '{
    "name": "openarity",
    "authorization_flow": "<default-provider-authorization-implicit-consent pk>",
    "invalidation_flow": "<default-provider-invalidation-flow pk>",
    "client_type": "public",
    "grant_types": ["authorization_code", "refresh_token"],
    "signing_key": "<the self-signed certificate pk>",
    "sub_mode": "user_username",
    "redirect_uris": [{"matching_mode": "strict", "url": "http://localhost:8080/callback"}]
  }'

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  "$HOST/api/v3/core/applications/" \
  -d '{"name": "Openarity", "slug": "openarity", "provider": <provider pk>}'
```

The three that matter:

- **`grant_types` defaults to `[]`**, and an empty list refuses every login with
  `invalid_request` / "The request is otherwise malformed". The log line that
  explains it is `Invalid grant_type for provider`, and it is only in the
  authentik container's log — the browser sees nothing useful.
- **`invalidation_flow` is required** and cannot be null.
- **`sub_mode` decides what `sub` contains.** The default, `hashed_user_id`,
  gives an opaque hash, so `OPENARITY_SUPER_ADMINS=akadmin` will never match and
  every privileged call returns 403. `user_username` puts the username there.
  Either is fine — just know which you chose before setting super admins.

A **public** client means no secret to distribute, which is what a CLI and a
browser both want. Authentik then requires PKCE, so a hand-built authorize URL
needs `code_challenge` and `code_challenge_method=S256`.

Confirm discovery before going further:

```sh
curl -s "$HOST/application/o/openarity/.well-known/openid-configuration" | jq .issuer
```

### Step 4 — point the brain at it

```sh
cat >> .env <<'EOF'
OPENARITY_OIDC_ENABLED=true
OPENARITY_OIDC_ISSUER=http://192.168.1.4:9000/application/o/openarity/
OPENARITY_OIDC_AUDIENCE=<the provider's client ID>
OPENARITY_SUPER_ADMINS=akadmin
OPENARITY_DEV_TOKEN=
EOF

docker compose -f docker-compose.yml -f docker-compose.authentik.yml --profile brain up -d
```

`OPENARITY_DEV_TOKEN=` empty turns the shared token off, so the only way in is a
real one. The issuer must match `jq .issuer` above **exactly**, trailing slash
included.

Swapping providers is those four settings and nothing else — no code, no
migration. `OPENARITY_SUPER_ADMINS` matches the `sub` claim, not an email
address, and the value differs between providers even for the same person.

### Step 5 — get a token

The brain is an API; nothing in it performs a login. Until the CLI exists, run
the authorization code flow by hand — or point any OIDC client at the values
above. `GET /whoami` is the quickest check that it worked:

```sh
curl -s -H "Authorization: Bearer $ACCESS_TOKEN" http://192.168.1.4:21120/whoami
```

A first login creates the user row with no memberships. Whoever is listed in
`OPENARITY_SUPER_ADMINS` can then create a team and add the first member.

### Making it reproducible

None of the above is code. A provider created through the API or the UI lives
only in that container's database, so a colleague cloning the repository starts
from nothing. Authentik's answer is **blueprints** — YAML applied at startup
from `/blueprints` — and moving this configuration into one is the right next
step before anyone else uses this stack.

**The discovery document is fetched once, at startup.** An issuer that is
unreachable then is a failed boot rather than a degraded service — the brain
refuses to start rather than accept tokens it cannot verify.

## Kubernetes

```sh
kubectl apply -f deployment/k8s/
```

Read `secret.yaml` before applying it: the committed file is a template with a
placeholder DSN, and it is the one file here you should not use as-is. Nothing
here provides Postgres — these manifests assume one already exists at the host
in the DSN, whether that is a managed service or an operator.

**These have never run on a cluster.** They pass `kubeconform -strict`, which
checks them against the Kubernetes schema and nothing more: it cannot tell you
that an image pulls, that a probe passes, or that the init container ordering
behaves. The compose stack is the part that has been exercised end to end.

Three things in `deployment.yaml` are decisions rather than defaults:

- **Migrations run as an init container in every pod.** `brain migrate up`
  takes a Postgres advisory lock, so replicas starting together serialise
  instead of racing. A pod therefore cannot serve traffic against a schema it
  has not applied.
- **Liveness never touches the database.** If it did, a Postgres blip would
  restart every pod simultaneously and turn a recoverable outage into a crash
  loop. Readiness is the probe that checks it, so an affected pod leaves the
  Service and returns on its own.
- **No CPU limit.** Throttling a latency-sensitive service to reclaim cycles it
  is not using costs tail latency and saves nothing.

For Authentik on Kubernetes, use the upstream Helm chart rather than anything
hand-written here — it owns the database migrations, the worker, and the
outpost lifecycle:

```sh
helm repo add authentik https://charts.goauthentik.io
helm install authentik authentik/authentik -f your-values.yaml
```

The brain's side is the same three settings as above, in `configmap.yaml`.

## What is not here yet

An Ingress, TLS, a HorizontalPodAutoscaler, a PodDisruptionBudget, and a
NetworkPolicy. All of them depend on the cluster they are going into — an
Ingress written against nginx is wrong on Gateway API, and a NetworkPolicy is
wrong wherever the CNI does not enforce it. They belong in whatever overlay or
chart wraps these manifests.

## Images

Every push to `main` that touches the brain publishes one, built from the
`Dockerfile` here by
[`publish-image.yml`](../.github/workflows/publish-image.yml):

```text
ghcr.io/laplacianai/openarity-brain:latest
ghcr.io/laplacianai/openarity-brain:sha-<commit>
```

**Pin the `sha-` tag in anything you deploy.** `latest` is a moving label — it
points at a different image tomorrow, which leaves no name for the one that was
working today. That is the whole reason both tags exist, and it is why
`k8s/deployment.yaml` referencing `:latest` is a placeholder rather than a
recommendation.

The workflow publishes and nothing more. It names no host and no environment,
because which tag a deployment follows, and when it picks one up, belongs to
whoever runs it.
