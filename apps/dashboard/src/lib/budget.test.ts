import { describe, expect, it } from "vitest";
import { budgetOf, shortTokens } from "@/lib/budget";

describe("budgetOf", () => {
  it("is what was spent of the budget, in whole percent", () => {
    expect(budgetOf({ spent: 573_529, token_budget: 2_000_000 })).toEqual({
      spent: 573_529,
      budget: 2_000_000,
      percent: 29,
      state: "ok",
    });
  });

  it("is nearly spent from nine tenths, and spent at the budget", () => {
    expect(budgetOf({ spent: 1_799_999, token_budget: 2_000_000 })?.state).toBe("ok");
    expect(budgetOf({ spent: 1_800_000, token_budget: 2_000_000 })?.state).toBe("near");
    expect(budgetOf({ spent: 1_999_999, token_budget: 2_000_000 })?.state).toBe("near");
    expect(budgetOf({ spent: 2_000_000, token_budget: 2_000_000 })?.state).toBe("spent");
    expect(budgetOf({ spent: 1_995_000, token_budget: 2_000_000 })).toMatchObject({
      state: "near",
      percent: 99,
    });
    expect(budgetOf({ spent: 2_020_000, token_budget: 2_000_000 })).toMatchObject({
      state: "spent",
      percent: 101,
    });
  });

  it("is nothing from a brain that sends no budget", () => {
    expect(budgetOf({})).toBeNull();
    expect(budgetOf({ spent: 5 })).toBeNull();
    expect(budgetOf({ spent: 5, token_budget: 0 })).toBeNull();
  });
});

describe("shortTokens", () => {
  it("shortens to k and M", () => {
    expect(
      [950, 1000, 573_529, 999_499, 1_000_000, 1_854_210, 2_000_000, 12_345_678].map(shortTokens),
    ).toEqual(["950", "1k", "574k", "999k", "1M", "1.85M", "2M", "12.3M"]);
  });
});
