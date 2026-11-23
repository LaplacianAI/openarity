import type {
  Improvement,
  ImprovementChange,
  ImprovementFilter,
  ImprovementKind,
  ImprovementMark,
  ImprovementWatch,
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
 * The tabs: what is waiting, what is in force, what in force its watch
 * flagged, everything that ended without being (or no longer being) in
 * force, and the passes of the improver that wrote them.
 */
export type Tab = "waiting" | "applied" | "flagged" | "closed" | "passes";

/** The tabs that list proposals, each by the statuses it holds. */
export type StatusTab = Exclude<Tab, "passes">;

export const TAB_STATUSES: Record<StatusTab, ImprovementFilter[]> = {
  waiting: ["waiting"],
  applied: ["applied"],
  flagged: ["flagged"],
  closed: ["rejected", "reverted", "withdrawn"],
};

export const TAB_WORDS: Record<Tab, string> = {
  waiting: "Waiting",
  applied: "Applied",
  flagged: "Flagged",
  closed: "Closed",
  passes: "Passes",
};

/**
 * The tabs a caller may open: waiting, the closed group and the passes quote
 * runs only an approver may read; flagged changes are applied ones, which
 * every member sees.
 */
export function tabsFor(canApprove: boolean): Tab[] {
  return canApprove
    ? ["waiting", "applied", "flagged", "closed", "passes"]
    : ["applied", "flagged"];
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
  pass: Pick<ImproverPass, "status" | "proposed" | "screened_out" | "error" | "cut_short">,
): string {
  if (pass.status === "running") return "Running…";
  if (pass.status === "failed") return pass.error ? `Failed: ${pass.error}` : "Failed";
  const held =
    pass.screened_out > 0
      ? `${pass.screened_out.toLocaleString("en-US")} held back by the screen`
      : null;
  // Cut short, "nothing to change" would claim a look it never finished.
  let made: string;
  if (pass.cut_short) {
    if (pass.proposed > 0) made = `${count(pass.proposed, "proposal")}, then cut short`;
    // Held back is proposed, then refused: "before it proposed" would be false.
    else if (held) return `Cut short with none kept, ${held}`;
    else made = "Cut short before it proposed anything";
  } else {
    made = pass.proposed > 0 ? count(pass.proposed, "proposal") : "Nothing to change";
  }
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

/**
 * Why a watch flagged its change, in words. A change that saved nothing is
 * said by what it aimed to save: cheaper, quicker, or both.
 */
export function flagWords(
  flag: ImprovementWatch["flag"],
  saves: Improvement["saves"] = [],
): string {
  if (flag === "worse") return "its runs went worse";
  if (flag === "costlier") return "its runs cost more, with no fewer failures";
  if (flag === "both") return "its runs went worse and cost more";
  if (flag === "no_saving") {
    // Flagged when any one saving it named is missing, so both named reads
    // "both", not as if each failed.
    const cost = saves?.includes("cost");
    const time = saves?.includes("time");
    const aim =
      cost && time
        ? "both cheaper and quicker"
        : cost
          ? "cheaper"
          : time
            ? "quicker"
            : "cheaper or quicker";
    return `its good runs did not get ${aim}, as it expected`;
  }
  return "";
}

/** "Flagged, its runs went worse: 7 of 10 went wrong after, 2 of 10 before." */
export function watchHeadline(w: ImprovementWatch, saves: Improvement["saves"] = []): string {
  const counts = `${w.after.bad} of ${w.after.runs} went wrong after, ${w.before.bad} of ${w.before.runs} before`;
  switch (w.status) {
    case "watching":
      return `Watching: ${w.after.runs} of ${w.needed} runs so far`;
    case "clear":
      return `Clear: ${counts}`;
    case "flagged":
      return `Flagged, ${flagWords(w.flag, saves)}: ${counts}`;
    case "cleared":
      return `Flagged (${flagWords(w.flag, saves)}), then cleared: ${counts}`;
    case "ended":
      return w.ended_why === "reverted"
        ? `Ended when reverted: ${w.after.runs} of ${w.needed} runs counted`
        : `Ended with too few runs: ${w.after.runs} of ${w.needed} in 30 days`;
  }
}

/** The agent's usual rate before the change, and the bar it sets. */
export function usualWords(w: ImprovementWatch): string {
  if (w.bar === null || w.bar === undefined) {
    return `Too little history to judge against: ${count(w.baseline_runs, "run")} before it.`;
  }
  return `Usually ${w.baseline_bad} of ${w.baseline_runs} went wrong; flagged at ${w.bar} of ${w.needed}.`;
}

const tokensNumber = new Intl.NumberFormat("en-US");

/**
 * "Tokens a run 1,000 → 1,200 · steps 4 → 5", or null with nothing counted.
 * An efficiency change is judged on its good runs, so its tokens are said as
 * theirs.
 */
export function costWords(w: ImprovementWatch, goal?: Improvement["goal"]): string | null {
  const n = (v: number | null | undefined) =>
    v === null || v === undefined ? "—" : tokensNumber.format(v);
  if (w.before.tokens == null && w.after.tokens == null) return null;
  const a = goal === "efficiency" ? "a good run" : "a run";
  return `Tokens ${a} ${n(w.before.tokens)} → ${n(w.after.tokens)} · steps ${n(w.before.steps)} → ${n(w.after.steps)}`;
}

/** "41 s", "1 min 5 s", or "—" for none. */
export function workTime(ms: number | null | undefined): string {
  if (ms === null || ms === undefined) return "—";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  const rest = s % 60;
  return rest === 0 ? `${Math.floor(s / 60)} min` : `${Math.floor(s / 60)} min ${rest} s`;
}

/** "Work time a good run 41 s → 33 s", leaving out waits for a person, or null with neither side counted. */
export function timeWords(w: ImprovementWatch): string | null {
  if (w.before.work_ms == null && w.after.work_ms == null) return null;
  return `Work time a good run ${workTime(w.before.work_ms)} → ${workTime(w.after.work_ms)}`;
}

/** What an efficiency change aims to save, in words: "Saves cost", "Saves time". */
export const SAVES_WORDS: Record<"cost" | "time", string> = {
  cost: "Saves cost",
  time: "Saves time",
};

/** The waste marks and the spread an efficiency pass is started by, in words. */
const REASON_WORDS: Record<string, string> = {
  repeated_call: "the same call made twice",
  retried_error: "a failed call made again",
  unused_tools: "tools offered and left unused",
  near_step_cap: "runs close to their step limit",
  long_tool_wait: "a long wait on a tool",
  spread: "costly runs far above the usual",
};

/** "efficiency:repeated_call:crm__lookup" as its words and tool; null for any other signature. */
export function efficiencyReason(signature: string): { words: string; target: string } | null {
  if (!signature.startsWith("efficiency:")) return null;
  const rest = signature.slice("efficiency:".length);
  const i = rest.indexOf(":");
  const name = i < 0 ? rest : rest.slice(0, i);
  return {
    words: REASON_WORDS[name] ?? name.replace(/_/g, " "),
    target: i < 0 ? "" : rest.slice(i + 1),
  };
}

/** "The pattern it targeted: in 3 of 10 runs before, 0 of 10 after", or null for a removal. */
export function patternWords(w: ImprovementWatch): string | null {
  if (w.before.signature == null || w.after.signature == null) return null;
  return `The pattern it targeted: in ${w.before.signature} of ${w.before.runs} runs before, ${w.after.signature} of ${w.after.runs} after`;
}
