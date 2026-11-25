import type { BrowserProvider, SandboxProvider, SearchProvider, TeamSettings } from "@/api/types";
import type { Picked } from "@/components/picker";
import { type CompactionDraft, grouped, parseWhole } from "@/lib/compaction";
import { type EfficiencyDraft, efficiencyDraftOf, spreadOf } from "@/lib/efficiency-settings";
import { REPLAY_NUMBERS, type ReplayDraft, replayDraftOf } from "@/lib/replay-settings";
import {
  sameWorkspace,
  WORKSPACE_FIELD_SET,
  WORKSPACE_FIELDS,
  type WorkspaceDraft,
  workspaceOf,
} from "@/lib/team-settings/workspace";

type DraftFields = EfficiencyDraft &
  WorkspaceDraft & {
    modelOn: boolean;
    modelName: string;
    maxTokens: string;
    temperature: string;
    provider: SearchProvider;
    url: string;
    key: string;
    dropKey: boolean;
    sandbox: SandboxProvider;
    sandboxURL: string;
    /** E2B only; empty for the default template. */
    sandboxTemplate: string;
    /** Typed only; the stored key is never read back. */
    sandboxKey: string;
    browser: BrowserProvider;
    browserURL: string;
    /** Typed only; the stored key is never read back. */
    browserKey: string;
    /** A self-hosted browser only: reach it with no key. */
    dropBrowserKey: boolean;
    improvement: boolean;
    /** Whether runs are judged on a model of their own, not the team's. */
    judgeOn: boolean;
    judgeName: string;
    judgeMaxTokens: string;
    judgeTemperature: string;
    /** The share of good runs studied, as typed. */
    sampleShare: string;
    /** The most improver passes in a day, as typed. */
    dailyPasses: string;
    /** What one try at a pass may spend, as typed. */
    passTokens: string;
    passMinutes: string;
    /** The judged runs a daily pass waits for, as typed. */
    dailyRuns: string;
    /** Messages, and the token lines, as typed: compared by value. */
    compaction: string;
    tokensMin: string;
    tokensMax: string;
    /** The chosen hub agent; null for the built-in orchestrator. */
    hub: Picked | null;
  };

export type SettingsDraft = DraftFields & ReplayDraft;

export function draftOf(s: TeamSettings): SettingsDraft {
  return {
    modelOn: s.model !== null,
    modelName: s.model?.name ?? "",
    maxTokens: s.model ? String(s.model.max_tokens) : "200000",
    temperature: s.model?.temperature === undefined ? "" : String(s.model.temperature),
    provider: s.search.provider,
    url: s.search.url ?? "",
    key: "",
    dropKey: false,
    // A brain from before sandboxes sends none: the team has none.
    sandbox: s.sandbox?.provider ?? "none",
    sandboxURL: s.sandbox?.url ?? "",
    sandboxTemplate: s.sandbox?.template ?? "",
    sandboxKey: "",
    ...workspaceOf(s.sandbox),
    // A brain from before browsers sends none: the team has none.
    browser: s.browser?.provider ?? "none",
    browserURL: s.browser?.url ?? "",
    browserKey: "",
    dropBrowserKey: false,
    improvement: s.improvement,
    judgeOn: s.judge_model != null,
    judgeName: s.judge_model?.name ?? "",
    judgeMaxTokens: s.judge_model ? String(s.judge_model.max_tokens) : "4096",
    judgeTemperature:
      s.judge_model?.temperature === undefined ? "" : String(s.judge_model?.temperature ?? ""),
    sampleShare: String(s.improvement_sample_share ?? 10),
    dailyPasses: String(s.improvement_daily_passes ?? 20),
    passTokens: grouped(s.improvement_pass_tokens ?? 400_000),
    passMinutes: String(s.improvement_pass_minutes ?? 20),
    dailyRuns: String(s.improvement_daily_runs ?? 5),
    ...efficiencyDraftOf(s),
    ...replayDraftOf(s),
    compaction: String(s.compaction_messages),
    tokensMin: grouped(s.compaction_tokens_min),
    tokensMax: grouped(s.compaction_tokens_max),
    hub: s.hub,
  };
}

/** The groups of the page, each saved on its own. */
export type Group =
  | "model"
  | "hub"
  | "compaction"
  | "search"
  | "sandbox"
  | "browser"
  | "improvement";

