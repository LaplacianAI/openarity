# Personal installer — implementation plan

**Spec:** `docs/superpowers/specs/2026-09-05-personal-installer-design.md`

**Goal:** `oa setup` puts a working Openarity on a laptop — Postgres, dex,
brain, worker — and opens a browser at a dashboard the person can sign in to.

**Order is inverted from the spec's dependency order.** The spec names the
release pipeline first because nothing can be *downloaded* without it. This
plan builds the supervisor first, against binaries already on the machine, so
that `oa setup` is provably end-to-end before a release pipeline exists to
feed it. Task 5 then swaps "find a binary" from local to downloaded, which is
one interface with two implementations rather than a rewrite.

**One correction to the spec:** the state file is `stack.yaml`, not
`stack.toml`. The CLI already depends on `gopkg.in/yaml.v3` and nothing on a
TOML parser, and a new dependency for one file is not worth it.

## Global constraints

- Go 1.25, `apps/cli` module. `make check` must pass there before each commit.
- Every new guard is broken first, and the failure recorded.
- No credential, passphrase or DSN with a password may appear in a diff.
- Windows, macOS and Linux are all first-class. Platform behaviour is chosen
  by an injected value, never by reading `runtime.GOOS` deep in a function —
  otherwise two of the three platforms are untestable on the CI runner.

## File structure

    apps/cli/internal/stack/
      layout.go     where things live, per platform
      ports.go      choosing a port nothing else holds
      state.go      stack.yaml — versions, ports, what setup decided
      find.go       locating a binary: local now, downloaded later
      child.go      one supervised process
      supervise.go  the set of them: start, stop, status
      setup.go      the ordered steps

    apps/cli/internal/command/stack/
      stack.go      oa setup / start / stop / status, wired to the above

`internal/stack` knows nothing about cobra; `internal/command/stack` knows
nothing about processes. The split is what lets the engine be tested without
a terminal and the commands be tested without a Postgres.

---

## Task 1 — layout, ports and state

**Files:** create `internal/stack/layout.go`, `ports.go`, `state.go` and their
tests.

**Produces:** `Dir(goos string, env func(string) string) (string, error)`,
`Layout` with `Bin/Data/Dex/Logs/StateFile`, `FreePort() (int, error)`,
`LoadState/SaveState`.

- [ ] **Step 1: the failing test for the data directory**

