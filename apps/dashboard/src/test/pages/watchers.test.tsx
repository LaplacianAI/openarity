import { QueryClient } from "@tanstack/react-query";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { Watcher } from "@/api/types";
import { brain, pageOf, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: ListRoute } = await import("@/routes/ui.schedules.index");
const { Route: NewRoute } = await import("@/routes/ui.schedules.watchers.new");
const { Route: DetailRoute } = await import("@/routes/ui.schedules.watchers.$watcherId");
await Promise.all([pageOf(ListRoute), pageOf(NewRoute), pageOf(DetailRoute)]);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const ALL = ["schedule:read", "schedule:write"];
const SECRET = "whsec_0123456789abcdef";
const HOOK = "https://brain.example/hooks/watchers/abc";

function watcher(over: Partial<Watcher> = {}): Watcher {
  return {
    id: "w1",
    name: "prices",
    objective: "Tell me what moved.",
    source: "web_page",
    url: "https://example.com/prices",
    every_seconds: 7200,
    cooldown_seconds: 3600,
    hub: true,
    target: { kind: "own" },
    session_id: "sess-1",
    status: "active",
    created_at: STAMP,
    updated_at: STAMP,
    ...over,
  };
}

function at(path: string, client?: QueryClient) {
  return showAt(
    path,
    [
      ["/ui/schedules/", ListRoute],
      ["/ui/schedules/watchers/new", NewRoute],
      ["/ui/schedules/watchers/$watcherId", DetailRoute],
    ],
    false,
    client,
  );
}

/** Everything the client holds: every query's data and every mutation's. */
function held(client: QueryClient): string {
  return JSON.stringify([
    client
      .getQueryCache()
      .getAll()
      .map((q) => q.state.data),
    client
      .getMutationCache()
      .getAll()
      .map((m) => m.state.data),
  ]);
}

function bodyOf(seen: Seen[], method: string, path: string): unknown {
  const s = seen.find((x) => x.method === method && x.path.split("?")[0] === path);
  return s ? JSON.parse(String(s.body)) : undefined;
}

const DETAIL = {
  "GET /teams/{id}/watchers/{wid}/checks": {
    body: {
      items: [
        { checked_at: STAMP, outcome: "changed", detail: "price moved", run_id: "r1" },
        { checked_at: STAMP, outcome: "error", detail: "404 Not Found" },
      ],
    },
  },
};

