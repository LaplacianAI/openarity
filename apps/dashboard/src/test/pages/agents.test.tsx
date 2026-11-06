import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { Agent, ListedAgent } from "@/api/types";
import {
  agentRequest,
  delegationWords,
  draftOf,
  grantWords,
  replaceRequest,
} from "@/lib/agent-draft";
import { brain, pageOf, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: ListRoute } = await import("@/routes/ui.agents.index");
const { Route: NewRoute } = await import("@/routes/ui.agents.new");
const { Route: AgentRoute } = await import("@/routes/ui.agents.$agentId");
await pageOf(ListRoute);
await pageOf(NewRoute);
await pageOf(AgentRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

function at(path: string) {
  return showAt(path, [
    ["/ui/agents/", ListRoute],
    ["/ui/agents/new", NewRoute],
    ["/ui/agents/$agentId", AgentRoute],
  ]);
}

const LIST = /^\/ui\/agents\/?$/;

const triage = {
  id: "a1",
  team_id: TEAM,
  name: "triage",
  description: "Sorts issues",
  kind: "custom",
  model: { name: "anthropic/claude-sonnet-5-5", max_tokens: 1024 },
  pattern: "react",
  created_at: STAMP,
  updated_at: STAMP,
};

/** triage as the list carries it. */
const listed: ListedAgent = {
  ...triage,
  delegation: "none",
  web: false,
  mcp_server_count: 1,
  skill_count: 1,
  delegate_count: 0,
};

const summary = {
  runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
  sessions: 0,
  waiting_senders: 0,
  servers_failing: 0,
  agents: 1,
};

const registry = {
  "GET /teams/{id}/agents": { body: { items: [listed] } },
  "GET /teams/{id}/summary": { body: summary },
  "GET /teams/{id}/mcp-servers": {
    body: {
      items: [
        {
          id: "m1",
          team_id: TEAM,
          name: "github",
          url: "https://a.b",
          env: {},
          bare: false,
          created_at: STAMP,
          updated_at: STAMP,
        },
      ],
    },
  },
  "GET /teams/{id}/skills": {
    body: {
      items: [
        {
          id: "s1",
          team_id: TEAM,
          name: "pdf",
          description: "d",
          source: "upload",
          created_at: STAMP,
          updated_at: STAMP,
        },
      ],
    },
  },
};

/** triage as the brain holds it whole. */
const stored = {
  ...triage,
  instructions: "",
  max_steps: 5,
  steer_continuations: 0,
  parser: false,
  mcp_servers: [],
  skills: [],
  delegation: "none",
  delegates: [],
  web: false,
};

/** Picks a name in the picker that adds a noun, then closes it. */
async function pick(noun: string, name: string) {
  await userEvent.click(screen.getByRole("button", { name: new RegExp(`add ${noun}`, "i") }));
  await userEvent.click(await screen.findByRole("option", { name }));
  await userEvent.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
}

/** The names on the chips of the picker labelled label, in order. */
function chips(label: string): string[] {
  const list = screen.queryByRole("list", { name: label });
  if (!list) return [];
  return within(list)
    .getAllByRole("listitem")
    .map((li) => li.querySelector("span")?.textContent ?? "");
}

function sent(seen: Seen[], method: string) {
  const s = seen.find((x) => x.method === method);
  return JSON.parse(String(s?.body));
}

/* --------------------------------------------------------------- the draft */

const draft = {
  name: "triage",
  description: " Sorts ",
  instructions: "Label it.",
  model: " m ",
  maxTokens: "1024",
  pattern: "react",
  maxSteps: "5",
  tokenBudget: "",
  servers: [{ id: "m1" }],
  skills: ["s1"],
  delegation: "none" as const,
  delegates: [] as string[],
  web: false,
};

describe("the draft as the API takes it", () => {
  it("sends what the API takes, with every chosen server granted whole", () => {
    expect(agentRequest(draft)).toEqual({
      name: "triage",
      description: "Sorts",
      instructions: "Label it.",
      model: { name: "m", max_tokens: 1024 },
      pattern: "react",
      max_steps: 5,
      mcp_servers: [{ id: "m1" }],
      skills: ["s1"],
      delegation: "none",
      web: false,
    });
  });

  it("sends a budget when one is set, and leaves the default to the brain when not", () => {
    expect(agentRequest(draft)).not.toHaveProperty("token_budget");
    expect(agentRequest({ ...draft, tokenBudget: " 250000 " })).toMatchObject({
      token_budget: 250000,
    });
    expect(agentRequest({ ...draft, tokenBudget: "100000000" })).toMatchObject({
      token_budget: 100_000_000,
    });
  });

  it.each([
    ["no name", { ...draft, name: " " }, /name/],
    ["no model", { ...draft, model: "" }, /model/],
    ["tokens that are not a number", { ...draft, maxTokens: "lots" }, /Max tokens/],
    ["zero tokens", { ...draft, maxTokens: "0" }, /Max tokens/],
    ["fractional steps", { ...draft, maxSteps: "2.5" }, /Max steps/],
    ["a budget that is not a number", { ...draft, tokenBudget: "lots" }, /Token budget/],
    ["a budget of zero", { ...draft, tokenBudget: "0" }, /Token budget/],
    ["a fractional budget", { ...draft, tokenBudget: "1.5" }, /Token budget/],
    ["a budget past the cap", { ...draft, tokenBudget: "100000001" }, /Token budget/],
  ])("refuses %s before anything is sent", (_, d, said) => {
    const got = agentRequest(d);
    expect(typeof got).toBe("string");
    expect(got).toMatch(said);
  });

  it("sends none and team with no list, whatever was chosen before", () => {
    for (const delegation of ["none", "team"] as const) {
      const got = agentRequest({ ...draft, delegation, delegates: ["a3"] });
      expect(got).toMatchObject({ delegation });
      expect(got).not.toHaveProperty("delegates");
    }
  });

  it("sends listed with its list", () => {
    expect(agentRequest({ ...draft, delegation: "listed", delegates: ["a3", "a4"] })).toMatchObject(
      { delegation: "listed", delegates: ["a3", "a4"] },
    );
  });

  it("sends the web as the draft holds it", () => {
    expect(agentRequest({ ...draft, web: true })).toMatchObject({ web: true });
    expect(agentRequest(draft)).toMatchObject({ web: false });
  });
});

// Saving replaces the agent whole: a grant the PUT does not name is revoked
// and a field it leaves out goes back to its default.
describe("the PUT for an edit", () => {
  const current = {
    ...stored,
    model: { name: "anthropic/claude-sonnet-5-5", max_tokens: 1024, temperature: 0.2 },
    instructions: "Label it.",
    max_steps: 7,
    steer_continuations: 2,
    mcp_servers: [{ id: "m1", allow: ["search"] }],
    skills: ["s1"],
    token_budget: 250000,
    output_schema: { name: "verdict", json: { type: "object" }, strict: true },
    parser: true,
    parser_model: { name: "anthropic/claude-haiku-5" },
  } as Agent;

  it("sends an untouched agent back as it is, limits and what the editor does not show", () => {
    expect(replaceRequest(current, draftOf(current))).toEqual({
      name: "triage",
      description: "Sorts issues",
      instructions: "Label it.",
      model: { name: "anthropic/claude-sonnet-5-5", max_tokens: 1024, temperature: 0.2 },
      pattern: "react",
      max_steps: 7,
      steer_continuations: 2,
      parser: true,
      output_schema: { name: "verdict", json: { type: "object" }, strict: true },
      parser_model: { name: "anthropic/claude-haiku-5" },
      mcp_servers: [{ id: "m1", allow: ["search"] }],
      skills: ["s1"],
      token_budget: 250000,
      delegation: "none",
      web: false,
    });
  });
});

describe("an agent in a row, in words", () => {
  it.each([
    [{ mcp_server_count: 2, skill_count: 3 }, "2 servers, 3 skills"],
    [{ mcp_server_count: 1, skill_count: 0 }, "1 server"],
    [{ mcp_server_count: 0, skill_count: 1 }, "1 skill"],
    [{ mcp_server_count: 0, skill_count: 0 }, "None"],
  ])("grants %o read %s", (counts, said) => {
    expect(grantWords({ ...listed, ...counts })).toBe(said);
  });

  it.each([
    [{ delegation: "none" }, "No one"],
    [{ delegation: "team" }, "Any agent"],
    [{ delegation: "listed", delegate_count: 2 }, "2 agents"],
    [{ delegation: "listed", delegate_count: 1 }, "1 agent"],
    [{ delegation: "listed", delegate_count: 0 }, "No one listed"],
  ] as const)("delegation %o reads %s", (setting, said) => {
    expect(delegationWords({ ...listed, ...setting })).toBe(said);
  });
});

/* ---------------------------------------------------------------- the list */

describe("the agents list", () => {
  const billing: ListedAgent = {
    ...listed,
    id: "a3",
    name: "billing",
    description: "Refunds",
    mcp_server_count: 2,
    skill_count: 3,
    web: true,
    delegation: "listed",
    delegate_count: 1,
  };

  it("says what each agent runs on, what it holds and whom it hands work to", async () => {
    brain({
      ...team([]),
      ...registry,
      "GET /teams/{id}/agents": { body: { items: [listed, billing] } },
    });
    at("/ui/agents");

    const row = (await screen.findByRole("link", { name: "billing" })).closest("tr") as HTMLElement;
    expect(within(row).getByText("Refunds")).toBeInTheDocument();
    expect(within(row).getByText("anthropic/claude-sonnet-5-5")).toBeInTheDocument();
    expect(within(row).getByText("2 servers, 3 skills")).toBeInTheDocument();
    expect(within(row).getByText("On")).toBeInTheDocument();
    expect(within(row).getByText("1 agent")).toBeInTheDocument();

    const alone = screen.getByRole("link", { name: "triage" }).closest("tr") as HTMLElement;
    expect(within(alone).getByText("Off")).toBeInTheDocument();
    expect(within(alone).getByText("No one")).toBeInTheDocument();
  });

  // The total is the brain's, not what loaded.
  it("says how many agents the team has, under its title", async () => {
    brain({
      ...team([]),
      ...registry,
      "GET /teams/{id}/summary": { body: { ...summary, agents: 12 } },
    });
    at("/ui/agents");
    expect(await screen.findByText("12 agents in the team.")).toBeInTheDocument();
  });

  it("opens an agent's page, by its link or from the keyboard", async () => {
    brain({ ...team([]), ...registry, "GET /teams/{id}/agents/{agentID}": { body: stored } });
    const router = at("/ui/agents");

    const link = await screen.findByRole("link", { name: "triage" });
    expect(link).toHaveAttribute("href", "/ui/agents/a1");
    (link.closest("tr") as HTMLElement).focus();
    await userEvent.keyboard("{Enter}");
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/agents/a1"));
  });

  it("offers defining an agent, on a page of its own, to someone with agent:write", async () => {
    brain({ ...team(["agent:write"]), ...registry });
    at("/ui/agents");
    expect(await screen.findByRole("link", { name: "Define agent" })).toHaveAttribute(
      "href",
      "/ui/agents/new",
    );
  });

  it("offers no write action without agent:write, and says what it takes", async () => {
    brain({ ...team(["skill:write"]), ...registry });
    at("/ui/agents");

    await screen.findByRole("link", { name: "triage" });
    expect(await screen.findByText("agent:write")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Define agent" })).toBeNull();
  });

  it("says when the team has no agents yet", async () => {
    brain({
      ...team(["agent:write"]),
      ...registry,
      "GET /teams/{id}/agents": { body: { items: [] } },
    });
    at("/ui/agents");
    expect(await screen.findByText("No agents yet")).toBeInTheDocument();
  });

  it("says why the agents could not be listed", async () => {
    brain({
      ...team([]),
      ...registry,
      "GET /teams/{id}/agents": { status: 500, text: "the store is down" },
    });
    at("/ui/agents");
    expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
  });
});

/* ------------------------------------------------------------- defining one */

describe("defining an agent", () => {
  const billing = { ...triage, id: "a3", name: "billing", description: "Refunds" };
  const clerk = { ...triage, id: "a4", name: "clerk", description: "Files" };

  function defining(actions = ["agent:write"], post: Record<string, unknown> = {}) {
    const seen = brain({
      ...team(actions),
      ...registry,
      "GET /teams/{id}/agents": { body: { items: [listed, billing, clerk] } },
      "POST /teams/{id}/agents": {
        status: 201,
        body: { ...stored, id: "a9", name: "reviewer" },
        ...post,
      },
      "GET /teams/{id}/agents/{agentID}": { body: { ...stored, id: "a9", name: "reviewer" } },
    });
    const router = at("/ui/agents/new");
    return { seen, router };
  }

  async function named() {
    await userEvent.type(await screen.findByLabelText(/^name$/i), "reviewer");
    await userEvent.type(screen.getByLabelText(/^model$/i), "m");
  }

  async function define(seen: Seen[]) {
    await userEvent.click(screen.getByRole("button", { name: "Define agent" }));
    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    return sent(seen, "POST");
  }

  it("suggests the team's model and the models its agents use, and lets another be typed", async () => {
    brain({
      ...team(["agent:write"]),
      ...registry,
      "GET /teams/{id}/agents": {
        body: {
          items: [listed, { ...listed, id: "a5", model: { name: "local/qwen", max_tokens: 1 } }],
        },
      },
      "GET /teams/{id}/settings": {
        body: {
          model: { name: "anthropic/claude-haiku-5", max_tokens: 1024 },
          search: { provider: "none", url: null, key_set: false },
          learning: false,
          compaction_messages: 40,
          hub: null,
        },
      },
    });
    at("/ui/agents/new");
    const model = await screen.findByLabelText(/^model$/i);
    await waitFor(() => expect(model).toHaveAttribute("placeholder", "anthropic/claude-haiku-5"));
    const list = document.getElementById(model.getAttribute("list") ?? "");
    await waitFor(() =>
      expect(Array.from(list?.querySelectorAll("option") ?? []).map((o) => o.value)).toEqual([
        "anthropic/claude-haiku-5",
        "anthropic/claude-sonnet-5-5",
        "local/qwen",
      ]),
    );
    expect(screen.getByText(/The team's model is anthropic\/claude-haiku-5\./)).toBeVisible();
  });

  it("names no model it does not know of when the team has none", async () => {
    defining();
    const model = await screen.findByLabelText(/^model$/i);
    expect(model).not.toHaveAttribute("placeholder");
  });

  it("lays the agent out in the parts a person decides", async () => {
    defining();
    await screen.findByLabelText(/^name$/i);
    const parts = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(parts).toEqual([
      "Identity",
      "Model and loop",
      "Tools and skills",
      "Delegation",
      "The web",
      "Limits",
    ]);
  });

  // The graph tool finds an agent by its description; the form says so where
  // the description is written.
  it("says an agent is found by its description", async () => {
    defining();
    expect(
      await screen.findByText(/other agents find this one by its description/i),
    ).toBeInTheDocument();
  });

  it("defines an agent with the budget typed, then opens its page", async () => {
    const { seen, router } = defining();
    await named();
    await userEvent.type(screen.getByLabelText(/^token budget$/i), "250000");

    expect((await define(seen)).token_budget).toBe(250000);
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/agents/a9"));
    // Nothing is unsaved once it is defined, so leaving asks nothing.
    expect(screen.queryByRole("heading", { name: /leave without saving/i })).toBeNull();
  });

  it("defines an agent with the servers and skills chosen", async () => {
    const { seen } = defining();
    await named();
    await pick("server", "github");
    await pick("skill", "pdf");

    expect(await define(seen)).toMatchObject({
      name: "reviewer",
      pattern: "react",
      max_steps: 5,
      mcp_servers: [{ id: "m1" }],
      skills: ["s1"],
    });
  });

  it("refuses a draft it cannot send, and sends nothing", async () => {
    const { seen } = defining();
    await userEvent.type(await screen.findByLabelText(/^model$/i), "m");
    await userEvent.click(screen.getByRole("button", { name: "Define agent" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("An agent needs a name.");
    expect(seen.some((s) => s.method === "POST")).toBe(false);
  });

  it("shows the brain's refusal beside the action, and stays", async () => {
    const { router } = defining(["agent:write"], {
      status: 400,
      text: 'pattern "wander" is not one the brain runs',
    });
    await named();
    await userEvent.click(screen.getByRole("button", { name: "Define agent" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("is not one the brain runs");
    expect(router.state.location.pathname).toBe("/ui/agents/new");
  });

  it("works alone and off the web unless told otherwise", async () => {
    const { seen } = defining();
    await named();

    expect(screen.getByRole("radio", { name: /works alone/i })).toBeChecked();
    expect(
      screen.getByRole("switch", { name: /search the web and read pages/i }),
    ).not.toBeChecked();
    const body = await define(seen);
    expect(body).toMatchObject({ delegation: "none", web: false });
    expect(body).not.toHaveProperty("delegates");
  });

  it("goes on the web once switched", async () => {
    const { seen } = defining();
    await named();
    await userEvent.click(screen.getByRole("switch", { name: /search the web and read pages/i }));
    expect((await define(seen)).web).toBe(true);
  });

  it("hands work to any agent in the team, with no list", async () => {
    const { seen } = defining();
    await named();
    await userEvent.click(screen.getByRole("radio", { name: /any agent in the team/i }));

    expect(screen.queryByRole("button", { name: /add agent/i })).toBeNull();
    const body = await define(seen);
    expect(body.delegation).toBe("team");
    expect(body).not.toHaveProperty("delegates");
  });

  it("lists the team's agents only when only some are chosen", async () => {
    const { seen } = defining();
    await named();
    await userEvent.click(screen.getByRole("radio", { name: /only these agents/i }));
    await pick("agent", "billing");

    expect(await define(seen)).toMatchObject({ delegation: "listed", delegates: ["a3"] });
  });

  it("forgets a list when it is set to work alone again", async () => {
    const { seen } = defining();
    await named();
    await userEvent.click(screen.getByRole("radio", { name: /only these agents/i }));
    await pick("agent", "billing");
    await userEvent.click(screen.getByRole("radio", { name: /works alone/i }));

    const body = await define(seen);
    expect(body.delegation).toBe("none");
    expect(body).not.toHaveProperty("delegates");
  });

  it("asks before leaving with something typed, and stays when told to", async () => {
    const { router } = defining();
    await userEvent.type(await screen.findByLabelText(/^name$/i), "rev");
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(
      await screen.findByRole("heading", { name: "Leave without saving?" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: /leave/i })).toBeNull());
    expect(router.state.location.pathname).toBe("/ui/agents/new");
    expect(screen.getByLabelText(/^name$/i)).toHaveValue("rev");

    await userEvent.click(screen.getByRole("link", { name: "Agents" }));
    await userEvent.click(await screen.findByRole("button", { name: "Leave" }));
    await waitFor(() => expect(router.state.location.pathname).toMatch(LIST));
  });

  it("leaves without asking when nothing was typed", async () => {
    const { router } = defining();
    await screen.findByLabelText(/^name$/i);
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(router.state.location.pathname).toMatch(LIST));
  });

  it("offers no form without agent:write, and says what it takes", async () => {
    defining(["skill:write"]);
    expect(await screen.findByText("agent:write")).toBeInTheDocument();
    expect(screen.queryByLabelText(/^name$/i)).toBeNull();
  });
});

/* -------------------------------------------------------------- editing one */

describe("editing an agent", () => {
  const current = {
    ...stored,
    model: { name: "anthropic/claude-sonnet-5-5", max_tokens: 1024, temperature: 0.2 },
    instructions: "Label it.",
    max_steps: 7,
    steer_continuations: 2,
    mcp_servers: [{ id: "m1", allow: ["search"] }],
    skills: ["s1"],
    token_budget: 250000,
  };

  // What the brain holds, as the PUT takes it: no id, kind or stamps.
  const definition = {
    name: "triage",
    description: "Sorts issues",
    instructions: "Label it.",
    model: { name: "anthropic/claude-sonnet-5-5", max_tokens: 1024, temperature: 0.2 },
    pattern: "react",
    max_steps: 7,
    steer_continuations: 2,
    parser: false,
    mcp_servers: [{ id: "m1", allow: ["search"] }],
    skills: ["s1"],
    token_budget: 250000,
    delegation: "none",
    web: false,
  };

  function editing(
    agent: Record<string, unknown>,
    extra: Parameters<typeof brain>[0] = {},
    actions = ["agent:write"],
  ) {
    const seen = brain({
      ...team(actions),
      ...registry,
      "GET /teams/{id}/agents/{agentID}": { body: agent },
      "PUT /teams/{id}/agents/{agentID}": { body: agent },
      "DELETE /teams/{id}/agents/{agentID}": { status: 204 },
      ...extra,
    });
    const router = at("/ui/agents/a1");
    return { seen, router };
  }

  async function opened() {
    await waitFor(() => expect(screen.getByLabelText(/^name$/i)).toHaveValue("triage"));
  }

  async function save(seen: Seen[]) {
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    return sent(seen, "PUT");
  }

  /** A change of description, so there is something to save. */
  async function touch() {
    await userEvent.type(screen.getByLabelText(/^description$/i), " and more");
  }

  it("opens with the agent's current values, its grants by name", async () => {
    editing(current);
    await opened();

    expect(screen.getByRole("heading", { level: 1, name: "triage" })).toBeInTheDocument();
    expect(screen.getByLabelText(/^model$/i)).toHaveValue("anthropic/claude-sonnet-5-5");
    expect(screen.getByLabelText(/^max steps$/i)).toHaveValue("7");
    expect(screen.getByLabelText(/^token budget$/i)).toHaveValue("250000");
    expect(screen.getByLabelText(/^instructions$/i)).toHaveValue("Label it.");
    await waitFor(() => expect(chips("MCP servers")).toEqual(["github"]));
    await waitFor(() => expect(chips("Skills")).toEqual(["pdf"]));
    // A grant limited to some tools says which, so it is not taken for whole.
    expect(screen.getByRole("button", { name: "Tools of github" })).toHaveTextContent(
      "only search",
    );
  });

  it("offers saving only once something has changed, and discarding puts it back", async () => {
    editing(current);
    await opened();
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull();

    await touch();
    expect(screen.getByText(/unsaved changes/i)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Discard" }));

    expect(screen.getByLabelText(/^description$/i)).toHaveValue("Sorts issues");
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull();
  });

  it("goes back to no unsaved changes when a change is undone by hand", async () => {
    editing(current);
    await opened();
    await userEvent.type(screen.getByLabelText(/^description$/i), "!");
    await userEvent.type(screen.getByLabelText(/^description$/i), "{Backspace}");
    expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull();
  });

  it("sends a new description with everything else unchanged, then has nothing unsaved", async () => {
    const { seen } = editing(current);
    await opened();
    const description = screen.getByLabelText(/^description$/i);
    await userEvent.clear(description);
    await userEvent.type(description, "Sorts and labels issues");

    expect(await save(seen)).toEqual({ ...definition, description: "Sorts and labels issues" });
    expect(seen.find((s) => s.method === "PUT")?.path).toBe(`/teams/${TEAM}/agents/a1`);
    await waitFor(() => expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull());
  });

  it("revokes a skill that is removed", async () => {
    const { seen } = editing(current);
    await opened();
    await userEvent.click(await screen.findByRole("button", { name: "Remove pdf" }));
    expect(await save(seen)).toEqual({ ...definition, skills: [] });
  });

  it("keeps what the editor does not show", async () => {
    const { seen } = editing({
      ...current,
      output_schema: { name: "verdict", json: { type: "object" }, strict: true },
      parser: true,
      parser_model: { name: "anthropic/claude-haiku-5" },
    });
    await opened();
    await touch();

    expect(await save(seen)).toEqual({
      ...definition,
      description: "Sorts issues and more",
      output_schema: { name: "verdict", json: { type: "object" }, strict: true },
      parser: true,
      parser_model: { name: "anthropic/claude-haiku-5" },
    });
  });

  it("shows the brain's refusal beside the save", async () => {
    editing(current, {
      "PUT /teams/{id}/agents/{agentID}": {
        status: 400,
        text: 'pattern "wander" is not one the brain runs',
      },
    });
    await opened();
    await touch();
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("is not one the brain runs");
    expect(screen.getByRole("button", { name: /^save$/i })).toBeInTheDocument();
  });

  it("asks before leaving with unsaved changes, and not once they are saved", async () => {
    const { seen, router } = editing(current);
    await opened();
    await touch();
    await userEvent.click(screen.getByRole("link", { name: "Agents" }));
    expect(
      await screen.findByRole("heading", { name: "Leave without saving?" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
    expect(router.state.location.pathname).toBe("/ui/agents/a1");

    await save(seen);
    await waitFor(() => expect(screen.queryByRole("button", { name: /^save$/i })).toBeNull());
    await userEvent.click(screen.getByRole("link", { name: "Agents" }));
    await waitFor(() => expect(router.state.location.pathname).toMatch(LIST));
  });

  it("deletes the agent once it is confirmed, then lists the rest", async () => {
    const { seen, router } = editing(current);
    await opened();
    await userEvent.click(screen.getByRole("button", { name: "Delete agent" }));
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);

    const dialog = await screen.findByRole("dialog", { name: "Delete triage?" });
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(router.state.location.pathname).toMatch(LIST));
    expect(seen.find((s) => s.method === "DELETE")?.path).toBe(`/teams/${TEAM}/agents/a1`);
  });

  // Without agent:write the agent is read in the same layout, and nothing moves.
  it("shows the agent to a reader without agent:write, with no control that changes it", async () => {
    const { seen } = editing(current, {}, ["skill:write"]);
    await opened();

    expect(await screen.findByText("agent:write")).toBeInTheDocument();
    expect(screen.getByLabelText(/^name$/i)).toBeDisabled();
    expect(screen.getByRole("switch", { name: /search the web/i })).toBeDisabled();
    expect(await screen.findByRole("button", { name: "Remove pdf" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Delete agent" })).toBeNull();
    expect(seen.some((s) => s.method !== "GET")).toBe(false);
  });

  it("says why an agent could not be read", async () => {
    editing(current, { "GET /teams/{id}/agents/{agentID}": { status: 404, text: "not found" } });
    // A link to an agent of another team, or one since deleted.
    expect(await screen.findByText("Couldn't find this agent in this team.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /try again/i })).toBeNull();
    expect(screen.queryByLabelText(/^name$/i)).toBeNull();
  });

  /* ------------------------------------------------------------ delegation */

  describe("whom it hands work to", () => {
    const billing = { ...listed, id: "a3", name: "billing", description: "Refunds" };
    const clerk = { ...listed, id: "a4", name: "clerk", description: "Files" };
    const withTeam = { "GET /teams/{id}/agents": { body: { items: [listed, billing, clerk] } } };

    it("shows team as stored, not a new agent's default, and sends it back", async () => {
      const { seen } = editing({ ...stored, delegation: "team" }, withTeam);
      await opened();

      expect(screen.getByRole("radio", { name: /any agent in the team/i })).toBeChecked();
      await touch();
      const body = await save(seen);
      expect(body.delegation).toBe("team");
      expect(body).not.toHaveProperty("delegates");
    });

    it("shows a list as stored, never offering the agent itself, and sends it back", async () => {
      const { seen } = editing({ ...stored, delegation: "listed", delegates: ["a3"] }, withTeam);
      await opened();

      expect(screen.getByRole("radio", { name: /only these agents/i })).toBeChecked();
      await waitFor(() => expect(chips("Agents it may hand work to")).toEqual(["billing"]));
      await userEvent.click(screen.getByRole("button", { name: /add agent/i }));
      await screen.findByRole("option", { name: "clerk" });
      expect(screen.queryByRole("option", { name: "triage" })).toBeNull();
      await userEvent.keyboard("{Escape}");
      await touch();
      expect(await save(seen)).toMatchObject({ delegation: "listed", delegates: ["a3"] });
    });

    it("sends a list changed in the editor", async () => {
      const { seen } = editing({ ...stored, delegation: "listed", delegates: ["a3"] }, withTeam);
      await opened();
      await userEvent.click(await screen.findByRole("button", { name: "Remove billing" }));
      await pick("agent", "clerk");
      expect(await save(seen)).toMatchObject({ delegation: "listed", delegates: ["a4"] });
    });

    it("sets an agent to work alone, dropping its list", async () => {
      const { seen } = editing({ ...stored, delegation: "listed", delegates: ["a3"] }, withTeam);
      await opened();
      await userEvent.click(screen.getByRole("radio", { name: /works alone/i }));

      const body = await save(seen);
      expect(body.delegation).toBe("none");
      expect(body).not.toHaveProperty("delegates");
    });
  });

  /* -------------------------------------------------------------- pickers */

  describe("the pickers", () => {
    const loops = {
      "GET /teams/{id}/loops": { body: { items: [{ name: "plan" }, { name: "react" }] } },
    };

    it("offers the patterns the brain runs", async () => {
      const { seen } = editing(stored, loops);
      await opened();

      const pattern = screen.getByLabelText(/^pattern$/i);
      await waitFor(() =>
        expect(
          within(pattern)
            .getAllByRole("option")
            .map((o) => o.textContent),
        ).toEqual(["plan", "react"]),
      );
      expect(pattern).toHaveValue("react");
      await userEvent.selectOptions(pattern, "plan");
      expect((await save(seen)).pattern).toBe("plan");
    });

    // A pattern the brain no longer lists is still the agent's; the editor
    // shows it rather than quietly changing it on save.
    it("keeps a pattern the brain does not list", async () => {
      const { seen } = editing({ ...stored, pattern: "custom-triage" }, loops);
      await opened();

      expect(screen.getByLabelText(/^pattern$/i)).toHaveValue("custom-triage");
      await touch();
      expect((await save(seen)).pattern).toBe("custom-triage");
    });

    it("asks the brain for what was typed, a short page at a time", async () => {
      const { seen } = editing(stored);
      await opened();

      await userEvent.click(screen.getByRole("button", { name: /add skill/i }));
      await userEvent.type(await screen.findByRole("combobox", { name: /search skills/i }), "pd");
      await waitFor(() =>
        expect(
          seen.some((s) => s.path.startsWith(`/teams/${TEAM}/skills?`) && s.path.includes("q=pd")),
        ).toBe(true),
      );
      expect(seen.find((s) => s.path.includes("q=pd"))?.path).toContain("limit=20");
    });

    // A grant past the team's first page is not among the names already read,
    // so the chip reads it by id rather than showing the id.
    it("names a grant the first page did not hold", async () => {
      editing(
        { ...stored, skills: ["s9"] },
        {
          "GET /teams/{id}/skills/{skillID}": {
            body: { id: "s9", team_id: TEAM, name: "far-away", description: "", source: "upload" },
          },
        },
      );
      await opened();
      await waitFor(() => expect(chips("Skills")).toEqual(["far-away"]));
    });

    // The editor opens before the team's lists arrive; an id not in them yet
    // is not read by itself unless the list comes back without it.
    it("reads no grant by itself that the team's first page names", async () => {
      const { seen } = editing(current);
      await opened();
      await waitFor(() => expect(chips("Skills")).toEqual(["pdf"]));
      await waitFor(() => expect(chips("MCP servers")).toEqual(["github"]));

      const one = seen.filter((s) => /\/(mcp-servers|skills|agents)\/[^/?]+$/.test(s.path));
      expect(one.map((s) => s.path)).toEqual([`/teams/${TEAM}/agents/a1`]);
    });

    describe("a server's tools", () => {
      const tools = {
        "GET /teams/{id}/mcp-servers/{sid}/tools": {
          body: {
            items: [
              { name: "search_code", description: "", input_schema: {} },
              { name: "create_issue", description: "", input_schema: {} },
            ],
          },
        },
      };

      it("limits a server granted whole to the tools ticked", async () => {
        const { seen } = editing({ ...stored, mcp_servers: [{ id: "m1" }] }, tools);
        await opened();

        const limit = await screen.findByRole("button", { name: "Tools of github" });
        expect(limit).toHaveTextContent("every tool");
        await userEvent.click(limit);
        await userEvent.click(await screen.findByRole("checkbox", { name: "search_code" }));
        expect(screen.getByRole("checkbox", { name: "Every tool" })).not.toBeChecked();
        await userEvent.keyboard("{Escape}");

        expect((await save(seen)).mcp_servers).toEqual([{ id: "m1", allow: ["search_code"] }]);
      });

      // A grant of no tools grants nothing, so unticking the last is every tool.
      it.each(["search_code", "Every tool"])(
        "goes back to every tool when %s is unticked",
        async (untick) => {
          const { seen } = editing(
            { ...stored, mcp_servers: [{ id: "m1", allow: ["search_code"] }] },
            tools,
          );
          await opened();
          await userEvent.click(await screen.findByRole("button", { name: "Tools of github" }));
          await userEvent.click(await screen.findByRole("checkbox", { name: untick }));
          await userEvent.keyboard("{Escape}");

          expect((await save(seen)).mcp_servers).toEqual([{ id: "m1" }]);
        },
      );

      it("keeps a server's limit when another server is added", async () => {
        const linear = {
          id: "m2",
          team_id: TEAM,
          name: "linear",
          url: "https://c.d",
          env: {},
          bare: false,
          created_at: STAMP,
          updated_at: STAMP,
        };
        const servers = registry["GET /teams/{id}/mcp-servers"].body.items;
        const { seen } = editing(
          { ...stored, mcp_servers: [{ id: "m1", allow: ["search_code"] }] },
          { ...tools, "GET /teams/{id}/mcp-servers": { body: { items: [...servers, linear] } } },
        );
        await opened();
        await waitFor(() => expect(chips("MCP servers")).toEqual(["github"]));

        await pick("server", "linear");
        expect((await save(seen)).mcp_servers).toEqual([
          { id: "m1", allow: ["search_code"] },
          { id: "m2" },
        ]);
      });
    });
  });

  /* -------------------------------------------------------------- the web */

  describe("on an agent that is on the web", () => {
    const WEB = /search the web and read pages/i;

    // A PUT is whole: a save about something else must not take the web away.
    it("keeps the grant through a save that changes something else", async () => {
      const { seen } = editing({ ...stored, web: true });
      await opened();

      expect(screen.getByRole("switch", { name: WEB })).toBeChecked();
      await touch();
      expect(await save(seen)).toMatchObject({ web: true, description: "Sorts issues and more" });
    });

    it("takes the grant away when switched off", async () => {
      const { seen } = editing({ ...stored, web: true });
      await opened();
      await userEvent.click(screen.getByRole("switch", { name: WEB }));
      expect((await save(seen)).web).toBe(false);
    });
  });
});
