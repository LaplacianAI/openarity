import type {
  Improvement,
  ImprovementChange,
  ImprovementKind,
  ImprovementMark,
  ImprovementStatus,
  ImproverPass,
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
 * The tabs: what is waiting, what is in force, everything that ended without
 * being (or no longer being) in force, and the passes of the improver that
 * wrote them.
 */
export type Tab = "waiting" | "applied" | "closed" | "passes";

/** The tabs that list proposals, each by the statuses it holds. */
export type StatusTab = Exclude<Tab, "passes">;

export const TAB_STATUSES: Record<StatusTab, ImprovementStatus[]> = {
  waiting: ["waiting"],
  applied: ["applied"],
  closed: ["rejected", "reverted", "withdrawn"],
};

export const TAB_WORDS: Record<Tab, string> = {
  waiting: "Waiting",
  applied: "Applied",
  closed: "Closed",
  passes: "Passes",
};

/** The tabs a caller may open: waiting, the closed group and the passes quote runs only an approver may read. */
export function tabsFor(canApprove: boolean): Tab[] {
  return canApprove ? ["waiting", "applied", "closed", "passes"] : ["applied"];
}

/** The tab asked for if it may be opened, else the first the caller has. */
export function tabOf(asked: Tab | undefined, canApprove: boolean): Tab {
  const tabs = tabsFor(canApprove);
  return asked !== undefined && tabs.includes(asked) ? asked : (tabs[0] as Tab);
}

/** What a pass found, one signature read apart: "tool", "crm__close_ticket", "misused tool". */
export type SignatureWords = { part: string; target: string; cause: string };

/** "tool:crm__close_ticket:misused_tool", or "instructions::skipped_step", in words. */
export function signatureWords(signature: string): SignatureWords {
  // The part and cause come from closed sets with no colon, so a target
  // holding one ("tool:crm:lookup") still reads back whole between them.
  const first = signature.indexOf(":");
  const last = signature.lastIndexOf(":");
  const words = (t: string) => t.replace(/_/g, " ").trim();
  if (first < 0) return { part: words(signature), target: "", cause: "" };
  if (first === last)
    return {
      part: words(signature.slice(0, first)),
      target: "",
      cause: words(signature.slice(first + 1)),
    };
  return {
    part: words(signature.slice(0, first)),
    target: signature.slice(first + 1, last),
    cause: words(signature.slice(last + 1)),
  };
}

/** Whether any pass on the pages read is still running: the list is read again until none is. */
export function anyPassRunning(
  pages: { items: Pick<ImproverPass, "status">[] }[] | undefined,
): boolean {
  return pages?.some((p) => p.items.some((x) => x.status === "running")) ?? false;
}

/** "skill:triage" as "skill triage"; a tool is "server/tool", which stays as it is. */
export function prunedWords(pruned: string): string {
  const at = pruned.indexOf(":");
  return at < 0 ? pruned : `${pruned.slice(0, at)} ${pruned.slice(at + 1)}`;
}

/** What came of a pass, as a sentence: a person is told what it did, not its status. */
export function passOutcome(
  pass: Pick<ImproverPass, "status" | "proposed" | "screened_out" | "error">,
): string {
  if (pass.status === "running") return "Running…";
  if (pass.status === "failed") return pass.error ? `Failed: ${pass.error}` : "Failed";
  const held =
    pass.screened_out > 0
      ? `${pass.screened_out.toLocaleString("en-US")} held back by the screen`
      : null;
  const made = pass.proposed > 0 ? count(pass.proposed, "proposal") : "Nothing to change";
  return held ? `${made}, ${held}` : made;
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
