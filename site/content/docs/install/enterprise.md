---
title: A deployment
weight: 2
---

Compose on one host, or Kubernetes. The brain in a container, Postgres and an
identity provider you already run, and a worker that has to be running or
nothing outside Postgres is ever erased.

Everything here lives in [`deployment/`](https://github.com/LaplacianAI/openarity/tree/main/deployment),
whose README is the reference. This page is the shape of the decision.

## The rule that explains most of the confusion

The brain compares a token's `iss` claim against `OPENARITY_OIDC_ISSUER` **as a
string**. Your browser and the brain must therefore reach the identity provider
at the *same* address.

A container's `127.0.0.1` is its own loopback, not the host's, and
`host.docker.internal` does not resolve on macOS hosts. On a laptop that means
the machine's LAN address, set once as `BIND_ADDR` in `deployment/.env`; in a
real deployment it means the name the provider is published under.

Getting this wrong produces a login that completes in the browser and a 401
from the brain, which reads like a broken token rather than a mismatched
string.

## Compose

```sh
cd deployment
make            # list targets
make up         # dependencies only — the normal loop, brain on your machine
make image      # and the brain, from the image CI publishes
make staging    # image plus authentik: OIDC only, no development token
make dex        # a lighter provider, from a committed config
make down       # stop, keeping the data
make destroy    # and delete every volume
```

`make image` runs `ghcr.io/laplacianai/openarity-brain`. Compose runs a
`migrate` service to completion before the brain starts — without it the brain
comes up healthy against an empty schema and every route returns 500.

The identity provider is a choice, not a dependency. Authentik is the
reference; dex is the lighter one and is what a personal install uses.
Keycloak, Okta, Entra and Auth0 all work — the brain needs discovery at
`<issuer>/.well-known/openid-configuration` and nothing else.

{{< callout type="warning" >}}
`OPENARITY_ENVIRONMENT=staging` or `production` refuses to start with a
development token configured, and stops serving `/docs` and `/openapi.yaml`.
That is the point of the setting: those are development affordances.
{{< /callout >}}

## Kubernetes

```sh
kubectl apply -f deployment/k8s/
```

Five manifests: `configmap.yaml`, `secret.yaml`, `service.yaml`,
`deployment.yaml` and `worker.yaml`. Read `secret.yaml` before applying it —
the committed file is a template with a placeholder DSN, and it is the one file
here you should not use as-is.

**Nothing here provides Postgres.** The manifests assume one exists at the host
in the DSN, whether that is a managed service or an operator.

Three things in `deployment.yaml` are decisions rather than defaults:

- **Migrations run as an init container in every pod.** `brain migrate up`
  takes a Postgres advisory lock, so replicas starting together serialise
  instead of racing, and no pod can serve traffic against a schema it has not
  applied.
- **Liveness never touches the database.** If it did, a Postgres blip would
  restart every pod at once and turn a recoverable outage into a crash loop.
  Readiness is the probe that checks it, so an affected pod leaves the Service
  and comes back on its own.
- **No CPU limit.** Throttling a latency-sensitive service to reclaim cycles it
  is not using costs tail latency and saves nothing.

{{< callout type="error" >}}
**These manifests have never run on a cluster.** They pass `kubeconform
-strict`, which checks them against the Kubernetes schema and nothing more — it
cannot tell you that an image pulls, that a probe passes, or that the init
container ordering behaves. The compose stack is the part that has been
exercised end to end.
{{< /callout >}}

For authentik on Kubernetes use the upstream Helm chart rather than anything
hand-written — it owns the database migrations, the worker and the outpost
lifecycle. The brain's side is three settings in `configmap.yaml`.

## The worker is not optional

Deleting a team removes its rows, but not the attachment bytes in the object
store or the secrets in the vault — there is no transaction spanning them. Each
deletion records what it owes, and `brain reap` settles it, destroying a
deleted team's key first so every one of its attachments becomes unreadable
immediately.

`brain worker` runs it on a schedule, sweeps once at startup so a fresh
deployment is not idle for an interval, replays ticks it missed while it was
down, and refuses to start if the secret store cannot delete. In compose it
belongs to the `brain` profile; in Kubernetes it is `worker.yaml`.

{{< callout type="warning" >}}
A deployment that never runs the worker never erases anything outside Postgres.
`reap` exits non-zero when an erasure has been outstanding for a day, which is
the alert worth having.
{{< /callout >}}

## Configuration

Every variable, its default, and whether anything reads it yet is on the
[configuration page](/docs/platform/configuration). Four matter more than the
rest in a deployment:

| | |
| --------------------------- | ------------------------------------------------ |
| `OPENARITY_POSTGRES_DSN`    | the only thing with no sensible default          |
| `OPENARITY_OIDC_ISSUER`     | must match the `iss` claim exactly, as a string  |
| `OPENARITY_SUPER_ADMINS`    | subjects, not emails; empty means nobody can act |
| `OPENARITY_ENVIRONMENT`     | what turns the development affordances off       |

## What is not here yet

No Helm chart, no operator, and no published manifest for anything but the
brain and its worker. The compose files are the exercised path; the Kubernetes
manifests are a starting point that has been checked for shape and not for
behaviour.
