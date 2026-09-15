---
title: Docker
weight: 1
---

One command, from a clone to a dashboard you can sign in to.

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/deployment
make start-docker
```

It asks which identity provider you want, generates everything that is
missing, brings the stack up, waits until the brain answers, and prints the
address and the sign-in.

Needs Docker, `openssl`, `python3` and `curl`. Nothing else — not Go, not
Node. The brain comes from the published image unless you ask for otherwise.

## What it generates

Only what is absent. Run it twice and the second run changes nothing, apart
from minting a fresh `secret_id` — a credential with a lease, so reissuing it
is the cheap path to a stack whose store was rebuilt.

| | |
| --- | --- |
| `deployment/.env` | from `.env.example`, with every secret filled |
| `BIND_ADDR` | this machine's LAN address |
| `DEX_PASSWORD_HASH` | a passphrase, hashed, printed once |
| `openbao/keys/unseal.key` | 32 random bytes |
| `openbao/keys/init-keys.json` | `bao init` |
| the brain's AppRole | minted, written into `.env` |
| authentik's OAuth provider | over its API, if you chose authentik |

None of those are in git, which is why every other `make` target in that
directory fails on a fresh clone and this one does not.

## The two providers

Openarity speaks OIDC to exactly one provider. That is deliberate: an
organisation wanting Google *and* GitHub *and* LDAP puts an identity provider
in front and lets it federate, rather than teaching every service about every
provider.

| | Dex | Authentik |
| --- | --- | --- |
| Containers | one | three, and its own Postgres |
| Configuration | a file committed to this repo | a database, created over an API |
| Admin UI | none | yes |
| Federates Google, GitHub, LDAP | yes, by editing its config | yes, by clicking |

Dex is the default and the one to take unless you are specifically testing
federation. Its configuration is a file in git, so a fresh clone gets the same
provider and there is nothing to drift.

They are alternatives, not a pair. A stack with two issuers is a stack where
the brain trusts one and rejects the other's tokens.

```sh
make start-docker PROVIDER=dex        # do not ask
make start-docker PROVIDER=authentik
make start-docker BUILD=1             # build the brain from this tree
```

## The address, and why it is not localhost

A token carries the issuer that minted it, and the brain compares that against
`OPENARITY_OIDC_ISSUER` **as a string**. So everything that talks to the
identity provider — your browser, and the brain — has to reach it at the same
address.

On a laptop that rules out `127.0.0.1`, because a container's loopback is not
your machine's, and `host.docker.internal` does not resolve on a macOS host
either. So the stack publishes on your LAN address.

{{< callout type="warning" >}}
**The identity provider and Postgres are reachable from your network** for as
long as the stack is up. That is the trade for a browser login that works. On
an untrusted network, run the brain on the host instead — see the
[platform quick start](/docs/platform/quick-start) — and leave `BIND_ADDR`
alone.
{{< /callout >}}

`BIND_ADDR` is an address to *bind*, and `0.0.0.0` is a perfectly good one: it
publishes on every interface. It is not an address anything can *reach*, so no
issuer, callback or health check may contain it — `start-docker` uses the LAN
address for every URL it writes when the bind address is a wildcard.

## What comes up

| | |
| --- | --- |
| Dashboard | `21120`, under `/ui` |
| Brain API | `21120` |
| Brain webhooks | `21121` |
| Identity provider | `5556` for dex, `9000` for authentik |
| Postgres | `15432` |
| OpenBao | not published — the brain reaches it inside the network |
| MinIO | `19000`, console on `19001` |

The dashboard is not a separate service. The brain serves it from its own
binary, so there is nothing extra to start and nothing to keep in sync.

Every port moves: `API_PORT`, `POSTGRES_PORT`, `REDIS_PORT`, `FALKORDB_PORT`
and `DEX_PORT` are read from `.env`. Only the host side moves — the containers
still talk to each other on the standard ports.

## What it is, once it is up

A staging stack, not a development one: OIDC only with the shared token
refused outright, the real OpenBao rather than the in-memory one, and MinIO
for attachments. Those are the settings that decide whether anything survives
a restart, so the one command that new people run is the one that gets them
right.

```sh
oa context create local --server http://<the printed address>:21120
oa login
oa whoami
```

A first login creates the user row with no memberships. Whoever is listed in
`OPENARITY_SUPER_ADMINS` can then create a team and add the first member —
`start-docker` sets that for you, to dex's committed subject or to `akadmin`.

## Stopping and starting again

```sh
cd deployment
make down       # stop everything; volumes survive, so data does
make ps         # what is running
make logs       # follow the brain; make logs service=dex
make destroy    # and delete every volume — databases, the provider, OpenBao
```

`make destroy` has no undo and nothing warns you, which is why it is not
called `clean`.

## When the sign-in fails

Three failures account for nearly all of them, and none says what is wrong.

**"Failed to fetch" when you click sign in.** The dashboard is being served at
an origin the provider will not talk to. Authentik derives the origins it
answers CORS for from the redirect URIs registered on the provider and from
nothing else, so an unregistered origin gets a `200` with the header quietly
omitted — which a browser reports as a network error. `start-docker` registers
`localhost`, `127.0.0.1` and the LAN address; if you reach the dashboard by
some other name, register that one too.

**The brain will not start, and its log says `connection refused` against the
provider's address.** The issuer it was configured with is not somewhere it can
reach. `docker compose logs brain` says which address it tried.

**The code page 404s during `oa login` against authentik.** Authentik ships no
flow with the *Stage Configuration* designation and its brand points at none,
so the device flow hands you a code and then has nowhere to show it.
`start-docker` creates that flow; a provider set up by hand needs it too.

## Doing it by hand

Every step `start-docker` takes is in [deployment/QUICKSTART.md][quickstart],
with the reason for each — worth reading before running this against
infrastructure that is not yours, because minting an AppRole writes a policy
and a role into a secret store you may share with something else.

[quickstart]: https://github.com/LaplacianAI/openarity/blob/main/deployment/QUICKSTART.md
