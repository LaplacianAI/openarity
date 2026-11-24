import { describe, expect, it } from "vitest";

import type { ImprovementReplay, ImprovementReplayRun } from "@/api/types";
import { replayCost, replayOutcome, replaySummary, replayWorse } from "@/lib/improvements";

/** The outcome the brain gives a done try, as its fixtures carry it. */
function outcomeOf(
  original: "good" | "bad",
  replayed: ImprovementReplayRun["replayed"],
): ImprovementReplayRun["outcome"] {
  if (replayed !== "good" && replayed !== "bad") return "unclear";
  if (original === "bad") return replayed === "good" ? "fixed" : "still_bad";
  return replayed === "good" ? "kept" : "broke";
}

function tried(
  original: "good" | "bad",
  replayed: ImprovementReplayRun["replayed"],
  extra: Partial<ImprovementReplayRun> = {},
): ImprovementReplayRun {
  return {
    run: { id: crypto.randomUUID(), readable: true },
    chosen: original === "bad" ? "cited" : "good",
    status: "done",
    original,
    replayed,
    outcome: extra.status && extra.status !== "done" ? null : outcomeOf(original, replayed),
    withheld: false,
    diverged: false,
    recorded: 1,
    live: 0,
    not_made: 0,
    before: { tokens: 18_400, steps: 9, work_ms: 41_000 },
    after: { tokens: 12_000, steps: 5, work_ms: 20_000 },
    ...extra,
  };
}

describe("a try", () => {
  it.each([
    [tried("bad", "good"), "Went wrong, now done", "good"],
    [tried("bad", "bad"), "Went wrong, still does", "neutral"],
    [tried("good", "good"), "Went well, still does", "neutral"],
    [tried("good", "bad"), "Went well, now goes wrong", "bad"],
    [tried("bad", "cant_tell"), "Could not tell", "neutral"],
    [tried("bad", null, { status: "skipped" }), "Not tried", "neutral"],
    [tried("bad", null, { status: "failed" }), "The try failed", "neutral"],
    [tried("bad", null, { status: "queued" }), "Still to try", "neutral"],
  ])("reads %#", (run, words, tone) => {
    expect(replayOutcome(run)).toEqual({ words, tone });
  });

  it("says its cost beside the run's", () => {
    expect(replayCost(tried("bad", "good"))).toBe(
      "18,400 → 12,000 tokens · 9 → 5 steps · 41 s → 20 s",
    );
    expect(replayCost(tried("bad", null, { status: "queued", after: null }))).toBeNull();
  });
});

describe("the tries", () => {
  const done: ImprovementReplay = {
    status: "done",
    runs: [
      tried("bad", "good"),
      tried("bad", "good"),
      tried("good", "good"),
      tried("good", "bad"),
      tried("bad", null, { status: "skipped" }),
    ],
  };

  it("are summed up by what each came to, counting only those tried", () => {
    expect(replaySummary(done)).toBe(
      "Tried on 4 past runs: 2 that went wrong now done · 1 that went well still do · 1 that went well now go wrong",
    );
  });

  it("are worse when a run that went well now goes wrong", () => {
    expect(replayWorse(done)).toBe(true);
    expect(
      replayWorse({ status: "done", runs: [tried("bad", "bad"), tried("good", "good")] }),
    ).toBe(false);
    expect(replayWorse(null)).toBe(false);
  });

  it.each([
    ["off", "Not tried on past runs"],
    ["trying", "Trying on past runs…"],
    ["none", "Not tried on any past run"],
  ] as const)("say %s", (status, words) => {
    expect(replaySummary({ status, runs: [] })).toMatch(words);
  });

  it("say how many are tried while some are still to run", () => {
    expect(
      replaySummary({
        status: "trying",
        runs: [
          tried("bad", "good"),
          tried("good", null, { status: "trying" }),
          tried("good", null, { status: "queued" }),
          tried("bad", null, { status: "skipped" }),
        ],
      }),
    ).toBe("Trying on past runs… 1 of 3 tried");
  });
});
