import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, show, TEAM, team } from "@/test/brain";

const { Route, agentRequest } = await import("@/routes/ui.agents.index");
const Agents = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

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

const registry = {
  "GET /teams/{id}/agents": { body: { items: [triage] } },
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

describe("the define form", () => {
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
});

it("offers no write action without agent:write", async () => {
  brain({ ...team(["skill:write", "tool:write"]), ...registry });
  show(Agents);

  await screen.findByText("triage");
  expect(await screen.findByText("agent:write")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /define agent/i })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /^delete$/i })).not.toBeInTheDocument();
});

// An agent's grants arrive as ids. They are shown by the names the team
// gave them, from the team's own lists.
it("shows an agent's grants by name", async () => {
  brain({
    ...team([]),
    ...registry,
    "GET /teams/{id}/agents/{agentID}": {
      body: {
        ...triage,
        instructions: "Label it.",
        max_steps: 5,
        steer_continuations: 0,
        parser: false,
        mcp_servers: [{ id: "m1", allow: ["search_code"] }],
        skills: ["s1"],
      },
    },
  });
  show(Agents);

  await userEvent.click(await screen.findByRole("button", { name: /triage/ }));
  expect(await screen.findByText("github")).toBeInTheDocument();
  expect(screen.getByText("search_code")).toBeInTheDocument();
  expect(await screen.findByText("pdf")).toBeInTheDocument();
  expect(screen.getByText("Label it.")).toBeInTheDocument();
});

it.each([
  [250000, "250,000 tokens a run"],
  [undefined, "the default budget"],
])("shows a budget of %s as %s", async (budget, said) => {
  brain({
    ...team([]),
    ...registry,
    "GET /teams/{id}/agents/{agentID}": {
      body: {
        ...triage,
        instructions: "Label it.",
        max_steps: 5,
        steer_continuations: 0,
        parser: false,
        mcp_servers: [],
        skills: [],
        ...(budget === undefined ? {} : { token_budget: budget }),
      },
    },
  });
  show(Agents);

  await userEvent.click(await screen.findByRole("button", { name: /triage/ }));
  expect(await screen.findByText(new RegExp(said))).toBeInTheDocument();
});

// The graph tool finds an agent by its description; the form says so where
// the description is written.
it("says an agent is found by its description", async () => {
  brain({ ...team(["agent:write"]), ...registry });
  show(Agents);

  await userEvent.click(await screen.findByRole("button", { name: /define agent/i }));

  expect(screen.getByText(/other agents find this one by its description/i)).toBeInTheDocument();
});

