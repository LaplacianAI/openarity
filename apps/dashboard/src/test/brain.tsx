/**
 * A fake brain for page tests: answers by method and path, records every
 * request, and 404s anything unrouted so a call nobody expected fails the
 * assertion that looks for it rather than hanging.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { type ComponentType, Suspense } from "react";
import { vi } from "vitest";

import { setToken } from "@/api/token";

export const TEAM = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d";

export type Reply = { status?: number; body?: unknown; text?: string };
export type Seen = { method: string; path: string; body: BodyInit | null | undefined };

/** "GET /teams/{id}/skills" style keys, with {x} matching one segment. */
export function brain(routes: Record<string, Reply | ((seen: Seen) => Reply)>): Seen[] {
  const seen: Seen[] = [];
  const compiled = Object.entries(routes).map(([key, reply]) => {
    const [method, pattern] = key.split(" ") as [string, string];
    const re = new RegExp(`^${pattern.replace(/\{[^}]+\}/g, "[^/]+")}$`);
    return { method, re, reply };
  });

  vi.stubGlobal(
    "fetch",
    vi.fn<typeof fetch>(async (input, init) => {
      const url = new URL(String(input), "http://brain.test");
      const method = init?.method ?? "GET";
      const s: Seen = { method, path: url.pathname + url.search, body: init?.body };
      seen.push(s);

      const hit = compiled.find((r) => r.method === method && r.re.test(url.pathname));
      if (!hit) return new Response("no route in the fake brain", { status: 404 });
      const reply = typeof hit.reply === "function" ? hit.reply(s) : hit.reply;
      const status = reply.status ?? 200;
      if (status === 204) return new Response(null, { status });
      if (reply.text !== undefined) return new Response(reply.text, { status });
      return new Response(JSON.stringify(reply.body ?? {}), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return seen;
}

/** The team every page is scoped to, and the caller's permissions in it. */
export function team(actions: string[], superAdmin = false): Record<string, Reply> {
  return {
    "GET /teams": { body: { items: [{ id: TEAM, name: "platform", role: "member" }] } },
    "GET /teams/{id}/permissions": { body: { actions, super_admin: superAdmin } },
  };
}

/**
 * Renders a route's component. The router plugin splits each route's
 * component into its own chunk, so the first render suspends while it
 * loads — exactly as in the browser — and needs a boundary to wait in.
 */
export function show(Page: ComponentType) {
  setToken("a-token");
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <Suspense fallback={null}>
        <Page />
      </Suspense>
    </QueryClientProvider>,
  );
}

export const STAMP = "2026-10-01T00:00:00Z";

/**
 * A route's component, loaded. The first load of a split chunk takes longer
 * than a query's default wait, so it is done once, before the tests, rather
 * than raced inside the first of them.
 */
export async function pageOf(route: { options: { component?: unknown } }): Promise<ComponentType> {
  const component = route.options.component as ComponentType & { preload?: () => Promise<void> };
  await component.preload?.();
  return component;
}
