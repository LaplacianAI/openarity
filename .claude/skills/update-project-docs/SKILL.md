---
name: update-project-docs
description: Update the repository's public-facing documents — README.md, CONTRIBUTING.md, SECURITY.md, CODE_OF_CONDUCT.md, the issue templates and the pull request template. Covers which document owns which fact, what has to be re-verified before writing, and the GitHub community-standards checklist. Use whenever a change adds a command, an environment variable, a dependency, an endpoint, or anything else these documents claim.
---

# Update the project documents

These are the files a stranger reads before they read any code. They are wrong
by default — every change to the build, the configuration or the feature set
silently invalidates something in them.

## Step 1 — work out which file owns the fact

Each fact lives in exactly one place. Duplicating it means one copy goes stale.

| The fact                                       | Lives in                                |
| ---------------------------------------------- | --------------------------------------- |
| What the project is, what works *today*         | `README.md`                             |
| Environment variables and their defaults        | `README.md`                             |
| How to run it as a user                         | `README.md`                             |
| How to set up a development machine             | `CONTRIBUTING.md`                       |
| Branch, commit and pull request rules           | `CONTRIBUTING.md`                       |
| Which command must pass before pushing          | `CONTRIBUTING.md`                       |
| How to report a vulnerability, supported versions | `SECURITY.md`                         |
| Deliberate security decisions worth not reporting | `SECURITY.md`                         |
| Conduct and enforcement                         | `CODE_OF_CONDUCT.md`                    |
| What we ask for in a bug report                 | `.github/ISSUE_TEMPLATE/bug_report.yml` |
| What a pull request has to demonstrate          | `.github/pull_request_template.md`      |

The README links the others. Nothing links back to the README.

## Step 2 — the triggers

Do not wait to be asked. These changes make a document wrong:

- **A new environment variable** → the README configuration table. The
  `add-env-var` skill already tells you this.
- **A new `make` target a contributor needs** → `CONTRIBUTING.md`, and the
  README's Development block if it is one of the everyday four.
- **A new command or subcommand** → the README quick start.
- **A dependency with a version floor** — a Postgres feature, a Go version →
  the prerequisites in both files.
- **A feature actually shipping** → move it out of "not built yet" in the
  README. This is the one people forget, and it is the one that matters: a
  README that undersells is fixed by a two-line edit, and nobody is angry.
- **A new endpoint that is unauthenticated on purpose** → the "things worth
  knowing" list in `SECURITY.md`.

## Step 3 — verify, do not remember

Every command in these files gets run before it is committed. Not skimmed —
run.

```sh
psql -h 127.0.0.1 -U "$USER" -d postgres -c 'CREATE DATABASE openarity_docs_check'
export OPENARITY_POSTGRES_DSN="postgres://$USER@127.0.0.1:5432/openarity_docs_check?sslmode=disable"

cd apps/brain
go run ./cmd/brain migrate up      # expect: Applied migrations count=1
go run ./cmd/brain &               # expect: two listeners

curl -s 127.0.0.1:21120/healthz    # expect: ok
curl -s 127.0.0.1:21120/readyz     # expect: ready
```

Then drop the database. A quick-start block that has never been run in a clean
database is a guess.

For the configuration table, read `internal/config/config.go` and copy the
defaults from the struct tags. Do not type them from memory — the ports are
unusual (`21120`, `21121`) and the prefix is `OPENARITY_`, so a plausible
guess is a wrong one.

## Step 4 — say what is true, including the unflattering part

The README's job at this stage is to stop someone wasting an afternoon.

- A **status note near the top** — no release, no stable API, design still
  moving.
- **"What works today"** listing only what runs, and a closing line naming what
  does not exist yet.
- Never describe a planned component in the present tense. The design document
  is where intentions live; the README is where facts live.

The unused configuration entries — FalkorDB, Redis, Vault, the model router —
are marked "not used yet" for exactly this reason. Keep that marking accurate
in both directions.

## Step 5 — the two documents you do not write

- **`CODE_OF_CONDUCT.md`** is Contributor Covenant 2.1 *verbatim*. GitHub
  matches it by content, so a paraphrase loses the community-standards
  checkmark. The only editable text is the contact address in the Enforcement
  section. If it ever needs regenerating:

  ```sh
  curl -sS https://raw.githubusercontent.com/EthicalSource/contributor_covenant/release/content/version/2/1/code_of_conduct.md \
    | tail -n +6 \
    | sed 's|\[INSERT CONTACT METHOD\]|<contact>|' > CODE_OF_CONDUCT.md
  ```

  `tail -n +6` strips the TOML front matter the upstream file carries.

- **`LICENSE`** is MIT, unchanged. Changing it is not a documentation task.

## Step 6 — the community-standards checklist

GitHub scores this at *Insights → Community Standards*. All of it is present;
keep it that way:

- Description and topics in the About box — repository settings, not a file
- `README.md`
- `LICENSE`
- `CODE_OF_CONDUCT.md`
- `CONTRIBUTING.md`
- `SECURITY.md`
- Issue templates
- Pull request template

`.github/ISSUE_TEMPLATE/config.yml` sets `blank_issues_enabled: false`, so
every issue arrives through a form. Adding a template means adding it to that
directory; the contact links in `config.yml` are what route security reports
away from public issues.

## Step 7 — check it renders

Markdown tables in this repository are lint-checked with `MD060` in aligned
style: every pipe in a table must line up with the header row. Padding cells by
hand goes wrong the moment a cell is long, so let something else do it, or keep
long values out of the table entirely — the Postgres DSN sits in a code block
below its table for exactly this reason.

Before committing:

- Every relative link resolves — `[SECURITY.md](SECURITY.md)`, not a URL
- Every fenced block declares a language, `text` if nothing else fits
- The CI badge points at the workflow file that actually exists

## Step 8 — commit

Documentation changes are `docs:` and go through a pull request like anything
else, because `main` is protected.

```text
docs: add contributing, security and issue templates
docs(brain): record the new OPENARITY_GRAPH_URL default
```

Scope it to an app only when it describes that app. The root documents are
unscoped.
