import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { BlueprintPlan } from "@/api/types";
import { brain, pageOf, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.compose");
await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const PLAN = "/teams/{id}/blueprints/plan";
const APPLY = "/teams/{id}/blueprints/apply";

const github = {
  id: "m1",
  team_id: TEAM,
  name: "github",
  url: "https://a.b",
  env: {},
  bare: false,
  created_at: STAMP,
  updated_at: STAMP,
};

const writer = {
  id: "a1",
  team_id: TEAM,
  name: "writer",
  description: "Writes",
  kind: "custom",
  model: { name: "m", max_tokens: 64 },
  pattern: "react",
  created_at: STAMP,
  updated_at: STAMP,
  instructions: "Write.",
  max_steps: 3,
  steer_continuations: 0,
  parser: false,
  output_schema: { name: "reply", json: { type: "object" }, strict: true },
  mcp_servers: [{ id: "m1" }],
  skills: [],
  delegation: "none",
  delegates: [],
  web: false,
  delegate_count: 0,
  mcp_server_count: 1,
  skill_count: 0,
};

const toChange: BlueprintPlan = {
  changes: [
    { kind: "mcp_server", name: "helpdesk", action: "create" },
    { kind: "agent", name: "triage", action: "create" },
  ],
  refusals: [],
  digest: "sha256:shown",
};

const registry = {
  "GET /teams/{id}/agents": { body: { items: [writer] } },
  "GET /teams/{id}/agents/{agentID}": { body: writer },
  "GET /teams/{id}/mcp-servers": { body: { items: [github] } },
  "GET /teams/{id}/mcp-servers/{serverID}": { body: github },
  "GET /teams/{id}/skills": { body: { items: [] } },
  "GET /teams/{id}/loops": { body: { items: [{ name: "react" }] } },
  "GET /teams/{id}/settings": {
    body: {
      model: null,
      search: { provider: "none", url: null, key_set: false },
      learning: false,
      compaction_messages: 40,
      hub: null,
      updated_at: null,
    },
  },
};

function open(extra: Record<string, unknown> = {}) {
  const seen = brain({ ...team(["agent:write"]), ...registry, ...extra });
  showAt("/ui/compose", [["/ui/compose", Route]]);
  return seen;
}

function posted(seen: Seen[], path: string): unknown[] {
  const re = new RegExp(`^${path.replace("{id}", "[^/]+")}$`);
  return seen
    .filter((s) => s.method === "POST" && re.test(s.path))
    .map((s) => JSON.parse(String(s.body)));
}

function draftRows(): string[] {
  const list = screen.queryByRole("list", { name: undefined });
  if (!list) return [];
  return within(screen.getByRole("region", { name: "Draft" }))
    .queryAllByRole("listitem")
    .map((li) => li.textContent ?? "");
}

async function draftServer(name: string, url: string) {
  await userEvent.click(await screen.findByRole("button", { name: "MCP server" }));
  const dialog = await screen.findByRole("dialog");
  await userEvent.type(within(dialog).getByLabelText(/^name$/i), name);
  await userEvent.type(within(dialog).getByLabelText(/^url$/i), url);
  await userEvent.click(within(dialog).getByRole("button", { name: "Add to draft" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
}

async function draftAgent(name: string, then?: () => Promise<void>) {
  await userEvent.click(await screen.findByRole("button", { name: "Agent" }));
  const sheet = await screen.findByRole("dialog");
  await userEvent.type(within(sheet).getByLabelText(/^name$/i), name);
  await userEvent.type(within(sheet).getByLabelText(/^model$/i), "m");
  await then?.();
  await userEvent.click(within(sheet).getByRole("button", { name: "Add to draft" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
}

async function pick(noun: string, name: string | RegExp) {
  await userEvent.click(screen.getByRole("button", { name: new RegExp(`add ${noun}`, "i") }));
  await userEvent.click(await screen.findByRole("option", { name }));
  await userEvent.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("listbox")).toBeNull());
}

/** Picks one of the team's own to change: the pick itself opens its form. */
async function choose(noun: string, name: string) {
  await userEvent.click(await screen.findByRole("button", { name: "Change an existing one" }));
  await userEvent.click(screen.getByRole("button", { name: new RegExp(`add ${noun}`, "i") }));
  await userEvent.click(await screen.findByRole("option", { name }));
}

describe("composing a draft", () => {
  it("grants an agent a server the draft creates, and plans them as one blueprint", async () => {
    const seen = open({ [`POST ${PLAN}`]: { body: toChange } });

    await draftServer("helpdesk", "https://h.example/mcp");
    await draftAgent("triage", () => pick("server", /helpdesk \(in draft\)/));
    expect(draftRows()).toEqual([
      expect.stringContaining("helpdesk"),
      expect.stringContaining("triage"),
    ]);

    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    expect(await screen.findByText("2 to create, 0 to change, 0 unchanged.")).toBeInTheDocument();

    expect(posted(seen, PLAN)[0]).toMatchObject({
      mcp_servers: [{ name: "helpdesk", url: "https://h.example/mcp" }],
      agents: [{ name: "triage", mcp_servers: [{ name: "helpdesk" }], skills: [] }],
    });
    expect(posted(seen, APPLY)).toHaveLength(0);
  });

  it("names a grant of the team's own server by its name", async () => {
    const seen = open({ [`POST ${PLAN}`]: { body: toChange } });

    await draftAgent("triage", () => pick("server", "github"));
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    await waitFor(() => expect(posted(seen, PLAN)).toHaveLength(1));
    expect(posted(seen, PLAN)[0]).toMatchObject({
      agents: [{ mcp_servers: [{ name: "github" }] }],
    });
  });

  it("applies the plan it showed, with its digest, and empties the draft", async () => {
    const seen = open({
      [`POST ${PLAN}`]: { body: toChange },
      [`POST ${APPLY}`]: {
        body: {
          changes: [
            { kind: "mcp_server", name: "helpdesk", action: "create", id: "m9" },
            { kind: "agent", name: "triage", action: "create", id: "a9" },
          ],
          discovered: [
            { server: "helpdesk", tools: [], error: "the server did not answer in time" },
          ],
        },
      },
    });

    await draftServer("helpdesk", "https://h.example/mcp");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByRole("heading", { name: "Applied" })).toBeInTheDocument();
    expect(posted(seen, APPLY)[0]).toMatchObject({ plan_digest: "sha256:shown" });
    expect(screen.getByText(/Nothing drafted yet/)).toBeInTheDocument();
    expect(
      screen.getByText(/has no tools yet: the server did not answer in time/),
    ).toBeInTheDocument();
    expect(screen.getByText(/open it on MCP servers and discover it/)).toBeInTheDocument();
  });

  // Otherwise Apply would send a plan for a draft no longer on the screen.
  it("takes the plan away as soon as the draft changes", async () => {
    open({ [`POST ${PLAN}`]: { body: toChange } });

    await draftServer("helpdesk", "https://h.example/mcp");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await screen.findByRole("button", { name: "Apply" });

    await userEvent.click(screen.getByRole("button", { name: "Remove helpdesk" }));
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(screen.queryByRole("heading", { name: "Plan" })).toBeNull();
  });

  it("offers no Apply while the plan has refusals, and shows each", async () => {
    open({
      [`POST ${PLAN}`]: {
        body: {
          changes: [{ kind: "agent", name: "triage", action: "create" }],
          refusals: [
            {
              kind: "agent",
              name: "triage",
              message: "you need agent:write to create or change an agent",
            },
          ],
          digest: "d",
        },
      },
    });

    await draftAgent("triage");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));

    expect(await screen.findByText(/you need agent:write/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
  });

  it("shows the new plan when the team changed, and applies nothing", async () => {
    open({
      [`POST ${PLAN}`]: { body: toChange },
      [`POST ${APPLY}`]: {
        status: 409,
        text: JSON.stringify({
          ...toChange,
          digest: "new",
          changes: [{ kind: "mcp_server", name: "helpdesk", action: "update", fields: ["url"] }],
        }),
      },
    });

    await draftServer("helpdesk", "https://h.example/mcp");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByText(/The team changed since that plan/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Applied" })).toBeNull();
  });

  it("says the brain's sentence when an apply fails for another reason", async () => {
    open({
      [`POST ${PLAN}`]: { body: toChange },
      [`POST ${APPLY}`]: {
        status: 409,
        text: "the team changed while this was applied: plan again",
      },
    });

    await draftServer("helpdesk", "https://h.example/mcp");
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("plan again");
  });

  it("offers no Plan for an empty draft", async () => {
    open();
    expect(await screen.findByRole("button", { name: "Plan" })).toBeDisabled();
  });
});

describe("editing the draft", () => {
  it("reopens a drafted item with what was entered, and replaces it", async () => {
    const seen = open({ [`POST ${PLAN}`]: { body: toChange } });

    await draftServer("helpdesk", "https://h.example/mcp");
    await userEvent.click(screen.getByRole("button", { name: "Edit helpdesk" }));
    const dialog = await screen.findByRole("dialog");
    const url = within(dialog).getByLabelText(/^url$/i);
    expect(url).toHaveValue("https://h.example/mcp");
    await userEvent.clear(url);
    await userEvent.type(url, "https://h2.example/mcp");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add to draft" }));

    expect(draftRows()).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await waitFor(() => expect(posted(seen, PLAN)).toHaveLength(1));
    expect(posted(seen, PLAN)[0]).toMatchObject({
      mcp_servers: [{ url: "https://h2.example/mcp" }],
    });
  });

  // An edit on the Agents screen keeps what its form does not show; a draft
  // describes the agent whole, so it must keep them too.
  it("changes an existing agent, keeping what the form does not show", async () => {
    const seen = open({ [`POST ${PLAN}`]: { body: toChange } });

    await choose("agent", "writer");
    const sheet = await screen.findByRole("dialog", { name: /change writer/i });
    await userEvent.click(within(sheet).getByRole("button", { name: "Add to draft" }));

    expect(draftRows()).toEqual([expect.stringContaining("Changes the existing one")]);
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await waitFor(() => expect(posted(seen, PLAN)).toHaveLength(1));
    expect(posted(seen, PLAN)[0]).toMatchObject({
      agents: [
        {
          name: "writer",
          output_schema: { name: "reply", json: { type: "object" }, strict: true },
          mcp_servers: [{ name: "github" }],
        },
      ],
    });
  });

  it("refuses renaming an existing one, which would make a second", async () => {
    open();

    await choose("server", "github");
    const dialog = await screen.findByRole("dialog", { name: /change github/i });
    const name = within(dialog).getByLabelText(/^name$/i);
    await userEvent.clear(name);
    await userEvent.type(name, "gh");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add to draft" }));

    expect(within(dialog).getByRole("alert")).toHaveTextContent("cannot be renamed here");
    expect(draftRows()).toHaveLength(0);
  });

  it("says what is wrong with a form before it is drafted", async () => {
    open();
    await userEvent.click(await screen.findByRole("button", { name: "MCP server" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add to draft" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("A server needs a name.");
  });

  it("drafts a skill folder by the name its SKILL.md gives, and sends its files", async () => {
    const seen = open({ [`POST ${PLAN}`]: { body: toChange } });

    const skill = new File(["---\nname: support-faq\n---\nhi\n"], "SKILL.md");
    Object.defineProperty(skill, "webkitRelativePath", { value: "faq-folder/SKILL.md" });
    await userEvent.upload(await screen.findByLabelText("Skill folder"), [skill]);

    expect(await screen.findByText("support-faq")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Plan" }));
    await waitFor(() => expect(posted(seen, PLAN)).toHaveLength(1));
    expect(posted(seen, PLAN)[0]).toMatchObject({
      skills: [{ files: [{ path: "faq-folder/SKILL.md" }] }],
    });
  });
});
