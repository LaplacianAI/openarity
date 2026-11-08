---
name: fix-lint
description: Fix a Biome or TypeScript failure in the dashboard. Covers make fmt, the rules that are errors, what is not checked, tsc failures, and when a biome-ignore is acceptable. Use when make check, make fmt-check, make lint or make typecheck fails.
---

# Fix a lint or type failure

## Step 1 — let Biome fix what it can

```sh
make fmt        # npx biome check --write .   (same as: npm run fix)
make fmt-check  # npx biome check .           what CI runs
make lint       # npx biome lint .            rules only
```

Formatting (spaces, indent 2, line width 100, double quotes, semicolons,
trailing commas "all") and import order are fixed by `make fmt`. Run it first,
then read what is left.

## Step 2 — the rules that are errors

From `biome.json`, on top of the recommended preset:

| Rule | Fix |
| --- | --- |
| `suspicious/noExplicitAny` | type it: `unknown` and narrow, or a type from `api/types.ts` |
| `suspicious/noConsole` (only `console.error` and `console.warn` are allowed) | remove it, or use a toast or a thrown `Error` |
| `correctness/useExhaustiveDependencies` | add the dependency; if that loops, the value should be a ref, a callback with `useCallback`, or computed inside the effect |
| `correctness/useHookAtTopLevel` | move the hook above any early return or condition |

The accessibility rules in the recommended preset fail on a click handler with
no key handler, a non-semantic element with a role, and `autoFocus`. Fix them
by using the right element (`button`, `a`, `fieldset`) first.

## Step 3 — what is not checked

`src/styles.css` and `src/routeTree.gen.ts` are excluded from Biome, and
`src/components/ui/**` (vendored shadcn) has the linter turned off. Do not
hand-edit the route tree to quiet anything; it regenerates. A rule firing in a
file you own is not a reason to move the code into `components/ui`.

## Step 4 — type errors

```sh
make typecheck   # npx tsc --noEmit
```

`make build` runs it too. Fix the type; do not cast through `unknown` to get
past a mismatch with the API, because `api/types.ts` is meant to say what the
brain sends. If the type is wrong, check `apps/brain/api/openapi.yaml` and
correct the type there. If the route tree is stale, run `make dev` or `make
build` once so the router plugin regenerates `routeTree.gen.ts`.

## Step 5 — a `biome-ignore`, almost never

The form is `// biome-ignore <rule>: <reason>` on the line above, and the reason
is required. The existing ones are a pattern for what qualifies:

- `lint/a11y/useSemanticElements` on the nav group in `routes/ui.tsx`: a
  `fieldset` is for form controls and these are links.
- `lint/a11y/useKeyWithClickEvents` in `components/command-palette.tsx` and
  `components/picker.tsx`: another element already handles the keys.
- `lint/suspicious/noArrayIndexKey` in `components/run-detail.tsx`: the list is
  ranked or ordered and its items can repeat.
- `lint/correctness/useExhaustiveDependencies` in `routes/ui.graph.tsx`: the
  keys stand for the lists they are made from.

A reason says why the rule is wrong *here*. "Needed to pass lint" is not one. If
the honest reason is that the code is awkward, change the code.

## Step 6 — run everything

`make check` runs `fmt-check lint typecheck cover build audit`, the same six
steps as `.github/workflows/ci.yml`. Run it before opening a pull request.

## The mistake this prevents

Silencing a rule instead of fixing the code (`any`, a suppressed dependency
list, a cast around a wrong type) moves the failure from CI to the operator's
screen.
