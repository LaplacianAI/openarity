import { describe, expect, it } from "vitest";

import type { Message, Reply, Run, RunStatus } from "@/api/types";

import { awaitsReply, conversation, noAnswer, replyLine } from "./conversation";

const T = (min: number) => new Date(Date.UTC(2026, 10, 1, 9, min)).toISOString();

function msg(id: string, min: number): Message {
  return { id, external_id: `x-${id}`, user_id: "u", text: id, received_at: T(min) };
}

function run(id: string, min: number, extra: Partial<Run> = {}): Run {
  return {
    id,
    session_id: "s",
    agent: "triage",
    status: "succeeded",
    input: id,
    output: null,
    error: null,
    input_tokens: 0,
    output_tokens: 0,
    created_at: T(min),
    started_at: null,
    finished_at: null,
    parent_run_id: null,
    depth: 0,
    hub: false,
    ...extra,
  };
}

const ids = (c: ReturnType<typeof conversation>) =>
  c.turns.map((t) => (t.kind === "message" ? t.message.id : t.run.id));

describe("conversation", () => {
  it("reads oldest first, people and runs in one order, from lists paged newest first", () => {
    const c = conversation(
      { items: [msg("m3", 30), msg("m1", 10)], more: false },
      { items: [run("r2", 20), run("r4", 40)], more: false },
    );
    expect(ids(c)).toEqual(["m1", "r2", "m3", "r4"]);
    expect(c.readFurther).toEqual({ messages: false, runs: false });
  });

  // A run started by a message lands in the same second as it.
  it("puts a message before a run at the same instant", () => {
    const c = conversation(
      { items: [msg("m", 10)], more: false },
      { items: [run("r", 10)], more: false },
    );
    expect(ids(c)).toEqual(["m", "r"]);
  });

  it("holds back what is older than the runs read so far, and reads the runs further", () => {
    const c = conversation(
      { items: [msg("m5", 50), msg("m1", 10)], more: false },
      { items: [run("r4", 40), run("r3", 30)], more: true },
    );
    // Unread runs may sit between m1 and r3: m1 waits for them.
    expect(ids(c)).toEqual(["r3", "r4", "m5"]);
    expect(c.readFurther).toEqual({ messages: false, runs: true });
  });

  it("reads further only the list that stops short, when both have more", () => {
    const c = conversation(
      { items: [msg("m5", 50), msg("m1", 10)], more: true },
      { items: [run("r4", 40), run("r3", 30)], more: true },
    );
    expect(ids(c)).toEqual(["r3", "r4", "m5"]);
    expect(c.readFurther).toEqual({ messages: false, runs: true });
  });

  it("reads both further when they stop at the same instant", () => {
    const c = conversation(
      { items: [msg("m", 30)], more: true },
      { items: [run("r", 30)], more: true },
    );
    expect(c.readFurther).toEqual({ messages: true, runs: true });
  });

  it("places nothing while a list with more has none read", () => {
    const c = conversation({ items: [], more: true }, { items: [run("r", 30)], more: false });
    expect(ids(c)).toEqual([]);
    expect(c.readFurther).toEqual({ messages: true, runs: false });
  });

  it("shows a list read whole on its own", () => {
    const c = conversation({ items: [msg("m", 30)], more: false }, { items: [], more: false });
    expect(ids(c)).toEqual(["m"]);
  });
});

describe("noAnswer", () => {
  const said = (status: RunStatus, extra: Partial<Run> = {}) =>
    noAnswer(run("r", 0, { status, ...extra }));

  it("says what stands in for an answer, for every status", () => {
    expect(said("queued")).toBe("Waiting to start.");
    expect(said("running")).toBe("Working on it…");
    expect(said("cancelled")).toBe("Cancelled before it answered.");
    expect(said("succeeded")).toBe("It finished without an answer.");
    expect(said("failed")).toBe("It failed before answering.");
  });

  it("says a failure in the dashboard's words, not as stored", () => {
    expect(said("failed", { error: "the run exceeded its token budget of 20000" })).toBe(
      "It ran out of its token budget.",
    );
  });
});

describe("replyLine", () => {
  const reply = (extra: Partial<Reply>): Reply => ({
    kind: "answer",
    status: "pending",
    attempts: 0,
    last_error: null,
    sent_at: null,
    next_attempt_at: null,
    ...extra,
  });

  it("says nothing for a run that answers no channel, or a brain that does not say", () => {
    expect(replyLine(null)).toBeNull();
    expect(replyLine(undefined)).toBeNull();
  });

  it("says a sent reply was sent, and when", () => {
    expect(replyLine(reply({ status: "sent", attempts: 1, sent_at: T(4) }))).toEqual({
      tone: "sent",
      text: "The answer was sent to the channel",
      when: { lead: " at ", iso: T(4) },
    });
  });

  // Not tried yet, or claimed and being posted: there is no failure to report.
  it("says a reply with no failure behind it is on its way", () => {
    expect(replyLine(reply({ attempts: 1 }))).toMatchObject({
      tone: "waiting",
      text: "Sending the answer to the channel…",
      when: null,
    });
  });

  it("says why a reply waiting to retry failed, and when it is tried again", () => {
    expect(
      replyLine(
        reply({ attempts: 2, last_error: "the reply URL answered 503", next_attempt_at: T(10) }),
      ),
    ).toEqual({
      tone: "waiting",
      text: "The answer has not reached the channel yet: the reply URL answered 503",
      when: { lead: ". Trying again at ", iso: T(10) },
    });
  });

  it("says a given-up reply was not sent, and why", () => {
    expect(
      replyLine(
        reply({
          status: "failed",
          attempts: 1,
          last_error: "custom: the channel has no reply URL",
        }),
      ),
    ).toMatchObject({
      tone: "failed",
      text: "The answer was not sent: custom: the channel has no reply URL",
    });
    expect(replyLine(reply({ status: "failed" }))?.text).toBe(
      "The answer was not sent: it was given up",
    );
  });

  it("names an apology as an apology", () => {
    expect(replyLine(reply({ kind: "apology", status: "sent", sent_at: T(4) }))?.text).toBe(
      "The apology was sent to the channel",
    );
  });

  it("keeps the page reading only while some reply is still on its way", () => {
    expect(awaitsReply([run("a", 1), run("b", 2, { reply: reply({ status: "sent" }) })])).toBe(
      false,
    );
    expect(awaitsReply([run("a", 1), run("b", 2, { reply: reply({}) })])).toBe(true);
  });
});
