import { describe, expect, it } from "vitest";

import type { RunEvent } from "@/api/types";
import { argumentPairs, graphHits, groupTrace, pretty, type Step, stepsOf } from "./run-trace";

let seq = 0;
function e(kind: string, data: unknown = {}): RunEvent {
  seq += 1;
  return { seq, kind, data };
}

function onlyStep(events: RunEvent[], n = 0): Step {
  const s = stepsOf(groupTrace(events))[n];
  if (!s) throw new Error(`no step ${n}`);
  return s;
}

describe("groupTrace", () => {
  it("is empty for no events", () => {
    expect(groupTrace([])).toEqual({
      started: null,
      items: [],
      finished: null,
      ended: null,
      usage: { input: 0, output: 0, cached: 0, total: 0 },
    });
  });

  it("reads the run's start, and keeps a field of the wrong type out", () => {
    const t = groupTrace([
      e("started", { agent: "a", model: "m", pattern: "react", tools: ["x", 3], skills: "nope" }),
    ]);
    expect(t.started).toEqual({
      agent: "a",
      model: "m",
      pattern: "react",
      tools: ["x", "3"],
      skills: [],
    });
    expect(t.items).toEqual([]);
  });

  it("puts each call and its result in the step that was current", () => {
    const t = groupTrace([
      e("step", { step: 1 }),
      e("tool_call", { id: "a", name: "search", arguments: '{"q":1}' }),
      e("tool_result", { id: "a", name: "search", output: "hit", duration_ms: 7 }),
      e("step", { step: 2 }),
      e("tool_call", { id: "b", name: "graph", arguments: "{}" }),
      e("tool_result", { id: "b", name: "graph", output: "", error: "down", truncated: true }),
    ]);
    const [one, two] = stepsOf(t);
    expect(one?.number).toBe(1);
    expect(one?.calls).toEqual([
      {
        id: "a",
        name: "search",
        arguments: '{"q":1}',
        truncated: false,
        result: { output: "hit", error: "", truncated: false, durationMs: 7 },
      },
    ]);
    expect(two?.calls[0]?.result).toEqual({
      output: "",
      error: "down",
      truncated: true,
      durationMs: null,
    });
  });

  it("pairs results with their calls by id, whatever order they come back in", () => {
    const s = onlyStep([
      e("step", { step: 1 }),
      e("tool_call", { id: "a", name: "search", arguments: "1" }),
      e("tool_call", { id: "b", name: "search", arguments: "2" }),
      e("tool_result", { id: "b", name: "search", output: "for 2" }),
      e("tool_result", { id: "a", name: "search", output: "for 1" }),
    ]);
    expect(s.calls.map((c) => [c.arguments, c.result?.output])).toEqual([
      ["1", "for 1"],
      ["2", "for 2"],
    ]);
  });

  it("pairs a result with a call made in an earlier step", () => {
    const t = groupTrace([
      e("step", { step: 1 }),
      e("tool_call", { id: "a", name: "slow", arguments: "{}" }),
      e("step", { step: 2 }),
      e("tool_result", { id: "a", name: "slow", output: "late" }),
    ]);
    const [one, two] = stepsOf(t);
    expect(one?.calls[0]?.result?.output).toBe("late");
    expect(two?.calls).toEqual([]);
  });

  it("pairs by order when the pattern sends no ids, preferring the same tool", () => {
    const s = onlyStep([
      e("step", { step: 1 }),
      e("tool_call", { name: "a", arguments: "first a" }),
      e("tool_call", { name: "b", arguments: "first b" }),
      e("tool_call", { name: "a", arguments: "second a" }),
      e("tool_result", { name: "b", output: "B" }),
      e("tool_result", { name: "a", output: "A1" }),
      e("tool_result", { name: "a", output: "A2" }),
    ]);
    expect(s.calls.map((c) => [c.arguments, c.result?.output])).toEqual([
      ["first a", "A1"],
      ["first b", "B"],
      ["second a", "A2"],
    ]);
  });

  it("pairs an unnamed result with the oldest call waiting when no name matches", () => {
    const s = onlyStep([
      e("step", { step: 1 }),
      e("tool_call", { name: "a", arguments: "1" }),
      e("tool_call", { name: "b", arguments: "2" }),
      e("tool_result", { name: "", output: "x" }),
    ]);
    expect(s.calls.map((c) => c.result?.output ?? null)).toEqual(["x", null]);
  });

  it("does not pair a result whose id matches no call with one that has an id", () => {
    const s = onlyStep([
      e("step", { step: 1 }),
      e("tool_call", { id: "a", name: "search", arguments: "{}" }),
      e("tool_result", { id: "zzz", name: "search", output: "stray" }),
    ]);
    expect(s.calls).toHaveLength(2);
    expect(s.calls[0]?.result).toBeNull();
    expect(s.calls[1]).toMatchObject({ id: "zzz", arguments: null, result: { output: "stray" } });
  });

  it("leaves a call whose result has not arrived waiting", () => {
    const s = onlyStep([e("step", { step: 1 }), e("tool_call", { id: "a", name: "search" })]);
    expect(s.calls[0]?.result).toBeNull();
  });

  it("groups the same way however the events arrive", () => {
    const all = [
      e("started", { agent: "a", model: "m", tools: [], skills: [] }),
      e("step", { step: 1 }),
      e("usage", { input_tokens: 10, output_tokens: 5 }),
      e("tool_call", { id: "a", name: "search", arguments: "{}" }),
      e("tool_result", { id: "a", name: "search", output: "ok" }),
      e("step", { step: 2 }),
      e("usage", { input_tokens: 20, output_tokens: 1 }),
      e("finished", { status: "succeeded" }),
      { seq: null, kind: "end", data: { status: "succeeded" } },
    ];
    const whole = groupTrace(all);

    // As a live trace sees it: each prefix, one event more each time.
    const seen = all.map((_, i) => groupTrace(all.slice(0, i + 1)));
    expect(seen.at(-1)).toEqual(whole);
    expect(stepsOf(seen[3] as never)[0]?.calls[0]?.result).toBeNull();
    expect(stepsOf(seen[4] as never)[0]?.calls[0]?.result?.output).toBe("ok");
    expect(seen.map((t) => stepsOf(t).length)).toEqual([0, 1, 1, 1, 1, 2, 2, 2, 2]);
    expect(seen.map((t) => t.usage.total)).toEqual([0, 0, 15, 15, 15, 15, 36, 36, 36]);
    expect(seen[7]?.ended).toBeNull();
    expect(whole.ended).toEqual({ status: "succeeded" });
  });

  it("sums usage per step and for the run, and names each model once", () => {
    const t = groupTrace([
      e("step", { step: 1 }),
      e("usage", { model: "m1", input_tokens: 400, output_tokens: 65, cached_input_tokens: 100 }),
      e("usage", { model: "m1", input_tokens: 10, output_tokens: 5 }),
      e("usage", { model: "m2", input_tokens: "lots", output_tokens: null }),
      e("step", { step: 2 }),
      e("usage", { model: "m1", input_tokens: 1, output_tokens: 2, cached_input_tokens: 1 }),
    ]);
    const [one, two] = stepsOf(t);
    expect(one?.usage).toEqual({ input: 410, output: 70, cached: 100, total: 480 });
    expect(one?.models).toEqual(["m1", "m2"]);
    expect(two?.usage).toEqual({ input: 1, output: 2, cached: 1, total: 3 });
    expect(t.usage).toEqual({ input: 411, output: 72, cached: 101, total: 483 });
  });

  it("gives usage and calls that came before any step a step of their own", () => {
    const t = groupTrace([
      e("usage", { input_tokens: 3, output_tokens: 4 }),
      e("tool_call", { id: "a", name: "plan" }),
      e("step", { step: 1 }),
    ]);
    const [first, second] = stepsOf(t);
    expect(first?.number).toBeNull();
    expect(first?.usage.total).toBe(7);
    expect(first?.calls).toHaveLength(1);
    expect(second?.number).toBe(1);
  });

  it("keeps run-level events in order between the steps", () => {
    const t = groupTrace([
      e("started", {}),
      e("tool_unavailable", { name: "slack", reason: "unreachable" }),
      e("step", { step: 1 }),
      e("resumed", { messages: 4 }),
      e("usage", { input_tokens: 1, output_tokens: 1 }),
      e("finished", { status: "failed", error: "budget" }),
    ]);
    expect(t.items.map((i) => i.type)).toEqual(["unavailable", "step", "resumed", "step"]);
    expect(t.items[0]).toEqual({ type: "unavailable", name: "slack", reason: "unreachable" });
    expect(t.items[2]).toEqual({ type: "resumed", messages: 4 });
    // Usage after a resume belongs to no step from before it.
    expect(stepsOf(t)[0]?.usage.total).toBe(0);
    expect(stepsOf(t)[1]?.usage.total).toBe(2);
    expect(t.finished).toEqual({ status: "failed", error: "budget" });
  });

  it("keeps events of a kind it does not know, in the step or the run", () => {
    const before = e("novel", { a: 1 });
    const during = e("thought", "thinking");
    const t = groupTrace([before, e("step", { step: 1 }), during]);
    expect(t.items[0]).toEqual({ type: "other", event: before });
    expect(stepsOf(t)[0]?.other).toEqual([during]);
  });

  it("reads data that is not an object as empty", () => {
    const t = groupTrace([
      e("step", null),
      e("tool_call", ["x"]),
      e("tool_result", "text"),
      e("finished", 3),
    ]);
    expect(stepsOf(t)[0]?.number).toBeNull();
    expect(stepsOf(t)[0]?.calls[0]).toMatchObject({ id: null, name: "", arguments: "" });
    expect(stepsOf(t)[0]?.calls[0]?.result).toMatchObject({ output: "" });
    expect(t.finished).toEqual({ status: "", error: "" });
  });
});

