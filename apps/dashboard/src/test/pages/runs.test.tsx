import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: RunsRoute } = await import("@/routes/ui.runs.index");
const { Route: SessionRoute } = await import("@/routes/ui.sessions.$sessionId");
await pageOf(RunsRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const RUNS = "GET /teams/{id}/runs";

function run(id: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    session_id: `s-${id}`,
    agent: "orchestrator",
    status: "succeeded",
    input: "Why did the nightly export fail?",
    output: "It ran out of disk.",
    error: null,
    input_tokens: 1200,
    output_tokens: 34,
    created_at: STAMP,
    started_at: STAMP,
    finished_at: STAMP,
    parent_run_id: null,
    depth: 0,
    hub: true,
    ...extra,
  };
}

const summary = {
  runs_24h: { queued: 1, running: 2, succeeded: 6, failed: 3, cancelled: 0 },
  sessions: 4,
  waiting_senders: 0,
  servers_failing: 0,
};

function runs(extra: Parameters<typeof brain>[0] = {}) {
  return brain({
    ...team([]),
    [RUNS]: {
      body: {
        items: [run("r1"), run("r2", { agent: "billing", hub: false, status: "failed" })],
      },
    },
    "GET /teams/{id}/summary": { body: summary },
    ...extra,
  });
}

function at(path: string) {
  return showAt(path, [
    ["/ui/runs/", RunsRoute],
    ["/ui/sessions/$sessionId", SessionRoute],
  ]);
}

/** The last request for the team's runs, as a path with its query. */
function lastAsked(seen: { path: string }[]): URL {
  const asked = seen.filter((s) => s.path.startsWith(`/teams/${TEAM}/runs`));
  return new URL(asked.at(-1)?.path ?? "", "http://brain.test");
}

function stubScroll() {
  Element.prototype.scrollIntoView = vi.fn();
  onTestFinished(() => {
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });
}

describe("the team's runs", () => {
  it("lists who answered, how it went, what was asked and what it cost", async () => {
    runs();
    at("/ui/runs");

    const row = (await screen.findByRole("link", { name: "billing" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("failed")).toBeInTheDocument();
    expect(within(row).getByText("Why did the nightly export fail?")).toBeInTheDocument();
    expect(within(row).getByText("1,234")).toBeInTheDocument();
    expect(within(row).queryByText("hub")).toBeNull();

    const hub = screen.getByRole("link", { name: "orchestrator" }).closest("tr") as HTMLElement;
    expect(within(hub).getByText("hub")).toBeInTheDocument();
  });

  it("says how the last day went, from the brain's totals", async () => {
    runs();
    at("/ui/runs");
    expect(await screen.findByText("12 runs in the last 24 hours, 3 failed.")).toBeInTheDocument();
  });

  // A run is read in its session, with the run chosen.
  it("opens a run's trace in its session, by link or from the keyboard", async () => {
    runs();
    const router = at("/ui/runs");

    const link = await screen.findByRole("link", { name: "billing" });
    expect(link).toHaveAttribute("href", "/ui/sessions/s-r2?tab=runs&run=r2");

    // Past "What is Runs?" and the three filters, the rows are one stop.
    for (const name of ["What is Runs?", "Status", "Answered by", "When"]) {
      await userEvent.tab();
      expect(document.activeElement).toHaveAccessibleName(name);
    }
    await userEvent.tab();
    const first = screen.getByRole("link", { name: "orchestrator" }).closest("tr");
    expect(first).toHaveFocus();
    await userEvent.keyboard("{ArrowDown}{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-r2"));
    expect(router.state.location.search).toEqual({ tab: "runs", run: "r2" });
  });

  it("shows what was asked as text, never as markup", async () => {
    runs({ [RUNS]: { body: { items: [run("r1", { input: "<b>bold</b> request" })] } } });
    at("/ui/runs");
    expect(await screen.findByText("<b>bold</b> request")).toBeInTheDocument();
    expect(document.querySelector("tbody b")).toBeNull();
  });
});

describe("narrowing the runs", () => {
  // In the URL, so Home links straight to "failed in the last day".
  it("asks the brain for what a linked filter names", async () => {
    const seen = runs();
    const before = Date.now();
    at("/ui/runs?status=failed&who=hub&day=true");
    await screen.findByRole("link", { name: "billing" });

    const q = lastAsked(seen).searchParams;
    expect(q.getAll("status")).toEqual(["failed"]);
    expect(q.get("hub")).toBe("true");
    const since = Date.parse(q.get("since") ?? "");
    expect(before - since).toBeGreaterThanOrEqual(24 * 60 * 60 * 1000 - 1000);
    expect(before - since).toBeLessThan(24 * 60 * 60 * 1000 + 5000);
  });

  it("asks for a named agent's runs only, as hub=false", async () => {
    const seen = runs();
    at("/ui/runs?who=agents");
    await screen.findByRole("link", { name: "billing" });
    expect(lastAsked(seen).searchParams.get("hub")).toBe("false");
  });

  it("sends no filter it was not given, and none it does not know", async () => {
    const seen = runs();
    at("/ui/runs?status=exploded&who=everyone&day=maybe");
    await screen.findByRole("link", { name: "billing" });
    expect(lastAsked(seen).search).toBe("");
  });

  it("filters by a status chosen from the list, and keeps it in the URL", async () => {
    stubScroll();
    const seen = runs();
    const router = at("/ui/runs");
    (await screen.findByRole("combobox", { name: "Status" })).focus();
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.click(await screen.findByRole("option", { name: "Failed" }));

    await waitFor(() => expect(lastAsked(seen).searchParams.getAll("status")).toEqual(["failed"]));
    expect(router.state.location.search).toEqual({ status: "failed" });
  });

  it("loads older runs with the cursor, under the same filters", async () => {
    const seen = runs({
      [RUNS]: (s) =>
        s.path.includes("cursor=")
          ? { body: { items: [run("r3", { agent: "ledger", hub: false })] } }
          : { body: { items: [run("r1")], next_cursor: "c2" } },
    });
    at("/ui/runs?status=succeeded");
    await screen.findByRole("link", { name: "orchestrator" });
    await userEvent.click(screen.getByRole("button", { name: "Load more" }));

    expect(await screen.findByRole("link", { name: "ledger" })).toBeInTheDocument();
    const q = lastAsked(seen).searchParams;
    expect(q.get("cursor")).toBe("c2");
    expect(q.getAll("status")).toEqual(["succeeded"]);
  });
});

describe("when there is nothing to show", () => {
  it("says so when the team has never run", async () => {
    runs({ [RUNS]: { body: { items: [] } } });
    at("/ui/runs");
    expect(await screen.findByText("No runs yet")).toBeInTheDocument();
  });

  it("says when nothing fits the filters, and clears them", async () => {
    const seen = runs({ [RUNS]: { body: { items: [] } } });
    const router = at("/ui/runs?status=cancelled");
    expect(await screen.findByText("No runs match")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Clear the filters" }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    await waitFor(() => expect(lastAsked(seen).search).toBe(""));
  });

  it("says why the runs could not be listed", async () => {
    runs({ [RUNS]: { status: 500, text: "the store is down" } });
    at("/ui/runs");
    expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
  });
});
