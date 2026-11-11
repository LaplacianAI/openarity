import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { BlueprintPlan, Proposal, Run } from "@/api/types";
import { brain, type Reply, type Seen, STAMP, showAt, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.build");

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const SESSION = "0b7c3e52-8a41-4f6d-9e2b-5c1a7d3f8e60";
const RUNS = "GET /teams/{id}/sessions/{sid}/runs";
const START = "POST /teams/{id}/runs";
const PROPOSALS = "GET /teams/{id}/proposals";
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

  it("says the builder is working while its run is live, and waits to send", async () => {
    conversation({
      [RUNS]: { body: { items: [{ ...answered, status: "running", output: null }, asked] } },
      [PROPOSALS]: { body: { items: [] } },
    });
    at(`/ui/build?session=${SESSION}`);

    expect(await screen.findByText("The builder is working…")).toBeInTheDocument();
    expect(await screen.findByText(/still working on the last message/)).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox"), "And the FAQ.");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
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
