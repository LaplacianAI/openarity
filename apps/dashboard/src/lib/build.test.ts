import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";
import type { BlueprintPlan, Proposal, Run } from "@/api/types";
import { buildRefusal, isLive, NO_BUILDER_MODEL, needsOf, turnsOf } from "@/lib/build";

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
