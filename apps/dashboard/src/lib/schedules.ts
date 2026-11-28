/**
 * What a schedule or a watcher is doing, said for a person: its state, why
 * it paused, what each fire or check did, and the one line its page opens
 * with. Every word here is shown; none names a field or a code.
 */
import type {
  PausedReason,
  Schedule,
  ScheduledNewsItem,
  SchedulePlace,
  Watcher,
  WatcherStatus,
} from "@/api/types";
import { lengthSays, timeBoth } from "@/lib/when";

/** A status as `StatusPill` draws it: its word, which also picks its look. */
export function statusWord(status: string): string {
  return status === "waiting_secret" ? "needs its secret" : status;
}

/** Why one paused, in a sentence. */
export function pausedSays(
  reason: PausedReason | string | undefined,
  failing = "5 failed runs in a row",
): string {
  switch (reason) {
    case "by_person":
      return "Paused by a person.";
    case "failing":
      return `Paused after ${failing}.`;
    case "maker_cannot_run":
      return "Paused: its maker may no longer run this agent.";
    case "maker_gone":
      return "Paused: its maker left the team.";
    case "target_gone":
      return "Paused: where it answered, or the agent that ran it, is gone.";
    default:
      return "Paused.";
  }
}

/**
 * What one fire or check did. `look` is the status word `StatusPill` draws,
 * so a skipped or missed fire reads as something that did not happen, a
 * failure as a failure, and the quiet outcomes stay quiet.
 */
export type Outcome = { look: string; says: string };

const FIRES: Record<string, Outcome> = {
  started: { look: "running", says: "Started" },
  late: { look: "waiting", says: "Started late" },
  catch_up: { look: "waiting", says: "Ran once for the times it missed" },
  missed: { look: "missed", says: "Missed: the brain was down; one catch-up ran for it" },
  skipped_busy: { look: "skipped", says: "Skipped: its conversation was busy" },
  skipped_cap: { look: "skipped", says: "Skipped: the team's runs an hour were used" },
  skipped_maker: { look: "skipped", says: "Skipped: its maker may not run it now" },
  skipped_target: { look: "skipped", says: "Skipped: where it answers could not be reached" },
  silent: { look: "succeeded", says: "Ran, with nothing to say" },
  answered: { look: "answered", says: "Answered" },
  failed: { look: "failed", says: "Failed" },
  cancelled: { look: "cancelled", says: "Cancelled" },
};

const CHECKS: Record<string, Outcome> = {
  unchanged: { look: "unchanged", says: "Unchanged" },
  baseline: { look: "unchanged", says: "First look: kept to compare against" },
  changed: { look: "changed", says: "Changed: a run started" },
  held: { look: "waiting", says: "Changed, held for its cooldown" },
  error: { look: "failed", says: "Could not read it" },
  delivered: { look: "changed", says: "Called: a run started" },
  duplicate: { look: "unchanged", says: "Called again with the same delivery" },
  skipped_cap: { look: "skipped", says: "Skipped: the team's runs an hour were used" },
  skipped_maker: { look: "skipped", says: "Skipped: its maker may not run it now" },
  skipped_busy: { look: "skipped", says: "Skipped: its conversation was busy" },
};

export function fireSays(outcome: string): Outcome {
  return FIRES[outcome] ?? { look: outcome, says: outcome.replaceAll("_", " ") };
}

export function checkSays(outcome: string): Outcome {
  return CHECKS[outcome] ?? FIRES[outcome] ?? { look: outcome, says: outcome.replaceAll("_", " ") };
}

/** A news item's outcome, whichever kind made it. */
export function newsSays(item: ScheduledNewsItem): Outcome {
  return item.watcher_id ? checkSays(item.outcome) : fireSays(item.outcome);
}

/** Why a place cannot be picked. */
export function placeReason(p: SchedulePlace): string | null {
  if (p.available) return null;
  switch (p.reason) {
    case "needs_channel_write":
      return "Posting at a channel's top level needs channel:write";
    case "not_in_channel":
      return "The app is not in it: invite it first";
    default:
      return "Not available";
  }
}

/** What a watcher looks at, and how often. */
export function watchesSays(w: Pick<Watcher, "source" | "url" | "every_seconds">): string {
  if (w.source === "webhook") return "its address is called";
  const what = w.source === "feed" ? "the feed" : "the page";
  const at = w.url ? ` ${w.url.replace(/^https?:\/\//, "")}` : "";
  const every = w.every_seconds ? `, every ${lengthSays(w.every_seconds)}` : "";
  return `${what}${at}${every}`;
}

/** The one line a schedule's page opens with: what is next, or what needs the reader. */
export function scheduleFinding(s: Schedule, reader?: string): string | null {
  if (s.status === "proposed") return "Waiting for your yes: an agent proposed it.";
  if (s.waiting_change)
    return `An agent asked ${s.waiting_change.says}. It runs as it is until you confirm.`;
  if (s.status === "paused") return pausedSays(s.paused_reason);
  if (s.status === "done") return "It fired once, as it was set to, and will not fire again.";
  const [first, second] = s.upcoming;
  if (!first) return null;
  const a = timeBoth(first, s.timezone, reader).here;
  return second
    ? `Next fires ${a}, then ${timeBoth(second, s.timezone, reader).here}.`
    : `Next fires ${a}.`;
}

/** The same for a watcher. */
export function watcherFinding(w: Watcher): string | null {
  if (w.status === "proposed") return "Waiting for your yes: an agent proposed it.";
  if (w.status === "waiting_secret")
    return "It starts once it has a secret: generate one, and give it to the sender.";
  if (w.waiting_change)
    return `An agent asked ${w.waiting_change.says}. It runs as it is until you confirm.`;
  if (w.status === "paused")
    return pausedSays(w.paused_reason, "5 failed runs in a row, or a day of failing to read");
  return null;
}

export const WATCHER_STATUSES: WatcherStatus[] = ["proposed", "waiting_secret", "active", "paused"];
