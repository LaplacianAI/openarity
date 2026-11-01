import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

// BRAND.md is the palette's source and this app shares no code with the site
// or the CLI, so the colours are copied into styles.css. A copy nothing checks
// drifts — the dashboard's first palette was its own and nobody noticed — so
// these tests read the table in BRAND.md and fail when a copy disagrees.
const root = process.cwd();
const brand = readFileSync(join(root, "../../BRAND.md"), "utf8");
const css = readFileSync(join(root, "src/styles.css"), "utf8");

// |`accent`|`#5FD3BC`|`#0B6A5C`|the mark, …|  →  role, dark, light
function palette(): { role: string; dark: string; light: string }[] {
  const rows = [...brand.matchAll(/^\|`([a-z]+)`\|`(#[0-9A-F]{6})`\|`(#[0-9A-F]{6})`\|/gm)];
  return rows.map(([, role = "", dark = "", light = ""]) => ({ role, dark, light }));
}

function block(selector: ":root" | ".dark"): string {
  const start = css.indexOf(`\n${selector} {`);
  expect(start, `${selector} block in styles.css`).toBeGreaterThan(-1);
  return css.slice(start, css.indexOf("\n}", start));
}

function value(body: string, name: string): string | undefined {
  return body.match(new RegExp(`--${name}:\\s*([^;]+);`))?.[1]?.trim();
}

describe("the palette", () => {
  it("is BRAND.md's eight roles", () => {
    expect(palette().map((r) => r.role)).toEqual([
      "bg",
      "surface",
      "text",
      "muted",
      "accent",
      "ok",
      "warn",
      "error",
    ]);
  });

  it.each([
    [":root", "light"],
    [".dark", "dark"],
  ] as const)("in %s copies BRAND.md's %s column exactly", (selector, mode) => {
    const body = block(selector);
    for (const row of palette()) {
      expect(value(body, `spectral-${row.role}`), row.role).toBe(row[mode]);
    }
  });

  // The roles the screens actually paint with. A token pointed at a colour of
  // its own instead of a role is the drift the copy above cannot see.
  it.each([":root", ".dark"] as const)("in %s gives each role its tokens", (selector) => {
    const body = block(selector);
    const roles: Record<string, string> = {
      background: "bg",
      surface: "surface",
      card: "surface",
      popover: "surface",
      sidebar: "surface",
      foreground: "text",
      "card-foreground": "text",
      "popover-foreground": "text",
      "sidebar-foreground": "text",
      "muted-foreground": "muted",
      primary: "accent",
      ring: "accent",
      "sidebar-primary": "accent",
      "sidebar-ring": "accent",
      success: "ok",
      warning: "warn",
      destructive: "error",
    };
    for (const [token, role] of Object.entries(roles)) {
      expect(value(body, token), token).toBe(`var(--spectral-${role})`);
    }
  });
});
