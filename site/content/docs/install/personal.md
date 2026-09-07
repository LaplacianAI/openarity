---
title: One machine
weight: 1
---

A laptop, or a box in a cupboard. Postgres, dex, the brain and its worker run
as ordinary processes under your own account — no Docker, no root, nothing
listening anywhere but `127.0.0.1`.

There is a window for this and a command for it. The window is the one most
people want; the command is what it drives, and what you would use on a server
with no desktop.

## Build it first

Nothing has been released, so the parts that are only ours have to be built.
Postgres and MinIO are fetched from their own publishers and are not affected.

```sh
git clone https://github.com/LaplacianAI/openarity
cd openarity

(cd apps/dashboard && make deps build)
(cd apps/brain && make ui-embed)

mkdir -p /tmp/openarity-bin
(cd apps/brain && go build -o /tmp/openarity-bin/brain ./cmd/brain)
(cd apps/cli && go build -o /tmp/openarity-bin/oa ./cmd/oa)
```

dex publishes no binaries either — every release since v2.41.0 carries zero
assets — so it is built from its own repository at the version Openarity pins:

```sh
version=$(grep -oE 'DexVersion *= *"[^"]+"' apps/cli/internal/stack/sources.go | grep -oE 'v[0-9.]+')
git clone --depth 1 --branch "$version" https://github.com/dexidp/dex.git /tmp/dex
(cd /tmp/dex && GOWORK=off CGO_ENABLED=0 go build -o /tmp/openarity-bin/dex ./cmd/dex)
```

`GOWORK=off` because a clone inside the Openarity checkout is covered by its
`go.work`, and Go refuses to build a directory inside a module the workspace
does not list.

{{< callout type="info" >}}
Once there is a release, everything above disappears: the installer fetches
`brain` and `dex` the way it already fetches Postgres and MinIO.
{{< /callout >}}

## The installer

A desktop application — `apps/installer`, a Tauri window around the same
command. It carries `oa`, `brain` and `dex` inside the bundle and reads the
progress `oa` prints, so there is nothing to type and no terminal to keep open.

Carrying them is what makes it work on its own: only Postgres is downloaded,
and the parts that are Openarity's own are already there.

```sh
cd apps/installer
make deps
make binaries OA=/tmp/openarity-bin/oa \
  BRAIN=/tmp/openarity-bin/brain DEX=/tmp/openarity-bin/dex
make bundle
```

`make bundle` produces a `.dmg`, `.msi` or `.deb` under
`src-tauri/target/release/bundle/`. Tauri v2 needs Rust 1.77 or newer.

What you see is six steps, in this order:

| | |
| ------------------------ | -------------------------------------------------- |
| Finding what it needs    | oa, brain and dex, from inside the bundle            |
| Downloading PostgreSQL   | about 70MB, with a percentage                        |
| Installing the model gateway | only if you asked for one, and the long step    |
| Creating the database    | `initdb`, then the cluster starts                   |
| Setting up its tables    | the migrations                                      |
| Creating your sign-in    | dex, configured with one user — you                 |
| Starting Openarity       | and the dashboard opens                             |

Then it shows a passphrase, once.

{{< callout type="warning" >}}
**Write the passphrase down.** It is generated during setup, stored only as a
bcrypt hash in dex's configuration, and shown exactly once. There is no
recovery — losing it means deleting the install directory and starting again.
{{< /callout >}}

The window asks nothing else. It takes the recommended answer to every
question the command would ask, which is what most people want and is what the
next section describes.

{{< callout type="error" >}}
**The bundle is not signed.** macOS will say the application cannot be checked
for malicious software and Windows SmartScreen will warn, until there is a
Developer ID certificate and a Windows code-signing certificate to build with.
Building it yourself, as above, produces one your own machine already trusts.
{{< /callout >}}

## The command

The same install, with the questions asked.

{{% steps %}}

### Run setup

```sh
/tmp/openarity-bin/oa stack setup --bin-dir /tmp/openarity-bin
```

It asks three things, each with a recommended answer you take by pressing
enter:

**Where files are kept** — transcripts, uploads, anything an agent produces.
On this machine, in memory, a MinIO it runs and supervises for you, or a bucket
you already have anywhere S3-compatible.

