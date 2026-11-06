import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: HomeRoute } = await import("@/routes/ui.index");
await pageOf(HomeRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

type Summary = {
  runs_24h: Record<string, number>;
  sessions: number;
  waiting_senders: number | null;
  servers_failing: number;
};

const quiet: Summary = {
  runs_24h: { queued: 0, running: 1, succeeded: 9, failed: 0, cancelled: 0 },
  sessions: 3,
  waiting_senders: 0,
  servers_failing: 0,
};

function run(id: string, agent: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    session_id: `s-${id}`,
    agent,
    status: "succeeded",
    input: `asked ${id}`,
    output: null,
    error: null,
    input_tokens: 10,
    output_tokens: 5,
    created_at: STAMP,
    started_at: STAMP,
    finished_at: STAMP,
    parent_run_id: null,
    depth: 0,
    hub: true,
    ...extra,
  };
}

const RUNS = "GET /teams/{id}/runs";
const QUEUE = "GET /teams/{id}/senders/pending";

function home(
  summary: Summary,
  extra: Parameters<typeof brain>[0] = {},
  actions = ["channel:read"],
) {
  const seen = brain({
    ...team(actions),
    "GET /teams/{id}/summary": { body: summary },
    [RUNS]: (s) =>
      s.path.includes("status=failed")
        ? { body: { items: [run("f1", "billing", { status: "failed", hub: false })] } }
        : { body: { items: [run("h1", "orchestrator")], next_cursor: "more" } },
    [QUEUE]: {
      body: {
        items: [
          {
            sender_ref: "U1",
            sender_name: "Asha",
            seen_count: 1,
            first_seen: new Date(Date.now() - 5 * 60 * 60 * 1000).toISOString(),
            last_seen: STAMP,
            channel_id: "c1",
            channel_name: "support",
            channel_provider: "slack",
          },
        ],
      },
    },
    "GET /teams/{id}/mcp-servers": {
      body: {
        items: [
          {
            id: "m1",
            name: "github",
            bare: false,
            env: {},
            last_discovery_error: "the server did not answer in time",
          },
          { id: "m2", name: "linear", bare: false, env: {}, last_discovery_error: null },
        ],
      },
    },
    ...extra,
  });
  showAt("/ui/", [["/ui/", HomeRoute]]);
  return seen;
}

function asked(seen: Seen[], what: string): URL[] {
  return seen
    .filter((s) => s.path.startsWith(`/teams/${TEAM}/${what}`))
    .map((s) => new URL(s.path, "http://brain.test"));
}

describe("what Home says first", () => {
  it("says what needs the reader, in one line under its title", async () => {
    home({
      runs_24h: { ...quiet.runs_24h, failed: 3 },
      sessions: 3,
      waiting_senders: 2,
      servers_failing: 1,
    });
    expect(
      await screen.findByText(
        "3 runs failed in the last 24 hours, 2 people waiting to be let in and 1 server failing discovery.",
      ),
    ).toBeInTheDocument();
  });

  it("says when nothing does", async () => {
    home(quiet);
    expect(await screen.findByText("Nothing needs you right now.")).toBeInTheDocument();
  });

  // A count the reader may not see is not theirs to be told about.
  it("leaves out a queue the reader may not see", async () => {
    home({ ...quiet, waiting_senders: null, runs_24h: { ...quiet.runs_24h, failed: 1 } }, {}, []);
    expect(await screen.findByText("1 run failed in the last 24 hours.")).toBeInTheDocument();
  });

  // Real totals, each a way into the runs it counts.
  it("counts the last day's runs by status, each leading to those runs", async () => {
    home({
      ...quiet,
      runs_24h: { queued: 1, running: 2, succeeded: 1234, failed: 4, cancelled: 0 },
    });
    await waitFor(() =>
      expect(document.querySelector('[data-count="succeeded"]')).toHaveTextContent("1,234"),
    );
    const failed = document.querySelector('[data-count="failed"]')?.closest("a");
    expect(failed).toHaveTextContent("4");
    expect(failed).toHaveAttribute("href", "/ui/runs?status=failed&day=true");
  });

  // A link may not wrap a dl's term and its value, so the counts are a list of links.
  it("lists the counts as links, one to an item", async () => {
    home(quiet);
    const day = await screen.findByRole("region", { name: "Runs in the last 24 hours" });
    const items = within(day).getAllByRole("listitem");
    expect(items).toHaveLength(5);
    for (const item of items) expect(within(item).getByRole("link")).toBeInTheDocument();
    expect(day.querySelector("dt, dd")).toBeNull();
  });

  it("says why the summary could not be read", async () => {
    home(quiet, { "GET /teams/{id}/summary": { status: 500, text: "the store is down" } });
    expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
  });
});