describe("making a watcher", () => {
  it("asks for what each source takes", async () => {
    brain({ ...team(ALL) });
    at("/ui/schedules/watchers/new");
    expect(await screen.findByLabelText("Page address")).toBeInTheDocument();
    expect(screen.getByLabelText("Only this part (optional)")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "A feed" }));
    expect(screen.getByLabelText("Feed address")).toBeInTheDocument();
    expect(screen.queryByLabelText("Only this part (optional)")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("radio", { name: "A webhook" }));
    expect(screen.queryByLabelText(/address/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("How often it looks")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Cooldown")).toBeInTheDocument();
  });

  it("sends a page with its selector and a cooldown of none", async () => {
    const seen = brain({
      ...team(ALL),
      "POST /teams/{id}/watchers": { status: 201, body: watcher() },
      "GET /teams/{id}/watchers/{wid}": { body: watcher() },
      ...DETAIL,
    });
    const router = at("/ui/schedules/watchers/new");
    await userEvent.type(await screen.findByLabelText("Name"), "prices");
    await userEvent.type(screen.getByLabelText("What each run does"), "Tell me what moved.");
    await userEvent.type(screen.getByLabelText("Page address"), "https://example.com/prices");
    await userEvent.type(screen.getByLabelText("Only this part (optional)"), "#table");
    await userEvent.type(screen.getByLabelText("Cooldown"), "0");
    await userEvent.click(screen.getByRole("button", { name: "Make watcher" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/schedules/watchers/w1"));
    expect(bodyOf(seen, "POST", `/teams/${TEAM}/watchers`)).toEqual({
      name: "prices",
      objective: "Tell me what moved.",
      source: "web_page",
      url: "https://example.com/prices",
      selector: "#table",
      cooldown_seconds: 0,
    });
  });

  it("shows a webhook's address and secret once, keeps the secret out of the cache, and lets it go", async () => {
    brain({
      ...team(ALL),
      "POST /teams/{id}/watchers": {
        status: 201,
        body: watcher({
          source: "webhook",
          url: undefined,
          hook_url: HOOK,
          secret: SECRET,
          status: "active",
        }),
      },
      "GET /teams/{id}/watchers/{wid}": {
        body: watcher({ source: "webhook", url: undefined, hook_url: HOOK }),
      },
      ...DETAIL,
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const router = at("/ui/schedules/watchers/new", client);
    await userEvent.type(await screen.findByLabelText("Name"), "deploys");
    await userEvent.type(screen.getByLabelText("What each run does"), "Summarise the deploy.");
    await userEvent.click(screen.getByRole("radio", { name: "A webhook" }));
    await userEvent.click(screen.getByRole("button", { name: "Make watcher" }));

    const reveal = await screen.findByRole("region", { name: "The watcher's secret" });
    expect(within(reveal).getByText(SECRET)).toBeInTheDocument();
    expect(within(reveal).getByText(HOOK)).toBeInTheDocument();
    // The watcher it made is kept, without its secret.
    expect(client.getQueryData(["watchers", TEAM, "w1"])).toMatchObject({ hook_url: HOOK });
    expect(
      JSON.stringify(
        client
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
      ),
    ).not.toContain(SECRET);
    expect(window.sessionStorage.getItem("openarity.session") ?? "").not.toContain(SECRET);
    expect(JSON.stringify(window.history.state ?? {})).not.toContain(SECRET);

    await userEvent.click(within(reveal).getByRole("button", { name: "I have copied it" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/schedules/watchers/w1"));
    await screen.findByRole("heading", { name: "prices" });
    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain(SECRET);
    await waitFor(() => expect(held(client)).not.toContain(SECRET));
  });

  it("refuses an address that is not one, and sends nothing", async () => {
    const seen = brain({ ...team(ALL) });
    at("/ui/schedules/watchers/new");
    await userEvent.type(await screen.findByLabelText("Name"), "x");
    await userEvent.type(screen.getByLabelText("What each run does"), "y");
    await userEvent.type(screen.getByLabelText("Page address"), "example.com");
    await userEvent.click(screen.getByRole("button", { name: "Make watcher" }));
    expect(await screen.findByText(/starting https:\/\//)).toBeInTheDocument();
    expect(seen.some((s) => s.method === "POST")).toBe(false);
  });
});

describe("a watcher's page", () => {
  it("says what it watches, and lists each look in words", async () => {
    brain({ ...team(ALL), "GET /teams/{id}/watchers/{wid}": { body: watcher() }, ...DETAIL });
    at("/ui/schedules/watchers/w1");
    expect(
      await screen.findByText(/watches the page example.com\/prices, every 2 h/),
    ).toBeInTheDocument();
    expect(await screen.findByText("Changed: a run started")).toBeInTheDocument();
    expect(screen.getByText("Could not read it")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Look now" })).toBeInTheDocument();
  });

  it("makes a webhook waiting for its secret one, shown here once and dropped on Done", async () => {
    const seen = brain({
      ...team(ALL),
      "GET /teams/{id}/watchers/{wid}": {
        body: watcher({
          source: "webhook",
          url: undefined,
          hook_url: HOOK,
          status: "waiting_secret",
        }),
      },
      "POST /teams/{id}/watchers/{wid}/secret": {
        status: 201,
        body: { secret: SECRET, url: HOOK },
      },
      ...DETAIL,
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    at("/ui/schedules/watchers/w1", client);
    expect(await screen.findByText(/It starts once it has a secret/)).toBeInTheDocument();
    await userEvent.click(await screen.findByRole("button", { name: "Generate secret" }));
    const reveal = await screen.findByRole("region", { name: "The watcher's secret" });
    expect(within(reveal).getByText(SECRET)).toBeInTheDocument();
    expect(seen.filter((s) => s.path.endsWith("/secret"))).toHaveLength(1);

    await userEvent.click(within(reveal).getByRole("button", { name: "I have copied it" }));
    expect(screen.queryByText(SECRET)).not.toBeInTheDocument();
    await waitFor(() => expect(held(client)).not.toContain(SECRET));
  });

  it("sends a cooldown of none as 0, and clears a selector", async () => {
    const seen = brain({
      ...team(ALL),
      "GET /teams/{id}/watchers/{wid}": { body: watcher({ selector: "#t" }) },
      "PATCH /teams/{id}/watchers/{wid}": { body: watcher() },
      ...DETAIL,
    });
    at("/ui/schedules/watchers/w1");
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const cooldown = screen.getByLabelText("Cooldown");
    await userEvent.clear(cooldown);
    await userEvent.type(cooldown, "0");
    await userEvent.clear(screen.getByLabelText("Only this part (optional)"));
    expect(screen.queryByLabelText("Page address")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(bodyOf(seen, "PATCH", `/teams/${TEAM}/watchers/w1`)).toEqual({
        selector: "",
        cooldown_seconds: 0,
      }),
    );
  });
});

describe("the watchers list", () => {
  it("lists the reader's own, with what each watches", async () => {
    brain({
      ...team(ALL),
      "GET /teams/{id}/scheduled/news": { body: { items: [], count: 0, more: false } },
      "GET /teams/{id}/watchers": {
        body: {
          items: [
            watcher(),
            watcher({
              id: "w2",
              name: "deploys",
              source: "webhook",
              url: undefined,
              status: "waiting_secret",
            }),
          ],
        },
      },
    });
    at("/ui/schedules?view=watchers");
    const list = await screen.findByRole("region", { name: "Your watchers" });
    expect(await within(list).findByText("its address is called")).toBeInTheDocument();
    expect(within(list).getByText("needs its secret")).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "New watcher" })).toHaveAttribute(
      "href",
      "/ui/schedules/watchers/new",
    );
  });
});