**Where credentials are kept** — the tokens Openarity uses to reach the
services you connect to it. In the brain's own process, which loses them on
restart, or an OpenBao or Vault you already run. Choosing one asks how the
brain should log in; see [An AppRole, pasted or
minted](#an-approle-pasted-or-minted).

**Where models come from** — point at a gateway you already run, or have one
installed and started here. LiteLLM and OmniRoute both work; see
[A gateway of your own](#a-gateway-of-your-own) for what each costs. Nothing
calls it yet, because the agent loop is not wired into the brain, so the choice
is recorded and the gateway runs, waiting.

Not a terminal? It takes the defaults and says so, which is how the window
drives it.

### Write down the passphrase

```text
Openarity is running at http://127.0.0.1:21120/ui
Sign in as dev@openarity.local
Passphrase: hK4mNpQ7rTvXwY2z
Write it down — it is not stored anywhere and cannot be shown again.
```

### Use it

Openarity starts again when you log in. To hold it in a terminal instead:

```sh
oa stack start          # blocks; Ctrl-C stops everything
oa stack status         # what is running, and on which ports
oa stack stop           # from another terminal
```

{{% /steps %}}

## An AppRole, pasted or minted

The brain reaches an external secret store with an AppRole and will not start
without one — a secret store is a dependency, not a feature flag.

You are not asked which way you want it. Give a token that may administer the
server and the role is created for you; that is the only thing the token is
for, and it is all the installer asks. If you already have an AppRole, set
`OPENARITY_SECRETS_APPROLE_ID` and `OPENARITY_SECRETS_APPROLE_SECRET` in the
environment and it is used as it is — nothing is created and no token is
wanted.

Creating it does six things:

```text
enable the KV v2 mount, if it is not already
enable the approle auth method, if it is not already
write a policy named openarity-brain
create a role using it
read its role-id
generate a secret-id
```

The policy grants exactly what the brain needs — read and write on a team's
channel secrets and its attachment key, delete on their metadata, and renewal
of its own token. It grants no `list`, nothing under `sys` and no ability to
administer the store. That was checked against a real OpenBao rather than
assumed: a token holding only this policy can write
`secret/data/teams/T1/channels/C1` and is refused
`secret/data/teams/T1/tokens/K1`, refused a list, and refused its own policy.

A token that cannot do all six is refused at the first one it cannot, and named:

```text
oa: stack: that token may not enable the KV v2 mount at secret/ — minting needs
one that can enable mounts and auth methods and write policies, which usually
means a root token
```

{{< callout type="warning" >}}
**The admin token is more dangerous than what it creates.** It can do anything
to that server, where the AppRole can do the five things above. It is used for
those six calls and never written down — only the AppRole reaches the install's
credentials file. Minting also writes into infrastructure you own: on a server
shared with anything else, it is worth knowing that a policy and a role appear.
{{< /callout >}}

Running it twice is safe. Every step tolerates what is already there, so a
second install against the same server does not fail on a mount an operator
enabled years ago.

With neither an AppRole nor a token, setup says so before it downloads
anything, and names both ways out:

```text
oa: stack: no AppRole and no admin token — set OPENARITY_SECRETS_APPROLE_ID and
OPENARITY_SECRETS_APPROLE_SECRET, or give a token that may administer
http://127.0.0.1:8200 and one will be created
```

Either way, setup checks the store answers before it downloads anything. The
address is the usual mistake — the default is `8200` and a compose file
commonly publishes the same server somewhere else, so the store is running and
nothing is at the address regardless:

```text
oa: stack: nothing answered at http://127.0.0.1:8200 — check the address and
that the server is running (a compose file often publishes it on a different
port)
```

## A gateway of your own

Neither gateway ships a binary you can just run. LiteLLM publishes no release
assets at all and is Python with ninety dependencies; OmniRoute publishes an
Electron desktop application whose server form is a container. So the installer
fetches a runtime as well — that is the whole cost, and it is not small.

| | Runtime | On disk | Its own dashboard |
| ----------- | ------------------------- | ------- | ----------------- |
| **LiteLLM** | uv, which brings a Python | ~1.0GB  | no                |
| **OmniRoute** | Node                    | ~3.7GB  | yes               |

Both are measured, not estimated. Both are supervised like everything else —
started and stopped with `oa stack start` and `oa stack stop`, and listed by
`oa stack status`.

Everything a gateway downloads stays under the directory you choose, including
uv's interpreters and npm's cache, so deleting that directory removes the
gateway and nothing else.

{{< callout type="warning" >}}
**OmniRoute has a dashboard, and therefore a password.** Its own image defaults
that password to `CHANGEME` and warns about it in a log nobody reads. Setup
asks; leave the answer blank and one is generated and shown once, beside the
sign-in passphrase. It is kept in the install's credentials file and appears in
no log and not in `stack.yaml`.
{{< /callout >}}

Both are held to `127.0.0.1`. OmniRoute binds every interface unless told
otherwise, and says so itself — *"the inference plane is reachable by ANY
device that can route to this host, and requests are billed to your configured
providers"* — so the installer sets `OMNIROUTE_SERVER_HOST`. Its leaderboard
and pricing syncs are turned off too: they reach the internet daily and change
no routing.

The gateway listens on `20128`, which is where the brain already looks by
default.

## Where it lives

One directory, which you can move with `--root`:

| | |
| ------- | ------------------------------------------ |
| Linux   | `~/.local/share/openarity`                 |
| macOS   | `~/Library/Application Support/openarity`  |
| Windows | `%LOCALAPPDATA%\openarity`                 |

Inside it: `bin/` for the binaries, `data/` for the Postgres cluster, `dex/`
for the identity provider's configuration, `logs/`, and `stack.yaml` recording
the versions and ports that were chosen.

Backing up that directory backs up the install, provided nothing is running
while you copy it.

## Ports

Preferred, not fixed. Setup binds each one to check it is free and takes
another if it is not, so a machine already running Postgres on `5432` is not a
problem. What it settled on is in `stack.yaml` and in `oa stack status`.

| | |
| ------------- | ------- |
| Brain API     | `21120` |
| Brain webhook | `21121` |
| Model gateway | `20128` |
| Postgres      | `21432` |
| MinIO         | `21900` |
| dex           | `5556`  |

## Uninstalling

```sh
oa stack stop
rm -rf ~/.local/share/openarity
```

Autostart leaves one file behind — a systemd user unit on Linux, a launch agent
on macOS, a registry entry on Windows. `oa stack setup --no-autostart` never
creates it.

## What is not here yet

**A signed bundle.** The release builds a `.dmg`, an `.msi` and a `.deb` on
the platform each one is for, so there will be something to download — but
signing waits on a Developer ID certificate for macOS and a code-signing
certificate for Windows.

**More than one person.** dex is configured with a single user. Adding a second
means editing its configuration by hand, at which point a
[deployment](/docs/install/enterprise) is the better shape.

**Anything answering.** The brain holds what arrives and cannot yet reply —
that is true of every install, and the [platform
page](/docs/platform) says what is and is not built.
