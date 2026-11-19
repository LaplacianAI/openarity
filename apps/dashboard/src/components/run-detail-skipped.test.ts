import { describe, expect, it } from "vitest";

import { skippedWords } from "./run-detail";

describe("a skipped step", () => {
  it("says what was skipped and that the run went on", () => {
    expect(skippedWords("critique")).toBe("Self-review skipped, the answer kept");
    expect(skippedWords("rewrite")).toBe("Rewrite skipped, the answer before it kept");
    expect(skippedWords("plan")).toBe("Plan skipped, answered without one");
  });

  it("names a step this build does not know without inventing one", () => {
    expect(skippedWords("search")).toBe("A step was skipped");
  });
});
