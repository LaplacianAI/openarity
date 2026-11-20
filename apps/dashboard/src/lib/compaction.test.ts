import { describe, expect, it } from "vitest";
import {
  checkCompaction,
  explain,
  firstProblem,
  grouped,
  isDefault,
  parseWhole,
  tidied,
} from "./compaction";

const ok = { messages: "100", least: "50,000", most: "60,000" };

describe("parseWhole", () => {
  it("reads a number however its thousands are grouped", () => {
    for (const raw of ["60000", "60,000", "60 000", " 60 000 ", "60 000"]) {
      expect(parseWhole(raw)).toBe(60_000);
    }
  });

  it("refuses what is not a whole number", () => {
    for (const raw of ["", "  ", "60k", "60.5", "-60000", "6e4", "sixty"]) {
      expect(parseWhole(raw)).toBeNull();
    }
  });
});

describe("tidied", () => {
  it("groups a number and leaves a mistake as typed", () => {
    expect(tidied("1000000")).toBe("1,000,000");
    expect(tidied("60k")).toBe("60k");
    expect(grouped(30_000)).toBe("30,000");
  });
});

describe("checkCompaction", () => {
  it("passes the defaults, the ends of each range and a fixed line", () => {
    expect(checkCompaction(ok)).toEqual({});
    expect(checkCompaction({ messages: "10", least: "30000", most: "1000000" })).toEqual({});
    expect(checkCompaction({ messages: "10000", least: "40000", most: "40000" })).toEqual({});
  });

  it("names each field outside its range", () => {
    expect(checkCompaction({ ...ok, messages: "9" }).messages).toMatch(/10 to 10,000/);
    expect(checkCompaction({ ...ok, least: "29,999" }).least).toMatch(/30,000 to 1,000,000/);
    expect(checkCompaction({ ...ok, most: "1,000,001" }).most).toMatch(/30,000 to 1,000,000/);
    expect(checkCompaction({ ...ok, most: "" }).most).toBeDefined();
  });

  it("says once, naming both, when at least is above at most", () => {
    const p = checkCompaction({ ...ok, least: "70000" });
    expect(p.pair).toBe(
      "At least, 70,000, is above at most, 60,000. Lower the first or raise the second.",
    );
    expect(p.least).toBeUndefined();
    expect(p.most).toBeUndefined();
  });

  it("does not compare a pair while either is out of range", () => {
    expect(checkCompaction({ ...ok, least: "2000000", most: "40000" }).pair).toBeUndefined();
  });
});

describe("firstProblem", () => {
  it("names the field the Save stopped on", () => {
    expect(firstProblem(checkCompaction({ ...ok, messages: "x" }))).toMatch(
      /^Messages before a run compacts: use/,
    );
    expect(firstProblem(checkCompaction({ ...ok, most: "1" }))).toMatch(/^At most: use/);
    expect(firstProblem({})).toBeNull();
  });
});

describe("explain", () => {
  it("says what the lines do", () => {
    expect(explain(ok)).toBe(
      "A run compacts at a tenth of its token budget, but never below 50,000 or above 60,000 tokens.",
    );
    expect(explain({ ...ok, least: "40000", most: "40,000" })).toBe(
      "Every run compacts at 40,000 tokens.",
    );
  });

  it("says nothing the brain would refuse", () => {
    expect(explain({ ...ok, least: "70000" })).toBeNull();
    expect(explain({ ...ok, most: "abc" })).toBeNull();
  });
});

describe("isDefault", () => {
  it("compares by value, not by how it is typed", () => {
    expect(isDefault({ messages: "100", least: "50000", most: "60 000" })).toBe(true);
    expect(isDefault({ ...ok, most: "60,001" })).toBe(false);
  });
});
