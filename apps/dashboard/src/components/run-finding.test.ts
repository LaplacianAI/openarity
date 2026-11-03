import { describe, expect, it } from "vitest";

import type { Run, RunEvent } from "@/api/types";
import { groupTrace } from "@/lib/run-trace";

import { runFinding } from "./run-detail";

const RUN = { id: "r1", agent: "helper", status: "succeeded" } as Run;

let seq = 0;
function e(kind: string, data: unknown): RunEvent {
  seq += 1;
  return { seq, kind, data };
}

function step(n: number, spent: number, ...rest: RunEvent[]): RunEvent[] {
  return [e("step", { step: n }), e("usage", { input_tokens: spent, output_tokens: 0 }), ...rest];
}

function call(id: string, name: string, error = ""): RunEvent[] {
  return [
    e("tool_call", { id, name, arguments: "{}" }),
    e("tool_result", { id, name, output: "", ...(error ? { error } : {}) }),
  ];
}

describe("runFinding", () => {
  it("names the step and the tool where a failed run failed", () => {
    const trace = groupTrace([
      ...step(1, 100, ...call("a", "search")),
      ...step(2, 100, ...call("b", "lookup", "lookup is down")),
      ...step(3, 100),
    ]);
    expect(runFinding({ ...RUN, status: "failed" }, trace)).toBe("Failed at step 2, in lookup.");
  });

  it("names the last step when no tool failed", () => {
    const trace = groupTrace([...step(1, 100), ...step(2, 100)]);
    expect(runFinding({ ...RUN, status: "failed" }, trace)).toBe("Failed at step 2.");
  });

  it("says a failed run never reached a step", () => {
    expect(runFinding({ ...RUN, status: "failed" }, groupTrace([]))).toBe(
      "Failed before its first step.",
    );
  });

  it("takes the stream's own ending over a row not yet refreshed", () => {
    const trace = groupTrace([...step(1, 10), e("finished", { status: "failed", error: "x" })]);
    expect(runFinding({ ...RUN, status: "running" }, trace)).toBe("Failed at step 1.");
  });

  it("says which step a succeeded run spent its tokens on", () => {
    const trace = groupTrace([...step(1, 100), ...step(2, 620), ...step(3, 280)]);
    expect(runFinding(RUN, trace)).toBe("Step 2 spent 62% of the tokens.");
  });

  it("says nothing a one-step run, or a running run would not show", () => {
    expect(runFinding(RUN, groupTrace(step(1, 100)))).toBeNull();
    expect(
      runFinding({ ...RUN, status: "running" }, groupTrace([...step(1, 9), ...step(2, 1)])),
    ).toBeNull();
    expect(runFinding(RUN, groupTrace([...step(1, 0), ...step(2, 0)]))).toBeNull();
  });
});
