---
title: One machine
weight: 1
---

A laptop, or a box in a cupboard. Postgres, dex, the brain and its worker run
as ordinary processes under your own account — no Docker, no root, nothing
listening anywhere but `127.0.0.1`.

## What you need first

Nothing has been released, so the two binaries that are only ours have to be
built. Postgres and MinIO are fetched from their own publishers and are not
affected.

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

## Install

{{% steps %}}

### Run setup

```sh
/tmp/openarity-bin/oa stack setup --bin-dir /tmp/openarity-bin
```

It asks three questions, each with a recommended answer you can take by
pressing enter:

**Where files are kept** — transcripts, uploads, anything an agent produces.
On this machine, in memory, a MinIO it runs for you, or a bucket you already
have anywhere S3-compatible.

**Where credentials are kept** — the tokens Openarity uses to reach the
services you connect to it. In the brain's own process, which loses them on
restart, or an OpenBao or Vault you already run.

**Which model service to use** — anything speaking the OpenAI API. Nothing
calls it yet, because the agent loop is not wired into the brain, so this is
recorded for when it is.

Then it downloads Postgres, initialises a cluster, applies the migrations,
writes a dex configuration with one user, starts everything, and opens the
dashboard.

### Write down the passphrase

```text
Openarity is running at http://127.0.0.1:21120/ui
Sign in as dev@openarity.local
Passphrase: hK4mNpQ7rTvXwY2z
Write it down — it is not stored anywhere and cannot be shown again.
```

The passphrase is generated, shown once, and stored only as a bcrypt hash in
dex's configuration. There is no recovery: losing it means deleting the install
directory and running setup again.

### Use it

Openarity starts again when you log in. To hold it in a terminal instead:

```sh
oa stack start          # blocks; Ctrl-C stops everything
oa stack status         # what is running, and on which ports
oa stack stop           # from another terminal
```

{{% /steps %}}

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

**A desktop installer.** `apps/installer` is a Tauri application that drives
this same command through a window, for people who will not open a terminal. It
builds and runs, and it is not signed — macOS and Windows both refuse an
unsigned bundle without a deliberate override — so it is not distributed.

**Downloaded binaries.** Once there is a release, `oa stack setup` fetches
`brain` and `dex` the way it already fetches Postgres and MinIO, and everything
above the first step goes away.

**More than one person.** dex is configured with a single user. Adding a second
means editing its configuration by hand, at which point a
[deployment](/docs/install/enterprise) is the better shape.
