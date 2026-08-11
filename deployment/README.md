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

To run the brain in a container too:

```sh
docker compose -f deployment/docker-compose.yml --profile brain up -d --build
```

## Authentication

The brain speaks OIDC to exactly one provider. That is deliberate: an
organisation wanting Google *and* GitHub *and* LDAP puts an identity provider in
front and lets it federate, rather than teaching every service about every
provider.

Authentik is the reference, not a dependency. Keycloak, Okta, Entra, Auth0 and
Dex all work — the brain only needs discovery at
`<issuer>/.well-known/openid-configuration`.

```sh
cd deployment
cp authentik.env.example authentik.env     # then fill in both secrets
docker compose -f docker-compose.yml -f docker-compose.authentik.yml up -d
```

Then, in Authentik at <http://127.0.0.1:9000>:

1. Create an **OAuth2/OpenID Provider**. Note its client ID.
2. Create an **Application** with slug `openarity` and attach that provider.
3. Point the brain at it:

```sh
export OPENARITY_OIDC_ENABLED=true
export OPENARITY_OIDC_ISSUER='http://127.0.0.1:9000/application/o/openarity/'
export OPENARITY_OIDC_AUDIENCE='<the client ID>'
export OPENARITY_SUPER_ADMINS='<your sub claim>'
```

Swapping providers is those three settings and nothing else — no code, no
migration. `OPENARITY_SUPER_ADMINS` matches the `sub` claim, not an email
address, and the value differs between providers even for the same person.

**The discovery document is fetched once, at startup.** An issuer that is
unreachable then is a failed boot rather than a degraded service — the brain
refuses to start rather than accept tokens it cannot verify.

## Kubernetes

```sh
kubectl apply -f deployment/k8s/
```

Read `secret.yaml` before applying it: the committed file is a template with a
placeholder DSN, and it is the one file here you should not use as-is.

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

The images referenced are `ghcr.io/laplacianai/openarity-brain:latest`. Nothing
publishes them yet; build and push your own until that exists, and pin a tag
rather than tracking `latest` once it does.
