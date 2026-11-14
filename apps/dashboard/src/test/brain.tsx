/**
 * A fake brain for page tests: answers by method and path, records every
 * request, and 404s anything unrouted so a call nobody expected fails the
 * assertion that looks for it rather than hanging.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  type AnyRoute,
  createMemoryHistory,
  createRootRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { render } from "@testing-library/react";
import { type ComponentType, Fragment, StrictMode, Suspense } from "react";
import { vi } from "vitest";

import { setToken } from "@/api/token";
import { TooltipProvider } from "@/components/ui/tooltip";

export const TEAM = "6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d";

/** `stream` answers 200 with that body, unread, as a server-sent event stream would. */
export type Reply = {
  status?: number;
  body?: unknown;
  text?: string;
  stream?: ReadableStream<Uint8Array>;
};
export type Seen = {
  method: string;
  path: string;
  body: BodyInit | null | undefined;
  headers: Headers;
  signal: AbortSignal | null | undefined;
};

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
      // As a real fetch does: a request already aborted is never sent.
      if (init?.signal?.aborted) throw new DOMException("The operation was aborted.", "AbortError");
      const s: Seen = {
        method,
        path: url.pathname + url.search,
        body: init?.body,
        headers: new Headers(init?.headers),
        signal: init?.signal,
      };
      seen.push(s);

      const hit = compiled.find((r) => r.method === method && r.re.test(url.pathname));
      if (!hit) return new Response("no route in the fake brain", { status: 404 });
      const reply = typeof hit.reply === "function" ? hit.reply(s) : hit.reply;
      const status = reply.status ?? 200;
      if (reply.stream) return streamed(reply.stream, init?.signal);
      if (status === 204) return new Response(null, { status });
      if (reply.text !== undefined) return new Response(reply.text, { status });
      return new Response(JSON.stringify(reply.body ?? {}), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
  return seen;
}

let held: Promise<void> | null = null;

/**
 * Holds every upload half sent until release is called, so a test can see a
 * send while it is going up.
 */
export function holdUploads(): () => void {
  let release = () => {};
  held = new Promise((r) => {
    release = () => {
      held = null;
      r();
    };
  });
  return release;
}

/**
 * XMLHttpRequest, answered by the fake brain's fetch: what the client uses
 * for an upload it reports progress on. It reports half the body sent, waits
 * while uploads are held, then all of it, then the answer.
 */
class FakeXHR {
  upload: { onprogress: ((e: ProgressEvent) => void) | null } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  responseType = "";
  status = 0;
  statusText = "";
  response: ArrayBuffer | null = null;
  private method = "GET";
  private path = "";
  private headers = new Headers();
  private answer = new Headers();

  open(method: string, path: string) {
    this.method = method;
    this.path = path;
  }
  setRequestHeader(name: string, value: string) {
    this.headers.set(name, value);
  }
  getAllResponseHeaders(): string {
    return Array.from(this.answer.entries())
      .map(([k, v]) => `${k}: ${v}`)
      .join("\r\n");
  }
  abort() {
    this.onabort?.();
  }
  async send(body: XMLHttpRequestBodyInit | null) {
    const progress = (loaded: number) =>
      this.upload.onprogress?.({ lengthComputable: true, loaded, total: 100 } as ProgressEvent);
    progress(50);
    if (held) await held;
    progress(100);
    try {
      const res = await fetch(this.path, {
        method: this.method,
        headers: this.headers,
        body: body as BodyInit | null,
      });
      this.status = res.status;
      this.statusText = res.statusText;
      this.answer = res.headers;
      this.response = await res.arrayBuffer();
      this.onload?.();
    } catch {
      this.onerror?.();
    }
  }
}

/**
 * A stream response that fails its reader when the request is aborted, as a
 * real fetch does. Built by hand rather than with `new Response(stream)`:
 * which ReadableStream jsdom and undici each accept differs between Node
 * releases, and a plain object reads the same on all of them.
 */
function streamed(source: ReadableStream<Uint8Array>, signal?: AbortSignal | null): Response {
  const reader = source.getReader();
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      signal?.addEventListener("abort", () =>
        controller.error(new DOMException("The operation was aborted.", "AbortError")),
      );
    },
    async pull(controller) {
      const { done, value } = await reader.read();
      try {
        if (done) controller.close();
        else controller.enqueue(value);
      } catch {
        // Aborted while this read was pending; the reader already has its error.
      }
    },
    cancel: (reason) => reader.cancel(reason),
  });
  return {
    ok: true,
    status: 200,
    headers: new Headers({ "Content-Type": "text/event-stream" }),
    body,
  } as Response;
}

/** A stream the test writes to as it goes, and the signal of whoever reads it. */
export function feed() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const stream = new ReadableStream<Uint8Array>({
    start(c) {
      controller = c;
    },
  });
  const encoder = new TextEncoder();
  return {
    stream,
    push: (text: string) => controller.enqueue(encoder.encode(text)),
    close: () => controller.close(),
  };
}

/** A stream that delivers these chunks, exactly as split, and closes. */
export function chunks(...parts: (string | Uint8Array)[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream<Uint8Array>({
    start(c) {
      for (const p of parts) c.enqueue(typeof p === "string" ? encoder.encode(p) : p);
      c.close();
    },
  });
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
      <TooltipProvider>
        <Suspense fallback={null}>
          <Page />
        </Suspense>
      </TooltipProvider>
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

/**
 * Renders the given routes under a real router, starting at `at`, for pages
 * that link to one another or keep state in the URL. Each route is the page's
 * own `Route`, hung off a bare root at its full path — what the generated
 * tree does under the /ui layout, without the layout's sidebar and sign-in.
 * Returns the router, so a test can read where it ended up.
 */
export function showAt(at: string, routes: [path: string, route: AnyRoute][], strict = false) {
  setToken("a-token");
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const root = createRootRoute({ component: Outlet });
  for (const [path, route] of routes) {
    route.update({ id: path, path, getParentRoute: () => root } as never);
  }
  const router = createRouter({
    routeTree: root.addChildren(routes.map(([, r]) => r)),
    history: createMemoryHistory({ initialEntries: [at] }),
  });
  const Wrap = strict ? StrictMode : Fragment;
  render(
    <Wrap>
      <QueryClientProvider client={client}>
        <TooltipProvider>
          <RouterProvider router={router} />
        </TooltipProvider>
      </QueryClientProvider>
    </Wrap>,
  );
  return router;
}
