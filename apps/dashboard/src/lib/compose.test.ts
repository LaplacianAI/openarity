import { describe, expect, it } from "vitest";

import type { AgentRequest } from "@/api/types";
import { EMPTY } from "@/lib/agent-draft";
import {
  blueprintAgent,
  blueprintOf,
  blueprintServer,
  draftedOf,
  draftId,
  draftName,
  type Item,
  isDraftId,
  put,
  skillName,
  withDrafted,
} from "@/lib/compose";
import { EMPTY_SERVER } from "@/lib/server-draft";

function file(path: string, body = "x"): File {
  const f = new File([body], path.split("/").pop() as string);
  Object.defineProperty(f, "webkitRelativePath", { value: path });
  return f;
}

const agent = (name: string, extra: Partial<AgentRequest> = {}): Item => ({
  kind: "agent",
  name,
  form: EMPTY,
  request: { name, model: { name: "m", max_tokens: 1 }, pattern: "react", max_steps: 1, ...extra },
});

const server = (name: string): Item => ({
  kind: "mcp_server",
  name,
  form: EMPTY_SERVER,
  request: { name, url: "https://h.example/mcp", auth_secret_ref: "teams/t/mcp/h#k" },
});

// Ids the brain gave; their names are only known by asking.
const names: Record<string, string> = { s1: "github", k1: "pdf", a1: "writer" };
const nameOf = async (_kind: string, id: string) => names[id] ?? `?${id}`;

describe("draft ids", () => {
  it("name an item that does not exist yet, and never look like a uuid", () => {
    const id = draftId("servers", "help:desk");
    expect(isDraftId(id)).toBe(true);
    expect(draftName(id)).toBe("help:desk");
    expect(isDraftId("6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d")).toBe(false);
    expect(draftName("s1")).toBe("s1");
  });

  it("say what the draft creates, by kind", () => {
    expect(draftedOf([server("helpdesk"), agent("triage")])).toEqual({
      servers: ["helpdesk"],
      skills: [],
      agents: ["triage"],
    });
  });
});

describe("a picker's search", () => {
  const brain = async () => ({
    items: [
      { id: "s1", name: "github" },
      { id: "s2", name: "helpdesk" },
    ],
    more: true,
  });

  it("offers the draft's matches first, and a drafted name only once", async () => {
    const search = withDrafted({ servers: ["helpdesk"], skills: [], agents: [] }, "servers", brain);
    expect(await search("HELP")).toEqual({
      items: [
        { id: draftId("servers", "helpdesk"), name: "helpdesk (in draft)" },
        { id: "s1", name: "github" },
      ],
      more: true,
    });
  });

  it("leaves the brain's search alone with nothing drafted", () => {
    expect(withDrafted(undefined, "servers", brain)).toBe(brain);
  });
});

describe("putting an item in the draft", () => {
  it("adds a new one at the end", () => {
    expect(put([server("a")], agent("b")).map((i) => i.name)).toEqual(["a", "b"]);
  });

  it("replaces one of the same kind and name in place", () => {
    const bare: Item = {
      kind: "mcp_server",
      name: "a",
      form: { ...EMPTY_SERVER, bare: true },
      request: { name: "a", url: "https://h.example/mcp", bare: true },
    };
    const next = put([server("a"), agent("b")], bare);
    expect(next.map((i) => i.name)).toEqual(["a", "b"]);
    expect(next[0]).toBe(bare);
  });

  // Renaming a drafted item in its form must not leave the old one behind.
  it("replaces the item being edited even when its name changed", () => {
    const old = agent("tirage");
    const next = put([old, server("a")], agent("triage"), old);
    expect(next.map((i) => i.name)).toEqual(["triage", "a"]);
  });

  it("keeps a server and an agent that share a name apart", () => {
    expect(put([server("x")], agent("x"))).toHaveLength(2);
  });
});

describe("the blueprint a draft becomes", () => {
  it("names a server's reference auth_secret", () => {
    expect(blueprintServer({ name: "h", url: "u", auth_secret_ref: "teams/t/mcp/h#k" })).toEqual({
      name: "h",
      url: "u",
      auth_secret: "teams/t/mcp/h#k",
    });
    expect(blueprintServer({ name: "c", command: ["x"] })).toEqual({ name: "c", command: ["x"] });
  });

  it("names every grant, the draft's by their own names and the team's by asking", async () => {
    const bp = await blueprintAgent(
      {
        name: "triage",
        model: { name: "m", max_tokens: 1 },
        pattern: "react",
        max_steps: 1,
        mcp_servers: [{ id: "s1", allow: ["search"] }, { id: draftId("servers", "helpdesk") }],
        skills: ["k1", draftId("skills", "faq")],
        delegation: "listed",
        delegates: ["a1"],
        output_schema: { name: "o", json: {}, strict: true },
      },
      nameOf,
    );
    expect(bp).toEqual({
      name: "triage",
      model: { name: "m", max_tokens: 1 },
      pattern: "react",
      max_steps: 1,
      delegation: "listed",
      output_schema: { name: "o", json: {}, strict: true },
      mcp_servers: [{ name: "github", allow: ["search"] }, { name: "helpdesk" }],
      skills: ["pdf", "faq"],
      delegates: ["writer"],
    });
  });

  it("gathers servers, agents and skill folders into one blueprint", async () => {
    const faq = {
      name: "faq",
      files: [{ path: "faq/SKILL.md", file: file("faq/SKILL.md", "hi") }],
    };
    const bp = await blueprintOf(
      [server("helpdesk"), { kind: "skill", name: "faq", folder: faq }, agent("triage")],
      nameOf,
    );
    expect(bp.mcp_servers).toHaveLength(1);
    expect(bp.agents?.[0]).toMatchObject({ name: "triage", mcp_servers: [], skills: [] });
    expect(bp.skills).toEqual([{ files: [{ path: "faq/SKILL.md", data: "aGk=" }] }]);
  });

  it("leaves out what the draft has none of", async () => {
    expect(await blueprintOf([], nameOf)).toEqual({});
  });
});

describe("a skill's name", () => {
  const folder = (body: string | null) => ({
    name: "folder-name",
    files:
      body === null
        ? []
        : [{ path: "folder-name/SKILL.md", file: file("folder-name/SKILL.md", body) }],
  });

  it("is what its SKILL.md says", async () => {
    expect(await skillName(folder("---\nname: support-faq\ndescription: x\n---\nbody\n"))).toBe(
      "support-faq",
    );
    expect(await skillName(folder('---\nname: "quoted"\n---\n'))).toBe("quoted");
  });

  it("is the folder's when SKILL.md is missing or says none", async () => {
    expect(await skillName(folder(null))).toBe("folder-name");
    expect(await skillName(folder("no frontmatter"))).toBe("folder-name");
  });
});
