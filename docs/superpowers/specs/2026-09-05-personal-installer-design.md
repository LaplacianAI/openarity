# The personal installer

**Status:** design, approved 2026-09-05. Nothing here is built.

One command puts a working Openarity on a laptop: Postgres, dex, the brain,
the worker, and a browser open at a dashboard the person can sign in to. No
Docker, no `psql`, no editing a `.env`.

## Who this is for

The README already draws the line: enterprises and developers get Docker and
Kubernetes; a personal or homelab user gets binaries. This spec is the second
half. It assumes someone who will not remember `127.0.0.1:21120` and should
never have to type it.

## Decomposition

This is two sub-projects, and only the first is specified here.

|   | What it is | Toolchain |
| - | ---------- | --------- |
| **A** | the engine, plus `oa setup / start / stop / status / upgrade` | Go, already in the repo |
| **B** | a desktop app — window, tray icon, Start button | Rust/Tauri, code signing, notarisation |

B is a view over A, the same way the dashboard is a view over the brain. It
shells out to `oa … --json` and never reimplements supervision. It cannot be
designed sensibly until A's command surface exists, so it gets its own spec.

## What has to exist first

**Nothing publishes a `brain` binary.** `publish-image.yml` builds a container
image and stops there. `oa setup` has nothing to download until a release
pipeline exists, so that is task one, not a footnote.

**dex publishes no binaries at all**, and never has — every release from
v2.41.0 to v2.45.1 carries zero assets. It is not importable as a library
either: the module declares `github.com/dexidp/dex` while tagging `v2.45.1`,
with no `/v2` suffix, so Go refuses the tagged versions outright.

    $ curl -s .../dexidp/dex/v2/@v/v2.45.1.mod
    not found: invalid version: go.mod has non-.../v2 module path
    "github.com/dexidp/dex" (and .../v2/go.mod does not exist)

So openarity builds dex itself, from source at a pinned tag, and publishes it
as an openarity release asset. dex is Apache 2.0; the `LICENSE` and `NOTICE`
travel with the binary and the release notes name the upstream tag.

**That build is `CGO_ENABLED=0`**, because it is cross-compiled for four
platforms. dex handles this deliberately — `storage/sql/sqlite_no_cgo.go` is a
stub whose `Open` returns "SQLite storage is not available: binary compiled
without CGO support" — so a CGO-free dex has no sqlite storage.

**Which is why dex stores its state in Postgres here**, in the cluster the
installer already runs, in its own database. `deployment/dex/config.yaml` keeps
sqlite3, because the upstream container image is built with CGO; the personal
config differs on this one field and the two must not be assumed identical.
One datastore is also one backup and one thing to corrupt.

## Layout

    <data dir>/openarity/
      bin/        postgres/, dex, brain      downloaded, versions pinned
      data/       the Postgres cluster
      dex/        config.yaml                0600, holds a bcrypt hash
      logs/       openarity.log
      stack.toml  ports, versions, PIDs

`<data dir>` is per-platform and is **not** where the CLI keeps `config.yaml`:

| Platform | Path |
| -------- | ---- |
| macOS | `~/Library/Application Support/openarity/` |
| Linux | `${XDG_DATA_HOME:-~/.local/share}/openarity/` |
| Windows | `%LOCALAPPDATA%\openarity\` |

`%LOCALAPPDATA%`, not `%APPDATA%`, and so not `os.UserConfigDir()` — which
returns the Roaming profile on Windows. A domain-joined machine syncs Roaming
between logins, and a Postgres cluster inside it would be copied across the
network mid-write. This is the one path that must be read from the environment
directly rather than taken from the standard library.

The CLI's own config stays at `os.UserConfigDir()/openarity`, where it is
today. A Postgres cluster is not configuration, and putting one in `~/.config`
on Linux would be wrong in a way that only shows up when someone syncs that
directory between machines.

## The processes

Four children, supervised by `oa`:

    postgres        the cluster, on a free high port
    dex             the identity provider, :5556
    brain serve     API :21120, webhooks :21121, dashboard /ui
    brain worker    the reaper sweep — a personal install still owes retention

`brain worker` is a separate subcommand rather than a goroutine inside
`serve`, and this design does not change that. Running it means a personal
install behaves like a real one; skipping it means erasures silently never
happen.

## The commands

    oa setup      download, initdb, migrate, generate credentials, start, open
    oa start      start what setup already prepared
    oa stop       stop it
    oa status     what is running, on which ports, and whether it is healthy
    oa upgrade    stop, replace binaries, migrate, start

`status` takes `--json`, because sub-project B parses it.

### What `setup` does, in order

1. Refuse to run twice. An existing `stack.toml` means `oa start`, not setup.
2. Pick ports. Bind-test each; never assume 5432 is free.
3. Download Postgres, dex and brain for this platform; verify checksums.
4. `initdb`, start Postgres, create the `openarity` and `dex` databases.
5. Generate a passphrase. Write only its bcrypt hash into `dex/config.yaml`.
6. `brain migrate up`.
7. Start dex, brain, worker. Wait for `/readyz`.
8. Print the passphrase **once**, then open `http://127.0.0.1:21120/ui`.

Step 2 is not defensive programming, it is the failure this repository has
already produced twice: a Homebrew Postgres 14 owning 5432 while the intended
database was elsewhere, once wasting a verification run and once a migration.

## Versions

Postgres binaries come from the zonky.io Maven artifacts, which is what
`embedded-postgres` uses. Availability was checked per platform rather than
recalled:

