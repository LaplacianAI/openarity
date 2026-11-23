import type { TeamSettings, TeamSettingsRequest } from "@/api/types";
import { parseWhole } from "@/lib/compaction";

/** The draft of the weekly efficiency pass, as typed. */
export type EfficiencyDraft = {
  efficiency: boolean;
  /** Good runs, days, the spread as a multiple ("2" for 2×), runs a sign needs. */
  effRuns: string;
  effDays: string;
  effSpread: string;
  effMarkRuns: string;
  effMargin: Margin;
};

/** Bad runs sooner an efficiency change is flagged. */
export type Margin = "0" | "1" | "2" | "3";

/** The settings of the weekly efficiency pass, as the brain names them. */
type EfficiencyKey =
  | "improvement_efficiency"
  | "improvement_efficiency_runs"
  | "improvement_efficiency_days"
  | "improvement_efficiency_spread"
  | "improvement_efficiency_mark_runs"
  | "improvement_efficiency_margin";

/** The efficiency pass's ranges and defaults, as the brain holds them. */
export const EFFICIENCY = {
  runs: { min: 10, max: 500, start: 20 },
  days: { min: 7, max: 60, start: 14 },
  /** In percent of the usual run: 200 is twice. */
  spread: { min: 125, max: 1000, start: 200 },
  markRuns: { min: 2, max: 50, start: 3 },
} as const;

/**
 * What a sign of waste is, said as the brain finds it. These thresholds are
 * the brain's own (unusedOffered, unusedCalled, nearCapShare, longToolWait in
 * apps/brain/internal/improve/efficiency.go): change both together.
 */
export const WASTE_SIGNS =
  "the same call made twice, a failed call made again, 20 or more tools offered and 2 or fewer used, a run that used 80% or more of its steps, or a tool that took over 30 seconds";

export const MARGINS: readonly { value: Margin; label: string }[] = [
  { value: "0", label: "As strictly as any change" },
  { value: "1", label: "1 bad run sooner" },
  { value: "2", label: "2 bad runs sooner" },
  { value: "3", label: "3 bad runs sooner" },
];

/** The stored margin as one the page offers; the default for one it does not. */
function marginOf(n: number | undefined): Margin {
  return MARGINS.find((m) => m.value === String(n))?.value ?? "1";
}

export function efficiencyDraftOf(s: TeamSettings): EfficiencyDraft {
  return {
    efficiency: s.improvement_efficiency ?? false,
    effRuns: String(s.improvement_efficiency_runs ?? EFFICIENCY.runs.start),
    effDays: String(s.improvement_efficiency_days ?? EFFICIENCY.days.start),
    effSpread: String((s.improvement_efficiency_spread ?? EFFICIENCY.spread.start) / 100),
    effMarkRuns: String(s.improvement_efficiency_mark_runs ?? EFFICIENCY.markRuns.start),
    effMargin: marginOf(s.improvement_efficiency_margin),
  };
}

/** A spread typed as a multiple, "2", "1.5×" or "1,5", in percent; null when it is not one. */
export function spreadOf(raw: string): number | null {
  const t = raw.trim().replace(/[×x]$/i, "").trim().replace(",", ".");
  if (!/^\d+(\.\d{1,2})?$/.test(t)) return null;
  return Math.round(Number(t) * 100);
}

function wholeNumber(raw: string): number | null {
  const t = raw.trim();
  return /^\d+$/.test(t) ? Number(t) : null;
}

/** What is wrong with each efficiency number, by field; empty when nothing is. */
export type EfficiencyProblems = Partial<
  Record<"effRuns" | "effDays" | "effSpread" | "effMarkRuns", string>
>;

/**
 * Each number checked as the brain checks it. Nothing is said of a pass that
 * is off: its numbers are hidden and not sent.
 */
export function efficiencyProblems(d: EfficiencyDraft): EfficiencyProblems {
  if (!d.efficiency) return {};
  const out: EfficiencyProblems = {};
  const counts = [
    ["effRuns", EFFICIENCY.runs],
    ["effDays", EFFICIENCY.days],
    ["effMarkRuns", EFFICIENCY.markRuns],
  ] as const;
  for (const [field, range] of counts) {
    const n = wholeNumber(d[field]);
    if (n === null || n < range.min || n > range.max) {
      out[field] = `A whole number from ${range.min} to ${range.max}.`;
    }
  }
  const runs = wholeNumber(d.effRuns);
  const marks = wholeNumber(d.effMarkRuns);
  if (!out.effMarkRuns && runs !== null && marks !== null && marks > runs) {
    out.effMarkRuns = `No more than the good runs needed, ${runs}.`;
  }
  const pct = spreadOf(d.effSpread);
  if (pct === null || pct < EFFICIENCY.spread.min || pct > EFFICIENCY.spread.max) {
    out.effSpread = "A number from 1.25 to 10, such as 2 for twice.";
  }
  return out;
}

const NAMES: Record<keyof EfficiencyProblems, string> = {
  effRuns: "Good runs needed",
  effDays: "Days to look back",
  effSpread: "Costliest runs, against the usual",
  effMarkRuns: "Runs a sign of waste needs",
};

/**
 * What the efficiency pass's Save sends: the switch when it changed and,
 * while the pass is on, each number that changed. A number that is wrong is
 * a sentence naming its field instead.
 */
export function efficiencyRequest(
  d: EfficiencyDraft,
  base: EfficiencyDraft,
): Pick<TeamSettingsRequest, EfficiencyKey> | string {
  const out: Pick<TeamSettingsRequest, EfficiencyKey> = {};
  if (d.efficiency !== base.efficiency) out.improvement_efficiency = d.efficiency;
  if (!d.efficiency) return out;
  const problems = efficiencyProblems(d);
  const first = (Object.keys(NAMES) as (keyof EfficiencyProblems)[]).find((f) => problems[f]);
  const why = first ? problems[first] : undefined;
  if (first && why) return `${NAMES[first]}: ${why.charAt(0).toLowerCase()}${why.slice(1)}`;
  const counts = [
    ["effRuns", "improvement_efficiency_runs"],
    ["effDays", "improvement_efficiency_days"],
    ["effMarkRuns", "improvement_efficiency_mark_runs"],
  ] as const;
  for (const [field, key] of counts) {
    if (parseWhole(d[field]) !== parseWhole(base[field])) out[key] = Number(d[field].trim());
  }
  if (spreadOf(d.effSpread) !== spreadOf(base.effSpread)) {
    out.improvement_efficiency_spread = spreadOf(d.effSpread) ?? undefined;
  }
  if (d.effMargin !== base.effMargin) out.improvement_efficiency_margin = Number(d.effMargin);
  return out;
}
