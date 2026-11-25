import { describe, expect, it } from "vitest";

import type { SessionEnvelope, SessionEvent } from "@/api/types";

import { type Conversation, empty, fold, saying, stepOf, worked } from "./session-stream";

const at = "2026-11-13T10:00:00Z";
let n = 0;
/** Frames numbered in the order written, as the brain numbers them. */
function frames(...events: SessionEvent[]): SessionEnvelope[] {
  return events.map((e) => ({ ...e, seq: ++n }) as SessionEnvelope);
}
const all = (envs: SessionEnvelope[], from: Conversation = empty) => envs.reduce(fold, from);

const said = (id: string, text: string, pending = true): SessionEvent => ({
  kind: "message",
  data: {
    at,
    id,
    user_id: "u1",
    parts: [{ type: "text", text }],
    ...(pending ? { state: "pending" as const } : {}),
  },
});
const started = (run: string, agent = "hub", parent?: string): SessionEvent => ({
  kind: "run_started",
  data: { at, run_id: run, agent, ...(parent ? { parent_run_id: parent } : {}) },
});
const step = (run: string, s: number): SessionEvent => ({
  kind: "step_started",
  data: { at, run_id: run, step: s },
});
const text = (run: string, delta: string, s?: number): SessionEvent => ({
  kind: "text_message_chunk",
  data: { at, run_id: run, delta, ...(s ? { message_id: `${run}:${s}:text` } : {}) },
});
const thought = (run: string, delta: string, s?: number): SessionEvent => ({
  kind: "reasoning_message_chunk",
  data: { at, run_id: run, delta, ...(s ? { message_id: `${run}:${s}:reasoning` } : {}) },
});
const consumed = (run: string, ...ids: string[]): SessionEvent => ({
  kind: "message_consumed",
  data: { at, run_id: run, message_ids: ids },
});
const finished = (run: string, output: string, status = "succeeded" as const): SessionEvent => ({
  kind: "run_finished",
  data: { at: "2026-11-13T10:00:18Z", run_id: run, status, output },
});