/** Which of the draft's fields each group owns. */
export const FIELDS: Record<Group, (keyof SettingsDraft)[]> = {
  model: ["modelOn", "modelName", "maxTokens", "temperature"],
  hub: ["hub"],
  compaction: ["compaction", "tokensMin", "tokensMax"],
  search: ["provider", "url", "key", "dropKey"],
  sandbox: ["sandbox", "sandboxURL", "sandboxTemplate", "sandboxKey", ...WORKSPACE_FIELDS],
  browser: ["browser", "browserURL", "browserKey", "dropBrowserKey"],
  improvement: [
    "improvement",
    "judgeOn",
    "judgeName",
    "judgeMaxTokens",
    "judgeTemperature",
    "sampleShare",
    "dailyPasses",
    "passTokens",
    "passMinutes",
    "dailyRuns",
    "efficiency",
    "effRuns",
    "effDays",
    "effSpread",
    "effMarkRuns",
    "effMargin",
    "replay",
    "replayRuns",
    "replayTokens",
    "replayMinutes",
    "replayDaily",
  ],
};

export function sameValue(
  a: SettingsDraft[keyof SettingsDraft],
  b: SettingsDraft[keyof SettingsDraft],
) {
  // The hub is an object: two picks of the same agent are the same choice.
  if (a !== null && typeof a === "object" && b !== null && typeof b === "object") {
    return a.name === b.name;
  }
  return a === b;
}

/** Fields compared by the number they hold: "60000" is the stored 60,000. */
const WHOLE_NUMBER_FIELDS = new Set<keyof SettingsDraft>([
  "compaction",
  "tokensMin",
  "tokensMax",
  "sampleShare",
  "dailyPasses",
  "passTokens",
  "passMinutes",
  "dailyRuns",
  "effRuns",
  "effDays",
  "effMarkRuns",
  "replayRuns",
  "replayTokens",
  "replayMinutes",
  "replayDaily",
]);

/** The efficiency pass's numbers: hidden, and never sent, while it is off. */
const EFFICIENCY_NUMBER_FIELDS: ReadonlySet<keyof SettingsDraft> = new Set([
  "effRuns",
  "effDays",
  "effSpread",
  "effMarkRuns",
  "effMargin",
]);

/** Trying on past runs' numbers: hidden, and never sent, while it is off. */
const REPLAY_NUMBER_FIELDS: ReadonlySet<keyof SettingsDraft> = new Set(REPLAY_NUMBERS);

/** Whether a group's fields differ from what the brain holds. */
export function isDirty(group: Group, d: SettingsDraft, stored: TeamSettings): boolean {
  const base = draftOf(stored);
  const changed = FIELDS[group].some((f) =>
    WORKSPACE_FIELD_SET.has(f) ||
    (EFFICIENCY_NUMBER_FIELDS.has(f) && !d.efficiency) ||
    (REPLAY_NUMBER_FIELDS.has(f) && !d.replay)
      ? false
      : f === "effSpread"
        ? spreadOf(d.effSpread) !== spreadOf(base.effSpread)
        : WHOLE_NUMBER_FIELDS.has(f)
          ? (parseWhole(d[f] as string) ?? d[f]) !== (parseWhole(base[f] as string) ?? base[f])
          : !sameValue(d[f], base[f]),
  );
  // A workspace's times and size are compared by what they mean: a size
  // typed and then put back to the provider's is no change.
  return changed || (group === "sandbox" && !sameWorkspace(d, base));
}

/** The compaction fields of a draft, as lib/compaction checks them. */
export function compactionOf(d: SettingsDraft): CompactionDraft {
  return { messages: d.compaction, least: d.tokensMin, most: d.tokensMax };
}

export function wholeNumber(raw: string): number | null {
  const t = raw.trim();
  return /^\d+$/.test(t) ? Number(t) : null;
}

/** A copy of `draft` with the named fields taken from `from`. */
export function withFields(
  draft: SettingsDraft,
  from: SettingsDraft,
  fields: readonly (keyof SettingsDraft)[],
): SettingsDraft {
  const out = { ...draft };
  for (const f of fields) copyField(out, from, f);
  return out;
}

function copyField<K extends keyof SettingsDraft>(to: SettingsDraft, from: SettingsDraft, k: K) {
  to[k] = from[k];
}

/** The draft's compaction fields from lib/compaction's names: the inverse of compactionOf. */
export function compactionPatch(p: Partial<CompactionDraft>): Partial<SettingsDraft> {
  return {
    ...(p.messages === undefined ? {} : { compaction: p.messages }),
    ...(p.least === undefined ? {} : { tokensMin: p.least }),
    ...(p.most === undefined ? {} : { tokensMax: p.most }),
  };
}