describe("graphHits", () => {
  it("reads ranked matches and the walk that led to each", () => {
    const out = JSON.stringify({
      results: [
        {
          kind: "skill",
          name: "s",
          description: "d",
          score: 0.9,
          path: [{ kind: "skill", name: "s" }],
        },
        {
          kind: "agent",
          name: "a",
          description: "",
          score: 0.5,
          path: [
            { kind: "agent", name: "a" },
            { kind: "skill", name: "s" },
          ],
        },
      ],
    });
    expect(graphHits(out)).toEqual([
      { kind: "skill", name: "s", description: "d", score: 0.9, via: [] },
      {
        kind: "agent",
        name: "a",
        description: "",
        score: 0.5,
        via: [{ kind: "skill", name: "s" }],
      },
    ]);
  });

  it("reads no matches as none, and anything else as not the graph's output", () => {
    expect(graphHits('{"results":[]}')).toEqual([]);
    expect(graphHits('{"results":"x"}')).toBeNull();
    expect(graphHits("[1]")).toBeNull();
    expect(graphHits('{"results":[{"na')).toBeNull();
  });

  it("survives a match missing its fields", () => {
    expect(graphHits('{"results":[null]}')).toEqual([
      { kind: "", name: "", description: "", score: 0, via: [] },
    ]);
  });
});

describe("argumentPairs", () => {
  it("reads an object's fields as text", () => {
    expect(argumentPairs('{"q":"a","n":3,"o":{"x":true}}')).toEqual([
      ["q", "a"],
      ["n", "3"],
      ["o", '{"x":true}'],
    ]);
  });

  it("is null for anything that is not a JSON object", () => {
    expect(argumentPairs("[1,2]")).toBeNull();
    expect(argumentPairs("null")).toBeNull();
    expect(argumentPairs('{"cut')).toBeNull();
  });
});

describe("pretty", () => {
  it("indents JSON and leaves anything else as it was", () => {
    expect(pretty('{"a":[1]}')).toBe('{\n  "a": [\n    1\n  ]\n}');
    expect(pretty("plain")).toBe("plain");
  });
});
