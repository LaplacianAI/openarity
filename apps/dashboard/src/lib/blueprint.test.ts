import { describe, expect, it } from "vitest";

import {
  base64,
  folderFrom,
  missingFolders,
  readBlueprint,
  tally,
  withSkills,
} from "@/lib/blueprint";

function file(path: string, body = "x"): File {
  const f = new File([body], path.split("/").pop() as string);
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

describe("reading a blueprint", () => {
  it("reads YAML, keeping servers and agents as written and skills as the paths they name", () => {
    const read = readBlueprint(
      "mcp_servers:\n  - name: helpdesk\n    url: https://h.example/mcp\nskills:\n  - path: ./skills/support-faq\nagents:\n  - name: triage\n    max_steps: 5\n",
    );
    expect(read).toEqual({
      ok: true,
      blueprint: {
        mcp_servers: [{ name: "helpdesk", url: "https://h.example/mcp" }],
        agents: [{ name: "triage", max_steps: 5 }],
      },
      skillPaths: ["./skills/support-faq"],
    });
  });

  it("reads JSON, which is YAML", () => {
    expect(readBlueprint('{"agents": [{"name": "a"}]}')).toMatchObject({
      ok: true,
      blueprint: { agents: [{ name: "a" }] },
    });
  });

  it.each([
    ["", "Write or paste"],
    ["agents: [", "Line"],
    ["- a list", "is a map"],
    ["agent:\n  - name: a\n", '"agent" is not part of a blueprint'],
    ["agents: triage\n", "agents is a list"],
    ["agents: [triage]\n", "agents is a list"],
    ["skills: ./faq\n", "skills is a list"],
    ["skills:\n  - name: faq\n", "Skill 1 has no path"],
  ])("refuses %j", (text, words) => {
    const read = readBlueprint(text);
    expect(read.ok).toBe(false);
    expect(read.ok ? "" : read.problem).toContain(words);
  });

  it("lets a section be empty", () => {
    expect(readBlueprint("agents:\nskills:\n")).toEqual({
      ok: true,
      blueprint: {},
      skillPaths: [],
    });
  });
});

describe("skill folders", () => {
  it("takes a folder by its own name and leaves .git out", () => {
    const f = folderFrom([file("faq/SKILL.md"), file("faq/.git/HEAD"), file("faq/faq.md")]);
    expect(f?.name).toBe("faq");
    expect(f?.files.map((x) => x.path)).toEqual(["faq/SKILL.md", "faq/faq.md"]);
  });

  it("is nothing when the picker gave nothing", () => {
    expect(folderFrom([])).toBeNull();
  });

  it("matches a path to a folder by its last segment", () => {
    const faq = { name: "support-faq", files: [] };
    expect(missingFolders(["./skills/support-faq", "skills/support-faq/", "./pdf"], [faq])).toEqual(
      ["./pdf"],
    );
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
