---
name: upgrade-the-go-toolchain
description: Raise the Go version across the monorepo — after govulncheck reports a standard-library vulnerability, or to pick up a release. Covers every file that pins the version, why CI needs no edit, reinstalling the tools, and verifying the advisories are actually gone. Use whenever the Go version changes.
---

# Upgrade the Go toolchain

Almost always triggered by `make vuln` going red on a standard-library
advisory. Nothing in this repository can fix one of those — the fix is a newer
toolchain, and the version is written down in eight places that do not update
each other.

Doing six of the eight leaves a repository that builds perfectly and lies in
its documentation.

## Step 1 — find the version that actually fixes it

`govulncheck` names it. Do not guess from the Go release page:

```sh
cd apps/cli && make vuln
```

```text
Vulnerability #4: GO-2026-5972
  Standard library
    Found in: encoding/asn1@go1.26.5
    Fixed in: encoding/asn1@go1.26.6
```

Take the highest `Fixed in` across every advisory reported — one of them will
need a later patch than the others. If an advisory has no `Fixed in`, it is
unfixed upstream and the upgrade will not clear the build; say so rather than
bumping and hoping.

## Step 2 — confirm the toolchain downloads before editing anything

```sh
GOTOOLCHAIN=go1.26.6 go version
```

A machine with `GOSUMDB=off` in its environment — common when `GOPRIVATE` is
set for a company proxy — fails here:

```text
go: download go1.26.6: verifying module: checksum database disabled by GOSUMDB=off
```

Go requires checksum verification for a *toolchain* module specifically. Enable
it for the one command:

```sh
GOSUMDB=sum.golang.org GOTOOLCHAIN=go1.26.6 go version
```

That is more verification, not less. Never edit the machine's global `GOSUMDB`
to make this pass, and never reach for a flag that skips the check — a Go
toolchain is the one download where an unverified binary compiles everything
else you ship.

## Step 3 — the files that pin the version

```text
go.work                                    the go directive
apps/brain/go.mod                          the go directive
apps/cli/go.mod                            the go directive
README.md                                  the Quick start prerequisites
CONTRIBUTING.md                            the prerequisites table
apps/brain/README.md                       the prerequisites line
apps/cli/README.md                         the prerequisites line
.github/ISSUE_TEMPLATE/bug_report.yml      the environment placeholder
```

Find them rather than trusting this list, which goes stale the moment a
document is added — substitute the version you are leaving:

```sh
grep -rn '1\.26\.5' --include='*.md' --include='*.yml' --include='go.mod' \
  --include='go.work' --include='Makefile' . | grep -v node_modules
```

Beware a bare `perl -pi -e` across the whole tree: this file is full of the old
version as illustration, and rewriting it would make the skill describe an
upgrade nobody did.

The issue-template placeholder is the one people skip. It is a placeholder, so
it is never *wrong* exactly — but it is the version a reporter will copy, and a
version nobody runs makes a bug report harder to reproduce.

## Step 4 — CI needs no edit

Both jobs resolve the version from the module:

```yaml
      - uses: actions/setup-go@v7
        with:
          go-version-file: apps/brain/go.mod
```

That is deliberate. A hard-coded `go-version:` is a ninth place to forget, and
the failure is silent — CI passes on a toolchain nobody develops against.
Leave it alone.

## Step 5 — reinstall the tools

```sh
cd apps/brain && make tools
cd ../cli && make tools
```

Anything installed with `go install` was compiled by the old toolchain, and
`golangci-lint` in particular refuses to analyse packages built by a newer one.
Skipping this produces a confusing lint failure that looks like a code problem.

## Step 6 — verify

Both modules, and the brain needs a database:

```sh
cd apps/brain && make check db=postgres
cd ../cli && make check
```

`make vuln` reporting `No vulnerabilities found` is the point of the exercise —
check it said that, rather than that the command exited zero because you were
looking at `lint`. If advisories remain, they are new ones: run step 1 again
against the fresh output rather than assuming the bump failed.

## Step 7 — commit

One commit. Splitting the `go.mod` files from the documentation leaves a
commit that claims a version the docs contradict.

```text
build: go 1.26.6, so the standard-library advisories clear
```

`build:` rather than `chore:` — it changes what the project is compiled with.
Unscoped, because it crosses both modules.

In the pull request, name the advisories that are cleared and paste the
`govulncheck` line that says so. A reviewer cannot tell a security upgrade from
a routine version bump without it.
