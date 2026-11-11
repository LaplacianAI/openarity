import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { BlueprintPlan, Proposal, Run } from "@/api/types";
import { brain, feed, type Reply, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route, POLL_MS } = await import("@/routes/ui.build");

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const SESSION = "0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60";
const RUNS = "GET /teams/{id}/sessions/{sid}/runs";
const START = "POST /teams/{id}/runs";
const PROPOSALS = "GET /teams/{id}/proposals";
const EVENTS = "GET /teams/{id}/runs/{rid}/events";
const PROPOSAL = "GET /teams/{id}/proposals/{pid}";
const APPLY = "POST /teams/{id}/proposals/{pid}/apply";

function run(id: string, input: string, extra: Partial<Run> = {}): Run {
  return {
    id,
    session_id: SESSION,
    agent: "builder",
    status: "succeeded",
    input,
    output: null,
    error: null,
    input_tokens: 1,
    output_tokens: 1,
    created_at: STAMP,
    started_at: STAMP,
    finished_at: STAMP,
    parent_run_id: null,
    depth: 0,
    hub: false,
    builder: true,
    delegates: [],
    reply: null,
    ...extra,
  };
}

const asked = run("r1", "An agent that triages support email.", {
  output: "Which model should it use?",
  created_at: "2026-10-01T00:00:00Z",
});
const answered = run("r2", "Sonnet.", {
  output: "I propose a triage agent.",
  created_at: "2026-10-01T00:01:00Z",
});

const plan: BlueprintPlan = {
  changes: [{ kind: "agent", name: "triage", action: "create" }],
  refusals: [],
  digest: "sha256:mine",
};

const proposal: Proposal = {
  id: "p1",
  session_id: SESSION,
  run_id: "r2",
  summary: "A triage agent with the FAQ",
  state: "open",
  created_at: "2026-10-01T00:01:30Z",
};

function at(path: string) {
  return showAt(path, [["/ui/build", Route]]);
}

function conversation(
  over: Record<string, Reply | ((s: Seen) => Reply)> = {},
  actions = ["run:write"],
) {
  return brain({
    ...team(actions),
    // The brain answers newest first; the chat shows oldest first.
    [RUNS]: { body: { items: [answered, asked] } },
    [PROPOSALS]: { body: { items: [proposal] } },
    [PROPOSAL]: { body: { ...proposal, plan } },
    [APPLY]: {
      body: {
        changes: [{ kind: "agent", name: "triage", action: "create", id: "a9" }],
        discovered: [],
      },
    },
    ...over,
  });
}

