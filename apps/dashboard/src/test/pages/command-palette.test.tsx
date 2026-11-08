import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";

import { brain, type Reply, type Seen, showAt, TEAM, team } from "@/test/brain";

const { Route: UiRoute } = await import("@/routes/ui");

const real = window.matchMedia;
let seen: Seen[] = [];
afterEach(() => {
  window.matchMedia = real;
});

function open(actions: string[] = [], started: Reply = { status: 404 }) {
  window.matchMedia = ((query: string) => ({
    matches: true,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  seen = brain({
    ...team(actions),
    "POST /teams/{id}/runs": started,
    "GET /whoami": { body: { subject: "ann" } },
    "GET /teams/{id}/channels": { body: { items: [] } },
    "GET /teams/{id}/agents": {
      body: {
        items: [
          { id: "a-1", name: "log-reader", description: "Reads logs" },
          { id: "a-2", name: "ops-lead" },
        ],
      },
    },
    "GET /teams/{id}/skills": { body: { items: [{ name: "incident-postmortem" }] } },
    "GET /teams/{id}/sessions": {
      body: {
        items: [
          { id: "s-1", provider_ref: "eng-support:t-1001", kind: "thread" },
          {
            id: "s-2",
            provider_ref: "",
            kind: "direct",
            first_asked: "Why did the nightly export fail?",
          },
        ],
      },
    },
  });
  return showAt("/ui", [["/ui", UiRoute]]);
}

describe("the command palette", () => {
  it("opens on ⌘K and finds a screen or an agent by name, opening the agent's own page", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });

    await userEvent.keyboard("{Meta>}k{/Meta}");
    const palette = await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.type(within(palette).getByRole("combobox"), "log");

    const options = await within(palette).findAllByRole("option");
    expect(options.map((o) => o.textContent)).toContain("log-readerAgent");
    expect(options[0]).toHaveAttribute("aria-selected", "true");

    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/agents/a-1"));
    expect(screen.queryByRole("dialog", { name: "Go to" })).toBeNull();
  });

  it("moves through what it found with the arrow keys, and opens a session's own page", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });

    await userEvent.click(screen.getByRole("button", { name: /Search/ }));
    const palette = await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.type(within(palette).getByRole("combobox"), "s");
    await within(palette).findByRole("option", { name: /eng-support:t-1001/ });

    const target = within(palette).getByRole("option", { name: /eng-support:t-1001/ });
    while (target.getAttribute("aria-selected") !== "true") await userEvent.keyboard("{ArrowDown}");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-1"));
  });

  // A session started by asking the team is found by its question.
  it("finds a direct session by what was first asked in it", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });

    await userEvent.keyboard("{Meta>}k{/Meta}");
    const palette = await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.type(within(palette).getByRole("combobox"), "nightly");
    const option = await within(palette).findByRole("option", {
      name: /Why did the nightly export fail\?/,
    });

    while (option.getAttribute("aria-selected") !== "true") await userEvent.keyboard("{ArrowDown}");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-2"));
  });

  it("closes on Escape without going anywhere", async () => {
    const router = open();
    await screen.findByRole("navigation", { name: "Main" });
    await userEvent.keyboard("{Control>}k{/Control}");
    await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Go to" })).toBeNull());
    expect(router.state.location.pathname).toBe("/ui");
  });
});

describe("asking the team from the palette", () => {
  const queued = {
    id: "r-9",
    session_id: "s-9",
    agent: "orchestrator",
    status: "queued",
    input: "why did the deploy fail",
    hub: true,
  };
  const posts = () => seen.filter((s) => s.method === "POST" && s.path === `/teams/${TEAM}/runs`);

  async function typed(text: string) {
    await screen.findByRole("navigation", { name: "Main" });
    await userEvent.keyboard("{Meta>}k{/Meta}");
    const palette = await screen.findByRole("dialog", { name: "Go to" });
    await userEvent.type(within(palette).getByRole("combobox"), text);
    return palette;
  }

  it("asks what was typed when nothing has that name, and opens the run's trace", async () => {
    const router = open(["run:write"], { status: 202, body: queued });
    const palette = await typed("why did the deploy fail");

    const option = await within(palette).findByRole("option", { name: /Ask the team/ });
    expect(option).toHaveTextContent("why did the deploy fail");
    expect(option).toHaveAttribute("aria-selected", "true");
    expect(within(palette).queryByText("Nothing by that name.")).toBeNull();

    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-9"));
    expect(router.state.location.search).toEqual({ tab: "runs", run: "r-9" });
    expect(JSON.parse(String(posts()[0]?.body))).toEqual({ input: "why did the deploy fail" });
    expect(screen.queryByRole("dialog", { name: "Go to" })).toBeNull();
  });

  // Asking comes last, so Enter on a name still goes where it always went.
  it("opens a name that matched on Enter, and asks only on Ctrl+Enter", async () => {
    const router = open(["run:write"], { status: 202, body: queued });
    const palette = await typed("log");
    const options = await within(palette).findAllByRole("option");
    expect(options.at(-1)).toHaveTextContent("Ask the team");

    await userEvent.keyboard("{Control>}{Enter}{/Control}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/s-9"));
    expect(JSON.parse(String(posts()[0]?.body))).toEqual({ input: "log" });
  });

  it("goes to the name on plain Enter, asking nothing", async () => {
    const router = open(["run:write"], { status: 202, body: queued });
    await typed("log");
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/agents/a-1"));
    expect(posts()).toHaveLength(0);
  });

  it("offers no asking to someone who may not start runs", async () => {
    open([]);
    const palette = await typed("why did the deploy fail");
    expect(await within(palette).findByText("Nothing by that name.")).toBeInTheDocument();
    expect(within(palette).queryByRole("option", { name: /Ask the team/ })).toBeNull();
  });

  it("stays open on a refusal and says why, with the question still typed", async () => {
    const router = open(["run:write"], { status: 409, text: "the team has no model set" });
    const palette = await typed("anyone there");
    await within(palette).findByRole("option", { name: /Ask the team/ });
    await userEvent.keyboard("{Enter}");

    expect(await within(palette).findByRole("alert")).toHaveTextContent(
      "The hub can't run until a model is set.",
    );
    expect(within(palette).getByRole("combobox")).toHaveValue("anyone there");
    expect(router.state.location.pathname).toBe("/ui");
  });
});
