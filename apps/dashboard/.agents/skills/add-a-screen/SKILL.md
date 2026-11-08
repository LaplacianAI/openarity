---
name: add-a-screen
description: Add a screen (a route) to the dashboard. Covers the route file name and createFileRoute path, filters in the URL through validateSearch, the navGroups entry and what the command palette finds by itself, the capture plan, empty/error/forbidden states, permissions for writes, the page test, and the three layouts to look at. Use every time a route is added.
---

# Add a screen

A screen is one file under `src/routes/`, one entry in `navGroups`, one entry in
the capture plan and one page test. A screen missing from the capture plan is
never looked at again; `scripts/capture/plan.test.mjs` fails when it is missing.

## Step 1 — the route file

TanStack file routes, flat names with dots. The router plugin regenerates
`src/routeTree.gen.ts` when `make dev` or `make build` runs; never edit it.

| File | `createFileRoute` path |
| --- | --- |
| `ui.runs.index.tsx` | `"/ui/runs/"` |
| `ui.approvals.tsx` | `"/ui/approvals"` |
| `ui.agents.new.tsx` | `"/ui/agents/new"` |
| `ui.agents.$agentId.tsx` | `"/ui/agents/$agentId"` |

A list with a detail page is `ui.<name>.index.tsx` plus `ui.<name>.$id.tsx`.
The component lives in the same file and is split into its own chunk by
`autoCodeSplitting`. Everything under `/ui` renders inside `src/routes/ui.tsx`,
which gates on the token, so a screen never checks for sign-in itself.

Read the team from `useCurrentTeam()` and pass `enabled: Boolean(teamId)` to
each query, as `ui.runs.index.tsx` does.

## Step 2 — filters go in the URL

Anything a reader could link to or expect to survive a reload (a status filter,
a tab, which list) is search state, validated by the route:

```tsx
export const Route = createFileRoute("/ui/runs/")({
  validateSearch: runsSearch,
  component: Runs,
});
```

`runsSearch` returns every key, with `undefined` for a value it does not know,
so a hand-edited URL cannot reach the query. Read it with `Route.useSearch()`,
change it with `Route.useNavigate()` and `search: (prev) => ({ ...prev, ...next
})`, and put the search object in the query key.

## Step 3 — the navigation entry

Add an item inside `navGroups()` in `src/routes/ui.tsx`. Items are `{ to, label,
icon, exact }`; the groups are Work (Home, Runs, Sessions, Approvals), Build
(Agents, Skills, MCP servers, Channels), Understand (Graph) and Team (Team
settings, Teams, Deleted teams for super admins). Choose the group by the job
the screen is for, not the noun it lists.

That one edit also gives the screen to the phone's More list, the tablet rail
and ⌘K: `Shell` builds the palette's `screens` from `navGroups`. Only agents,
skills and sessions are searched by name, in `components/command-palette.tsx`;
a new kind of thing that should be found by name is added there.

The phone's bottom bar shows only Home, Runs and Approvals (`QuickBar`). Do not
add to it without asking.

## Step 4 — the capture plan

Add an entry to `SCREENS` in `scripts/capture/plan.mjs`:

```js
{ name: "agent-new", path: "/ui/agents/new" },
{ name: "agent", path: "/ui/agents", follow: "/ui/agents/", followName: "log-reader" },
```

`follow` opens a link under that path for a page that needs an id, `tabs` shoots
further tabs, `open` is the button whose dialog is also shot. The test in
`plan.test.mjs` maps every `ui.*.tsx` file to a path and fails with `no capture
for <path>`.

## Step 5 — states and permissions

- Nothing yet: `EmptyState` from `@/components/states` (title, body, one action).
- A read failed: `ErrorState` with `what`, `error` and `onRetry`. It shows the
  brain's own sentence; a 403 becomes the forbidden state and a 404 says it is
  not in this team, neither with a retry.
- A read the caller may not make: `ForbiddenState` naming the permission.
- Loading: `Skeleton` in the block's own frame, so the page does not jump.

A shared state that is new goes in `src/gallery/index.tsx` (a `Section` and a
`State` per variant), which is reviewed without a brain.

A write is offered through `usePermissions(teamId).can("agent:write")`. While
`isPending` offer nothing; when the caller cannot, show the control with what it
takes, not nothing (PRODUCT.md, principle 5). The brain checks every route
anyway.

## Step 6 — the page test

`src/test/pages/<name>.test.tsx`; the `write-tests` skill has the skeleton. A
screen with a link to another screen or search state uses `showAt`.

## Step 7 — look at it at three widths

The shell has three layouts: phone below 48rem, an icon rail from 48rem, the
full sidebar from 80rem. Check the screen at 1440, 768 and 390 in light and dark,
either with `make dev` and the browser or `make capture label=<name>` (run
`make capture-login` once; `CAPTURE_BASE_URL` comes from your shell). The screen
must answer its question at 390.

## The mistake this prevents

A route that works and was never added to `navGroups` is reachable only by URL.
One never added to `SCREENS` is never captured, so the next redesign skips it.
A filter held in `useState` is lost on reload and cannot be linked.
