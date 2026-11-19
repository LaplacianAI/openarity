import { describe, expect, it } from "vitest";

import type { ToolApproval } from "@/api/types";

import {
  answeredBy,
  fromApproval,
  fromStream,
  mayAnswer,
  merged,
  outcome,
  timeLeft,
  toolParts,
  whyNot,
} from "./approvals";

const api: ToolApproval = {
  id: "a1",
  run_id: "r1",
  root_run_id: "r0",
  call_id: "c1",
  tool: "github__create_issue",
  class: "write",
  arguments: { title: "Bug" },
  summary: "title: Bug",
  code: "K7QX",
  status: "pending",
  requested_for: "me",
  require_other: false,
  decided_by: null,
  decided_via: null,
  scope: null,
  reason: null,
  expires_at: "2026-11-13T11:00:00Z",
  decided_at: null,
  used_at: null,
  created_at: "2026-11-13T10:00:00Z",
};

const holds =
  (...p: string[]) =>
  (x: string) =>
    p.includes(x);

describe("toolParts", () => {
  it("splits a server's tool, and leaves a built-in whole", () => {
    expect(toolParts("github__create_issue")).toEqual({ server: "github", name: "create_issue" });
    expect(toolParts("web_search")).toEqual({ server: null, name: "web_search" });
  });
});

describe("timeLeft", () => {
  const t = Date.parse("2026-11-13T10:00:00Z");
  it("says minutes, then hours, then nothing once past", () => {
    expect(timeLeft("2026-11-13T10:12:00Z", t)).toBe("12 min left");
    expect(timeLeft("2026-11-13T10:00:20Z", t)).toBe("under a minute left");
    expect(timeLeft("2026-11-13T12:30:00Z", t)).toBe("2 h 30 min left");
    expect(timeLeft("2026-11-13T11:00:00Z", t)).toBe("1 h left");
    expect(timeLeft("2026-11-13T09:59:00Z", t)).toBeNull();
  });
});

describe("mayAnswer", () => {
  it("needs approval:self, and then decide or being the person it was asked for", () => {
    expect(mayAnswer(fromApproval(api), holds("approval:decide"), "x")).toBe(false);
    expect(mayAnswer(fromApproval(api), holds("approval:self"), "x")).toBe(false);
    expect(mayAnswer(fromApproval(api), holds("approval:self"), "me")).toBe(true);
    expect(mayAnswer(fromApproval(api), holds("approval:self", "approval:decide"), "x")).toBe(true);
  });

  it("refuses the person it was asked for when a rule wants someone else", () => {
    const r = fromApproval({ ...api, require_other: true });
    expect(mayAnswer(r, holds("approval:self"), "me")).toBe(false);
    expect(mayAnswer(r, holds("approval:self", "approval:decide"), "me")).toBe(false);
    expect(mayAnswer(r, holds("approval:self", "approval:decide"), "boss")).toBe(true);
    expect(whyNot(r, holds("approval:self"), "me")).toMatch(/someone other/);
    expect(whyNot(r, holds(), "me")).toMatch(/approval:self/);
  });
});

describe("outcome and answeredBy", () => {
  const view = fromApproval(api);
  it("says each ending, and a set-aside as the person's doing", () => {
    expect(outcome({ ...view, status: "approved", scope: "run" })).toBe("Approved for this run");
    expect(outcome({ ...view, status: "denied" })).toBe("Declined");
    expect(outcome({ ...view, status: "expired" })).toMatch(/Expired/);
    expect(outcome({ ...view, status: "cancelled", superseded: true })).toBe(
      "Set aside: you wrote again",
    );
  });

  it("says who and where when known, and the brain for a sweep", () => {
    expect(answeredBy({ ...view, by: "u2", via: "cli" }, () => "ann")).toBe("by ann with oa");
    expect(answeredBy({ ...view, by: "u2", via: null }, () => null)).toBe("by a team member");
    expect(answeredBy({ ...view, by: null, via: "sweep" }, () => null)).toBe("by the brain");
    expect(answeredBy(view, () => null)).toBe("");
  });
});

describe("fromApproval", () => {
  it("reads a request its person gave up by writing again as set aside, and one ended with its run as not", () => {
    const set = fromApproval({
      ...api,
      status: "cancelled",
      decided_by: "me",
      decided_via: "channel",
    });
    const ended = fromApproval({
      ...api,
      status: "cancelled",
      decided_by: null,
      decided_via: "sweep",
    });
    expect(set.superseded).toBe(true);
    expect(ended.superseded).toBe(false);
    expect(fromApproval({ ...api, status: "denied", decided_by: "me" }).superseded).toBe(false);
  });
});

describe("merged", () => {
  it("takes the stream's word on an answer, and the API's arguments", () => {
    const stream = {
      ...fromStream({
        id: "a1",
        runId: "r1",
        toolCallId: "c1",
        tool: api.tool,
        class: "write",
        summary: "title: Bug",
        code: "K7QX",
        expiresAt: api.expires_at,
        status: "cancelled",
        by: null,
        via: null,
        scope: null,
        superseded: true,
      }),
    };
    const m = merged(stream, api);
    expect(m).toMatchObject({ status: "cancelled", superseded: true, arguments: { title: "Bug" } });
    expect(merged(stream, undefined)).toBe(stream);
  });
});