describe("the runs on Home", () => {
  it("shows the last day's failures only when there are some", async () => {
    const seen = home({ ...quiet, runs_24h: { ...quiet.runs_24h, failed: 1 } });
    const block = await screen.findByRole("region", { name: "Failed in the last 24 hours" });
    expect(await within(block).findByRole("link", { name: "billing" })).toBeInTheDocument();

    const q = asked(seen, "runs").find((u) => u.searchParams.get("status") === "failed");
    expect(q?.searchParams.get("limit")).toBe("5");
    expect(Date.now() - Date.parse(q?.searchParams.get("since") ?? "")).toBeLessThan(
      24 * 60 * 60 * 1000 + 5000,
    );
  });

  it("asks for no failures when the summary says there were none", async () => {
    const seen = home(quiet);
    await screen.findByRole("region", { name: "Asked of the team" });
    expect(screen.queryByRole("region", { name: "Failed in the last 24 hours" })).toBeNull();
    expect(asked(seen, "runs").some((u) => u.searchParams.has("status"))).toBe(false);
  });

  it("shows the hub's latest runs, with a way to all of them", async () => {
    const seen = home(quiet);
    const block = await screen.findByRole("region", { name: "Asked of the team" });
    expect(await within(block).findByRole("link", { name: "orchestrator" })).toBeInTheDocument();
    expect(within(block).getByRole("link", { name: "View all" })).toHaveAttribute(
      "href",
      "/ui/runs?who=hub",
    );
    const q = asked(seen, "runs").find((u) => u.searchParams.get("hub") === "true");
    expect(q?.searchParams.get("limit")).toBe("5");
  });

  it("says when nobody has asked the team yet", async () => {
    home(quiet, { [RUNS]: { body: { items: [] } } });
    expect(await screen.findByText("Nobody has asked the team yet")).toBeInTheDocument();
  });
});

describe("what waits on Home", () => {
  it("shows who has waited longest, with a way to the queue", async () => {
    const seen = home({ ...quiet, waiting_senders: 4 });
    const block = await screen.findByRole("region", { name: "Waiting to be let in" });
    expect(await within(block).findByText("Asha")).toBeInTheDocument();
    expect(within(block).getByText("5 hours")).toBeInTheDocument();
    expect(within(block).getByRole("link", { name: "Review" })).toHaveAttribute(
      "href",
      "/ui/approvals",
    );
    expect(asked(seen, "senders/pending")[0]?.searchParams.get("limit")).toBe("3");
  });

  it("reads no queue when nobody is waiting", async () => {
    const seen = home(quiet);
    const block = await screen.findByRole("region", { name: "Waiting to be let in" });
    expect(within(block).getByText("No one is waiting.")).toBeInTheDocument();
    expect(asked(seen, "senders/pending")).toEqual([]);
  });

  it("shows no queue, and reads none, to someone who may not see it", async () => {
    const seen = home({ ...quiet, waiting_senders: null }, {}, []);
    await screen.findByRole("region", { name: "Asked of the team" });
    expect(screen.queryByRole("region", { name: "Waiting to be let in" })).toBeNull();
    expect(asked(seen, "senders/pending")).toEqual([]);
  });

  it("names the servers whose discovery failed, and why, only when some have", async () => {
    home({ ...quiet, servers_failing: 1 });
    const block = await screen.findByRole("region", { name: "Servers that need attention" });
    expect(await within(block).findByText("github")).toBeInTheDocument();
    expect(within(block).getByText("the server did not answer in time")).toBeInTheDocument();
    expect(within(block).queryByText("linear")).toBeNull();
  });

  it("reads no servers when none are failing", async () => {
    const seen = home(quiet);
    await screen.findByRole("region", { name: "Asked of the team" });
    expect(asked(seen, "mcp-servers")).toEqual([]);
  });
});

it("sends someone in no team to the teams screen", async () => {
  brain({ "GET /teams": { body: { items: [] } } });
  showAt("/ui/", [["/ui/", HomeRoute]]);
  expect(await screen.findByText("You are not in any team")).toBeInTheDocument();
  expect(screen.getByRole("link", { name: "Go to teams" })).toHaveAttribute("href", "/ui/teams");
});
