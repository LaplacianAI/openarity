# OpenBao

A secret store the brain reads channel credentials from. Postgres holds a
reference; the value only ever exists here.

`docker-compose.yml` runs OpenBao in **dev mode** — in memory, unsealed, root
token `dev-root`, everything lost on restart. That is right for a laptop and
wrong for anything that has to survive a reboot. This document is about the
other one: `docker-compose.openbao.yml`, an overlay with file storage and a
seal that opens itself.

## Why OpenBao and not Vault

Vault is BUSL-1.1 — source-available, not OSI-licensed. OpenBao is the MPL-2.0
Linux Foundation fork, and it is API compatible down to the `X-Vault-Token`
header, so the brain's client works against either with no code change. The
binary is `bao`, and it accepts `BAO_ADDR`/`BAO_TOKEN` as well as the `VAULT_*`
names.

The environment variables are *not* interchangeable in the image, though.
`BAO_DEV_ROOT_TOKEN_ID` sets the dev root token; `VAULT_DEV_ROOT_TOKEN_ID`
leaves the server running with a randomly generated one, which looks identical
until every request comes back 403.

## Setup, once

```sh
# 1. The seal key. Exactly 32 bytes — the static seal takes an AES-256 key and
#    rejects any other length.
openssl rand -out deployment/openbao/keys/unseal.key 32
chmod 600 deployment/openbao/keys/unseal.key

cd deployment
make bao-up        # start the real OpenBao
make bao-init      # once, ever — writes openbao/keys/init-keys.json
make bao-approle   # prints two lines for .env
```

Paste those two lines into `deployment/.env`, then either:

```sh
make bao           # dependencies and the brain, against the real OpenBao
make staging       # the whole stack: published image, OIDC, real OpenBao
```

Those two are the targets that include this overlay. `make up`, `make dev` and
`make image` deliberately use the in-memory OpenBao, so do not mix them with a
running real one — see the troubleshooting entry at the bottom.

On Linux the container runs as uid 100 and cannot read a `0600` file owned by
you. Give it the key without widening the mode:

```sh
sudo chown 100:1000 deployment/openbao/keys/unseal.key
```

Docker Desktop on macOS remaps bind mounts, so this is not needed there.

After the first setup, restarts need nothing. `docker restart`, `compose down`
and a host reboot all come back **unsealed with no command run** — that is the
entire point of the seal stanza.

## What `make bao-init` gives you

`init-keys.json` holds the **root token** and one **recovery key**. Both are a
full compromise. It is git-ignored along with the seal key, and `bao-init`
refuses to run a second time — re-initialising a store that already holds
secrets orphans every one of them.

Recovery keys are not unseal keys. Under any auto-unseal, OpenBao rejects
`-key-shares` outright and returns recovery keys instead; `unseal_keys_b64`
comes back as `[]`. They cannot unseal anything — they authorise
`operator generate-root` and rekey. Anything that greps `unseal_keys_b64` out
of this file gets `null`.

## What the brain may do

`openbao/policy-brain.hcl`, and it is deliberately small:

| Path | Capability |
| --- | --- |
| `secret/data/teams/+/channels/+` | `read`, `create`, `update` |
| `secret/metadata/teams/+/channels/+` | `delete` |
| `auth/token/renew-self` | `update` |

`+` matches exactly one path segment. That is the restriction: the brain
reaches a channel's secret in any team and nothing else under `teams/` — not a
team-level secret, not a future `teams/<id>/tokens/*`, not a path one segment
deeper. Verified against a running OpenBao rather than taken from the
documentation.

Two paths because KV v2 splits them. `Get` and `Put` go to `secret/data/…`;
`Delete` goes to `secret/metadata/…`, and only the metadata delete removes
every version — a disconnected channel's signing secret should not survive in
history. Granting `delete` there grants neither `read` nor `list`, so version
history stays closed.

Still no `list`: reading requires knowing the path already, while `list` on a
KV path hands over every team id.

This policy used to be read-only, on the reasoning that registering a channel
would get its own role. It will not while the brain is the thing serving
`POST /teams/{id}/channels` — a second credential held by the same process is
not a boundary, because the process holds both. The boundary is the path shape
above, and who may ask for a write is RBAC's `channel:write` on the route.

If channel registration ever moves out of the brain, the split becomes real
and worth making. A shorter-lived alternative, if the threat model ever calls
for it, is a token minted per write with a short TTL rather than one held for
the process's lifetime.

