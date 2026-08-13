---
name: file-an-issue
description: File a bug report or a feature request against Openarity, or route something that is neither. Covers which template to use, why blank issues are disabled, every required field and what makes each one actionable, the reproduction standard, and what must never appear in a public issue. Use for every issue.
---

# File an issue

`blank_issues_enabled: false` — every issue arrives through a form. There are
two, plus two contact links that route things away from the tracker entirely.

## Step 1 — pick the right destination

| What you have                                   | Where it goes                          |
| ----------------------------------------------- | -------------------------------------- |
| Behaviour differs from how it is documented      | **Bug report**                          |
| Something is hard or impossible today            | **Feature request**                     |
| A vulnerability, or anything with a credential   | **Security advisory** — never an issue  |
| A question, or an idea that is not yet concrete  | **Discussions**                         |

Both contact links are in `.github/ISSUE_TEMPLATE/config.yml`. Getting this
wrong has a cost in one direction only: a vulnerability filed as a public issue
cannot be unpublished. When unsure, use the advisory link — see
[SECURITY.md](../../../SECURITY.md).

"It does not do X yet" is a feature request, not a bug. The README's "not built
yet" list is the boundary: the graph, the planner, the agent runtime, channel
adapters and the dashboard do not exist, and `oa` cannot log in against a real
provider. None of those are bugs.

## Step 2 — the bug report

Every field is required except **Logs or output**, and every one is required
because a report missing it cannot be acted on.

**Where** — the area dropdown. `brain — API`, `brain — storage`,
`brain — configuration`, `CLI — oa`, `deployment`, `documentation`. Pick
`not sure` rather than guessing; a wrong label sends it to the wrong reader.

**What happened** — what you did and what the result was, in that order. The
result is what the machine printed, not your interpretation of it.

**What you expected instead** — say it even when it seems obvious. Half of all
bug reports are a disagreement about what the documented behaviour is, and this
field is where that becomes visible.

**Steps to reproduce** — the *smallest* sequence that shows it. Start from a
state someone else can reach:

```text
1. docker run --rm -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:18
2. cd apps/brain && go run ./cmd/brain migrate up
3. ...
```

Not "run the server and try it". If the smallest sequence needs a specific
config file, paste it — with secrets replaced by placeholders, not by real
values you consider harmless.

**Logs or output** — rendered as a code block, so no backticks. Paste it rather
than describing it. For anything about what `oa` prints, capture a real
terminal, because colour and column alignment do not exist in a pipe:

```sh
script -q /dev/null oa context list
```

**Version or commit** — `git rev-parse --short HEAD`, or a tag. There is no
release yet, so a commit is the only real answer.

**Environment** — OS and Go version. Add Postgres for a brain bug, and the
terminal plus `$TERM` for anything about what `oa` prints: theme detection sends
OSC 11 and refuses when `TERM` starts `screen`, `tmux` or `dumb`, so the value
is often the whole explanation.

## Step 3 — the feature request

**The problem** — the situation, not the solution. This is the field the
template asks for explicitly and the one most often filled with a solution
anyway. "I cannot see which teams a user belongs to without opening psql" is a
problem. "Add a `--json` flag to `oa users`" is a solution to a problem that was
never stated, and it forecloses the discussion.

**What you would like to happen** — now propose it.

**Alternatives you considered** — including doing nothing, and why that is not
enough. A proposal with no alternatives reads as a decision already made.

**Area** — same dropdown discipline as the bug report.

**Would you be willing to work on this?** — optional and honest. Ticking it
does not commit you to a deadline; leaving it unticked does not make the
proposal worth less.

Openarity is early and the design still moves. Opening this **before** writing
code is the point — it saves building against something about to change, and
CONTRIBUTING.md asks for it for anything larger than a bug fix.

## Step 4 — before submitting

The bug report's two required checkboxes are claims:

- **Searched existing issues** — actually search, including closed ones. A
  closed issue with the reasoning in it is a better answer than a new thread.
- **Not a security vulnerability** — if you are unsure whether something is,
  that uncertainty is itself the reason to use the advisory link instead.

## What never goes in a public issue

- A token, password, DSN with credentials, or private key — including in pasted
  logs and in a config file you thought was safe. A real local password reached
  a documentation file in this repository once; the same mistake in an issue is
  public immediately and permanently.
- An internal hostname or IP you would not put in the README.
- A vulnerability, or a description precise enough to become one.
- Someone else's personal data — an email in a log is personal data.

Redact by replacing, never by truncating. `Bearer oa_live_7f3…` still tells an
attacker the prefix, the length and the format.
