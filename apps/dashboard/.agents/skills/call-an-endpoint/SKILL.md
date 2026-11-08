---
name: call-an-endpoint
description: Use a brain endpoint from the dashboard. Covers reading the operation in apps/brain/api/openapi.yaml, transcribing its type into api/types.ts, the one function in api/index.ts through request<T>, paging, streams, queries and mutations, ApiError, the dev proxy, and the fake brain's route in the test. Use every time the dashboard calls an endpoint it did not before.
---

# Call an endpoint

The dashboard talks to the brain only through `src/api/`. `index.ts` has one
function per endpoint and none without one; `client.ts` is the only place that
does HTTP; `types.ts` holds the wire types. Nothing is generated: the types are
written by hand from the spec.

## Step 1 — read the operation

Open `apps/brain/api/openapi.yaml` and read the path, the method, the request
body, every response and the permission the route needs. Do not infer a field
from the screen design; if the spec does not have it, the type does not.

## Step 2 — the type

Add to `src/api/types.ts`, named as the JSON names, no richer than the server
(the file's own header says so):

```ts
export type Page<T> = {
  items: T[];
  next_cursor?: string;   // absent, not null, when there is no next page
};
```

A field is optional (`?: T`) when the spec omits it, and `?: T | null` only
where the spec says it is nullable. A request type is separate from the type it
returns (`AgentRequest` and `Agent`).

## Step 3 — the function

One exported function in `src/api/index.ts`, named for what it does, under the
section comment for its resource, going through `request<T>`:

```ts
export function replaceAgent(teamId: string, agentId: string, agent: AgentRequest): Promise<Agent> {
  return request<Agent>(`/teams/${teamId}/agents/${agentId}`, { method: "PUT", body: agent });
}
```

- Paths are relative: the page and the API share an origin.
- A body is `body: <object>`; `request` sets JSON headers. A file is `upload:
  FormData | Blob`; bytes back are `blob: true`.
- A list takes `cursor?: string | null` and appends `pageQuery(cursor, limit, q)`,
  which builds the query with `URLSearchParams`. Other parameters are added the
  same way, not by string concatenation of user text.
- A 204 returns `undefined`, so such a function is `Promise<void>`.
- An event stream is `request(path, { stream: true, signal })` and the body is
  read with `readFrames` from `api/sse.ts`; `followRunEvents` is the model,
  including that a 4xx ends it and a 5xx or a dropped connection resumes.
- Add `signal` to a read when a screen can abandon it.

## Step 4 — use it from a screen

Reads are `useQuery` (or `useInfiniteQuery` with `getNextPageParam:
nextCursorOf`); writes are `useMutation`. Keys start with the resource and the
team (`["pending", teamId]`), and a mutation's `onSuccess` invalidates the keys
it changed (`ui.approvals.tsx` also invalidates `["summary", teamId]`).

Do not set `retry` per query. The client in `src/routes/__root.tsx` already sets
`retry: false` for queries and mutations and `refetchOnWindowFocus: false`; a
failure is something the operator sees.

Gate a write with `usePermissions(teamId).can("<permission>")`.

## Step 5 — errors

`request` throws `ApiError(status, message)`. The message is the brain's own
sentence (it answers errors as plain text), so show it as it is:
`onError: (err: Error) => toast.error(err.message)`, or `mutation.error.message`
in the form. Branch on `err.status` only where the remedy differs (`403` in
`ui.teams.index.tsx`). A 401 is handled inside `request` by one renewal and one
retry; do not handle it in a screen.

## Step 6 — the dev proxy

`make dev` proxies `/auth`, `/teams`, `/users` and `/whoami` to
`DASHBOARD_BRAIN` (`vite.config.ts`). An endpoint under a new top-level prefix
is added to that list, or the dev server answers it with its own HTML.

## Step 7 — the test's route

In a page test, add the endpoint to `brain({...})` as `"METHOD /path/{id}"`;
`{id}` matches one segment. A call with no matching route answers 404 with "no
route in the fake brain", so an endpoint you forgot shows up as an error state
rather than a hang. Assert the request from the `Seen[]` that `brain()`
returns (`method`, `path` including the query, `body`).

## The mistake this prevents

A type with a field the brain never sends puts a number on screen that nothing
can justify. A function with no endpoint behind it is a screen with nothing
underneath. Concatenating a cursor or search text into the URL by hand breaks on
the first character that needs escaping.
