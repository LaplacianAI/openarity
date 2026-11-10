import type { Agent, AgentRequest, Blueprint, MCPServer, MCPServerRequest } from "@/api/types";
import type { SearchResult } from "@/components/picker";
import type { Draft } from "@/lib/agent-draft";
import { type Folder, withSkills } from "@/lib/blueprint";
import type { ServerDraft } from "@/lib/server-draft";

/**
 * A Compose draft: what a person has asked for and not applied, each item as
 * its form left it. Nothing in it exists in the team until Apply.
 */
export type Item =
  | {
      kind: "mcp_server";
      name: string;
      form: ServerDraft;
      existing?: MCPServer;
      request: MCPServerRequest;
    }
  | { kind: "skill"; name: string; folder: Folder }
  | { kind: "agent"; name: string; form: Draft; existing?: Agent; request: AgentRequest };

/** The names a draft will create, which the agent form offers beside the team's. */
export type Drafted = { servers: string[]; skills: string[]; agents: string[] };

const PREFIX = "draft:";
const KINDS = { servers: "mcp_server", skills: "skill", agents: "agent" } as const;

/**
 * The id an item that does not exist yet goes by in a picker. A uuid never
 * starts with "draft:", so it cannot be taken for one of the team's.
 */
export function draftId(kind: keyof Drafted, name: string): string {
  return `${PREFIX}${KINDS[kind]}:${name}`;
}

export function isDraftId(id: string): boolean {
  return id.startsWith(PREFIX);
}

/** The name in a draft id; the id itself for anything else. */
export function draftName(id: string): string {
  if (!isDraftId(id)) return id;
  return id.slice(id.indexOf(":", PREFIX.length) + 1);
}

/** What the draft creates, by kind. */
export function draftedOf(items: Item[]): Drafted {
  const of = (kind: Item["kind"]) => items.filter((i) => i.kind === kind).map((i) => i.name);
  return { servers: of("mcp_server"), skills: of("skill"), agents: of("agent") };
}

/**
 * A picker's search with the draft's matches first, marked as drafted, then
 * the brain's. A drafted name the team already has is offered once, as the
 * draft's: applying writes the draft's version.
 */
export function withDrafted(
  drafted: Drafted | undefined,
  kind: keyof Drafted,
  search: (q: string) => Promise<SearchResult>,
): (q: string) => Promise<SearchResult> {
  const names = drafted?.[kind] ?? [];
  if (names.length === 0) return search;
  return async (q) => {
    const want = q.trim().toLowerCase();
    const mine = names
      .filter((n) => n.toLowerCase().includes(want))
      .map((n) => ({ id: draftId(kind, n), name: `${n} (in draft)` }));
    const theirs = await search(q);
    const taken = new Set(names);
    return {
      items: [...mine, ...theirs.items.filter((i) => !taken.has(i.name))],
      more: theirs.more,
    };
  };
}

/** The draft with this item in it, in place of any item of the same kind and name. */
export function put(items: Item[], item: Item, replacing?: Item): Item[] {
  const same = (i: Item) => i === replacing || (i.kind === item.kind && i.name === item.name);
  const at = items.findIndex(same);
  if (at === -1) return [...items, item];
  return [...items.slice(0, at), item, ...items.slice(at + 1).filter((i) => !same(i))];
}

/** A server as a blueprint takes it: the API's request, its reference under auth_secret. */
export function blueprintServer(req: MCPServerRequest): Record<string, unknown> {
  const { auth_secret_ref, ...rest } = req;
  return auth_secret_ref ? { ...rest, auth_secret: auth_secret_ref } : rest;
}

/**
 * An agent as a blueprint takes it: the API's request, with what it is
 * granted named rather than given by id. `nameOf` says what an id is called;
 * a draft id carries its own name.
 */
export async function blueprintAgent(
  req: AgentRequest,
  nameOf: (kind: keyof Drafted, id: string) => Promise<string>,
): Promise<Record<string, unknown>> {
  const { mcp_servers, skills, delegates, ...rest } = req;
  const named = (kind: keyof Drafted, id: string) =>
    isDraftId(id) ? Promise.resolve(draftName(id)) : nameOf(kind, id);
  return {
    ...rest,
    mcp_servers: await Promise.all(
      (mcp_servers ?? []).map(async (g) => ({
        name: await named("servers", g.id),
        ...(g.allow ? { allow: g.allow } : {}),
      })),
    ),
    skills: await Promise.all((skills ?? []).map((id) => named("skills", id))),
    ...(delegates
      ? { delegates: await Promise.all(delegates.map((id) => named("agents", id))) }
      : {}),
  };
}

/** The draft as one blueprint, skills read from their folders. */
export async function blueprintOf(
  items: Item[],
  nameOf: (kind: keyof Drafted, id: string) => Promise<string>,
): Promise<Blueprint> {
  const servers = items.flatMap((i) =>
    i.kind === "mcp_server" ? [blueprintServer(i.request)] : [],
  );
  const agents = await Promise.all(
    items.flatMap((i) => (i.kind === "agent" ? [blueprintAgent(i.request, nameOf)] : [])),
  );
  const folders = items.flatMap((i) => (i.kind === "skill" ? [i.folder] : []));
  return withSkills(
    { ...(servers.length ? { mcp_servers: servers } : {}), ...(agents.length ? { agents } : {}) },
    folders,
  );
}

/**
 * A skill's name, as its SKILL.md says it, which is what the brain will call
 * it; the folder's own name when the file does not say.
 */
export async function skillName(folder: Folder): Promise<string> {
  const manifest = folder.files.find((f) => f.path.split("/").slice(1).join("/") === "SKILL.md");
  if (!manifest) return folder.name;
  const text = await textOf(manifest.file);
  const front = /^---\r?\n([\s\S]*?)\r?\n---/.exec(text)?.[1] ?? "";
  const name = /^name:\s*["']?([^"'\r\n]+?)["']?\s*$/m.exec(front)?.[1];
  return name?.trim() || folder.name;
}

function textOf(file: File): Promise<string> {
  if (typeof file.text === "function") return file.text();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error ?? new Error(`could not read ${file.name}`));
    reader.readAsText(file);
  });
}
