---
title: Quick start
weight: 1
---

Needs Go 1.26.6 and a Postgres 13 or newer you can reach. 13 is where
`gen_random_uuid()` became built-in, which the first migration uses. Running the
*tests* needs 18.

{{% steps %}}

### Clone and create the schema

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/apps/brain

go run ./cmd/brain migrate up
```

Migrations run behind an advisory lock, so two instances starting at once is
safe. `brain migrate down` rolls the last one back.

### Give it a way to authenticate callers

```sh
export OPENARITY_DEV_TOKEN=letmein
export OPENARITY_SUPER_ADMINS=dev
```

The service refuses to start with no authentication configured rather than
serving an open API. For a real provider, set `OPENARITY_OIDC_ENABLED` and
`OPENARITY_OIDC_ISSUER` instead.

### Serve

```sh
go run ./cmd/brain
```

Two listeners come up — the API on `21120` and webhooks on `21121`. Ctrl-C
shuts both down gracefully.

### Talk to it

```sh
curl -s 127.0.0.1:21120/healthz    # ok
curl -s 127.0.0.1:21120/readyz     # ready, or 503 if Postgres is unreachable

curl -s -H 'Authorization: Bearer letmein' 127.0.0.1:21120/whoami
# {"kind":"dev","issuer":"dev","subject":"dev","teams":[]}
```

Everything but the probes needs a token. Without one: 401.

{{% /steps %}}

## The other commands

`brain reap` completes deletions that Postgres cannot. Deleting a team removes
its rows, but not the attachment bytes in the object store or the secrets in
the vault — there is no transaction spanning them. Each deletion records what it
owes, and `reap` settles it, destroying a deleted team's key first so every one
of its attachments becomes unreadable immediately. It is idempotent, safe to run
twice at once, and exits non-zero when an erasure has been outstanding for a
day.

`brain worker` is what normally runs it. It hosts the background work on a
schedule, replays the ticks it missed while it was down, sweeps once at startup
so a fresh deployment is not idle for an interval, and refuses to start if the
secret store cannot delete.

{{< callout type="warning" >}}
A deployment that never runs the worker never erases anything outside Postgres.
{{< /callout >}}
