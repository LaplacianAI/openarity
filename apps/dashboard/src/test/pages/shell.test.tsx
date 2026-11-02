import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

const PENDING = "GET /teams/{id}/channels/{cid}/senders/pending";

function shell(pending: Record<string, number> = {}) {
  brain({
    ...team(["agent:write"]),
    "GET /whoami": { body: { subject: "ann", email: "ann@example.com" } },
    "GET /teams/{id}/channels": {
      body: { items: Object.keys(pending).map((id) => ({ id, name: id, provider: "custom" })) },
    },
    [PENDING]: (s) => {
      const id = s.path.split("/channels/")[1]?.split("/")[0] ?? "";
      const n = pending[id] ?? 0;
      return {
        body: {
          items: Array.from({ length: n }, (_, i) => ({
            external_id: `${id}-${i}`,
            channel_id: id,
          })),
        },
      };
    },
  });
  return showAt("/ui", [["/ui", UiRoute]]);
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
      ["Work", ["Home", "Sessions", "Approvals"]],
      ["Build", ["Agents", "Skills", "MCP servers", "Channels"]],
      ["Understand", ["Graph"]],
      ["Team", ["Team settings", "Teams"]],
    ]);
  });

  // A count on every page costs a request per channel, so it is read once a
  // minute, and shown only when someone is waiting.
  it("counts the senders waiting across every channel, and shows nothing when none are", async () => {
    widths(true, true);
    shell({ c1: 2, c2: 1 });
    const approvals = await screen.findByRole("link", { name: /Approvals/ });
    await waitFor(() => expect(approvals).toHaveTextContent("Approvals, 3 waiting"));
  });

  it("shows no count when nobody is waiting", async () => {
    widths(true, true);
    shell({ c1: 0 });
    const approvals = await screen.findByRole("link", { name: /Approvals/ });
    await new Promise((r) => setTimeout(r, 50));
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
    shell({ c1: 1 });

    const bar = await screen.findByRole("navigation", { name: "Quick" });
    expect(
      within(bar)
        .getAllByRole("link")
        .map((a) => a.textContent),
    ).toEqual(["Home", "Sessions", expect.stringMatching(/^Approvals/)]);
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

// The fake brain answers the team route with this team.
void TEAM;
