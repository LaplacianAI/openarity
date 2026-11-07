import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";

import { ASK_LIMIT_BYTES, askProblem, askRefusal, NO_MODEL } from "./ask";

describe("askProblem", () => {
  it("has nothing to say about an ordinary question", () => {
    expect(askProblem("What failed last night?")).toBeNull();
  });

  // The brain counts bytes, not characters: 35,000 euro signs are 105,000 bytes.
  it("counts the limit in bytes of UTF-8, as the brain does", () => {
    expect(askProblem("€".repeat(35_000))).toMatch(/more than 100 KB/);
    expect(askProblem("a".repeat(ASK_LIMIT_BYTES))).toBeNull();
    expect(askProblem("a".repeat(ASK_LIMIT_BYTES + 1))).toMatch(/more than 100 KB/);
  });

  it("catches a NUL character, which the brain refuses", () => {
    expect(askProblem("before\u0000after")).toMatch(/can't be sent/);
  });
});

describe("askRefusal", () => {
  it("sends a team with no model to its settings, in the dashboard's words", () => {
    const r = askRefusal(
      new ApiError(
        409,
        "the team has no model set, so its orchestrator cannot run; set one in the team's settings",
      ),
    );
    expect(r).toEqual({ text: NO_MODEL, settings: true });
  });

  it("says a refusal of the permission without naming it", () => {
    const r = askRefusal(new ApiError(403, "forbidden"));
    expect(r.settings).toBe(false);
    expect(r.text).toBe("You can't start runs in this team. An admin can grant it.");
  });

  it("passes on the rule a 400 names", () => {
    expect(askRefusal(new ApiError(400, "input must be at most 100 KiB")).text).toBe(
      "The brain refused it: input must be at most 100 KiB.",
    );
  });

  it("says another refusal was one, without guessing why", () => {
    expect(askRefusal(new ApiError(404, "no agent in this team has that name")).text).toBe(
      "The brain refused it.",
    );
  });

  it("tells a brain that is down or unreachable apart from a refusal", () => {
    for (const err of [new ApiError(502, "bad gateway"), new TypeError("Failed to fetch")]) {
      expect(askRefusal(err).text).toBe(
        "Couldn't reach the brain, so nothing was asked. Try again.",
      );
    }
  });
});
