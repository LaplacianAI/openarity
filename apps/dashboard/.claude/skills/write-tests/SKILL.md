---
name: write-tests
description: Write a dashboard test, either a page test in src/test/pages or a unit test beside a module. Covers the page-test skeleton, brain() and team(), the Seen[] it returns, show versus showAt, role queries and userEvent, coverage thresholds, and the step that proves a test fails when the guard is broken. Use for every test.
---

# Write a test

Vitest with jsdom, Testing Library. A test is not done until it has failed
against broken code.

## Step 1 — pick the level

| Testing | Where |
| --- | --- |
| a pure function in `lib/` or `api/` | `<module>.test.ts` beside it, no DOM |
| a shared component | `<component>.test.tsx` beside it |
| a screen | `src/test/pages/<name>.test.tsx` |

## Step 2 — the page-test skeleton

```tsx
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, showAt, TEAM, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.approvals");
await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});
```

The route is imported at the top level and `await pageOf(Route)` loads its split
chunk once, before the tests, so the first test does not race it. The
`afterEach` removes the stubbed `fetch` and the token; leaving either behind
fails a different test later.

## Step 3 — the fake brain

`brain(routes)` stubs `fetch` and returns the recorded requests:

```tsx
const seen = brain({
  ...team(["run:write"]),
  "GET /teams/{id}/runs": { body: { items: [run("r1")] } },
  "POST /teams/{id}/runs/{id}/cancel": { status: 204 },
});
```

- Keys are `"METHOD /path/{x}"`; `{x}` matches one segment. An unrouted call
  answers 404, so a call nobody expected fails the assertion that looks for it.
- `team(actions, superAdmin = false)` answers `GET /teams` (one team, `TEAM`)
  and `GET /teams/{id}/permissions`. Call it with the permissions the test is
  about, including `[]` for a reader.
- A reply is `{ status, body }`, `{ text }`, or a function of the `Seen`
  (`method`, `path` with its query, `body`, `headers`, `signal`).
- Assert what was asked: `seen.some((s) => s.path.includes("/senders/pending"))`
  and `JSON.parse(String(s.body))` for a write. Assert what was not asked too.
- Streams: `{ stream: feed().stream }` with `push`/`close` from `feed()`, or
  `chunks("...", "...")` for fixed pieces.
- `STAMP` is the fixed timestamp for fixtures. Write fixtures as small factory
  functions (`run(id, extra)`, `waiting(ref, name, ...)`) with overrides.

## Step 4 — render

- `show(Page)` renders one component under a `QueryClient` (`retry: false`) and
  `TooltipProvider`.
- `showAt(path, [[routePath, Route], ...])` renders under a real memory router,
  for a page that links to another or keeps state in the URL. Use the route path
  exactly as `createFileRoute` has it (`"/ui/runs/"` with its slash). It returns
  the router, so a test can read where it ended up.

## Step 5 — query like a person

Role and name, in the order a person would find it:

```tsx
await userEvent.click(await screen.findByRole("option", { name: "chen" }));
await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
expect(within(row).getByRole("alert")).toHaveTextContent("Choose which member they are first.");
```

`findBy*` for anything that arrives after a fetch, `getBy*` once it is there,
`within(row)` to scope. Use `userEvent`, not `fireEvent`. Reach for a test id
only when nothing else names the element. A Radix select needs
`Element.prototype.scrollIntoView` stubbed in jsdom (see `stubScroll` in
`approvals.test.tsx`).

Test the sentence a reader sees, not a class name or a colour. The palette is
pinned separately by `src/test/palette.test.ts`.

## Step 6 — coverage

`make cover` fails below the thresholds in `vite.config.ts`: lines 75,
statements 75, branches 65, functions 60. Never lower them to make a change
pass; raise them as coverage grows. `src/components/ui/**`, `src/test/**`,
`main.tsx` and `routeTree.gen.ts` are excluded. `make check` runs the lot.

## Step 7 — break it

For each guard the test claims to cover (a permission check, a disabled button,
a filter dropped from the query, an error branch), break it in the code, run
`make test`, and see the test fail. Put it back. A test that still passes proves
nothing about that guard; fix the test, or delete the guard if nothing can fire
it.

## The mistake this prevents

A test that asserts only that the page renders, or one that passes with the
permission check deleted. The first catches nothing; the second is the reason a
reader sees a button the brain will refuse.
