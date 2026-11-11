import { createRoute } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";

import { brain, showAt, TEAM, team } from "@/test/brain";

const { Route: UiRoute } = await import("@/routes/ui");

// The layout picks its navigation by width. jsdom has no matchMedia, so each
// test says which widths match: a phone matches neither query.
function widths(min768: boolean, min1280: boolean) {
  window.matchMedia = ((query: string) => ({
    matches: query.includes("80rem") ? min1280 : query.includes("48rem") ? min768 : false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
}

const real = window.matchMedia;
afterEach(() => {
  window.matchMedia = real;
});

/**
 * The shell over a team whose summary says `waiting` people are waiting:
 * null, as the brain answers a reader without channel:read.
 */
function shell(waiting: number | null = 0, actions = ["agent:write", "channel:read"]) {
  const seen = brain({
    ...team(actions),
    "GET /whoami": { body: { subject: "ann", email: "ann@example.com" } },
    "GET /teams/{id}/summary": {
      body: {
        runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
        sessions: 0,
        waiting_senders: waiting,
        servers_failing: 0,
      },
    },
  });
  showAt("/ui", [["/ui", UiRoute]]);
  return seen;
}

/** Whether the shell read any queue itself, rather than the summary's count. */
function readAQueue(seen: { path: string }[]): boolean {
  return seen.some((s) => s.path.includes("/senders") || s.path.includes("/channels"));
}

describe("the navigation on a wide screen", () => {
  it("groups the screens by job, and keeps settings and the account at the foot", async () => {
    widths(true, true);
    shell();

    const nav = await screen.findByRole("navigation", { name: "Main" });
    const groups = within(nav)
      .getAllByRole("group")
      .map((g) => [
        g.getAttribute("aria-label"),
        within(g)
          .getAllByRole("link")
          .map((a) => a.textContent),
      ]);
    expect(groups).toEqual([
      ["Work", ["Home", "Runs", "Sessions", "Approvals"]],
      ["Build", ["Agents", "Skills", "MCP servers", "Channels", "Builder", "Blueprint"]],
      ["Understand", ["Graph"]],
      ["Team", ["Team settings", "Teams"]],
    ]);
  });

  // The count is the summary's, one read for the whole team, refreshed once a
  // minute; the shell reads no channel's queue for it.
  it("counts the senders waiting across every channel from the team's summary", async () => {
    widths(true, true);
    const seen = shell(3);
    const approvals = await screen.findByRole("link", { name: /Approvals/ });
    await waitFor(() => expect(approvals).toHaveTextContent("Approvals, 3 waiting"));
    expect(readAQueue(seen)).toBe(false);
  });

  // Without channel:read the brain leaves the count out, and so does the shell.
  it("shows no count to someone who may not see the queue", async () => {
    widths(true, true);
    const seen = shell(null, ["agent:write"]);
    const approvals = await screen.findByRole("link", { name: /Approvals/ });
    await waitFor(() => expect(seen.some((s) => s.path.endsWith("/summary"))).toBe(true));
    expect(approvals).toHaveTextContent(/^Approvals$/);
    expect(readAQueue(seen)).toBe(false);
  });

  it("shows no count when nobody is waiting", async () => {
    widths(true, true);
    const seen = shell(0);
    const approvals = await screen.findByRole("link", { name: /Approvals/ });
    await waitFor(() => expect(seen.some((s) => s.path.endsWith("/summary"))).toBe(true));
    expect(approvals).toHaveTextContent(/^Approvals$/);
  });

  it("names the team and the reader's role in it, and keeps the account in a menu", async () => {
    widths(true, true);
    shell();
    expect(await screen.findByText("platform")).toBeInTheDocument();
    expect(screen.getByText("member")).toBeInTheDocument();

    await userEvent.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    expect(await screen.findByRole("menuitem", { name: "Sign out" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /theme/i })).toBeInTheDocument();
  });
});

describe("the navigation on a tablet", () => {
  // An icon rail: the labels are the links' names, shown as tooltips.
  it("keeps every screen reachable by name from an icon rail", async () => {
    widths(true, false);
    shell();
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: "Agents" })).toBeInTheDocument();
    expect(within(nav).queryByRole("group", { name: "Build" })).toBeInTheDocument();
  });
});

describe("the navigation on a phone", () => {
  it("puts Home, the runs, Approvals and More in a bar at the bottom", async () => {
    widths(false, false);
    shell(1);

    const bar = await screen.findByRole("navigation", { name: "Quick" });
    expect(
      within(bar)
        .getAllByRole("link")
        .map((a) => a.textContent),
    ).toEqual(["Home", "Runs", expect.stringMatching(/^Approvals/)]);
    expect(within(bar).getByRole("button", { name: "More" })).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
  });

  it("opens every screen and the account from More", async () => {
    widths(false, false);
    shell();
    await userEvent.click(await screen.findByRole("button", { name: "More" }));
    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByRole("link", { name: "Agents" })).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Sign out" })).toBeInTheDocument();
  });
});

/**
 * Widths that change while the page is open, as a window resized or a tablet
 * turned: each change tells the queries' listeners, as a browser would.
 */
function resizable() {
  let min768 = false;
  let min1280 = false;
  const heard = new Set<() => void>();
  window.matchMedia = ((query: string) => ({
    get matches() {
      return query.includes("80rem") ? min1280 : query.includes("48rem") ? min768 : false;
    },
    media: query,
    addEventListener: (_: string, f: () => void) => heard.add(f),
    removeEventListener: (_: string, f: () => void) => heard.delete(f),
  })) as unknown as typeof window.matchMedia;
  return (wide768: boolean, wide1280: boolean) =>
    act(() => {
      min768 = wide768;
      min1280 = wide1280;
      for (const f of heard) f();
    });
}

/** A page under the shell holding a draft in its own state, as an editor does. */
function DraftPage() {
  const [text, setText] = useState("");
  return <input aria-label="Draft" value={text} onChange={(e) => setText(e.target.value)} />;
}
UiRoute.addChildren([
  createRoute({ getParentRoute: () => UiRoute, path: "draft", component: DraftPage }),
]);

// Each width lays the shell out differently. The page inside it must be the
// same page throughout: rebuilt, it loses whatever was typed and not saved,
// and the editors' leave question never fires for a resize.
it("keeps the page, and what was typed in it, as the width crosses each layout", async () => {
  const resize = resizable();
  brain({
    ...team([]),
    "GET /whoami": { body: { subject: "ann", email: "ann@example.com" } },
  });
  showAt("/ui/draft", [["/ui", UiRoute]]);

  const draft = await screen.findByRole("textbox", { name: "Draft" });
  await userEvent.type(draft, "half a thought");

  for (const [min768, min1280, nav] of [
    [true, false, "Main"],
    [true, true, "Main"],
    [false, false, "Quick"],
    [true, false, "Main"],
  ] as const) {
    await resize(min768, min1280);
    expect(await screen.findByRole("navigation", { name: nav })).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "Draft" })).toBe(draft);
    expect(draft).toHaveValue("half a thought");
  }
});

// The fake brain answers the team route with this team.
void TEAM;
