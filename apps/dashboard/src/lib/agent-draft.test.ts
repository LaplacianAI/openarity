import { describe, expect, it } from "vitest";
import { webWords } from "./agent-draft";

describe("what an agent reaches on the web, in one cell", () => {
  it("names search, the browser, both, or neither", () => {
    expect(webWords({ web: true, browser: true })).toBe("Search and browser");
    expect(webWords({ web: true, browser: false })).toBe("Search");
    expect(webWords({ web: false, browser: true })).toBe("Browser");
    expect(webWords({ web: false, browser: false })).toBe("Off");
  });

  it("reads a brain that predates browsers as having none", () => {
    expect(webWords({ web: true })).toBe("Search");
  });
});
