import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";
import type { Proposal, Run } from "@/api/types";
import { buildRefusal, isLive, NO_BUILDER_MODEL, turnsOf } from "@/lib/build";

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
