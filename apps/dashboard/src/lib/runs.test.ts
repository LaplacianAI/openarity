import { describe, expect, it } from "vitest";

import { handedTo } from "./runs";

describe("handedTo", () => {
  it("says nothing for a run that handed no work on", () => {
    expect(handedTo({ delegates: [] })).toBeNull();
  });

  // A brain from before the field sends none: that is not "handed none", but
  // nothing can be said either way.
  it("says nothing when the brain did not say", () => {
    expect(handedTo({})).toBeNull();
  });

  it("names whom it handed work to, in the order it did", () => {
    expect(handedTo({ delegates: ["clerk"] })).toBe("→ clerk");
    expect(handedTo({ delegates: ["clerk", "billing", "auditor"] })).toBe(
      "→ clerk, billing, auditor",
    );
  });

  it("counts the rest past three names, and never cuts a name", () => {
    expect(handedTo({ delegates: ["a-1", "b-2", "c-3", "d-4", "e-5"] })).toBe(
      "→ a-1, b-2, c-3 and 2 more",
    );
  });
});
