import { describe, expect, it } from "vitest";

import { sessionName, UNNAMED } from "./sessions";

describe("sessionName", () => {
  it("is the provider's reference when there is one, whatever was asked", () => {
    expect(sessionName({ provider_ref: "ticket-42", first_asked: "Why?" })).toBe("ticket-42");
  });

  it("is what was first asked when there is no reference", () => {
    expect(sessionName({ provider_ref: "", first_asked: "Why did the export fail?" })).toBe(
      "Why did the export fail?",
    );
  });

  // A question is often pasted: a title is one line, never a block.
  it("puts the question on one line", () => {
    expect(
      sessionName({ provider_ref: "", first_asked: "  Why did\n\tthe export\r\n fail?  " }),
    ).toBe("Why did the export fail?");
  });

  it("treats a blank reference as none", () => {
    expect(sessionName({ provider_ref: "   ", first_asked: "Why?" })).toBe("Why?");
  });

  // Nothing asked yet, or a brain from before the field: still a name, never blank.
  it("says it is a direct session when there is nothing else to call it", () => {
    expect(sessionName({ provider_ref: "", first_asked: null })).toBe(UNNAMED);
    expect(sessionName({ provider_ref: "" })).toBe(UNNAMED);
    expect(sessionName({ provider_ref: "", first_asked: " \n " })).toBe(UNNAMED);
  });
});
