import type { Run, RunStatus, TeamSummary } from "@/api/types";

/** Every run status, in the order a reader looks for them. */
export const RUN_STATUSES: readonly RunStatus[] = [
  "running",
  "queued",
  "failed",
  "succeeded",
  "cancelled",
];

export const STATUS_WORDS: Record<RunStatus, string> = {
  running: "Running",
  queued: "Queued",
  failed: "Failed",
  succeeded: "Succeeded",
  cancelled: "Cancelled",
};

export const DAY_MS = 24 * 60 * 60 * 1000;

/** "3 runs", "1 run": a number with its noun, for a finding. */
export function count(n: number, one: string, many = `${one}s`): string {
  return `${n.toLocaleString("en-US")} ${n === 1 ? one : many}`;
}

/** "12 runs in the last 24 hours, 3 failed." */
export function dayFinding(summary: TeamSummary): string {
  const counts = summary.runs_24h;
  const total = Object.values(counts).reduce((n, c) => n + c, 0);
  if (total === 0) return "No runs in the last 24 hours.";
  const runs = `${count(total, "run")} in the last 24 hours`;
  return counts.failed > 0
    ? `${runs}, ${counts.failed.toLocaleString("en-US")} failed.`
    : `${runs}.`;
}

/** Past this many names, the rest are counted rather than named. */
const NAMED = 3;

/**
 * Whom a run handed work to, as one line under its agent: "→ clerk, billing",
 * or "→ a, b, c and 2 more". Null when it handed none, or the brain did not say.
 */
export function handedTo(run: Pick<Run, "delegates">): string | null {
  const names = run.delegates ?? [];
  if (names.length === 0) return null;
  const shown = names.slice(0, NAMED).join(", ");
  const rest = names.length - NAMED;
  return rest > 0 ? `→ ${shown} and ${rest} more` : `→ ${shown}`;
}
