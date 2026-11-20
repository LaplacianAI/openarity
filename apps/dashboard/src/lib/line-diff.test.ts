import { describe, expect, it } from "vitest";

import { lineDiff, withContext } from "./line-diff";

const text = (lines: ReturnType<typeof lineDiff>) => lines.map((l) => `${l.kind}:${l.text}`);

describe("lineDiff", () => {
  it("marks a replaced line as removed then added", () => {
    expect(text(lineDiff("a\nb\nc", "a\nB\nc"))).toEqual(["same:a", "del:b", "add:B", "same:c"]);
  });

  it("marks an added line and a removed line", () => {
    expect(text(lineDiff("a\nc", "a\nb\nc"))).toEqual(["same:a", "add:b", "same:c"]);
    expect(text(lineDiff("a\nb\nc", "a\nc"))).toEqual(["same:a", "del:b", "same:c"]);
  });

  it("treats no text as no lines, so a new file is all additions", () => {
    expect(text(lineDiff(null, "x\ny"))).toEqual(["add:x", "add:y"]);
    expect(text(lineDiff("x", null))).toEqual(["del:x"]);
    expect(lineDiff("", "")).toEqual([]);
  });

  it("finds nothing to change in equal text, whatever its line endings", () => {
    expect(lineDiff("a\r\nb", "a\nb").every((l) => l.kind === "same")).toBe(true);
  });

  it("keeps a moved block as removed and added rather than losing a line", () => {
    const d = lineDiff("a\nb\nc\nd", "c\nd\na\nb");
    expect(d.filter((l) => l.kind !== "add").map((l) => l.text)).toEqual(["a", "b", "c", "d"]);
    expect(d.filter((l) => l.kind !== "del").map((l) => l.text)).toEqual(["c", "d", "a", "b"]);
  });
});

describe("withContext", () => {
  const many = Array.from({ length: 12 }, (_, i) => `l${i}`);
  const changed = lineDiff(many.join("\n"), many.map((l) => (l === "l6" ? "X" : l)).join("\n"));

  it("folds the unchanged stretches away from a change", () => {
    const out = withContext(changed, 2);
    expect(out[0]).toEqual({ kind: "skip", count: 4 });
    expect(out.at(-1)).toEqual({ kind: "skip", count: 3 });
    expect(out.filter((l) => l.kind === "del" || l.kind === "add")).toHaveLength(2);
  });

  it("does not fold a single line, which would take as much room as the line", () => {
    const d = lineDiff("a\nb\nc\nd\ne", "A\nb\nc\nd\nE");
    expect(withContext(d, 1).some((l) => l.kind === "skip")).toBe(false);
    expect(withContext(d, 0).filter((l) => l.kind === "skip")).toEqual([
      { kind: "skip", count: 3 },
    ]);
  });

  it("shows every line when nothing changed within reach", () => {
    expect(withContext(lineDiff("a", "a"), 2)).toEqual([{ kind: "same", text: "a" }]);
  });
});

describe("a long file", () => {
  it("diffs only what lies between the lines both sides share", () => {
    const long = Array.from({ length: 4000 }, (_, i) => `line ${i}`);
    const edited = [...long];
    edited[2000] = "changed";
    const d = lineDiff(long.join("\n"), edited.join("\n"));
    expect(d).toHaveLength(4001);
    expect(d.filter((l) => l.kind !== "same")).toEqual([
      { kind: "del", text: "line 2000" },
      { kind: "add", text: "changed" },
    ]);
  });

  it("shows a middle too big for the table as removed then added", () => {
    const a = Array.from({ length: 1500 }, (_, i) => `a${i}`).join("\n");
    const b = Array.from({ length: 1500 }, (_, i) => `b${i}`).join("\n");
    const d = lineDiff(a, b);
    expect(d).toHaveLength(3000);
    expect(d[0]?.kind).toBe("del");
    expect(d[1500]?.kind).toBe("add");
  });
});