| Artifact | Latest |
| -------- | ------ |
| `darwin-arm64v8` | 18.6.0 — 84 versions, back to 10.20.0 |
| `darwin-amd64` | 18.6.0 |
| `linux-arm64v8` | 18.6.0 |
| `linux-amd64` | 18.6.0 |
| `windows-amd64` | 18.6.0 |
| `windows-arm64v8` | **none published** |

Native Apple Silicon needs no Rosetta at any version worth pinning. **Windows
on ARM has no native build**, so it runs the amd64 binaries under the x64
emulation Windows 11 provides. Setup says so rather than pretending the
install is native, and `oa status` reports the architecture it actually
downloaded.

Pin **18.6.0** — newest, and the brain's test suite already requires 18 or
newer for its `SQLSTATE 23001` assertion.

Every version — Postgres, dex, brain — is written into `stack.toml` at setup.
`oa upgrade` compares that file against what it is about to install, so an
upgrade is a diff rather than a guess.

## Identity

dex, as a supervised child, with a static password in a generated config —
protocol-identical to the enterprise path. The brain verifies a bearer token
against a JWKS either way and has no idea which kind of install it is on.

`BOOTSTRAP_FIRST_USER` already closes the bootstrap gap: the first person to
sign in claims an install that has no super admin, under an advisory lock,
re-checked after acquiring it. That work is done and this spec only turns it
on.

Three things about the generated passphrase:

- It is printed to the terminal exactly once, and to nothing else. Never to
  `openarity.log` — that file is what people paste into issues.
- Only the bcrypt hash is written to disk, in a file created `0600`.
- `oa setup` does not offer to remember it. Recovery is a documented
  `oa reset-password` in a later cut, not a copy of the plaintext.

## Reboot survival

| Platform | Mechanism |
| -------- | --------- |
| macOS | launchd user agent, `~/Library/LaunchAgents/` |
| Linux | systemd `--user` unit |
| Windows | Scheduled Task, trigger `ONLOGON` |

All three run `oa start`, so there is one start path and not two. All three are
**per-user and need no administrator**, which is the property that makes the
Windows case affordable: a Windows *Service* would need elevation, a different
privilege model, and a signed binary, where a logon-triggered Scheduled Task
needs none of those and matches what the other two platforms already do.

    schtasks /create /tn Openarity /tr "…\oa.exe start" /sc onlogon

## Where Windows genuinely differs

Four places, and each needs code rather than a path change.

**Stopping a child.** Windows has no `SIGTERM`. Postgres is stopped with
`pg_ctl stop -m fast` on every platform, which sidesteps the problem for the
one process where an ungraceful stop costs real data. For dex, the brain and
the worker, Windows children are started with `CREATE_NEW_PROCESS_GROUP` and
stopped with `GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)`, escalating to
`TerminateProcess` after a timeout. The escalation is not optional: a child
that ignores the event must not hang `oa stop` forever.

**File permissions.** `chmod 0600` is a no-op on Windows. The dex config holds
a bcrypt hash, so it is not nothing. `%LOCALAPPDATA%` is already ACL'd to the
user by the operating system, which is the real protection; on top of that the
installer removes inherited entries with `icacls /inheritance:r` so a
permissive parent directory cannot widen it. The spec states this rather than
letting a reader assume `0600` did something.

**Opening the browser.** `open` on macOS, `xdg-open` on Linux,
`rundll32 url.dll,FileProtocolHandler` on Windows. Not `cmd /c start`, whose
first quoted argument is the window title — a path containing a space silently
opens the wrong thing, and `C:\Users\First Last\…` is the common case.

**SmartScreen.** An unsigned `oa.exe` downloaded from GitHub triggers a
warning that reads like a malware alert to exactly the person this installer
is for. Signing is a real cost and is not in this cut, so the download page
shows the warning and says what to click. Pretending it will not appear is
worse than explaining it.

## Failure, and what it must say

Every failure names the thing that failed and what to do, because the person
reading it cannot read a stack trace:

| What happens | What it says |
| ------------ | ------------ |
| Download fails | which file, and that a retry is safe |
| A port is taken | which port, by which PID, and that setup picks another |
| `initdb` fails | the Postgres log line, not the exit code |
| The brain never becomes ready | the last 20 log lines, and where the log lives |
| Setup is interrupted | on rerun, that a partial install exists and how to remove it |

A partially completed setup is the common case — a laptop sleeps, wifi drops —
so every step is re-runnable and setup resumes rather than starting over.

## Testing

- **The download layer** takes an interface, so tests serve a fixture over
  `httptest` and never touch the network. Checksum mismatch is a test.
- **Port selection** is tested by holding a listener open on the port it would
  otherwise pick and asserting it picks another.
- **Supervision** is tested against a child that exits immediately, one that
  hangs, and one that ignores SIGTERM. The third is the one that matters:
  `stop` must escalate rather than block forever.
- **A real end-to-end setup** runs in CI on macOS, Linux and Windows runners,
  asserts
  `/readyz`, then asserts the passphrase appears in stdout and **not** in
  `openarity.log`.

That last assertion is the security guard, so it gets broken deliberately:
log the passphrase, watch the test fail, put it back.

## Risks

- **Building someone else's binary.** Tracking dex releases becomes our job.
  Mitigation: pin a tag, rebuild only on a deliberate bump, name the upstream
  tag in the release notes.
- **The zonky artifacts are a third-party mirror of Postgres.** If they stop
  publishing, the download source changes. The download layer is an interface
  partly for this reason.
- **`oa` becomes two things** — a client and a lifecycle manager. Accepted
  deliberately: it is the one binary a personal user has, and the alternative
  is a second binary to build, sign, explain and keep in step.

## Not in this cut

The desktop app, code signing on Windows and macOS, `oa reset-password`,
multi-user personal installs, and any migration path from a personal install
to a Docker one.
