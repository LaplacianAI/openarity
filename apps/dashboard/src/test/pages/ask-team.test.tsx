import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { NO_MODEL } from "@/lib/ask";
import { brain, pageOf, type Reply, type Seen, showAt, TEAM, team } from "@/test/brain";

const { Route: HomeRoute } = await import("@/routes/ui.index");
await pageOf(HomeRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

/** A conversation started and the hub taking it. */
const started = { session_id: "s-new", message_id: "m-new", run_id: "r-new" };

function home(actions: string[], answer: Reply = { status: 201, body: started }) {
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
    "POST /teams/{id}/sessions": answer,
  });
  const router = showAt("/ui/", [["/ui/", HomeRoute]]);
  return { seen, router };
}

const posts = (seen: Seen[]) =>
  seen.filter((s) => s.method === "POST" && s.path === `/teams/${TEAM}/sessions`);
const sent = (s: Seen | undefined) => JSON.parse(String(s?.body)) as { text: string; key: string };

describe("asking the team from Home", () => {
  it("starts a conversation on the dashboard, and lands on it to watch and reply", async () => {
    const { seen, router } = home(["run:write"]);
    const box = await screen.findByRole("textbox", { name: "Ask the team" });
    await userEvent.type(box, "  What failed in last night's deploy?  ");
    await userEvent.click(screen.getByRole("button", { name: "Ask" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-new"));
    expect(router.state.location.search).toEqual({});
    expect(posts(seen)).toHaveLength(1);
    const body = sent(posts(seen)[0]);
    expect(body.text).toBe("What failed in last night's deploy?");
    expect(body.key).toMatch(/^[A-Za-z0-9_-]{1,64}$/);
  });

  it("sends on Ctrl+Enter, and Enter alone is a new line", async () => {
    const { seen, router } = home(["run:write"]);
    const box = await screen.findByRole("textbox", { name: "Ask the team" });
    await userEvent.type(box, "first line{Enter}second");
    expect(posts(seen)).toHaveLength(0);
    expect(box).toHaveValue("first line\nsecond");

    await userEvent.keyboard("{Control>}{Enter}{/Control}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-new"));
    expect(sent(posts(seen)[0]).text).toBe("first line\nsecond");
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

  // The brain stores the message and starts nothing; a conversation that
  // would stay silent is not opened.
  it("says a team with no model can't be asked, points at its settings, and keeps the question", async () => {
    const { router, seen } = home(["run:write"], {
      status: 201,
      body: {
        ...started,
        run_id: undefined,
        unanswered: "the team has no model set, so its hub cannot run",
      },
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

    // Asked again once a model is set, it is a new message, not the stored one.
    await userEvent.click(screen.getByRole("button", { name: "Ask" }));
    await waitFor(() => expect(posts(seen)).toHaveLength(2));
    expect(sent(posts(seen)[1]).key).not.toBe(sent(posts(seen)[0]).key);
  });
});
