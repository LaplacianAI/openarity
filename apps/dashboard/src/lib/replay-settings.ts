import type { TeamSettings, TeamSettingsRequest } from "@/api/types";
import { grouped, parseWhole } from "@/lib/compaction";

/** The draft of trying changes on past runs, as typed. */
export type ReplayDraft = {
  replay: boolean;
  replayRuns: string;
  replayTokens: string;
  replayMinutes: string;
  replayDaily: string;
};

type ReplayKey =
  | "improvement_replay"
  | "improvement_replay_runs"
  | "improvement_replay_tokens"
  | "improvement_replay_minutes"
  | "improvement_replay_daily";

/** The ranges and defaults, as the brain holds them. */
export const REPLAY = {
  runs: { min: 1, max: 10, start: 4 },
  tokens: { min: 20_000, max: 1_000_000, start: 200_000 },
  minutes: { min: 1, max: 30, start: 10 },
  daily: { min: 1, max: 500, start: 40 },
} as const;

type NumberField = "replayRuns" | "replayTokens" | "replayMinutes" | "replayDaily";

const FIELDS: readonly {
  field: NumberField;
  key: Exclude<ReplayKey, "improvement_replay">;
  range: { min: number; max: number };
  name: string;
}[] = [
  {
    field: "replayRuns",
    key: "improvement_replay_runs",
    range: REPLAY.runs,
    name: "Past runs to try it on",
  },
  {
    field: "replayTokens",
    key: "improvement_replay_tokens",
    range: REPLAY.tokens,
    name: "Tokens for one try",
  },
  {
    field: "replayMinutes",
    key: "improvement_replay_minutes",
    range: REPLAY.minutes,
    name: "Minutes for one try",
  },
  {
    field: "replayDaily",
    key: "improvement_replay_daily",
    range: REPLAY.daily,
    name: "Tries a day",
  },
];

/** The numbers that are hidden, and never sent, while trying is off. */
export const REPLAY_NUMBERS: readonly NumberField[] = FIELDS.map((f) => f.field);

export function replayDraftOf(s: TeamSettings): ReplayDraft {
  return {
    replay: s.improvement_replay ?? false,
    replayRuns: String(s.improvement_replay_runs ?? REPLAY.runs.start),
    replayTokens: grouped(s.improvement_replay_tokens ?? REPLAY.tokens.start),
    replayMinutes: String(s.improvement_replay_minutes ?? REPLAY.minutes.start),
    replayDaily: String(s.improvement_replay_daily ?? REPLAY.daily.start),
  };
}

/** What is wrong with each number, by field; nothing while trying is off. */
export type ReplayProblems = Partial<Record<NumberField, string>>;

export function replayProblems(d: ReplayDraft): ReplayProblems {
  if (!d.replay) return {};
  const out: ReplayProblems = {};
  for (const { field, range } of FIELDS) {
    const n = parseWhole(d[field]);
    if (n === null || n < range.min || n > range.max) {
      out[field] =
        `A whole number from ${range.min.toLocaleString("en-US")} to ${range.max.toLocaleString("en-US")}.`;
    }
  }
  return out;
}

/**
 * What Save sends of trying on past runs: the switch when it changed and,
 * while it is on, each number that changed. A number that is wrong is a
 * sentence naming its field instead.
 */
export function replayRequest(
  d: ReplayDraft,
  base: ReplayDraft,
): Pick<TeamSettingsRequest, ReplayKey> | string {
  const out: Pick<TeamSettingsRequest, ReplayKey> = {};
  if (d.replay !== base.replay) out.improvement_replay = d.replay;
  if (!d.replay) return out;
  const problems = replayProblems(d);
  for (const { field, key, name } of FIELDS) {
    const why = problems[field];
    if (why) return `${name}: ${why.charAt(0).toLowerCase()}${why.slice(1)}`;
    const n = parseWhole(d[field]);
    if (n !== null && n !== parseWhole(base[field])) out[key] = n;
  }
  return out;
}
