/**
 * The Build chat: a conversation with the team's builder, read from the
 * session's builder runs and their proposals. Nothing here decides anything;
 * the brain plans and refuses.
 */
import type { Proposal, Run } from "@/api/types";
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
