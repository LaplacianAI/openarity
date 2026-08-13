---
name: open-a-pull-request
description: Open a pull request against main. Covers branching from a freshly fetched main, the title as a conventional commit, filling every section of .github/pull_request_template.md verbatim, what each checklist box actually requires before it may be ticked, and the branch mistakes that waste a review cycle. Use for every pull request, without exception.
---

# Open a pull request

`main` is protected: no direct pushes, no force-pushes, CI must pass. Every
change arrives through a pull request that uses
`.github/pull_request_template.md` — **not** a free-form body.

## Step 1 — branch from a freshly fetched main

```sh
git fetch origin
git switch -c feat/teams-command origin/main
```

Always `origin/main`, never a local `main` that may be behind, and never a
branch whose pull request has already merged.

**Pull requests here are squash-merged and the branch is deleted.** Pushing to
that branch name again recreates it carrying every already-merged commit under
its old hashes, so the new pull request shows twenty commits when two are new.
This has happened twice. Before pushing to a branch you used before:

```sh
gh pr list --state merged --head feat/cli --limit 1
```

If anything comes back, start a new branch off `origin/main` and cherry-pick
only the commits that are genuinely new. Then delete the resurrected one:

```sh
git push --delete origin feat/cli
```

Prefixes: `feat/`, `fix/`, `docs/`, `chore/`, `refactor/`, `test/`, `ci/`.

## Step 2 — the title is the commit message

The title becomes the squashed commit subject, so it is a
[Conventional Commit](https://www.conventionalcommits.org):

```text
feat(cli): oa teams, so membership stops needing psql
fix(brain): close the pool when migrate fails
docs: record why the dev token never leaves loopback
```

Imperative, no trailing period, under about 72 characters. Scope it to an app
(`brain`, `cli`) only when it describes that app; root-level changes are
unscoped.

## Step 3 — fill the template

Read `.github/pull_request_template.md` and fill **every** section. Delete the
HTML comments; keep the headings exactly as they are.

```sh
gh pr create --base main --title "feat(cli): ..." --body-file .github/pr-body.md
```

Write the body to a file first, then pass `--body-file`. A heredoc inside the
`gh` invocation makes backticks and `$` in pasted output the shell's problem.
Delete the file afterwards; it is not part of the change.

| Section                | What belongs there                                        |
| ---------------------- | --------------------------------------------------------- |
| **What this changes**  | One or two sentences. The diff already shows what.        |
| **Why**                | The problem. `Closes #123` if there is an issue.          |
| **How it was verified**| Commands you actually ran, and their **pasted output**.   |
| **Checklist**          | Every box, honestly. See step 4.                          |

Never replace a heading with your own. Never drop the checklist because the
change is small. A reviewer reads the same five headings on every pull request,
and that is the point of having them.

## Step 4 — the checklist is a claim, not a formality

Do not tick a box you have not done. Each one means something specific:

- **`make check` passes in every module touched** — every module. A change to
  `apps/brain/api/openapi.yaml` touches `apps/cli` too, because the client is
  generated from it. Pass `db=postgres` for the brain; without a database its
  tests skip and the coverage number is not the real one.
- **Tests cover the new behaviour, including at least one failure case** — the
  refusal path, and an assertion that the refusal *changed nothing*.
- **Each new guard was broken to confirm a test fails without it** — this is
  the one people tick without doing. Comment out the guard, run the tests, see
  the failure, put it back. A test that passes against broken code certifies
  nothing while looking like coverage.
- **Migrations have a `Down` and were applied and rolled back** — `brain
  migrate up` then `down`, against a real database.
- **The spec and the generated client were regenerated together** — if
  `openapi.yaml` changed, `cd apps/cli && make generate` and commit both. CI's
  `generate-check` fails otherwise, and it fails on the *next* pull request if
  they are split.
- **No secret, credential or DSN with a password appears in the diff** — read
  the diff, do not assume. A real local password reached a documentation file
  in this repository once and was caught at this step.
- **Documentation updated if behaviour or configuration changed** — see the
  `update-project-docs` skill for which file owns which fact.

If a box does not apply, leave it unticked and say why in one line underneath.
An unticked box with a reason is information; a ticked box that is not true is
a lie a reviewer will act on.

## Step 5 — verification means pasted output

"How it was verified" is the section that decays into fiction fastest. Paste
what the terminal printed:

```text
$ cd apps/cli && make check
golangci-lint run
0 issues.
go test -race ./...
ok  github.com/LaplacianAI/openarity/apps/cli/cmd/oa  1.8s
```

Not "ran make check, all green". If the change affects what the CLI prints,
capture it through a real terminal — colour and column alignment do not exist
in a pipe or a test:

```sh
script -q /dev/null ./bin/oa context list
```

If a check was skipped, say which and why. That is a review conversation worth
having; a silent omission is not.

## Step 6 — after opening it

```sh
gh pr checks <number>
```

Both jobs — `brain` and `cli` — must pass. `BLOCKED` while they queue is
normal; `BLOCKED` after they finish is branch protection wanting a review.

If `main` moves underneath it, rebase rather than merging `main` in:

```sh
git fetch origin && git rebase origin/main
git push --force-with-lease
```

`--force-with-lease`, never `--force`: it refuses if someone else pushed to the
branch in the meantime. Re-run `make check` after any rebase, and diff the
rebased tree against the pre-rebase one to prove nothing was dropped.

## What never goes in a pull request

- A built binary. `apps/cli/oa` and `bin/` are gitignored; a 10MB binary was
  staged once because `go build` with no `-o` names the output after the
  package.
- A `.env`, a token, or a real password — including in a documentation example.
- Generated output that disagrees with its source.
- An unrelated fix. Open a second pull request; a reviewer cannot approve half
  a diff.
