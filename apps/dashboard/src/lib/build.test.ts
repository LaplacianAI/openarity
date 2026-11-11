import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";
import type { BlueprintPlan, Proposal, Run, RunEvent } from "@/api/types";
import {
  buildRefusal,
  conversationsOf,
  isLive,
  liveOf,
  NO_BUILDER_MODEL,
  needsOf,
  secretGroups,
  turnsOf,
} from "@/lib/build";

const run = (id: string, at: string, extra: Partial<Run> = {}) =>
  ({ id, created_at: at, builder: true, status: "succeeded", ...extra }) as Run;
const proposal = (id: string, runId: string, at: string) =>
  ({ id, run_id: runId, created_at: at, state: "open" }) as Proposal;

describe("a conversation's turns", () => {
  it("are the builder's runs, oldest first, each with its newest proposal", () => {
    const turns = turnsOf(
      [
        run("b", "2026-01-02"),
        run("hub", "2026-01-03", { builder: false }),
        run("a", "2026-01-01"),
      ],
      [proposal("p1", "b", "2026-01-02T01"), proposal("p2", "b", "2026-01-02T02")],
    );
    expect(turns.map((t) => [t.run.id, t.proposal?.id ?? null])).toEqual([
      ["a", null],
      ["b", "p2"],
    ]);
  });

  it("are live while queued or running", () => {
    expect(isLive(run("a", "x", { status: "queued" }))).toBe(true);
    expect(isLive(run("a", "x", { status: "running" }))).toBe(true);
    expect(isLive(run("a", "x", { status: "failed" }))).toBe(false);
  });
});

describe("a refused ask", () => {
  it("names the builder's model, not the hub's", () => {
    expect(buildRefusal(new ApiError(409, "no model"))).toEqual({
      text: NO_BUILDER_MODEL,
      settings: true,
    });
    expect(buildRefusal(new ApiError(403, "")).settings).toBe(false);
  });
});

describe("needsOf", () => {
  const plan = (changes: BlueprintPlan["changes"]): BlueprintPlan => ({
    changes,
    refusals: [],
    digest: "d",
  });

  it("counts only servers the plan creates or changes, and an address only when it is new", () => {
    const bp = {
      mcp_servers: [
        { name: "a", url: "https://a.example/mcp", auth_secret: "a#t" },
        { name: "b", url: "https://b.example/mcp", auth_secret: "b#t", env: { X: "b#x", N: 3 } },
        { name: "c", url: "not a url", auth_secret: "a#t" },
        { name: "d", url: "https://d.example/mcp", auth_secret: "d#t" },
      ],
    };
    const got = needsOf(
      bp,
      plan([
        { kind: "mcp_server", name: "a", action: "create" },
        { kind: "mcp_server", name: "b", action: "update", fields: ["auth_secret"] },
        { kind: "mcp_server", name: "c", action: "create" },
        { kind: "mcp_server", name: "d", action: "unchanged" },
        { kind: "agent", name: "a", action: "create" },
      ]),
    );
    expect(got.secrets).toEqual(["a#t", "b#t", "b#x"]);
    expect(got.addresses).toEqual([{ server: "a", host: "a.example" }]);
  });

  it("asks nothing of a proposal with no blueprint or no servers", () => {
    expect(needsOf(undefined, plan([]))).toEqual({ secrets: [], addresses: [] });
    expect(needsOf({ agents: [{ name: "x" }] }, plan([]))).toEqual({ secrets: [], addresses: [] });
  });
});

describe("liveOf", () => {
  const ev = (kind: string, data: unknown): RunEvent => ({ seq: 1, kind, data });
  const call = (id: string, name: string, args: unknown = {}) =>
    ev("tool_call", {
      id,
      name,
      arguments: typeof args === "string" ? args : JSON.stringify(args),
    });

  it("names each call in plain words, and marks it once its result comes", () => {
    const { activity } = liveOf([
      call("1", "team"),
      ev("tool_result", { id: "1" }),
      call("2", "web_search", { query: "stripe mcp" }),
      call("3", "web_fetch", { url: "https://docs.stripe.example/mcp" }),
      ev("tool_result", { id: "3", error: "404" }),
      call("4", "graph", {}),
      call("5", "propose"),
      call("6", "Skill", { name: "x" }),
      call("7", "list_mcp_tools", "not json"),
    ]);
    expect(activity.map((a) => [a.label, a.state])).toEqual([
      ["Reading the team", "done"],
      ["Searching the web for \u201cstripe mcp\u201d", "running"],
      ["Reading docs.stripe.example/mcp", "failed"],
      ["Searching the team", "running"],
      ["Writing the proposal", "running"],
      ["Using Skill", "running"],
      ["Asking a server for its tools", "running"],
    ]);
  });

  it("keeps only the text since the last call as the reply", () => {
    const { text } = liveOf([
      ev("text", { text: "On my " }),
      ev("text", { text: "way." }),
      call("1", "team"),
      ev("tool_result", { id: "1" }),
      ev("text", { text: "Here " }),
      ev("text", { text: "it is." }),
      ev("text", { nope: 1 }),
      ev("step", null),
    ]);
    expect(text).toBe("Here it is.");
  });
});

describe("conversationsOf", () => {
  it("keeps each session's newest builder run, newest first, at most as many as asked", () => {
    const r = (id: string, session: string, builder = true) =>
      ({ id, session_id: session, builder, created_at: id }) as Run;
    const runs = [r("5", "a"), r("4", "b"), r("3", "a"), r("2", "c", false), r("1", "d")];
    expect(conversationsOf(runs).map((x) => x.id)).toEqual(["5", "4", "1"]);
    expect(conversationsOf(runs, 2).map((x) => x.id)).toEqual(["5", "4"]);
  });
});

describe("secretGroups", () => {
  it("groups a proposal's references by secret, and offers only what the brain can write", () => {
    expect(
      secretGroups(
        [
          "stripe#token",
          "teams/T/mcp/stripe#account",
          "stripe#token",
          "teams/OTHER/mcp/x#k",
          "team/deep#k",
          "nokey",
        ],
        "T",
      ),
    ).toEqual([
      { name: "stripe", keys: ["token", "account"], settable: true },
      { name: "teams/OTHER/mcp/x", keys: ["k"], settable: false },
      { name: "team/deep", keys: ["k"], settable: false },
      { name: "nokey", keys: [], settable: false },
    ]);
  });
});
