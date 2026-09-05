import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// The mark in public/ is a copy, not the original: Vite's publicDir has to sit
// inside the app, so the repository's assets/favicon.svg cannot be referenced
// from there directly. A copy with nothing watching it is a copy that goes
// stale — the brand file gets a new colour and the tab keeps the old one, and
// nothing anywhere fails. These two tests are what fails.
const root = process.cwd();

describe("the favicon", () => {
  it("is the mark from assets/, byte for byte", () => {
    const shipped = readFileSync(join(root, "public/favicon.svg"), "utf8");
    const source = readFileSync(join(root, "../../assets/favicon.svg"), "utf8");

    expect(shipped).toBe(source);
  });

  it("is the one the page asks for", () => {
    const html = readFileSync(join(root, "index.html"), "utf8");

    // Written without the base prefix; Vite adds it. Asking for /ui/favicon.svg
    // here would be rewritten to /ui/ui/favicon.svg and quietly 404.
    expect(html).toContain('<link rel="icon" type="image/svg+xml" href="/favicon.svg" />');
  });
});
