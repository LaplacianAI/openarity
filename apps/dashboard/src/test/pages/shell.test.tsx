import { createRoute } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { useCurrentTeam } from "@/api/session";
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
function shell(
  waiting: number | null = 0,
  actions = ["agent:write", "channel:read"],
  more: Parameters<typeof brain>[0] = {},
) {
  const seen = brain({
    ...team(actions),
    ...more,
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
  it("groups the screens by job, what waits for a person first, and the team last", async () => {
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
    // Home stands alone, first, with no group or heading of its own.
    expect(within(nav).getAllByRole("link")[0]).toHaveAccessibleName("Home");
    expect(groups).toEqual([
      ["Needs you", ["Sender approvals", "Tool approvals", "Improvements"]],
      ["Work", ["Runs", "Sessions"]],
      ["Build", ["Agents", "Skills", "MCP servers", "Channels", "Builder", "Blueprint", "Graph"]],
      ["Team", ["Team settings", "Teams"]],
    ]);
  });

  it("counts the changes waiting for an approver", async () => {
    widths(true, true);
    const seen = shell(0, ["agent:write", "improvement:approve"], {
      "GET /teams/{id}/improvements": { body: { items: [{ id: "p1" }, { id: "p2" }] } },
    });
    const link = await screen.findByRole("link", { name: /Improvements/ });
    await waitFor(() => expect(link).toHaveTextContent("Improvements, 2 waiting"));
    const asked = seen.find((s) => s.path.includes("/improvements"));
    expect(asked?.path).toContain("status=waiting");
  });

  it("asks nothing about changes of someone who may not approve them", async () => {
    widths(true, true);
    // Tool approvals' count is asked once permissions are known, as this
    // one would be: by then, the change count has had its chance.
    const seen = shell(0, ["agent:write", "approval:read"]);
    const link = await screen.findByRole("link", { name: /Improvements/ });
    await waitFor(() => expect(seen.some((s) => s.path.includes("/approvals"))).toBe(true));
    expect(link).toHaveTextContent(/^Improvements$/);
    expect(seen.some((s) => s.path.includes("/improvements"))).toBe(false);
  });

  // The count is the summary's, one read for the whole team, refreshed once a
  // minute; the shell reads no channel's queue for it.
  it("counts the senders waiting across every channel from the team's summary", async () => {
    widths(true, true);
    const seen = shell(3);
    const approvals = await screen.findByRole("link", { name: /Sender approvals/ });
    await waitFor(() => expect(approvals).toHaveTextContent("Sender approvals, 3 waiting"));
    expect(readAQueue(seen)).toBe(false);
  });

  // Without channel:read the brain leaves the count out, and so does the shell.
  it("shows no count to someone who may not see the queue", async () => {
    widths(true, true);
    const seen = shell(null, ["agent:write"]);
    const approvals = await screen.findByRole("link", { name: /Sender approvals/ });
    await waitFor(() => expect(seen.some((s) => s.path.endsWith("/summary"))).toBe(true));
    expect(approvals).toHaveTextContent(/^Sender approvals$/);
    expect(readAQueue(seen)).toBe(false);
  });

  it("shows no count when nobody is waiting", async () => {
    widths(true, true);
    const seen = shell(0);
    const approvals = await screen.findByRole("link", { name: /Sender approvals/ });
    await waitFor(() => expect(seen.some((s) => s.path.endsWith("/summary"))).toBe(true));
    expect(approvals).toHaveTextContent(/^Sender approvals$/);
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
  it("puts Home, the runs, Sender approvals and More in a bar at the bottom", async () => {
    widths(false, false);
    shell(1);

    const bar = await screen.findByRole("navigation", { name: "Quick" });
    expect(
      within(bar)
        .getAllByRole("link")
        .map((a) => a.textContent),
    ).toEqual(["Home", "Runs", expect.stringMatching(/^Sender approvals/)]);
    expect(within(bar).getByRole("button", { name: "More" })).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Main" })).toBeNull();
  });

  // Tool approvals and Improvements are only under More, so More says when
  // something waits there.
  it("marks More while something waits on a screen only it reaches", async () => {
    widths(false, false);
    shell(0, ["agent:write", "improvement:approve"], {
      "GET /teams/{id}/improvements": { body: { items: [{ id: "p1" }] } },
    });
    const bar = await screen.findByRole("navigation", { name: "Quick" });
    expect(
      await within(bar).findByRole("button", { name: "More, something waits there" }),
    ).toBeInTheDocument();
  });

  it("leaves More plain while nothing waits there", async () => {
    widths(false, false);
    const seen = shell(3);
    const bar = await screen.findByRole("navigation", { name: "Quick" });
    await waitFor(() => expect(seen.some((s) => s.path.endsWith("/summary"))).toBe(true));
    expect(within(bar).getByRole("button", { name: "More" })).toBeInTheDocument();
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
/** A page that says which team it is showing, read as every screen reads it. */
function TeamProbe() {
  const { teamId } = useCurrentTeam();
  return <p>showing {teamId}</p>;
}
UiRoute.addChildren([
  createRoute({ getParentRoute: () => UiRoute, path: "draft", component: DraftPage }),
  createRoute({ getParentRoute: () => UiRoute, path: "/", component: () => <p>home page</p> }),
  createRoute({ getParentRoute: () => UiRoute, path: "agents", component: TeamProbe }),
  createRoute({ getParentRoute: () => UiRoute, path: "teams", component: TeamProbe }),
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

const OTHER = "8a2c9e10-3b4d-4f5e-8a6b-7c8d9e0f1a2b";

/** Two teams, the first current, and the shell open at `at`. */
function twoTeams(at: string) {
  window.sessionStorage.removeItem("openarity.team");
  Element.prototype.scrollIntoView = vi.fn();
  onTestFinished(() => {
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    window.sessionStorage.removeItem("openarity.team");
  });
  widths(true, true);
  brain({
    ...team([]),
    "GET /teams": {
      body: {
        items: [
          { id: TEAM, name: "platform", role: "admin" },
          { id: OTHER, name: "ops", role: "admin" },
        ],
      },
    },
    "GET /whoami": { body: { subject: "ann", email: "ann@example.com" } },
  });
  return showAt(at, [["/ui", UiRoute]]);
}

async function switchTo(name: string) {
  (await screen.findByRole("combobox", { name: "Current team" })).focus();
  await userEvent.keyboard("{ArrowDown}");
  await userEvent.click(await screen.findByRole("option", { name }));
}

describe("switching teams", () => {
  // One team's screen shows that team's things, which the new team does not
  // have: switching from it goes Home rather than leave the old team's page.
  it("goes Home from a screen about one team", async () => {
    const router = twoTeams("/ui/agents");
    expect(await screen.findByText(`showing ${TEAM}`)).toBeInTheDocument();

    await switchTo("ops");

    expect(await screen.findByText("home page")).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/ui");
    expect(window.sessionStorage.getItem("openarity.team")).toBe(OTHER);
  });

  // Every screen reads the one current team: the page that is open moves
  // with the picker, not only the picker itself.
  it("moves the open screen to the new team, and leaves a screen about every team where it is", async () => {
    const router = twoTeams("/ui/teams");
    expect(await screen.findByText(`showing ${TEAM}`)).toBeInTheDocument();

    await switchTo("ops");

    expect(await screen.findByText(`showing ${OTHER}`)).toBeInTheDocument();
    expect(router.state.location.pathname).toBe("/ui/teams");
  });
});