it("defines an agent with the budget typed", async () => {
  const seen = brain({
    ...team(["agent:write"]),
    ...registry,
    "POST /teams/{id}/agents": { status: 201, body: { ...triage, id: "a2", name: "reviewer" } },
  });
  show(Agents);

  await userEvent.click(await screen.findByRole("button", { name: /define agent/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "reviewer");
  await userEvent.type(screen.getByLabelText(/^model$/i), "m");
  await userEvent.type(screen.getByLabelText(/^token budget$/i), "250000");
  await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  const body = JSON.parse(String(seen.find((s) => s.method === "POST")?.body));
  expect(body.token_budget).toBe(250000);
});

it("defines an agent with the servers and skills ticked", async () => {
  const seen = brain({
    ...team(["agent:write"]),
    ...registry,
    "POST /teams/{id}/agents": { status: 201, body: { ...triage, id: "a2", name: "reviewer" } },
  });
  show(Agents);

  await userEvent.click(await screen.findByRole("button", { name: /define agent/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "reviewer");
  await userEvent.type(screen.getByLabelText(/^model$/i), "anthropic/claude-sonnet-5-5");
  await pick("server", "github");
  await pick("skill", "pdf");
  await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  const body = JSON.parse(String(seen.find((s) => s.method === "POST")?.body));
  expect(body).toMatchObject({
    name: "reviewer",
    pattern: "react",
    max_steps: 5,
    mcp_servers: [{ id: "m1" }],
    skills: ["s1"],
  });
});

it("shows the brain's refusal in the form", async () => {
  brain({
    ...team(["agent:write"]),
    ...registry,
    "POST /teams/{id}/agents": { status: 400, text: 'pattern "wander" is not one the brain runs' },
  });
  show(Agents);

  await userEvent.click(await screen.findByRole("button", { name: /define agent/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "x");
  await userEvent.type(screen.getByLabelText(/^model$/i), "m");
  await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

  expect(await screen.findByRole("alert")).toHaveTextContent("is not one the brain runs");
});

// Editing replaces the agent whole: a grant the PUT does not name is revoked
// and a field it leaves out goes back to its default. So the form starts from
// the agent as the brain has it and sends all of it back.
describe("editing an agent", () => {
  const current = {
    ...triage,
    model: { name: "anthropic/claude-sonnet-5-5", max_tokens: 1024, temperature: 0.2 },
    instructions: "Label it.",
    max_steps: 7,
    steer_continuations: 2,
    parser: false,
    mcp_servers: [{ id: "m1", allow: ["search"] }],
    skills: ["s1"],
    token_budget: 250000,
    delegation: "none",
    delegates: [],
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

  function editing(agent: Record<string, unknown>, put: Record<string, unknown> = {}) {
    return brain({
      ...team(["agent:write"]),
      ...registry,
      "GET /teams/{id}/agents/{agentID}": { body: agent },
      "PUT /teams/{id}/agents/{agentID}": { body: agent, ...put },
    });
  }

  async function openEdit() {
    show(Agents);
    await userEvent.click(await screen.findByRole("button", { name: /^edit$/i }));
    await screen.findByRole("heading", { name: /edit agent/i });
    // The grants show by name once the team's servers and skills have loaded.
    await waitFor(() => expect(chips("MCP servers")).toEqual(["github"]));
    await waitFor(() => expect(chips("Skills")).toEqual(["pdf"]));
  }

  function putBody(seen: { method: string; body: unknown }[]) {
    return JSON.parse(String(seen.find((s) => s.method === "PUT")?.body));
  }

  it("offers no edit without agent:write", async () => {
    brain({ ...team(["skill:write"]), ...registry });
    show(Agents);

    await screen.findByText("triage");
    expect(await screen.findByText("agent:write")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^edit$/i })).not.toBeInTheDocument();
  });

  it("opens with the agent's current values", async () => {
    editing(current);
    await openEdit();

    expect(screen.getByLabelText(/^name$/i)).toHaveValue("triage");
    expect(screen.getByLabelText(/^model$/i)).toHaveValue("anthropic/claude-sonnet-5-5");
    expect(screen.getByLabelText(/^max steps$/i)).toHaveValue("7");
    expect(screen.getByLabelText(/^token budget$/i)).toHaveValue("250000");
    expect(screen.getByLabelText(/^instructions$/i)).toHaveValue("Label it.");
    expect(chips("MCP servers")).toEqual(["github"]);
    expect(chips("Skills")).toEqual(["pdf"]);
    // A grant limited to some tools says which, so it is not taken for whole.
    expect(screen.getByRole("button", { name: "Tools of github" })).toHaveTextContent(
      "only search",
    );
    expect(screen.getByRole("button", { name: /^save$/i })).toBeInTheDocument();
  });

  it("saved unchanged, sends the agent back as it is, limits and all", async () => {
    const seen = editing(current);
    await openEdit();
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    expect(seen.find((s) => s.method === "PUT")?.path).toBe(`/teams/${TEAM}/agents/a1`);
    expect(putBody(seen)).toEqual(definition);
  });

  it("sends a new description with everything else unchanged", async () => {
    const seen = editing(current);
    await openEdit();
    const description = screen.getByLabelText(/^description$/i);
    await userEvent.clear(description);
    await userEvent.type(description, "Sorts and labels issues");
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    expect(putBody(seen)).toEqual({ ...definition, description: "Sorts and labels issues" });
  });

  it("revokes a skill that is unticked", async () => {
    const seen = editing(current);
    await openEdit();
    await userEvent.click(screen.getByRole("button", { name: "Remove pdf" }));
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    expect(putBody(seen)).toEqual({ ...definition, skills: [] });
  });

  it("keeps what the form does not show", async () => {
    const shaped = {
      ...current,
      output_schema: { name: "verdict", json: { type: "object" }, strict: true },
      parser: true,
      parser_model: { name: "anthropic/claude-haiku-5" },
    };
    const seen = editing(shaped);
    await openEdit();
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    expect(putBody(seen)).toEqual({
      ...definition,
      output_schema: { name: "verdict", json: { type: "object" }, strict: true },
      parser: true,
      parser_model: { name: "anthropic/claude-haiku-5" },
    });
  });

  it("shows the brain's refusal in the form", async () => {
    editing(current, { status: 400, text: 'pattern "wander" is not one the brain runs' });
    await openEdit();
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("is not one the brain runs");
  });
});

/* ------------------------------------------------------------- delegation */

// Whom an agent may hand work to with the delegate tool. A new agent works
// alone: the box is ticked, and the brain's own default is the same.
describe("whom an agent may hand work to", () => {
  const billing = { ...triage, id: "a3", name: "billing", description: "Refunds" };
  const clerk = { ...triage, id: "a4", name: "clerk", description: "Files" };
  const withTeam = {
    ...registry,
    "GET /teams/{id}/agents": { body: { items: [triage, billing, clerk] } },
  };
  const ALONE = /works alone/i;

  describe("as the form sends it", () => {
    it("sends none with no list", () => {
      const got = agentRequest({ ...draft, delegation: "none", delegates: ["a3"] });
      expect(got).toMatchObject({ delegation: "none" });
      expect(got).not.toHaveProperty("delegates");
    });

    it("sends team with no list, whatever was ticked before", () => {
      const got = agentRequest({ ...draft, delegation: "team", delegates: ["a3"] });
      expect(got).toMatchObject({ delegation: "team" });
      expect(got).not.toHaveProperty("delegates");
    });

    it("sends listed with its list", () => {
      expect(
        agentRequest({ ...draft, delegation: "listed", delegates: ["a3", "a4"] }),
      ).toMatchObject({ delegation: "listed", delegates: ["a3", "a4"] });
    });
  });

  function created(seen: { method: string; body: unknown }[]) {
    return JSON.parse(String(seen.find((s) => s.method === "POST")?.body));
  }

  async function defining() {
    const seen = brain({
      ...team(["agent:write"]),
      ...withTeam,
      "POST /teams/{id}/agents": { status: 201, body: { ...triage, id: "a9", name: "reviewer" } },
    });
    show(Agents);
    await userEvent.click(await screen.findByRole("button", { name: /define agent/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "reviewer");
    await userEvent.type(screen.getByLabelText(/^model$/i), "m");
    return seen;
  }

  it("defines a new agent working alone unless told otherwise", async () => {
    const seen = await defining();

    expect(screen.getByRole("checkbox", { name: ALONE })).toBeChecked();
    expect(screen.queryByLabelText(/^hands work to$/i)).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(created(seen).delegation).toBe("none");
    expect(created(seen)).not.toHaveProperty("delegates");
  });

  it("offers any agent in the team once the box is unticked, and lists none", async () => {
    const seen = await defining();
    await userEvent.click(screen.getByRole("checkbox", { name: ALONE }));

    expect(screen.getByLabelText(/^hands work to$/i)).toHaveValue("team");
    expect(screen.queryByRole("button", { name: /add agent/i })).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(created(seen).delegation).toBe("team");
    expect(created(seen)).not.toHaveProperty("delegates");
  });

  it("lists the team's agents only when only some are chosen", async () => {
    const seen = await defining();
    await userEvent.click(screen.getByRole("checkbox", { name: ALONE }));
    await userEvent.selectOptions(screen.getByLabelText(/^hands work to$/i), "listed");
    await pick("agent", "billing");
    await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(created(seen)).toMatchObject({ delegation: "listed", delegates: ["a3"] });
  });

  it("forgets a list when the box is ticked again", async () => {
    const seen = await defining();
    await userEvent.click(screen.getByRole("checkbox", { name: ALONE }));
    await userEvent.selectOptions(screen.getByLabelText(/^hands work to$/i), "listed");
    await pick("agent", "billing");
    await userEvent.click(screen.getByRole("checkbox", { name: ALONE }));
    await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(created(seen).delegation).toBe("none");
    expect(created(seen)).not.toHaveProperty("delegates");
  });

  describe("on an agent that has a setting", () => {
    const stored = {
      ...triage,
      instructions: "",
      max_steps: 5,
      steer_continuations: 0,
      parser: false,
      mcp_servers: [],
      skills: [],
    };

    function editing(agent: Record<string, unknown>) {
      return brain({
        ...team(["agent:write"]),
        ...withTeam,
        "GET /teams/{id}/agents/{agentID}": { body: agent },
        "PUT /teams/{id}/agents/{agentID}": { body: agent },
      });
    }

    async function openEdit() {
      show(Agents);
      const [first] = await screen.findAllByRole("button", { name: /^edit$/i });
      await userEvent.click(first as HTMLElement);
      await screen.findByRole("heading", { name: /edit agent/i });
    }

    async function save(seen: { method: string; body: unknown }[]) {
      await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
      await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
      return JSON.parse(String(seen.find((s) => s.method === "PUT")?.body));
    }

    it("shows team as stored, not the new agent's default, and sends it back", async () => {
      const seen = editing({ ...stored, delegation: "team", delegates: [] });
      await openEdit();

      expect(screen.getByRole("checkbox", { name: ALONE })).not.toBeChecked();
      expect(screen.getByLabelText(/^hands work to$/i)).toHaveValue("team");
      const body = await save(seen);
      expect(body.delegation).toBe("team");
      expect(body).not.toHaveProperty("delegates");
    });

    it("shows a list as stored, without the agent itself, and sends it back", async () => {
      const seen = editing({ ...stored, delegation: "listed", delegates: ["a3"] });
      await openEdit();

      expect(screen.getByLabelText(/^hands work to$/i)).toHaveValue("listed");
      await waitFor(() => expect(chips("Agents it may hand work to")).toEqual(["billing"]));
      await userEvent.click(screen.getByRole("button", { name: /add agent/i }));
      await screen.findByRole("option", { name: "clerk" });
      expect(screen.queryByRole("option", { name: "triage" })).toBeNull();
      await userEvent.keyboard("{Escape}");
      expect(await save(seen)).toMatchObject({ delegation: "listed", delegates: ["a3"] });
    });

    it("sends a list changed in the form", async () => {
      const seen = editing({ ...stored, delegation: "listed", delegates: ["a3"] });
      await openEdit();
      await userEvent.click(await screen.findByRole("button", { name: "Remove billing" }));
      await pick("agent", "clerk");

      expect(await save(seen)).toMatchObject({ delegation: "listed", delegates: ["a4"] });
    });

    it("sets an agent to work alone, dropping its list", async () => {
      const seen = editing({ ...stored, delegation: "listed", delegates: ["a3"] });
      await openEdit();
      await userEvent.click(screen.getByRole("checkbox", { name: ALONE }));

      const body = await save(seen);
      expect(body.delegation).toBe("none");
      expect(body).not.toHaveProperty("delegates");
    });
  });
});

/* ----------------------------------------------------------- the pickers */

describe("the form's pickers", () => {
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
  };
  const loops = {
    "GET /teams/{id}/loops": { body: { items: [{ name: "plan" }, { name: "react" }] } },
  };

  function editing(agent: Record<string, unknown>, extra: Parameters<typeof brain>[0] = {}) {
    return brain({
      ...team(["agent:write"]),
      ...registry,
      ...loops,
      "GET /teams/{id}/agents/{agentID}": { body: agent },
      "PUT /teams/{id}/agents/{agentID}": { body: agent },
      ...extra,
    });
  }

  async function openEdit() {
    show(Agents);
    await userEvent.click(await screen.findByRole("button", { name: /^edit$/i }));
    await screen.findByRole("heading", { name: /edit agent/i });
  }

  async function save(seen: { method: string; body: unknown }[]) {
    await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    return JSON.parse(String(seen.find((s) => s.method === "PUT")?.body));
  }

  it("offers the patterns the brain runs", async () => {
    const seen = editing(stored);
    await openEdit();

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

  // A pattern the brain no longer lists is still the agent's; the form shows
  // it rather than quietly changing it on save.
  it("keeps a pattern the brain does not list", async () => {
    const seen = editing({ ...stored, pattern: "custom-triage" });
    await openEdit();

    const pattern = screen.getByLabelText(/^pattern$/i);
    expect(pattern).toHaveValue("custom-triage");
    expect((await save(seen)).pattern).toBe("custom-triage");
  });

  it("asks the brain for what was typed, a short page at a time", async () => {
    const seen = editing(stored);
    await openEdit();

    await userEvent.click(screen.getByRole("button", { name: /add skill/i }));
    await userEvent.type(await screen.findByRole("combobox", { name: /search skills/i }), "pd");
    await waitFor(() =>
      expect(
        seen.some((s) => s.path.startsWith(`/teams/${TEAM}/skills?`) && s.path.includes("q=pd")),
      ).toBe(true),
    );
    const asked = seen.find((s) => s.path.includes("q=pd"));
    expect(asked?.path).toContain("limit=20");
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
    await openEdit();

    await waitFor(() => expect(chips("Skills")).toEqual(["far-away"]));
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
      const seen = editing({ ...stored, mcp_servers: [{ id: "m1" }] }, tools);
      await openEdit();

      const limit = await screen.findByRole("button", { name: "Tools of github" });
      expect(limit).toHaveTextContent("every tool");
      await userEvent.click(limit);
      await userEvent.click(await screen.findByRole("checkbox", { name: "search_code" }));
      expect(screen.getByRole("checkbox", { name: "Every tool" })).not.toBeChecked();
      await userEvent.keyboard("{Escape}");

      expect((await save(seen)).mcp_servers).toEqual([{ id: "m1", allow: ["search_code"] }]);
    });

    // A grant of no tools grants nothing, so unticking the last is every tool.
    it("goes back to every tool when the last is unticked, or Every tool is", async () => {
      for (const untick of ["search_code", "Every tool"]) {
        const seen = editing(
          { ...stored, mcp_servers: [{ id: "m1", allow: ["search_code"] }] },
          tools,
        );
        const { unmount } = await (async () => {
          const r = show(Agents);
          await userEvent.click(await screen.findByRole("button", { name: /^edit$/i }));
          await screen.findByRole("heading", { name: /edit agent/i });
          return r;
        })();
        await userEvent.click(await screen.findByRole("button", { name: "Tools of github" }));
        await userEvent.click(await screen.findByRole("checkbox", { name: untick }));
        await userEvent.keyboard("{Escape}");

        expect((await save(seen)).mcp_servers).toEqual([{ id: "m1" }]);
        unmount();
        vi.unstubAllGlobals();
      }
    });

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
      const seen = editing(
        { ...stored, mcp_servers: [{ id: "m1", allow: ["search_code"] }] },
        { ...tools, "GET /teams/{id}/mcp-servers": { body: { items: [...servers, linear] } } },
      );
      await openEdit();

      await pick("server", "linear");
      expect((await save(seen)).mcp_servers).toEqual([
        { id: "m1", allow: ["search_code"] },
        { id: "m2" },
      ]);
    });
  });
});

/* ------------------------------------------------------------------ the web */

// web_search and web_fetch come as one grant. A new agent is off the web,
// as the brain's own default is.
describe("whether an agent is on the web", () => {
  const WEB = /search the web and read pages/i;

  it("sends the grant as the form holds it", () => {
    expect(agentRequest({ ...draft, web: true })).toMatchObject({ web: true });
    expect(agentRequest(draft)).toMatchObject({ web: false });
  });

  async function defining() {
    const seen = brain({
      ...team(["agent:write"]),
      ...registry,
      "POST /teams/{id}/agents": { status: 201, body: { ...triage, id: "a9", name: "reviewer" } },
    });
    show(Agents);
    await userEvent.click(await screen.findByRole("button", { name: /define agent/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "reviewer");
    await userEvent.type(screen.getByLabelText(/^model$/i), "m");
    return seen;
  }

  function created(seen: { method: string; body: unknown }[]) {
    return JSON.parse(String(seen.find((s) => s.method === "POST")?.body));
  }

  it("defines a new agent off the web unless the box is ticked", async () => {
    const seen = await defining();

    expect(screen.getByRole("checkbox", { name: WEB })).not.toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(created(seen).web).toBe(false);
  });

  it("sends web: true once the box is ticked", async () => {
    const seen = await defining();
    await userEvent.click(screen.getByRole("checkbox", { name: WEB }));
    await userEvent.click(screen.getByRole("button", { name: /^define$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(created(seen).web).toBe(true);
  });

  describe("on an agent that has it", () => {
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
      web: true,
    };

    function editing() {
      return brain({
        ...team(["agent:write"]),
        ...registry,
        "GET /teams/{id}/agents/{agentID}": { body: stored },
        "PUT /teams/{id}/agents/{agentID}": { body: stored },
      });
    }

    async function openEdit() {
      show(Agents);
      const [first] = await screen.findAllByRole("button", { name: /^edit$/i });
      await userEvent.click(first as HTMLElement);
      await screen.findByRole("heading", { name: /edit agent/i });
    }

    async function save(seen: { method: string; body: unknown }[]) {
      await userEvent.click(screen.getByRole("button", { name: /^save$/i }));
      await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
      return JSON.parse(String(seen.find((s) => s.method === "PUT")?.body));
    }

    // A PUT is whole: a save about something else must not take the web away.
    it("keeps the grant through a save that changes something else", async () => {
      const seen = editing();
      await openEdit();

      expect(screen.getByRole("checkbox", { name: WEB })).toBeChecked();
      await userEvent.type(screen.getByLabelText(/^description$/i), " and more");
      expect(await save(seen)).toMatchObject({ web: true, description: "Sorts issues and more" });
    });

    it("takes the grant away when the box is unticked", async () => {
      const seen = editing();
      await openEdit();
      await userEvent.click(screen.getByRole("checkbox", { name: WEB }));

      expect((await save(seen)).web).toBe(false);
    });
  });
});
