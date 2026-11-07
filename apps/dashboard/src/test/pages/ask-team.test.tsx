import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { NO_MODEL } from "@/lib/ask";
import { brain, pageOf, type Reply, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: HomeRoute } = await import("@/routes/ui.index");
await pageOf(HomeRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const queued = {
  id: "r-new",
  session_id: "s-new",
  agent: "orchestrator",
  status: "queued",
  input: "What failed in last night's deploy?",
  output: null,
  error: null,
  input_tokens: 0,
  output_tokens: 0,
  created_at: STAMP,
  started_at: null,
  finished_at: null,
  parent_run_id: null,
  depth: 0,
  hub: true,
};

function home(actions: string[], started: Reply = { status: 202, body: queued }) {
  const seen = brain({
    ...team(actions),
    "GET /teams/{id}/summary": {
      body: {
        runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
        sessions: 0,
        waiting_senders: null,
        servers_failing: 0,
      },
    },
    "GET /teams/{id}/runs": { body: { items: [] } },
    "POST /teams/{id}/runs": started,
  });
  const router = showAt("/ui/", [["/ui/", HomeRoute]]);
  return { seen, router };
}

const posts = (seen: Seen[]) =>
  seen.filter((s) => s.method === "POST" && s.path === `/teams/${TEAM}/runs`);

describe("asking the team from Home", () => {
  it("asks the hub, naming no agent and no session, and lands on the run's live trace", async () => {
    const { seen, router } = home(["run:write"]);
    const box = await screen.findByRole("textbox", { name: "Ask the team" });
    await userEvent.type(box, "  What failed in last night's deploy?  ");
    await userEvent.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-new"));
    expect(router.state.location.search).toEqual({ tab: "runs", run: "r-new" });
    const sent = posts(seen);
    expect(sent).toHaveLength(1);
    expect(JSON.parse(String(sent[0]?.body))).toEqual({
      input: "What failed in last night's deploy?",
    });
  });

  it("sends on Ctrl+Enter, and Enter alone is a new line", async () => {
    const { seen, router } = home(["run:write"]);
    const box = await screen.findByRole("textbox", { name: "Ask the team" });
    await userEvent.type(box, "first line{Enter}second");
    expect(posts(seen)).toHaveLength(0);
    expect(box).toHaveValue("first line\nsecond");

    await userEvent.keyboard("{Control>}{Enter}{/Control}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-new"));
    expect(JSON.parse(String(posts(seen)[0]?.body))).toEqual({ input: "first line\nsecond" });
  });

  it("offers no send for a blank question", async () => {
    const { seen } = home(["run:write"]);
    const box = await screen.findByRole("textbox", { name: "Ask the team" });
    await userEvent.type(box, "   ");
    expect(screen.getByRole("button", { name: "Ask" })).toBeDisabled();
    await userEvent.keyboard("{Control>}{Enter}{/Control}");
    expect(posts(seen)).toHaveLength(0);
  });

  // The box is the only thing on Home that writes; a reader who can't start
  // a run is not offered one the brain would refuse.
  it("is not offered to someone who may not start runs", async () => {
    home([]);
    await screen.findByText("Nothing needs you right now.");
    expect(screen.queryByRole("form", { name: "Ask the team" })).toBeNull();
  });

  it("says a team with no model can't be asked, points at its settings, and keeps the question", async () => {
    const { router } = home(["run:write"], {
      status: 409,
      text: "the team has no model set, so its orchestrator cannot run; set one in the team's settings",
    });
    const box = await screen.findByRole("textbox", { name: "Ask the team" });
    await userEvent.type(box, "Anyone there?");
    await userEvent.click(screen.getByRole("button", { name: "Ask" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(NO_MODEL);
    expect(alert).not.toHaveTextContent("orchestrator");
    expect(within(alert).getByRole("link", { name: /Open team settings/ })).toHaveAttribute(
      "href",
      "/ui/settings",
    );
    expect(box).toHaveValue("Anyone there?");
    expect(router.state.location.pathname).toBe("/ui/");

    // Editing the question clears what was said about the last try.
    await userEvent.type(box, "!");
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