describe("fold", () => {
  it("keeps the files sent with a message, in their order, apart from its text", () => {
    const pdf = { id: "f1", filename: "report.pdf", media_type: "application/pdf", size: 900 };
    const png = { id: "f2", filename: "chart.png", media_type: "image/png", size: 40 };
    const c = all(
      frames({
        kind: "message",
        data: {
          at,
          id: "m1",
          user_id: "u1",
          parts: [
            { type: "text", text: "summarise these" },
            { type: "file", file: pdf },
            { type: "file", file: png },
          ],
        },
      }),
    );
    expect(c.said.m1).toMatchObject({ text: "summarise these", files: [pdf, png] });
  });

  it("reads a message of files alone as one with no text", () => {
    const f = { id: "f1", filename: "a.txt", media_type: "text/plain", size: 3 };
    const c = all(
      frames({
        kind: "message",
        data: { at, id: "m1", user_id: "u1", parts: [{ type: "file", file: f }] },
      }),
    );
    expect(c.said.m1).toMatchObject({ text: "", files: [f] });
  });

  it("names no files for a message of text", () => {
    expect(all(frames(said("m1", "hi"))).said.m1?.files).toEqual([]);
  });

  it("reads a whole exchange: asked, taken, thought, called, answered", () => {
    const c = all(
      frames(
        said("m1", "why did it fail?"),
        started("r1"),
        consumed("r1", "m1"),
        step("r1", 1),
        thought("r1", "Need ", 1),
        thought("r1", "the logs.", 1),
        {
          kind: "tool_call_chunk",
          data: { at, run_id: "r1", tool_call_id: "c1", name: "read_logs", arguments: "{}" },
        },
        {
          kind: "tool_call_result",
          data: {
            at,
            run_id: "r1",
            tool_call_id: "c1",
            name: "read_logs",
            content: "timeout",
            duration_ms: 40,
          },
        },
        step("r1", 2),
        text("r1", "It timed ", 2),
        text("r1", "out.", 2),
        finished("r1", "It timed out."),
      ),
    );
    expect(c.order).toEqual([
      { kind: "said", id: "m1" },
      { kind: "run", id: "r1" },
    ]);
    expect(c.said.m1).toMatchObject({ text: "why did it fail?", pending: false, readBy: "r1" });
    const r = c.runs.r1;
    expect(r?.status).toBe("succeeded");
    expect(r?.output).toBe("It timed out.");
    expect(r?.steps.map((s) => [s.step, s.reasoning, s.text])).toEqual([
      [1, "Need the logs.", ""],
      [2, "", "It timed out."],
    ]);
    expect(r?.steps[0]?.tools).toEqual([
      { id: "c1", name: "read_logs", args: "{}", done: true, result: "timeout", ms: 40 },
    ]);
    // The message that started the run is not news inside it.
    expect(r?.steps.flatMap((s) => s.read)).toEqual([]);
  });

  it("shows nothing twice when a stream is followed again from an earlier frame", () => {
    const envs = frames(
      said("m1", "hi"),
      started("r1"),
      step("r1", 1),
      text("r1", "Hel", 1),
      text("r1", "lo", 1),
    );
    const once = all(envs);
    expect(all(envs, once)).toBe(once);
    expect(once.runs.r1?.steps[0]?.text).toBe("Hello");
  });

  it("puts a run handed work inside its parent, not in the conversation", () => {
    const c = all(
      frames(started("r1"), step("r1", 1), started("r2", "billing", "r1"), finished("r2", "paid")),
    );
    expect(c.order).toEqual([{ kind: "run", id: "r1" }]);
    expect(c.runs.r1?.children).toEqual(["r2"]);
    expect(worked(c, c.runs.r1 as never, Date.parse(at)).handed).toEqual(["billing"]);
  });

  it("lets a child whose parent it never saw stand on its own", () => {
    const c = all(frames(started("r2", "billing", "gone")));
    expect(c.order).toEqual([{ kind: "run", id: "r2" }]);
  });

  it("records a message read while the run worked at the step that read it", () => {
    const c = all(
      frames(
        started("r1"),
        step("r1", 1),
        said("m2", "also staging"),
        step("r1", 2),
        consumed("r1", "m2"),
      ),
    );
    expect(c.runs.r1?.steps.map((s) => s.read)).toEqual([[], ["m2"]]);
    expect(c.said.m2).toMatchObject({ pending: false, readBy: "r1" });
    expect(worked(c, c.runs.r1 as never, Date.parse(at)).read).toBe(1);
  });

  it("places writing with no message id on the step it follows", () => {
    // Recorded before writing kept its step.
    const c = all(
      frames(started("r1"), step("r1", 1), thought("r1", "hmm"), step("r1", 2), text("r1", "done")),
    );
    expect(c.runs.r1?.steps.map((s) => [s.reasoning, s.text])).toEqual([
      ["hmm", ""],
      ["", "done"],
    ]);
  });

  it("places writing by its message id even when its step was not seen", () => {
    const c = all(frames(started("r1"), text("r1", "late", 3)));
    expect(c.runs.r1?.steps.map((s) => [s.step, s.text])).toEqual([[3, "late"]]);
  });

  it("ignores events for a run it never saw start, and kinds it does not know", () => {
    const c = all(frames(text("ghost", "boo", 1), { kind: "other", name: "usage", data: {} }));
    expect(c.runs).toEqual({});
    expect(c.order).toEqual([]);
    expect(c.seen).toBe(n);
  });

  it("keeps a failure's error", () => {
    const c = all(
      frames(started("r1"), {
        kind: "run_finished",
        data: { at, run_id: "r1", status: "failed", error: "the model refused" },
      }),
    );
    expect(c.runs.r1).toMatchObject({ status: "failed", error: "the model refused", output: "" });
  });

  it("does not take a run twice", () => {
    const c = all(frames(started("r1"), step("r1", 1), started("r1", "other")));
    expect(c.runs.r1?.agent).toBe("hub");
    expect(c.order).toHaveLength(1);
  });

  it("does not repeat a step it already has", () => {
    const c = all(frames(started("r1"), step("r1", 1), step("r1", 1)));
    expect(c.runs.r1?.steps).toHaveLength(1);
  });

  it("does not mark a message read that it never saw", () => {
    const c = all(frames(consumed("r9", "m9")));
    expect(c.said).toEqual({});
  });

  it("keeps the files a run sent, in order, each call once", () => {
    const report = { id: "f1", filename: "report.pdf", media_type: "application/pdf", size: 9 };
    const chart = { id: "f2", filename: "chart.png", media_type: "image/png", size: 30 };
    const sent = (call: string, file: typeof report, caption?: string): SessionEvent => ({
      kind: "file_sent",
      data: { at, run_id: "r1", tool_call_id: call, file, ...(caption ? { caption } : {}) },
    });
    const c = all(
      frames(
        started("r1"),
        sent("c1", report, "Q3"),
        sent("c2", chart),
        sent("c1", report, "Q3"),
        finished("r1", "Here."),
      ),
    );
    expect(c.runs.r1?.files).toEqual([
      { toolCallId: "c1", file: report, caption: "Q3" },
      { toolCallId: "c2", file: chart, caption: "" },
    ]);
    expect(all(frames(sent("c9", report))).runs).toEqual({});
  });
});