The common shortcut is a root token in an environment variable. That is what
this exists to avoid.

### CI runs against this file

`apps/brain/internal/secrets/policy_integration_test.go` reads
`policy-brain.hcl` off disk, binds an AppRole to it, and drives the real store
through it: register a channel, read the secret back, disconnect it. Then it
asks for eight things the comments above claim are refused — a sibling kind of
secret, a path below a channel, the team itself, two kinds of list, a soft
delete, version history, and `sys/` — and requires a 403 for each.

It exists because that check used to be missing, and the gap was not what it
looked like. CI runs OpenBao in dev mode, which sounds like it would skip
policy evaluation entirely; it does not — dev mode enforces policy normally
and merely hands out a root token. The real gap was that every secrets test
built its AppRole from a mount-wide fixture, so the file that ships was never
loaded by anything until it reached a server. Restoring the read-only policy
this document describes above now fails in CI with the same 403 it produced in
staging.

One consequence worth reading before editing the file: the `metadata` rule
shadows any list grant at the parent path, because a LIST is checked with a
trailing slash and `+` matches the empty segment that creates. The comments in
the file carry the measurements.

## The seal ladder

| Deployment | Seal | The key lives |
| --- | --- | --- |
| Local development | dev mode | nowhere — in memory |
| Self-hosted, one host | `seal "static"` | a `file://` on the host, `0600` |
| Kubernetes, self-managed | `seal "static"` | a Secret, mounted as a file or `env://` |
| Kubernetes on a cloud | `seal "awskms"` / `gcpckms` | the cloud KMS, never in the cluster |
| Many clusters | `seal "transit"` | one OpenBao unseals the others |

The same stanza serves every rung. Only where the key lives changes, which is
why `bao.hcl` is also the Kubernetes config.

## What the static seal costs

**The key sits on the same host as the encrypted data, so a whole-host
compromise is a full compromise.** Say it plainly rather than discovering it
during an incident.

It still buys what the store is for: a Postgres dump leaks no secret, the
brain holds a scoped policy rather than everything, there is an audit trail,
and a credential can be rotated without a redeploy.

**Back `deployment/openbao/keys/` up somewhere that is not a backup of the
data volume.** Together in one archive they are plaintext.

Move up a rung when the host stops being the trust boundary — a cloud KMS key
means a stolen disk image is inert.

## Rotating the seal key

`n-1` to `n`: add `previous_key_id` and `previous_key` to the `seal` stanza
pointing at the old file, restart, and drop them once re-encryption has run.
Removing the old key before that leaves the store unopenable.

## Troubleshooting

**`Sealed true` after a restart.** The key file is not reaching the container.
Check that `deployment/openbao/keys/unseal.key` is exactly 32 bytes
(`wc -c`) and that the `./openbao/keys` mount is present and readable by
uid 100.

**`failed to persist keyring: mkdir: permission denied` on init.** The data
volume is mounted somewhere the container does not own. The image declares
`/openbao/file` and owns it as uid 100; Docker creates any other mount point
as root.

**Every read returns 404 on a fresh server.** KV v2 is not mounted. Dev mode
mounts it at `secret/` automatically and a file-storage server does not, so
`bao secrets enable -path=secret kv-v2` has to run — `make bao-approle` does
it. That 404 is indistinguishable from a missing secret, which is what makes
this one expensive to find.

**`docker compose up` fails with "required variable
OPENARITY_SECRETS_APPROLE_ID is missing a value".** Working as intended: the
overlay means a real secret store, and a brain with no credentials would fall
back to the in-memory one and verify nothing. Run `make bao-approle`.

**`image with reference openbao/openbao:… does not match the specified
platform`, on a target that was working.** Compose is trying to *replace* the
running OpenBao, and the platform mismatch is what stopped it — it is a
symptom, not the problem.

`up`, `dev` and `image` define `openbao` in dev mode. Running one of them while
the real OpenBao is up asks compose to recreate that container as the
in-memory one. The data volume survives untouched, so nothing looks lost;
every read simply returns 404, which is indistinguishable from a secret that
was never written.

Use the target that matches the OpenBao you are running — `make bao` for the
local loop, `make staging` for the full stack. Both include the overlay. If you
genuinely want dev mode back, remove the container first:

```sh
docker rm -f openarity-openbao-1
```

**The brain exits at startup with `secret store unavailable`.** Also working
as intended — it refuses to serve against a store it cannot reach, rather than
passing its probes and failing the first webhook of every channel.
