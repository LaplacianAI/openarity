import { describe, expect, it } from "vitest";

import type { ImprovementChange } from "@/api/types";
import {
  anyPassRunning,
  fieldWords,
  isScript,
  kindWords,
  listNames,
  missingWrites,
  passOutcome,
  prunedWords,
  scopeWords,
  signatureWords,
  tabOf,
  tabsFor,
  waitingFinding,
  writesNeeded,
} from "./improvements";

const NOW = Date.parse("2026-11-20T00:00:00Z");
const ago = (days: number) => new Date(NOW - days * 86_400_000).toISOString();

describe("waitingFinding", () => {
  it("says when nothing waits", () => {
    expect(waitingFinding([], false, NOW)).toBe("No changes wait for approval.");
  });

  it("names the count and how long the oldest has waited", () => {
    expect(waitingFinding([{ created_at: ago(1) }, { created_at: ago(3) }], false, NOW)).toBe(
      "2 changes wait for approval; the oldest for 3 days.",
    );
  });

  it("reads one change in the singular", () => {
    expect(waitingFinding([{ created_at: ago(3) }], false, NOW)).toBe(
      "1 change waits for approval, for 3 days.",
    );
  });

  it("does not claim an oldest past the first page", () => {
    expect(waitingFinding([{ created_at: ago(1) }, { created_at: ago(2) }], true, NOW)).toBe(
      "2 changes or more wait for approval.",
    );
  });
});

describe("what deciding needs", () => {
  it("is the write the change would need by hand", () => {
    expect(writesNeeded("skill_edit")).toEqual(["skill:write"]);
    expect(writesNeeded("skill_new")).toEqual(["agent:write", "skill:write"]);
    expect(writesNeeded("instructions")).toEqual(["agent:write"]);
    expect(writesNeeded("tools_narrow")).toEqual(["agent:write"]);
  });

  it("names only what the caller lacks", () => {
    expect(missingWrites("skill_new", (p) => p === "agent:write")).toEqual(["skill:write"]);
    expect(missingWrites("skill_new", () => true)).toEqual([]);
  });
});

describe("tabs", () => {
  it("shows a member Applied only, and an approver all three", () => {
    expect(tabsFor(false)).toEqual(["applied"]);
    expect(tabsFor(true)).toEqual(["waiting", "applied", "closed", "passes"]);
  });

  it("falls back to the first tab the caller has", () => {
    expect(tabOf("waiting", false)).toBe("applied");
    expect(tabOf(undefined, true)).toBe("waiting");
    expect(tabOf("closed", true)).toBe("closed");
    expect(tabOf("passes", true)).toBe("passes");
    expect(tabOf("passes", false)).toBe("applied");
  });
});

describe("words", () => {
  it("speaks of a kind and a scope as a person does", () => {
    expect(kindWords("tools_narrow")).toBe("Tool limits");
    expect(kindWords("skill_new")).toBe("New skill");
    expect(kindWords("something_newer")).toBe("something newer");
    expect(scopeWords("invariant")).toBe("general");
    expect(scopeWords("domain")).toBe("domain");
  });

  it("knows a skill's script by its path", () => {
    expect(isScript("files/scripts/fetch.py")).toBe(true);
    expect(isScript("files/references/notes.md")).toBe(false);
    expect(isScript("body")).toBe(false);
  });

  it("finds what a list lost and gained, and treats no list as empty", () => {
    const change = (before: string[] | null, after: string[] | null): ImprovementChange => ({
      field: "skills",
      type: "list",
      before,
      after,
    });
    expect(listNames(change(["a", "b"], ["b", "c"]))).toEqual([
      { name: "a", change: "del" },
      { name: "b", change: "kept" },
      { name: "c", change: "add" },
    ]);
    expect(listNames(change(null, ["x"]))).toEqual([{ name: "x", change: "add" }]);
  });

  it("names a field in the agent page's words, and a skill's file by its path", () => {
    expect(fieldWords("mcp_servers")).toBe("MCP servers");
    expect(fieldWords("files/scripts/round.py")).toBe("scripts/round.py");
    expect(fieldWords("something_new")).toBe("something new");
  });
});

describe("passes", () => {
  it("reads a signature as words, with the target apart", () => {
    expect(signatureWords("instructions::skipped_step")).toEqual({
      part: "instructions",
      target: "",
      cause: "skipped step",
    });
    expect(signatureWords("tool:crm__close_ticket:misused_tool")).toEqual({
      part: "tool",
      target: "crm__close_ticket",
      cause: "misused tool",
    });
    expect(signatureWords("tool:crm:lookup:wrong_arg")).toEqual({
      part: "tool",
      target: "crm:lookup",
      cause: "wrong arg",
    });
  });

  it("knows when a pass is still running", () => {
    expect(anyPassRunning(undefined)).toBe(false);
    expect(
      anyPassRunning([{ items: [{ status: "done" }] }, { items: [{ status: "failed" }] }]),
    ).toBe(false);
    expect(
      anyPassRunning([{ items: [{ status: "done" }] }, { items: [{ status: "running" }] }]),
    ).toBe(true);
  });

  it("says what was pruned", () => {
    expect(prunedWords("skill:triage")).toBe("skill triage");
    expect(prunedWords("tool:crm/close")).toBe("tool crm/close");
    expect(prunedWords("odd")).toBe("odd");
  });

  it.each([
    [{ status: "running", proposed: 0, screened_out: 0, error: null }, "Running…"],
    [
      { status: "failed", proposed: 0, screened_out: 0, error: "model timed out" },
      "Failed: model timed out",
    ],
    [{ status: "failed", proposed: 0, screened_out: 0, error: null }, "Failed"],
    [{ status: "done", proposed: 0, screened_out: 0, error: null }, "Nothing to change"],
    [{ status: "done", proposed: 1, screened_out: 0, error: null }, "1 proposal"],
    [{ status: "done", proposed: 2, screened_out: 0, error: null }, "2 proposals"],
    [
      { status: "done", proposed: 1, screened_out: 1, error: null },
      "1 proposal, 1 held back by the screen",
    ],
    [
      { status: "done", proposed: 0, screened_out: 3, error: null },
      "Nothing to change, 3 held back by the screen",
    ],
    [
      { status: "done", proposed: 0, screened_out: 0, error: null, cut_short: true },
      "Cut short before it proposed anything",
    ],
    [
      { status: "done", proposed: 0, screened_out: 1, error: null, cut_short: true },
      "Cut short with none kept, 1 held back by the screen",
    ],
    [
      { status: "done", proposed: 2, screened_out: 1, error: null, cut_short: true },
      "2 proposals, then cut short, 1 held back by the screen",
    ],
  ] as const)("says what came of %o", (pass, said) => {
    expect(passOutcome({ cut_short: false, ...pass })).toBe(said);
  });
});
