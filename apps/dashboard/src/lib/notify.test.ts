import { describe, expect, it } from "vitest";

import type { ToolApproval } from "@/api/types";
import { answerable, fresh, requestNotice, requestsNotice, sendersNotice } from "@/lib/notify";

function request(id: string, extra: Partial<ToolApproval> = {}): ToolApproval {
  return {
    id,
    run_id: "r1",
    root_run_id: "r1",
    call_id: `c-${id}`,
    tool: "github__create_issue",
    class: "write",
    arguments: {},
    summary: `title: ${id}`,
    code: "ABCD",
    status: "pending",
    requested_for: "me",
    require_other: false,
    decided_by: null,
    decided_via: null,
    scope: null,
    reason: null,
    expires_at: new Date(Date.now() + 60_000).toISOString(),
    decided_at: null,
    used_at: null,
    created_at: new Date().toISOString(),
    ...extra,
  };
}

const may =
  (...granted: string[]) =>
  (p: string) =>
    granted.includes(p);

describe("fresh", () => {
  it("only learns what is there on the first look", () => {
    expect(fresh(null, ["a", "b"])).toEqual([]);
  });

  it("is what was not seen before, in order", () => {
    expect(fresh(new Set(["a"]), ["a", "b", "c"])).toEqual(["b", "c"]);
  });

  it("is nothing when what was seen has only gone", () => {
    expect(fresh(new Set(["a", "b"]), ["a"])).toEqual([]);
  });
});

describe("answerable", () => {
  const mine = request("mine");
  const theirs = request("theirs", { requested_for: "sam" });
  const secondEyes = request("second", { require_other: true });

  it("keeps the viewer's own requests when they may approve their own", () => {
    expect(answerable([mine, theirs], may("approval:self"), "me")).toEqual([mine]);
  });

  it("keeps everyone's for someone who decides the team's", () => {
    expect(answerable([mine, theirs], may("approval:self", "approval:decide"), "me")).toEqual([
      mine,
      theirs,
    ]);
  });

  it("drops the viewer's own where a rule wants someone else to answer", () => {
    expect(answerable([secondEyes], may("approval:self", "approval:decide"), "me")).toEqual([]);
  });

  it("keeps nothing for someone who may not answer at all", () => {
    expect(answerable([mine, theirs], may("approval:read"), "me")).toEqual([]);
  });
});

describe("what a notification says", () => {
  it("names the tool and its server, and the request's summary", () => {
    expect(requestNotice(request("a"))).toEqual({
      title: "An agent asks before create_issue on github",
      body: "Open Tool approvals to answer.",
    });
  });

  it("names a built-in tool alone", () => {
    expect(requestNotice(request("a", { tool: "web_fetch" })).title).toBe(
      "An agent asks before web_fetch",
    );
  });

  it("never carries the summary, which is drawn from the call's arguments", () => {
    const n = requestNotice(request("a", { summary: "token: s3cr3t" }));
    expect(`${n.title} ${n.body}`).not.toContain("s3cr3t");
  });

  it("tells of a burst as one", () => {
    expect(requestsNotice(4)).toEqual({
      title: "Agents ask before acting",
      body: "4 new requests wait for your answer.",
    });
  });

  it("counts the people waiting to be let in", () => {
    expect(sendersNotice(1).body).toBe("Someone wrote in on a channel and waits to be let in.");
    expect(sendersNotice(3).body).toBe("3 people wrote in on a channel and wait to be let in.");
  });
});
