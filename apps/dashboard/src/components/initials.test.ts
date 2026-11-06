import { describe, expect, it } from "vitest";

import { initials } from "./ui-bits";

describe("initials", () => {
  it("takes the first letter of the first two words", () => {
    expect(initials("ines moreau")).toBe("IM");
    expect(initials("Ana Lucia Ferreira")).toBe("AL");
  });

  it("skips punctuation a name carries, rather than showing it", () => {
    expect(initials("Priya (contractor)")).toBe("PC");
    expect(initials("“Sam” O'Neil")).toBe("SO");
  });

  it("splits an email address and a handle into their parts", () => {
    expect(initials("tom.baker@example.com")).toBe("TB");
    // The domain is everyone's, not a second name.
    expect(initials("ines@halden.co")).toBe("I");
    expect(initials("@ines")).toBe("I");
    expect(initials("alerts-bot")).toBe("AB");
    expect(initials("U0991")).toBe("U");
  });

  it("keeps a letter outside the Latin alphabet whole", () => {
    expect(initials("李雷")).toBe("李");
    expect(initials("élodie roux")).toBe("ÉR");
    expect(initials("𝒜da lovelace")).toBe("𝒜L");
  });

  it("is empty for a name with no letters at all", () => {
    expect(initials("(—)")).toBe("");
    expect(initials("")).toBe("");
  });
});
