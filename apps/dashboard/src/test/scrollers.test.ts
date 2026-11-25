import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

// A box that scrolls must also be positioned. Otherwise an absolutely placed
// child (every sr-only label is one) is placed against the nearest positioned
// box outside it, which can be the page itself: a label far down a long run
// then makes the whole window taller than the screen, and the frame scrolls
// away over empty space. jsdom has no layout to show that, so this reads the
// class lists instead.
const root = join(process.cwd(), "src");
const scrolls = /(?<![-\w])overflow(?:-[xy])?-(?:auto|scroll)(?![-\w])/;
const positioned = /(?<![-\w])(?:relative|absolute|fixed|sticky)(?![-\w])/;
// Components whose own base classes already fix them to the screen.
const fixedComponents = /<(?:SheetContent|DialogContent)\b/;

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return sources(p);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [p] : [];
  });
}

/** Each quoted class string that makes a box scroll, with where it is. */
function unpositionedScrollers(file: string, text: string): string[] {
  const found: string[] = [];
  text.split("\n").forEach((line, i) => {
    for (const m of line.matchAll(/"([^"]*)"/g)) {
      const cls = m[1] ?? "";
      if (scrolls.test(cls) && !positioned.test(cls) && !fixedComponents.test(line)) {
        found.push(`${file}:${i + 1}: ${cls}`);
      }
    }
  });
  return found;
}

describe("scrolling boxes", () => {
  it("are all positioned, so nothing inside them escapes", () => {
    const found = sources(root).flatMap((f) =>
      unpositionedScrollers(relative(root, f), readFileSync(f, "utf8")),
    );
    expect(found).toEqual([]);
  });

  it("are caught when one is not", () => {
    expect(
      unpositionedScrollers("x.tsx", '<div className="max-h-64 overflow-y-auto">'),
    ).toHaveLength(1);
    expect(unpositionedScrollers("x.tsx", '<div className="relative overflow-x-auto">')).toEqual(
      [],
    );
    expect(unpositionedScrollers("x.tsx", '<div className="overflow-x-hidden">')).toEqual([]);
  });
});
