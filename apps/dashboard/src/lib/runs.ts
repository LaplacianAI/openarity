import type { RunStatus, TeamSummary } from "@/api/types";

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
