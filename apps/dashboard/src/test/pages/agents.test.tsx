import { screen, waitFor } from "@testing-library/react";
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

const draft = {
  name: "triage",
  description: " Sorts ",
  instructions: "Label it.",
  model: " m ",
  maxTokens: "1024",
  pattern: "react",
  maxSteps: "5",
  tokenBudget: "",
  servers: ["m1"],
  skills: ["s1"],
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
  await userEvent.click(await screen.findByRole("checkbox", { name: "github" }));
  await userEvent.click(screen.getByRole("checkbox", { name: "pdf" }));
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
