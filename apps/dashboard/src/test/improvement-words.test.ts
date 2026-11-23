import { describe, expect, it } from "vitest";

import type { ImprovementWatch } from "@/api/types";
import { flagWords, watchHeadline } from "@/lib/improvements";

describe("no saving", () => {
  it.each([
    [["cost"], "did not get cheaper,"],
    [["time"], "did not get quicker,"],
    [["cost", "time"], "did not get both cheaper and quicker,"],
    [[], "did not get cheaper or quicker,"],
  ] as const)("aimed at %j, says %j", (saves, want) => {
    expect(flagWords("no_saving", [...saves])).toContain(want);
  });

  it("is said again once cleared", () => {
    const w = {
      status: "cleared",
      flag: "no_saving",
      needed: 10,
      before: { runs: 10, bad: 1 },
      after: { runs: 10, bad: 1 },
    } as unknown as ImprovementWatch;
    expect(watchHeadline(w, ["time"])).toBe(
      "Flagged (its good runs did not get quicker, as it expected), then cleared: 1 of 10 went wrong after, 1 of 10 before",
    );
  });
});
