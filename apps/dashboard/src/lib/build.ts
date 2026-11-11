/**
 * The Build chat: a conversation with the team's builder, read from the
 * session's builder runs and their proposals. Nothing here decides anything;
 * the brain plans and refuses.
 */
import type {
  Blueprint,
  BlueprintChange,
  BlueprintPlan,
  Proposal,
  Run,
  RunEvent,
} from "@/api/types";
import { type AskRefusal, askRefusal } from "@/lib/ask";

/** One exchange: what the person asked, the builder's run, and what it proposed. */
export type Turn = { run: Run; proposal: Proposal | null };

/** A run is live until it ends; the chat polls while one is. */
export function isLive(run: Run): boolean {
  return run.status === "queued" || run.status === "running";
}

/**
 * The session's builder runs, oldest first, each with the newest proposal it
 * made. A run proposes up to five times and only its last one stays open,
 * so the newest is the one that matters.
 */
export function turnsOf(runs: Run[], proposals: Proposal[]): Turn[] {
  const byRun = new Map<string, Proposal>();
  for (const p of proposals) {
    const seen = byRun.get(p.run_id);
    if (!seen || p.created_at > seen.created_at) byRun.set(p.run_id, p);
  }
  return runs
    .filter((r) => r.builder)
    .slice()
    .sort((a, b) => (a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0))
    .map((run) => ({ run, proposal: byRun.get(run.id) ?? null }));
}

export const NO_BUILDER_MODEL = "The builder can't run until the team has a model.";

/** Why the builder could not be asked, in the chat's words. */
export function buildRefusal(err: unknown): AskRefusal {
  const r = askRefusal(err);
  return r.settings ? { text: NO_BUILDER_MODEL, settings: true } : r;
}

/**
 * What a proposal asks of the person besides Apply: each secret its servers
 * name, which they write to the secret store themselves, and each address it
 * would newly send the team's agents to. Only servers the plan creates or
 * changes count; one left alone asks nothing new.
 */
export type Needs = { secrets: string[]; addresses: { server: string; host: string }[] };

export function needsOf(blueprint: Blueprint | undefined, plan: BlueprintPlan): Needs {
  const touched = new Map<string, BlueprintChange>();
  for (const c of plan.changes) {
    if (c.kind === "mcp_server" && c.action !== "unchanged") touched.set(c.name, c);
  }
  const secrets = new Set<string>();
  const addresses: Needs["addresses"] = [];
  for (const s of blueprint?.mcp_servers ?? []) {
    const name = typeof s.name === "string" ? s.name : "";
    const change = touched.get(name);
    if (!change) continue;
    if (typeof s.auth_secret === "string") secrets.add(s.auth_secret);
    if (s.env && typeof s.env === "object") {
      for (const ref of Object.values(s.env as Record<string, unknown>)) {
        if (typeof ref === "string") secrets.add(ref);
      }
    }
    const moved = change.action === "create" || change.fields?.includes("url");
    const host = typeof s.url === "string" ? hostOf(s.url) : null;
    if (moved && host) addresses.push({ server: name, host });
  }
  return { secrets: [...secrets].sort(), addresses };
}

function hostOf(url: string): string | null {
  try {
    return new URL(url).host || null;
  } catch {
    return null;
  }
}

/**
 * The conversations to carry on: one per session, from its newest builder
 * run, newest first. The runs arrive newest first, so the first seen of a
 * session is its latest.
 */
export function conversationsOf(runs: Run[], atMost = 10): Run[] {
  const seen = new Set<string>();
  const out: Run[] = [];
  for (const r of runs) {
    if (!r.builder || seen.has(r.session_id)) continue;
    seen.add(r.session_id);
    out.push(r);
    if (out.length === atMost) break;
  }
  return out;
}

/** One thing the builder is doing, or did, in plain words. */
export type Activity = { id: string; label: string; state: "running" | "done" | "failed" };

/**
 * A live builder run as the chat shows it: a line per tool it called, done or
 * failed once its result came, and the text it has written since its last
 * call, which becomes the answer. What it wrote before a call was on the way
 * there; the call's line says what it did.
 */
export function liveOf(events: RunEvent[]): { activity: Activity[]; text: string } {
  const activity: Activity[] = [];
  let text = "";
  for (const e of events) {
    const d = (e.data ?? {}) as Record<string, unknown>;
    const id = typeof d.id === "string" ? d.id : "";
    if (e.kind === "text" && typeof d.text === "string") {
      text += d.text;
    } else if (e.kind === "tool_call") {
      text = "";
      activity.push({
        id,
        label: labelOf(String(d.name ?? ""), argsOf(d.arguments)),
        state: "running",
      });
    } else if (e.kind === "tool_result") {
      const a = [...activity].reverse().find((x) => x.id === id && x.state === "running");
      if (a) a.state = typeof d.error === "string" && d.error !== "" ? "failed" : "done";
    }
  }
  return { activity, text };
}

function argsOf(raw: unknown): Record<string, unknown> {
  if (typeof raw !== "string") return {};
  try {
    const v = JSON.parse(raw);
    return v && typeof v === "object" ? (v as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function hostOrSelf(url: unknown): string {
  if (typeof url !== "string") return "a server";
  try {
    const u = new URL(url);
    return u.host + (u.pathname === "/" ? "" : u.pathname);
  } catch {
    return url;
  }
}

const quoted = (v: unknown) => (typeof v === "string" && v !== "" ? ` \u201c${v}\u201d` : "");

function labelOf(name: string, args: Record<string, unknown>): string {
  switch (name) {
    case "team":
      return "Reading the team";
    case "graph":
      return `Searching the team${quoted(args.query) ? ` for${quoted(args.query)}` : ""}`;
    case "find_mcp_servers":
      return `Looking up${quoted(args.query)} in the MCP registry`;
    case "list_mcp_tools":
      return `Asking ${hostOrSelf(args.url)} for its tools`;
    case "web_search":
      return `Searching the web for${quoted(args.query)}`;
    case "web_fetch":
      return `Reading ${hostOrSelf(args.url)}`;
    case "propose":
      return "Writing the proposal";
    default:
      return `Using ${name}`;
  }
}

/** A secret a proposal names, by its name and the keys it needs; settable when the brain can write it. */
export type SecretGroup = { name: string; keys: string[]; settable: boolean };

const SEGMENT = /^[A-Za-z0-9_-]{1,128}$/;

/**
 * The secrets a proposal names, grouped by secret: every key of one is
 * written together, since a write replaces the keys it held. A reference may
 * be relative (name#key) or whole (teams/<team>/mcp/name#key); one nested
 * deeper, or another team's, is shown but not offered to set.
 */
export function secretGroups(refs: string[], teamId: string): SecretGroup[] {
  const root = `teams/${teamId}/mcp/`;
  const groups = new Map<string, SecretGroup>();
  for (const ref of refs) {
    const rel = ref.startsWith(root) ? ref.slice(root.length) : ref;
    const at = rel.lastIndexOf("#");
    const name = at > 0 ? rel.slice(0, at) : rel;
    const key = at > 0 ? rel.slice(at + 1) : "";
    const settable = !rel.startsWith("teams/") && SEGMENT.test(name) && SEGMENT.test(key);
    const g = groups.get(name) ?? { name, keys: [], settable };
    if (key && !g.keys.includes(key)) g.keys.push(key);
    g.settable = g.settable && settable;
    groups.set(name, g);
  }
  return [...groups.values()];
}