describe("the builder's conversation", () => {
  it("reads oldest first, with the proposal under the turn that made it", async () => {
    const seen = conversation();
    at(`/ui/build?session=${SESSION}`);

    const turns = await screen.findAllByRole("listitem", { name: undefined });
    const list = screen.getByRole("list", { name: "Conversation" });
    const text = list.textContent ?? "";
    expect(text.indexOf("Which model")).toBeLessThan(text.indexOf("I propose"));
    expect(turns.length).toBeGreaterThan(0);

    const card = await screen.findByRole("region", { name: "Proposal · open" });
    expect(within(card).getByText("A triage agent with the FAQ")).toBeInTheDocument();
    expect(await within(card).findByText("triage")).toBeInTheDocument();
    expect(seen.some((s) => s.path.includes(`/proposals?session=${SESSION}`))).toBe(true);
  });

  it("applies the plan it shows, with that plan's digest, and says what was made", async () => {
    // As the brain does: once applied, the conversation reads it as applied.
    let applied = false;
    const seen = conversation({
      [APPLY]: () => {
        applied = true;
        return {
          body: {
            changes: [
              { kind: "agent", name: "triage", action: "create", id: "a9" },
              { kind: "mcp_server", name: "billing", action: "create", id: "m9" },
            ],
            discovered: [{ server: "billing", tools: ["refund", "charge"] }],
          },
        };
      },
      [PROPOSALS]: () => ({
        body: { items: [{ ...proposal, state: applied ? "applied" : "open" }] },
      }),
    });
    at(`/ui/build?session=${SESSION}`);

    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    const card = await screen.findByRole("region", { name: "Proposal · applied" });
    expect(within(card).getByRole("heading", { name: "Applied" })).toBeInTheDocument();
    // The re-read list says applied; what was made stays on the card.
    await waitFor(() =>
      expect(seen.filter((s) => s.path.includes("/proposals?")).length).toBeGreaterThan(1),
    );
    const kept = screen.getByRole("region", { name: "Proposal · applied" });
    expect(within(kept).getByText("triage")).toBeInTheDocument();
    expect(within(kept).getByText("2 tools discovered")).toBeInTheDocument();
    const sent = seen.find((s) => s.method === "POST" && s.path.endsWith("/proposals/p1/apply"));
    expect(JSON.parse(String(sent?.body))).toEqual({ plan_digest: "sha256:mine" });
  });

  it("names the secrets to set and each new address before Apply", async () => {
    conversation({
      [PROPOSAL]: {
        body: {
          ...proposal,
          blueprint: {
            mcp_servers: [
              {
                name: "billing",
                url: "https://mcp.billing.example/mcp",
                auth_secret: "billing#token",
              },
              { name: "files", command: ["npx", "files-mcp"], env: { KEY: "files#key" } },
              {
                name: "helpdesk",
                url: "https://helpdesk.example/mcp",
                auth_secret: "helpdesk#token",
              },
            ],
          },
          plan: {
            ...plan,
            changes: [
              { kind: "mcp_server", name: "billing", action: "create" },
              { kind: "mcp_server", name: "files", action: "create" },
              { kind: "mcp_server", name: "helpdesk", action: "unchanged", id: "m1" },
            ],
          },
        },
      },
    });
    at(`/ui/build?session=${SESSION}`);

    const needs = await screen.findByRole("region", { name: "Needs from you" });
    expect(within(needs).getByText("billing#token")).toBeInTheDocument();
    expect(within(needs).getByText("files#key")).toBeInTheDocument();
    expect(within(needs).getByText("mcp.billing.example")).toBeInTheDocument();
    // A server the plan leaves alone asks nothing new.
    expect(within(needs).queryByText("helpdesk#token")).not.toBeInTheDocument();
    expect(within(needs).queryByText("helpdesk.example")).not.toBeInTheDocument();
  });

  const needing = {
    [PROPOSAL]: {
      body: {
        ...proposal,
        blueprint: {
          mcp_servers: [
            {
              name: "billing",
              url: "https://mcp.billing.example/mcp",
              auth_secret: "billing#token",
            },
            { name: "files", command: ["npx", "files-mcp"], env: { A: "files#a", B: "files#b" } },
            { name: "deep", url: "https://d.example/mcp", auth_secret: "team/deep#token" },
          ],
        },
        plan: {
          ...plan,
          changes: [
            { kind: "mcp_server", name: "billing", action: "create" },
            { kind: "mcp_server", name: "files", action: "create" },
            { kind: "mcp_server", name: "deep", action: "create" },
          ],
        },
      },
    },
  };

  it("sets a secret the proposal needs, its value going to the store and never shown again", async () => {
    const seen = conversation(
      { ...needing, "PUT /teams/{id}/mcp-secrets/{name}": { status: 204 } },
      ["run:write", "tool:write"],
    );
    at(`/ui/build?session=${SESSION}`);

    const form = await screen.findByRole("form", { name: "Secret billing" });
    const field = within(form).getByLabelText("billing#token");
    await userEvent.type(field, "bill-secret");
    await userEvent.click(within(form).getByRole("button", { name: "Set" }));

    expect(await screen.findByText(/Set\. Apply, and its server/)).toBeInTheDocument();
    const put = seen.find((s) => s.method === "PUT");
    expect(put?.path).toBe(`/teams/${TEAM}/mcp-secrets/billing`);
    expect(JSON.parse(String(put?.body))).toEqual({ values: { token: "bill-secret" } });
    expect(field).toHaveValue("");
  });

  it("writes every key of one secret together, and only once all are filled", async () => {
    const seen = conversation(
      { ...needing, "PUT /teams/{id}/mcp-secrets/{name}": { status: 204 } },
      ["run:write", "tool:write"],
    );
    at(`/ui/build?session=${SESSION}`);

    const form = await screen.findByRole("form", { name: "Secret files" });
    const set = within(form).getByRole("button", { name: "Set" });
    await userEvent.type(within(form).getByLabelText("files#a"), "one");
    expect(set).toBeDisabled();
    await userEvent.type(within(form).getByLabelText("files#b"), "two");
    await userEvent.click(set);

    await waitFor(() => expect(seen.filter((s) => s.method === "PUT")).toHaveLength(1));
    expect(JSON.parse(String(seen.find((s) => s.method === "PUT")?.body))).toEqual({
      values: { a: "one", b: "two" },
    });
    // A secret nested deeper is shown, not offered.
    expect(screen.queryByRole("form", { name: /Secret team\/deep/ })).not.toBeInTheDocument();
    expect(screen.getByText(/write it to the secret store yourself/)).toBeInTheDocument();
  });

  it("offers no Set without tool:write, and says what it takes", async () => {
    conversation(needing, ["run:write"]);
    at(`/ui/build?session=${SESSION}`);

    const form = await screen.findByRole("form", { name: "Secret billing" });
    expect(within(form).queryByRole("button", { name: "Set" })).not.toBeInTheDocument();
    expect((await screen.findAllByText(/Setting it needs tool:write/)).length).toBeGreaterThan(0);
  });

  it("offers no Apply for a plan with a refusal, and shows why", async () => {
    conversation({
      [PROPOSAL]: {
        body: {
          ...proposal,
          plan: {
            ...plan,
            refusals: [{ kind: "agent", name: "triage", message: "agent:write is needed" }],
          },
        },
      },
    });
    at(`/ui/build?session=${SESSION}`);

    expect(await screen.findByText("agent:write is needed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).not.toBeInTheDocument();
  });

  it("shows the new plan when the team moved, and applies nothing", async () => {
    const moved: BlueprintPlan = {
      changes: [{ kind: "agent", name: "triage", action: "update", fields: ["instructions"] }],
      refusals: [],
      digest: "sha256:now",
    };
    const seen = conversation({ [APPLY]: { status: 409, text: JSON.stringify(moved) } });
    at(`/ui/build?session=${SESSION}`);

    await userEvent.click(await screen.findByRole("button", { name: "Apply" }));

    expect(await screen.findByText(/The team changed since this plan/)).toBeInTheDocument();
    expect(screen.getByText("instructions")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => {
      const applies = seen.filter((s) => s.method === "POST" && s.path.endsWith("/apply"));
      expect(JSON.parse(String(applies.at(-1)?.body))).toEqual({ plan_digest: "sha256:now" });
    });
  });

  it("says a proposal the builder replaced is superseded, with no Apply", async () => {
    conversation({ [PROPOSALS]: { body: { items: [{ ...proposal, state: "superseded" }] } } });
    at(`/ui/build?session=${SESSION}`);

    expect(
      await screen.findByRole("region", { name: "Proposal · superseded" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Apply" })).not.toBeInTheDocument();
  });

  it("shows what the builder is doing as it does it, and its reply as it is written", async () => {
    const live = feed();
    const seen = conversation({
      [RUNS]: { body: { items: [{ ...answered, status: "running", output: null }, asked] } },
      [PROPOSALS]: { body: { items: [] } },
      [EVENTS]: { stream: live.stream },
    });
    at(`/ui/build?session=${SESSION}`);

    expect(await screen.findByText("The builder is starting…")).toBeInTheDocument();
    const frame = (seq: number, kind: string, data: unknown) =>
      live.push(`id: ${seq}\nevent: ${kind}\ndata: ${JSON.stringify(data)}\n\n`);
    frame(1, "text", { text: "Let me look." });
    frame(2, "tool_call", { id: "c1", name: "team", arguments: "{}" });
    frame(3, "tool_result", { id: "c1", name: "team", output: "{}" });
    frame(4, "tool_call", {
      id: "c2",
      name: "find_mcp_servers",
      arguments: JSON.stringify({ query: "stripe" }),
    });
    frame(5, "tool_result", { id: "c2", name: "find_mcp_servers", error: "the registry is down" });
    frame(6, "tool_call", {
      id: "c3",
      name: "list_mcp_tools",
      arguments: JSON.stringify({ url: "https://mcp.stripe.example/mcp" }),
    });
    frame(7, "text", { text: "I propose **three** agents" });

    const doing = await screen.findByRole("list", { name: "What the builder is doing" });
    expect(await within(doing).findByText("Reading the team")).toBeInTheDocument();
    expect(
      within(doing).getByText(/Looking up \u201cstripe\u201d in the MCP registry \(it failed/),
    ).toBeInTheDocument();
    expect(
      within(doing).getByText("Asking mcp.stripe.example/mcp for its tools"),
    ).toBeInTheDocument();
    // Only the text since the last call is the reply; the rest led to a call.
    expect(await screen.findByText("three")).toBeInTheDocument();
    expect(screen.queryByText("Let me look.")).not.toBeInTheDocument();

    expect(await screen.findByText(/still working on the last message/)).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox"), "And the FAQ.");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    // Its stream ending reads the conversation again at once, not at the next poll.
    const reads = () => seen.filter((x) => x.path.includes("/sessions/")).length;
    const before = reads();
    live.push(`event: end\ndata: {"status":"succeeded"}\n\n`);
    await waitFor(() => expect(reads()).toBeGreaterThan(before), { timeout: POLL_MS / 2 });
  });

  it("says why a run failed, with its trace", async () => {
    conversation({
      [RUNS]: {
        body: {
          items: [{ ...asked, status: "failed", output: null, error: "the model timed out" }],
        },
      },
      [PROPOSALS]: { body: { items: [] } },
    });
    at(`/ui/build?session=${SESSION}`);

    expect(await screen.findByText(/couldn't finish: the model timed out/)).toBeInTheDocument();
  });
});

describe("finding a conversation again", () => {
  it("lists recent conversations, one per session, newest first, and opens one", async () => {
    const OTHER = "1d2e3f40-5a6b-4c7d-8e9f-0a1b2c3d4e5f";
    brain({
      ...team(["run:write"]),
      "GET /teams/{id}/runs": {
        body: {
          items: [
            run("r9", "Add a deploy agent", {
              status: "running",
              created_at: "2026-10-02T00:00:00Z",
            }),
            run("r8", "Older turn, same session", { created_at: "2026-10-01T12:00:00Z" }),
            run("r7", "Billing agents", { session_id: OTHER, created_at: "2026-10-01T00:00:00Z" }),
            run("r6", "Not the builder's", { builder: false, session_id: "x" }),
          ],
        },
      },
    });
    at("/ui/build");

    const recent = await screen.findByRole("region", { name: "Recent conversations" });
    const links = within(recent).getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual([
      expect.stringContaining("Add a deploy agent"),
      expect.stringContaining("Billing agents"),
    ]);
    expect(links[0]?.getAttribute("href")).toBe(`/ui/build?session=${SESSION}`);
    expect(links[1]?.getAttribute("href")).toBe(`/ui/build?session=${OTHER}`);
    expect(within(recent).queryByText("Not the builder's")).not.toBeInTheDocument();
  });

  it("asks for builder runs only", async () => {
    const seen = brain({ ...team(["run:write"]), "GET /teams/{id}/runs": { body: { items: [] } } });
    at("/ui/build");

    await waitFor(() => expect(seen.some((s) => s.path.includes("/runs?"))).toBe(true));
    expect(seen.find((s) => s.path.includes("/runs?"))?.path).toContain("builder=true");
    expect(screen.queryByRole("region", { name: "Recent conversations" })).not.toBeInTheDocument();
  });

  it("shows no list inside a conversation", async () => {
    conversation();
    at(`/ui/build?session=${SESSION}`);
    await screen.findByRole("list", { name: "Conversation" });
    expect(screen.queryByRole("region", { name: "Recent conversations" })).not.toBeInTheDocument();
  });
});

describe("asking the builder", () => {
  it("starts a builder run, and a first message opens its conversation", async () => {
    const seen = brain({
      ...team(["run:write"]),
      [START]: {
        status: 202,
        body: run("r9", "x", { status: "queued", output: null, finished_at: null }),
      },
      [RUNS]: { body: { items: [] } },
      [PROPOSALS]: { body: { items: [] } },
    });
    const router = at("/ui/build");

    expect(await screen.findByText("What should the team have?")).toBeInTheDocument();
    await userEvent.type(await screen.findByRole("textbox"), "An agent for billing questions.");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => expect(router.state.location.search).toEqual({ session: SESSION }));
    const start = seen.find((s) => s.method === "POST" && s.path.endsWith("/runs"));
    expect(JSON.parse(String(start?.body))).toEqual({
      input: "An agent for billing questions.",
      builder: true,
    });
  });

  it("carries a reply on in the same session", async () => {
    const seen = conversation({
      [START]: {
        status: 202,
        body: run("r3", "x", { status: "queued", output: null, finished_at: null }),
      },
    });
    at(`/ui/build?session=${SESSION}`);

    await screen.findByRole("region", { name: "Proposal · open" });
    await userEvent.type(await screen.findByRole("textbox"), "Give it the helpdesk server too.");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await waitFor(() => {
      const start = seen.find((s) => s.method === "POST" && s.path.endsWith("/runs"));
      expect(JSON.parse(String(start?.body))).toEqual({
        input: "Give it the helpdesk server too.",
        builder: true,
        session_id: SESSION,
      });
    });
  });

  it("names the team's model when the builder has none, with the way to settings", async () => {
    brain({
      ...team(["run:write"]),
      [START]: { status: 409, text: "the team has no model set, so its builder cannot run" },
    });
    at("/ui/build");

    await userEvent.type(await screen.findByRole("textbox"), "An agent.");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    expect(
      await screen.findByText("The builder can't run until the team has a model."),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open team settings/ })).toBeInTheDocument();
  });

  it("lets someone without run:write read the conversation but not send", async () => {
    conversation({}, []);
    at(`/ui/build?session=${SESSION}`);

    expect(await screen.findByText("I propose a triage agent.")).toBeInTheDocument();
    expect(await screen.findByText(/needs run:write/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
  });

  it("ignores a session in the URL that is not an id", async () => {
    const seen = brain({ ...team(["run:write"]) });
    at("/ui/build?session=not-an-id");

    expect(await screen.findByText("What should the team have?")).toBeInTheDocument();
    expect(seen.some((s) => s.path.includes("not-an-id"))).toBe(false);
  });
});
