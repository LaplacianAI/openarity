import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

// A box that scrolls must also be positioned. Otherwise an absolutely placed
// child (every sr-only label is one) is placed against the nearest positioned
// box outside it, which can be the page itself: a label far down a long run
// then makes the whole window taller than the screen, and the frame scrolls
// away over empty space. jsdom has no layout to show that, so this reads the
// class lists instead.
//
// It reads each quoted string ("…", '…' or `…`) on its own: a class list split
// across a cn() call is judged string by string, so keep `relative` in the same
// string as the overflow class. Only unprefixed classes count: `md:relative`
// does not position the box at every width, and `lg:overflow-auto` is still a
// scroller.
// Run from apps/dashboard, as every other test that reads files is; the count
// below fails if it is not, rather than passing on an empty tree.
const root = join(process.cwd(), "src");
const scrolls = /(?<![-\w])(?:[a-z0-9-]+:)*overflow(?:-[xy])?-(?:auto|scroll)(?![-\w])/;
const positioned = /(?<![-\w:])(?:relative|absolute|fixed|sticky)(?![-\w])/;

function sources(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = join(dir, e.name);
    if (e.isDirectory()) return sources(p);
    return /\.tsx?$/.test(e.name) && !/\.test\.tsx?$/.test(e.name) ? [p] : [];
  });
}

/** Each quoted class string that makes a box scroll but does not position it. */
function unpositionedScrollers(file: string, text: string): string[] {
  const found: string[] = [];
  text.split("\n").forEach((line, i) => {
    for (const m of line.matchAll(/"([^"]*)"|'([^']*)'|`([^`]*)`/g)) {
      const cls = m[1] ?? m[2] ?? m[3] ?? "";
      if (scrolls.test(cls) && !positioned.test(cls)) found.push(`${file}:${i + 1}: ${cls}`);
    }
  });
  return found;
}

describe("scrolling boxes", () => {
  it("are all positioned, so nothing inside them escapes", () => {
    const files = sources(root);
    expect(files.length).toBeGreaterThan(100);
    const found = files.flatMap((f) =>
      unpositionedScrollers(relative(root, f), readFileSync(f, "utf8")),
    );
    expect(found).toEqual([]);
  });

  it.each([
    ['<div className="max-h-64 overflow-y-auto">', 1],
    ['<div className="overflow-scroll">', 1],
    ["<div className='overflow-auto'>", 1],
    ["cn(`overflow-x-auto px-2`)", 1],
    ['<div className="md:relative overflow-y-auto">', 1],
    ['<div className="lg:overflow-auto">', 1],
    ['<SheetContent className="w-full overflow-y-auto">', 1],
    ['<div className="relative overflow-x-auto">', 0],
    ['"fixed inset-0 overflow-y-auto"', 0],
    ['<div className="overflow-x-hidden">', 0],
  ] as const)("%s → %i", (line, n) => {
    expect(unpositionedScrollers("x.tsx", line)).toHaveLength(n);
  });
});