describe("stepOf", () => {
  it("reads the step from a message id", () => {
    expect(stepOf("0192:2:text")).toBe(2);
    expect(stepOf(undefined)).toBeNull();
    expect(stepOf("nonsense")).toBeNull();
    expect(stepOf("r:0:text")).toBeNull();
  });
});

describe("saying", () => {
  it("is the latest text written, the one being written now", () => {
    const c = all(frames(started("r1"), step("r1", 1), text("r1", "first", 1), step("r1", 2)));
    expect(saying(c.runs.r1 as never)).toBe("first");
    expect(saying(all(frames(text("r1", "second", 2)), c).runs.r1 as never)).toBe("second");
  });
});

describe("worked", () => {
  it("counts steps and tools, and how long the run took", () => {
    const c = all(frames(started("r1"), step("r1", 1), step("r1", 2), finished("r1", "x")));
    expect(worked(c, c.runs.r1 as never, 0)).toMatchObject({ steps: 2, tools: 0, ms: 18000 });
  });

  it("has no time for a run whose clock it cannot read", () => {
    const c = all(
      frames({ kind: "run_started", data: { at: "nope", run_id: "r1", agent: "hub" } }),
    );
    expect(worked(c, c.runs.r1 as never, 0).ms).toBeNull();
  });
});

const asked = (run: string, id = "a1"): SessionEvent => ({
  kind: "approval_requested",
  data: {
    at,
    run_id: run,
    approval_id: id,
    tool_call_id: "c1",
    tool: "github__create_issue",
    class: "write",
    summary: "title: Bug",
    code: "K7QX",
    expires_at: "2026-11-13T11:00:00Z",
  },
});
const waits = (run: string, said = "I want to open an issue."): SessionEvent => ({
  kind: "run_waiting",
  data: { at, run_id: run, reason: "approval", said },
});
const decided = (
  id: string,
  decision: "approved" | "denied" | "expired" | "cancelled",
  extra: Record<string, unknown> = {},
): SessionEvent =>
  ({
    kind: "approval_decided",
    data: { at, run_id: "r1", approval_id: id, decision, ...extra },
  }) as SessionEvent;

describe("fold: approvals", () => {
  it("shows a request the run made, and the run waiting with its own words", () => {
    const c = all(frames(started("r1"), step("r1", 1), asked("r1"), waits("r1")));
    expect(c.requests.a1).toMatchObject({
      tool: "github__create_issue",
      class: "write",
      code: "K7QX",
      status: "pending",
      superseded: false,
    });
    expect(c.runs.r1).toMatchObject({
      status: "waiting",
      requests: ["a1"],
      waiting: { reason: "approval", said: "I want to open an issue." },
    });
  });

  it("settles a request when it is answered, saying who and where", () => {
    const c = all(
      frames(
        started("r1"),
        asked("r1"),
        waits("r1"),
        decided("a1", "approved", { by: "u2", via: "dashboard", scope: "run" }),
      ),
    );
    expect(c.requests.a1).toMatchObject({
      status: "approved",
      by: "u2",
      via: "dashboard",
      scope: "run",
    });
  });

  it("marks a request set aside because the person wrote again", () => {
    const c = all(
      frames(started("r1"), asked("r1"), decided("a1", "cancelled", { superseded: true })),
    );
    expect(c.requests.a1).toMatchObject({ status: "cancelled", superseded: true });
  });

  it("ignores an answer to a request it never saw", () => {
    const c = all(frames(started("r1"), decided("nope", "approved")));
    expect(c.requests).toEqual({});
  });

  it("does not show a request twice when a frame is read again", () => {
    const envs = frames(started("r1"), asked("r1"), waits("r1"));
    const once = all(envs);
    expect(all(envs, once)).toBe(once);
    expect(all(frames(asked("r1")), once).runs.r1?.requests).toEqual(["a1"]);
  });

  it("runs again once its next step starts, and the wait is over", () => {
    const c = all(frames(started("r1"), asked("r1"), waits("r1"), step("r1", 2)));
    expect(c.runs.r1).toMatchObject({ status: "running", waiting: null });
  });

  it("ends a waiting run that is cancelled", () => {
    const c = all(frames(started("r1"), waits("r1"), finished("r1", "", "cancelled" as never)));
    expect(c.runs.r1).toMatchObject({ status: "cancelled", waiting: null });
  });
});
