# Openarity

[![ci](https://github.com/LaplacianAI/openarity/actions/workflows/ci.yml/badge.svg)](https://github.com/LaplacianAI/openarity/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![go](https://img.shields.io/badge/go-1.26-00ADD8.svg)](go.work)

An open-source agent platform where agents, tools, skills and learnings are a
knowledge graph, not a list.

> **Early.** There is no release, no agent runtime and no stable API yet. What
> exists is the service skeleton described below. The design is still moving —
> open an issue before building anything substantial on top of it.

## The idea

Most agent frameworks pick tools by ranking a flat list. Openarity picks by
traversing relationships.

Agents, tools, skills and past learnings are stored as a knowledge graph, and
the orchestrator uses agentic graph RAG to decide what a task needs — which
agent, which tools, which skills, which learnings from previous runs. What a
tool *relates to* is the signal: the skills that use it, the learnings derived
from it, the agents that hold it.

A planner turns a request into a workflow with branches, loops and retries.
Channels — Slack, Discord, Telegram, webhooks — are pluggable and all normalise
into one message format, so every channel works with every part of the system
without special cases.

## What works today

Two things run: the `brain` service, and `oa`, the CLI that talks to it.

The brain is production-shaped, but it does not yet do anything an agent
platform does:

- Two HTTP listeners, one for the API and one for webhooks, with full timeouts
  and graceful shutdown on `SIGINT`/`SIGTERM`
- Liveness (`/healthz`) and readiness (`/readyz`) probes — readiness checks
  Postgres and returns 503 when it cannot reach it
- Structured logging, text with source in development and JSON elsewhere
- Environment-only configuration, validated at startup, with credentials
  redacted whenever config is printed
- A Postgres connection pool and versioned migrations behind an advisory lock,
  applied with `brain migrate up`
- Type-safe queries generated from SQL by sqlc, and a transaction helper that
  rolls back on error or panic
- Authentication against an OIDC provider, or a shared token for development.
  A verified caller becomes a user row on first sight, so nobody is
  pre-provisioned
- Role-based authorisation. Roles and their permissions are rows, so adding a
  role is a migration rather than a release
- A teams API — create a team, list them, and manage membership
- An inbound gateway. A channel is a team's connection to one platform; its
  webhook arrives on the public listener, is verified against that channel's
  own signing secret, and becomes a normalised message whatever sent it
- A generic webhook adapter anyone can integrate against today, and a
  conformance suite a new adapter has to pass before it is trusted
- Approval before anything is stored. A message from a sender nobody has linked
  to a user is dropped; only their id and name are queued, so a stranger cannot
  put text into your database by finding your URL

`oa` is early too, but it is real:

- `oa login` — a browser device flow against whichever identity provider the
  brain uses. The brain publishes its issuer, so a client is configured with one
  address rather than with a copy of the server's own settings
- Credentials in the OS keychain, falling back to a `0600` file on a machine
  that has none. Never in the config file, which is the one meant to be
  readable, synced and pasted into an issue
- Logins renew themselves. An expired access token is exchanged silently before
  the request, and only the provider saying the login is dead discards it — a
  provider having a bad minute does not log you out
- Named contexts — a brain, and the credential it issued, under the same name
  because a token is only valid where it came from. Create, rename, switch,
  delete
- Settings that say where they came from, so "I set it and it did not take" is
  answerable. A flag beats an environment variable beats the config file beats
  the built-in
- `--output table|json|yaml` on every command, so output is readable or
  parseable without a second tool
- Teams and membership from the terminal — list, create, and add or remove a
  member, instead of a `psql` session against the brain's database
- Channels — connect one, list them, disconnect it. The signing secret is
  generated for you and shown once, or read from stdin if you have one already;
  there is no `--secret` flag, because a command line is world-readable
- Against a development brain it needs no setup: it finds the shared token in
  your shell, and only ever sends it to a loopback address

Not built yet: the graph, the planner, the agent runtime, the dashboard, and
outbound replies — the brain can hold a conversation's messages but cannot yet
answer one. Slack, Discord and Telegram adapters are not written; the seam they
plug into is, and `custom` is a working generic webhook in the meantime.

## Quick start

Requires Go 1.26.6 and a Postgres 13 or newer you can reach — 13 is where
`gen_random_uuid()` became built-in, which the first migration needs. Running
the *tests* needs 18; see [CONTRIBUTING.md](CONTRIBUTING.md). CI runs against
Postgres 18.

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity/apps/brain

go run ./cmd/brain migrate up    # create the schema

# Serving needs a way to authenticate callers. For a local run, a shared token
# is enough; name yourself a super admin to get past authorisation as well.
export OPENARITY_DEV_TOKEN=letmein
export OPENARITY_SUPER_ADMINS=dev

go run ./cmd/brain               # serve; Ctrl-C to shut down
```

`brain reap` is the third command, and a deployment needs it on a schedule.
Deleting a team, a channel or a person removes their rows; it cannot remove the
attachment bytes in the object store or the secrets in the vault, because
Postgres has no transaction that spans them. Each deletion records what it owes
instead, and `reap` completes it — destroying a deleted team's key first, which
makes every one of its attachments unreadable immediately. It is idempotent and
safe to run twice at once, exits non-zero when an erasure has been outstanding
for a day, and a deployment that never runs it never erases anything outside
Postgres.

`brain worker` is the fourth command, and it is what normally runs the third. It
hosts the background work — the reaper today on a fifteen-minute schedule,
agent runs later — replays the ticks it missed while it was down, sweeps once
at startup so a fresh deployment is not idle for an interval, and refuses to
start if the secret store cannot delete. See
[deployment/SCHEDULING.md](deployment/SCHEDULING.md).

Every other setting has a working default. The service refuses to start with no
authentication configured rather than serving an open API — set
`OPENARITY_DEV_TOKEN` for development, or `OPENARITY_OIDC_ENABLED` and
`OPENARITY_OIDC_ISSUER` against a real provider.

In another shell:

```sh
curl -s 127.0.0.1:21120/healthz    # ok
curl -s 127.0.0.1:21120/readyz     # ready, or 503 if Postgres is unreachable

# Everything else needs a token. Without one: 401.
curl -s -H 'Authorization: Bearer letmein' 127.0.0.1:21120/whoami
# {"kind":"dev","issuer":"dev","subject":"dev","teams":[]}

curl -s -H 'Authorization: Bearer letmein' \
     -d '{"name":"platform"}' 127.0.0.1:21120/teams
```

`brain migrate down` rolls the last migration back.

## The CLI

`oa` talks to a brain over the same HTTP API. Against a development brain it
needs no setup — it finds `OPENARITY_DEV_TOKEN` in your shell:

```sh
cd openarity/apps/cli
make install                     # puts oa on your PATH, at $(go env GOPATH)/bin

oa whoami                        # the brain at 127.0.0.1:21120, unconfigured
```

More than one brain is a context — an address, and the credential that brain
issued under the same name, because a token is only valid where it came from:

```sh
oa context create staging --server https://brain.staging.example.com
oa context use local
oa context list
# * local    http://127.0.0.1:21120             no token
#   staging  https://brain.staging.example.com  no token
```

Anywhere that is not a development brain, log in. `oa` asks the brain which
identity provider it trusts, prints a code and an address, and waits:

```sh
oa context use staging
oa login
# open  https://auth.example.com/device
# code  WXYZ-ABCD
# or open https://auth.example.com/device?code=WXYZ-ABCD — the code is already in it
# waiting for approval, up to 5m0s…
# logged in  staging  https://brain.staging.example.com

oa logout          # this context only; the others keep theirs
```

The browser does not have to be on the same machine as the terminal — the code
is what proves it is you approving, which is what makes this work over SSH.

After that, nothing else needs doing: an expired token is renewed in the
background on the next command. `oa login` comes back when the provider says the
login itself is over, not when it merely had a bad minute.

Teams and membership are reachable without a database session, by name rather
than by uuid:

```sh
oa teams list
oa teams create platform                      # super admins only

oa teams members list platform
oa teams members add platform alice --role member
oa teams members remove platform alice
```

Every argument still accepts an id, so existing scripts keep working and pay no
lookup. `alice` is the subject she signs in as — she has to have logged in once,
because a user row is created on first sight and never synced from the provider:

```sh
oa users list                                 # everyone who has logged in
oa users list alice                           # one exact subject, with her id
```

`add` sends the subject to the brain rather than looking it up, so naming
somebody needs `membership:write` in that team and nothing more. `oa users list`
is for the other question — who is there at all — and needs `user:read` in some
team.

A channel is a team's connection to one platform. Creating one generates a
signing secret and prints it once — it is written to the secret store and no
endpoint can read it back:

```sh
oa channels create platform support --provider custom
oa channels list platform
oa channels delete platform support
```

The brain then serves `POST /hooks/custom/<channel-id>` on its webhook
listener, and accepts a delivery only if it carries a fresh timestamp and an
HMAC of the raw body under that secret. Nothing is stored until a sender has
been approved.

When the provider issues its own secret rather than accepting ours — Slack's
Signing Secret, Meta's App Secret — pipe it in. There is deliberately no
`--secret` flag: arguments are readable by every process on the machine through
`ps`, and they land in shell history.

```sh
printf %s "$SLACK_SIGNING_SECRET" |
    oa channels create platform slack --provider slack --secret-stdin
```

Who may speak through a channel is a separate decision from connecting it. A
sender nobody has approved gets their provider-side id recorded and their
message dropped:

```sh
oa channels senders pending platform support
oa channels senders approve platform support U01ABC alice
oa channels senders remove platform support U01ABC
```

What they say lands in a session — one conversation, whichever channel it
arrived on, and the thing an agent will later work inside:

```sh
oa sessions list platform
oa sessions list platform --channel support
oa sessions read platform 6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d
```

Being in the team is the whole qualification to read one. Text and refs are
quoted on their way to a terminal, because the hook URL is public and a
terminal reads an escape sequence as an instruction; `-o json` carries them
exactly as they arrived.

A list is one page per call. When more remain the response carries a cursor,
and the table says how to use it:

```sh
oa teams list --limit 20
oa teams list --cursor eyJjIjoiMjAyNi0wOC0xNFQwMDowMDowMFoifQ
```

Every command takes `--output table|json|yaml`, and `oa config` makes a choice
stick:

```sh
oa context list -o json | jq -r '.[].server'
oa config set output json        # or OPENARITY_OUTPUT=json for one shell
```

`oa config show` reports every setting and where it came from, which is the
answer to "I set it and it did not take":

```text
context  local                   (~/.config/openarity/config.yaml)
server   http://127.0.0.1:21120  (~/.config/openarity/config.yaml)
theme    auto                    (default)
output   table                   (default)
token    set (1174 characters)   (the macOS keychain)
```

A flag beats an environment variable, which beats the config file, which beats
the built-in. The token value is never printed — not truncated, not masked; only
its length and where it was found.

That last source is the whole of the credential design. A login is written to
the OS keychain — the macOS keychain, Windows Credential Manager, or whatever
`libsecret` is fronting on Linux — and never to `config.yaml`. Where there is no
keychain, or the token is too large for one, it lands in `credentials.yaml`
beside the config file at `0600`. Reads try the keychain first and fall through,
so the two never disagree about which is current.

## API

Everything except the probes requires `Authorization: Bearer <token>`.

| Endpoint                              | Who may call it                     |
| ------------------------------------- | ----------------------------------- |
| `GET /healthz`, `GET /readyz`         | anyone, unauthenticated             |
| `GET /auth/config`                    | anyone, unauthenticated             |
| `GET /whoami`                         | any authenticated caller            |
| `GET /users`                          | `user:read` in **some** team        |
| `POST /teams`                         | super admins                        |
| `GET /teams`                          | every team, or your own             |
| `GET /teams/{id}`                     | members, and super admins           |
| `GET /teams/{id}/members`             | members, and super admins           |
| `POST /teams/{id}/members`            | `membership:write` in **that** team |
| `DELETE /teams/{id}/members/{userID}` | `membership:write` in **that** team |

A team you are not in is a 404, never a 403 — a 403 would confirm the id exists.

`GET /users` is the only route whose check is "somewhere" rather than "here",
because a caller looking somebody up has no team in mind yet. It is restricted
at all because the directory is every subject and email the deployment has seen:
open to anyone, `?subject=` becomes a way to test whether a username exists.

Adding a member does **not** go through it. `POST /teams/{id}/members` takes
`user_id` **or** `subject` — exactly one — and resolves the subject itself, so
naming somebody needs no permission to read the directory. A subject nobody has
is a 404; one that matches two people, which needs two issuers, is a 409 listing
the ids.

Listings are paged and take `?limit=` (default 50, maximum 100) and `?cursor=`:

```json
{"items": [], "next_cursor": "eyJjIjoiMjAyNi0wOC0xMVQxNToxNToyNi4uLiJ9"}
```

`next_cursor` is present only while more rows remain; its absence is the end of
the collection. Pass it back as `?cursor=` to fetch the next page.

## Configuration

The brain is configured by environment only — no files, no flags — every
variable prefixed `OPENARITY_`:

| Variable                   | Default                     | What it does                     |
| -------------------------- | --------------------------- | -------------------------------- |
| `OPENARITY_ENVIRONMENT`    | `development`               | Selects the log format           |
| `OPENARITY_LOG_LEVEL`      | `info`                      | `debug`, `info`, `warn`, `error` |
| `OPENARITY_API_BIND`       | `127.0.0.1:21120`           | API listener address             |
| `OPENARITY_WEBHOOK_BIND`   | `127.0.0.1:21121`           | Webhook listener address         |
| `OPENARITY_POSTGRES_DSN`   | see below                   | Relational store                 |
| `OPENARITY_FALKOR_DB_URL`  | `redis://127.0.0.1:6380`    | Graph store — not used yet       |
| `OPENARITY_REDIS_URL`      | `redis://127.0.0.1:6379`    | Cache and queues — not used yet  |
| `OPENARITY_OMNI_ROUTE_URL` | `http://localhost:20128/v1` | Model router — not used yet      |
| `OPENARITY_OIDC_ENABLED`   | `false`                     | Verify tokens against an IdP     |
| `OPENARITY_OIDC_ISSUER`    | empty                       | Issuer URL, required if enabled  |
| `OPENARITY_OIDC_AUDIENCE`  | `openarity`                 | Audience the token must carry    |
| `OPENARITY_DEV_TOKEN`      | empty                       | Shared token — development only  |
| `OPENARITY_SUPER_ADMINS`   | empty                       | Comma-separated token subjects   |

The Postgres default is:

```text
postgres://postgres:postgres@localhost:5432/openarity?sslmode=disable
```

Secrets are never stored in Postgres or the graph. Rows hold secret-store path
references only.

### Secrets and object storage

Both pick an implementation by name. Nothing is inferred from which other
settings happen to be set — a typo fails at startup listing the valid values,
rather than falling back to a store that holds nothing and losing everything
quietly.

| Variable                           | Default                 | What it does                             |
| ---------------------------------- | ----------------------- | ---------------------------------------- |
| `OPENARITY_SECRETS_BACKEND`        | `static`                | `static`, `openbao`, `vault`             |
| `OPENARITY_SECRETS_ADDR`           | `http://localhost:8200` | Where the secret store listens           |
| `OPENARITY_SECRETS_APPROLE_ID`     | empty                   | AppRole id, required outside `static`    |
| `OPENARITY_SECRETS_APPROLE_SECRET` | empty                   | AppRole secret                           |
| `OPENARITY_SECRETS_KV_MOUNT`       | `secret`                | The KV v2 mount to read and write        |
| `OPENARITY_OBJECTS_BACKEND`        | `memory`                | `memory`, `filesystem`, `s3`             |
| `OPENARITY_OBJECTS_PATH`           | see below               | Where `filesystem` writes                |
| `OPENARITY_OBJECTS_ENDPOINT`       | empty                   | Required by `s3`                         |
| `OPENARITY_OBJECTS_REGION`         | `us-east-1`             | Sent by `s3`, ignored by most clones     |
| `OPENARITY_OBJECTS_BUCKET`         | `openarity`             | The one bucket the brain uses            |
| `OPENARITY_OBJECTS_ACCESS_KEY`     | empty                   | `s3` credential                          |
| `OPENARITY_OBJECTS_SECRET_KEY`     | empty                   | `s3` credential                          |

`OPENARITY_OBJECTS_PATH` defaults to `/var/lib/openarity/objects`.

`static` and `memory` hold their contents in the process and lose them on
restart. That is the point in development; outside it both are refused at
startup, because a brain that starts and silently loses every attachment is
worse than one that does not start.

`openbao` and `vault` name the same adapter today — OpenBao is the fork of the
last MPL-2.0 Vault, so the API and the KV v2 semantics are unchanged. They are
two names because the two are developed separately now, and what sits behind
one should be able to change without anybody editing their configuration.

`s3` speaks the S3 API, which is a de-facto interface rather than a standard.
MinIO, Garage, Ceph, SeaweedFS, Cloudflare R2, Backblaze B2 and Google Cloud
Storage all serve it; only whole-object put, get and delete are used, which is
the part every implementation actually serves.

A webhook carrying a file writes here: the gateway fetches it, decides its type
from the bytes rather than from what the sender claimed, encrypts it under a
per-team AES-256-GCM key held in the secret store, and writes a row naming the
object. The store never holds a key and never holds a plaintext, so reading one
team's attachments needs the bucket *and* that team's key.

Reading one back goes through the brain rather than through a URL to the
bucket, because the bucket holds ciphertext under a key it does not have. The
response carries the type recorded when the file arrived, under
`X-Content-Type-Options: nosniff` — never a type guessed from the filename.

`OPENARITY_DEV_TOKEN` is a single shared secret compared in constant time. It
exists so a development machine does not need an identity provider, and it has
no place in a deployment — use OIDC there. `OPENARITY_SUPER_ADMINS` lists token
subjects, not email addresses, and a super admin bypasses every team-scoped
check.

The CLI reads its own variables, and unlike the brain it also has a config
file. Each one overrides the file for one shell:

| Variable                 | Default           | What it does                     |
| ------------------------ | ----------------- | -------------------------------- |
| `OPENARITY_SERVER`       | `127.0.0.1:21120` | Which brain to talk to           |
| `OPENARITY_TOKEN`        | empty             | The credential to send           |
| `OPENARITY_OUTPUT`       | `table`           | `table`, `json`, `yaml`          |
| `OPENARITY_THEME`        | `auto`            | `auto`, `dark`, `light`          |
| `OPENARITY_DEV_TOKEN`    | empty             | Sent only to a loopback address  |
| `OPENARITY_CONFIG_DIR`   | see below         | Where the config file lives      |
| `OPENARITY_NO_KEYCHAIN`  | empty             | Any value: use the file, not it  |

`oa` sends `OPENARITY_DEV_TOKEN` only when the resolved server is loopback.
Letting the server decide would mean any host reached by a typo could claim to
be in development and collect the secret from your shell.

`OPENARITY_TOKEN` is for CI, where there is no browser to complete a login. It
overrides whatever is stored, and it is not renewed — there is no refresh token
behind a value pasted into a shell.

The config file lives at `oa config path` — `~/.config/openarity/config.yaml`
on Linux, `~/Library/Application Support/openarity/config.yaml` on macOS, and
under `OPENARITY_CONFIG_DIR` when that is set. It is written with `oa config`
and `oa context`, so it never needs a text editor, and it holds no credential:
`oa login` writes to the OS keychain, or to `credentials.yaml` in the same
directory at `0600` where there is none. `OPENARITY_NO_KEYCHAIN` forces the
file — useful on a headless box where unlocking a keyring is its own problem.

## Repository layout

```text
apps/brain/     the Go backend
  cmd/brain/    entrypoint, argument parsing, the serve and migrate commands
  internal/     config, server, middleware, store
  api/          openapi.yaml — the contract, hand-written
apps/cli/       oa, the command-line client
  cmd/oa/       the commands
  internal/     config, contexts, credentials, output formats
  internal/client/  generated from the brain's spec by oapi-codegen
sdk/agent/      the agent loop, as a library
  loops/        the reasoning patterns — ReAct today, code mode later
  models/       clients for anything speaking OpenAI chat completions
  examples/     a runnable agent, against a stub gateway or a real one
deployment/     manifests
go.work         ties every Go module in the repository together
```

A monorepo, because every app sits on one contract —
`apps/brain/api/openapi.yaml`.
It is hand-written and reviewed as a diff rather than generated from code, so a
change to what callers may rely on is as hard to sneak past review as a
migration. The CLI's client is generated *from* it, and CI fails if the
committed client and the spec disagree.

Each app is its own Go module, so `apps/brain/internal/` is unreachable from
`apps/cli/` by construction. Nothing is shared but the spec.

`sdk/agent` is a module for the same reason and a stronger one: it runs an
agent's loop and must never persist, authorise or select anything. Being a
separate module means it *cannot* import `apps/brain/internal`, so that is the
compiler's rule rather than a convention someone relaxes under deadline. It is
not under `apps/` because that directory means "has a main, ships as a
container", and a library has neither. The dashboard will
join them; channel clients that ship through an app store get their own
repositories.

## Development

Each module has its own Makefile and its own CI job.

```sh
cd apps/brain
make            # list targets
make tools      # golangci-lint, govulncheck, gopls, goose
make check      # everything CI runs: tidy, generate, format, vet, lint, build, cover, vuln
make generate   # regenerate the sqlc query code after changing a .sql file
```

```sh
cd apps/cli
make check      # the same gate, no database needed
make generate   # regenerate the client after the brain's spec changes
make install    # put oa on your PATH
```

Tests that need Postgres skip unless a database is named, so the brain's
`make check` works with nothing running. Point them at one with
`make check db=postgres`; CI does the same against a service container.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow.

## Contributing

Pull requests are welcome. For anything larger than a bug fix, open an issue
first — the design still moves.

- [Contributing guidelines](CONTRIBUTING.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
- [Security policy](SECURITY.md) — never report a vulnerability as a public issue

## License

[MIT](LICENSE).
