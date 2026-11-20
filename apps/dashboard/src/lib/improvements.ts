import type {
  Improvement,
  ImprovementChange,
  ImprovementKind,
  ImprovementMark,
  ImprovementStatus,
} from "@/api/types";
import { waitedFor } from "@/lib/format";
import { count } from "@/lib/runs";

/** The permission that decides a proposal, over and above the write it makes. */
export const APPROVE = "improvement:approve";

export const KIND_WORDS: Record<ImprovementKind, string> = {
  instructions: "Instructions",
  description: "Description",
  skill_grant: "Skill added",
  skill_drop: "Skill removed",
  skill_new: "New skill",
  skill_edit: "Skill edit",
  server_grant: "Server access",
  server_drop: "Server removed",
  tools_narrow: "Tool limits",
  builtin_tool: "Built-in tools",
  tool_note: "Tool note",
  limits: "Limits",
  delegation: "Delegation",
  pattern: "Loop",
};

export const MARK_WORDS: Record<ImprovementMark, string> = {
  widens_access: "widens access",
  widens_reach: "widens reach",
  raises_cost: "raises cost",
  changes_code: "changes code",
  changes_discovery: "changes how it is found",
  changes_behaviour: "changes how it works",
};

/** "Instructions", or a kind this build does not know, as the brain named it. */
export function kindWords(kind: string): string {
  return KIND_WORDS[kind as ImprovementKind] ?? kind.replace(/_/g, " ");
}

export function scopeWords(scope: Improvement["scope"]): string {
  return scope === "invariant" ? "general" : "domain";
}

/** What deciding a proposal of this kind needs, beyond improvement:approve. */
export function writesNeeded(kind: ImprovementKind): string[] {
  if (kind === "skill_edit") return ["skill:write"];
  if (kind === "skill_new") return ["agent:write", "skill:write"];
  return ["agent:write"];
}

/** The write permissions of the kind that the caller does not hold. */
export function missingWrites(kind: ImprovementKind, can: (p: string) => boolean): string[] {
  return writesNeeded(kind).filter((p) => !can(p));
}

/** Whether a changed field is a skill's script, which is shown in full and never folded. */
export function isScript(field: string): boolean {
  return field.startsWith("files/scripts/");
}

/** A name in a list change: kept, or taken away, or added. */
export type ListName = { name: string; change: "kept" | "del" | "add" };

/**
 * A list change in reading order: the names as they were, those taken away
 * marked, then the names added. The kept ones give the change its context.
 */
export function listNames(change: ImprovementChange): ListName[] {
  const before = Array.isArray(change.before) ? change.before : [];
  const after = Array.isArray(change.after) ? change.after : [];
  return [
    ...before.map((name): ListName => ({ name, change: after.includes(name) ? "kept" : "del" })),
    ...after.filter((n) => !before.includes(n)).map((name): ListName => ({ name, change: "add" })),
  ];
}

const FIELD_WORDS: Record<string, string> = {
  instructions: "Instructions",
  description: "Description",
  pattern: "Pattern",
  max_tokens: "Max tokens",
  max_steps: "Max steps",
  steer_continuations: "Continuations after a steer",
  token_budget: "Token budget",
  delegation: "Delegation",
  web: "Can search the web and read pages",
  browser: "Can drive a browser",
  workspace: "Can work in a workspace",
  skills: "Skills",
  delegates: "Agents it may hand work to",
  mcp_servers: "MCP servers",
  notes: "Tool notes",
  body: "Body",
};

/** A changed field in the words the agent and skill pages use; a skill's file is its path. */
export function fieldWords(field: string): string {
  if (field.startsWith("files/")) return field.slice("files/".length);
  return FIELD_WORDS[field] ?? field.replace(/_/g, " ");
}

/**
 * The tabs: what is waiting, what is in force, and everything that ended
 * without being (or no longer being) in force.
 */
export type Tab = "waiting" | "applied" | "closed";

export const TAB_STATUSES: Record<Tab, ImprovementStatus[]> = {
  waiting: ["waiting"],
  applied: ["applied"],
  closed: ["rejected", "reverted", "withdrawn"],
};

export const TAB_WORDS: Record<Tab, string> = {
  waiting: "Waiting",
  applied: "Applied",
  closed: "Closed",
};

/** The tabs a caller may open: waiting and the closed group quote runs only an approver may read. */
export function tabsFor(canApprove: boolean): Tab[] {
  return canApprove ? ["waiting", "applied", "closed"] : ["applied"];
}

/** The tab asked for if it may be opened, else the first the caller has. */
export function tabOf(asked: Tab | undefined, canApprove: boolean): Tab {
  const tabs = tabsFor(canApprove);
  return asked !== undefined && tabs.includes(asked) ? asked : (tabs[0] as Tab);
}

/** "2 changes wait for approval; the oldest for 3 days." */
export function waitingFinding(
  items: Pick<Improvement, "created_at">[],
  more: boolean,
  now: number = Date.now(),
): string {
  if (items.length === 0) return "No changes wait for approval.";
  const n = `${count(items.length, "change")}${more ? " or more" : ""}`;
  const verb = items.length === 1 && !more ? "waits" : "wait";
  // Newest first, so the oldest is the last one read; past a page it is unknown.
  const oldest = items.reduce((a, b) =>
    Date.parse(a.created_at) <= Date.parse(b.created_at) ? a : b,
  );
  const tail = more
    ? ""
    : items.length === 1
      ? `, for ${waitedFor(oldest.created_at, now)}`
      : `; the oldest for ${waitedFor(oldest.created_at, now)}`;
  return `${n} ${verb} for approval${tail}.`;
}
