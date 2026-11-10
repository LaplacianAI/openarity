import { describe, expect, it } from "vitest";

import { base64, folderFrom, tally, withSkills } from "@/lib/blueprint";

function file(path: string, body = "x"): File {
  const f = new File([body], path.split("/").pop() as string);
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

describe("skill folders", () => {
  it("takes a folder by its own name and leaves .git out", () => {
    const f = folderFrom([file("faq/SKILL.md"), file("faq/.git/HEAD"), file("faq/faq.md")]);
    expect(f?.name).toBe("faq");
    expect(f?.files.map((x) => x.path)).toEqual(["faq/SKILL.md", "faq/faq.md"]);
  });

  it("is nothing when the picker gave nothing", () => {
    expect(folderFrom([])).toBeNull();
  });

  it("sends each folder as a skill, every file's bytes as base64", async () => {
    const f = folderFrom([file("faq/SKILL.md", "hello")]);
    const bp = await withSkills({ agents: [{ name: "a" }] }, f ? [f] : []);
    expect(bp).toEqual({
      agents: [{ name: "a" }],
      skills: [{ files: [{ path: "faq/SKILL.md", data: "aGVsbG8=" }] }],
    });
  });

  it("leaves a blueprint with no folders as it was", async () => {
    const bp = { agents: [] };
    expect(await withSkills(bp, [])).toBe(bp);
  });

  // String.fromCharCode(...bytes) on a whole file overflows the argument
  // list somewhere past a hundred thousand bytes.
  it("encodes a file larger than one slice", () => {
    const big = new Uint8Array(0x8000 * 3 + 7).fill(65);
    expect(atob(base64(big.buffer))).toHaveLength(big.length);
  });
});

describe("tally", () => {
  it("counts what a plan creates, changes and leaves", () => {
    expect(
      tally([
        { kind: "agent", name: "a", action: "create" },
        { kind: "agent", name: "b", action: "update", fields: ["model"] },
        { kind: "skill", name: "c", action: "unchanged" },
        { kind: "skill", name: "d", action: "unchanged" },
      ]),
    ).toEqual({ create: 1, update: 1, same: 2 });
  });
});