```go
func TestTheDataDirectoryIsPerPlatform(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		goos, want string
		env        map[string]string
	}{
		{goos: "darwin", env: map[string]string{"HOME": "/Users/x"},
			want: "/Users/x/Library/Application Support/openarity"},
		{goos: "linux", env: map[string]string{"HOME": "/home/x"},
			want: "/home/x/.local/share/openarity"},
		{goos: "linux", env: map[string]string{"HOME": "/home/x", "XDG_DATA_HOME": "/data"},
			want: "/data/openarity"},
		{goos: "windows", env: map[string]string{"LOCALAPPDATA": `C:\Users\x\AppData\Local`},
			want: `C:\Users\x\AppData\Local\openarity`},
	} {
		got, err := Dir(tc.goos, lookup(tc.env))
		if err != nil {
			t.Fatalf("Dir(%s) = %v", tc.goos, err)
		}
		if got != tc.want {
			t.Errorf("Dir(%s) = %q, want %q", tc.goos, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: run it, watch it fail** — `undefined: Dir`
- [ ] **Step 3: implement `Dir`.** `goos` and `env` are parameters precisely
      so all three cases run on one runner. Windows reads `LOCALAPPDATA` and
      not `os.UserConfigDir()`, which returns Roaming.
- [ ] **Step 4: the Roaming test.** Assert `Dir("windows", …)` does not equal
      the `APPDATA` value when both are set. The spec's reason — a domain
      profile syncing a Postgres cluster mid-write — is invisible in code, so
      the test is where it is recorded.
- [ ] **Step 5: `FreePort`, and the test that holds a listener open** on the
      port it would otherwise return, asserting it picks another.
- [ ] **Step 6: `State` round-trip**, plus `SaveState` refusing to overwrite
      an existing file — that refusal is what makes `oa setup` idempotent
      rather than destructive.
- [ ] **Step 7: break each guard.** Ignore `XDG_DATA_HOME`; use `APPDATA`;
      drop the overwrite check. Record each failure.
- [ ] **Step 8: `make check`, then commit.**

---

## Task 2 — one supervised child

**Files:** create `internal/stack/child.go`, `child_windows.go`,
`child_unix.go`, and tests.

**Consumes:** nothing. **Produces:** `Child{Name, Path, Args, Env, Log}` with
`Start(ctx) error`, `Stop(timeout) error`, `Running() bool`, `PID() int`.

- [ ] **Step 1: the test that matters first** — a child that ignores the stop
      signal must not hang `Stop`. Drive it with a helper binary built by the
      test that traps and sleeps; assert `Stop(200ms)` returns inside a second
      and the process is gone.
- [ ] **Step 2: run it, watch it fail.**
- [ ] **Step 3: implement `Stop` as signal-then-escalate.** Unix sends
      `SIGTERM` then `SIGKILL`; Windows starts the child with
      `CREATE_NEW_PROCESS_GROUP`, sends `CTRL_BREAK_EVENT`, then
      `TerminateProcess`. Two files with a build tag, one exported behaviour.
- [ ] **Step 4: the environment test.** A child's `Env` is explicit and empty
      unless set. Go's `exec` gives a `nil` `Env` the parent's entire
      environment — including `OPENARITY_*` values that would silently
      override the config the installer just wrote.
- [ ] **Step 5: logs append, never truncate**, so a restart does not erase the
      evidence of why the last run failed.
- [ ] **Step 6: break the escalation** — return after the signal without the
      kill — and confirm the hang test fails.
- [ ] **Step 7: `make check`, commit.**

---

## Task 3 — the set of them

**Files:** create `internal/stack/supervise.go` and its test.

**Consumes:** `Child`, `State`. **Produces:** `Stack.Start(ctx) error`,
`Stop(ctx) error`, `Status(ctx) ([]Status, error)` where `Status` carries
name, PID, port, and health.

- [ ] **Step 1: the ordering test.** Postgres must be accepting connections
      before `brain migrate up` runs, and dex must answer before the brain is
      declared ready. Assert with fakes that record their start order.
- [ ] **Step 2: run it, watch it fail.**
- [ ] **Step 3: implement start as ordered-with-readiness**, not
      fire-and-forget. Each child has a `Ready(ctx) error` probe; the next one
      does not start until it passes.
- [ ] **Step 4: stop in reverse order**, Postgres last, and via
      `pg_ctl stop -m fast` rather than a signal — the one process where an
      ungraceful stop costs data.
- [ ] **Step 5: the partial-start test.** If the third child fails to become
      ready, the first two are stopped rather than left running. Assert the
      fakes were stopped.
- [ ] **Step 6: break the reverse order and the rollback**; record both
      failures.
- [ ] **Step 7: `make check`, commit.**

---

## Task 4 — `oa setup`, against local binaries

**Files:** create `internal/stack/find.go`, `setup.go`,
`internal/command/stack/stack.go`; modify `cmd/oa/main.go` to register it.

**Consumes:** everything above. **Produces:** the four commands.

- [ ] **Step 1: `Finder` interface** — `Find(name string) (path string, err
      error)`. `LocalFinder` resolves from `PATH` and a repo build; the
      downloading one arrives in Task 5. One interface, so Task 5 is a new
      implementation and not a rewrite.
- [ ] **Step 2: the resumability test.** Setup interrupted after step 4 and
      re-run must continue, not fail and not start over. Drive it by making
      step 5 return an error, then re-running with it fixed.
- [ ] **Step 3: implement the eight ordered steps** from the spec.
- [ ] **Step 4: the passphrase test — the security guard.** Assert the
      generated passphrase appears in stdout exactly once and appears
      **nowhere** in the log file or in `dex/config.yaml`, which holds only its
      bcrypt hash.
- [ ] **Step 5: break that guard** by logging the passphrase, watch it fail,
      put it back. This is the one guard in the plan whose failure is a
      security incident rather than a bug.
- [ ] **Step 6: `oa status --json`**, because sub-project B parses it.
- [ ] **Step 7: register the commands, extend the CLI's command-list test.**
- [ ] **Step 8: `make check`, commit.**

---

## Task 5 — downloads

**Files:** create `internal/stack/download.go`; add `DownloadingFinder` to
`find.go`.

- [ ] **Step 1: fixture-served download test** over `httptest` — never the
      network, in any test, ever.
- [ ] **Step 2: checksum mismatch is a failure**, and the partial file is
      removed rather than left to be found by the next run.
- [ ] **Step 3: resume a partial download**, since a laptop's wifi drops.
- [ ] **Step 4: platform mapping** — the zonky artifact name per
      GOOS/GOARCH, including `windows/arm64` mapping to the amd64 build with
      a message saying so.
- [ ] **Step 5: break the checksum check**, confirm the test fails.
- [ ] **Step 6: `make check`, commit.**

---

## Task 6 — release pipeline

**Files:** create `.goreleaser.yaml`, `.github/workflows/release.yml`,
`.github/workflows/build-dex.yml`.

- [ ] **Step 1: GoReleaser for `brain` and `oa`** — darwin/linux arm64+amd64,
      windows/amd64.
- [ ] **Step 2: the dex build** — checkout a pinned tag, `CGO_ENABLED=0 go
      build ./cmd/dex`, publish with upstream `LICENSE` and `NOTICE` and the
      tag named in the release notes.
- [ ] **Step 3: checksums published alongside**, because Task 5 verifies them.
- [ ] **Step 4: a dry-run release** on a tag in a fork before the real one.
- [ ] **Step 5: commit.**

---

## Task 7 — reboot survival

**Files:** create `internal/stack/autostart_darwin.go`, `_linux.go`,
`_windows.go` and tests.

- [ ] **Step 1: each platform writes its unit and can remove it** — launchd
      plist, systemd `--user` unit, `schtasks /create /sc onlogon`.
- [ ] **Step 2: assert the generated unit contains an absolute path**, since
      none of the three run with a useful working directory.
- [ ] **Step 3: `oa setup --no-autostart`** for anyone who does not want it.
- [ ] **Step 4: `make check`, commit.**

---

## Task 8 — end to end in CI

- [ ] **Step 1: a job on macOS, Linux and Windows runners** that runs `oa
      setup`, asserts `/readyz`, asserts the passphrase is absent from the
      log, then `oa stop`.
- [ ] **Step 2: assert a second `oa setup` refuses** rather than reinstalling
      over a working install.
- [ ] **Step 3: documentation** — README quick start gains the personal path;
      `deployment/README.md` keeps the Docker one.
