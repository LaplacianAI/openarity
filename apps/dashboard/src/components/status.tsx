/**
 * Every status in the product, drawn one way: its word, in its tone, with an
 * icon or a dot so the colour is never the only signal.
 *
 * Runs carry an icon for each state, since a reader scans a run list for the
 * failed and the running. Sessions and senders carry a dot: their states are
 * quieter, and a column of icons there would shout.
 */
import {
  Ban,
  CheckCircle2,
  CircleDashed,
  Clock,
  Loader2,
  type LucideIcon,
  XCircle,
} from "lucide-react";

import { cn } from "@/lib/utils";

type Look = { tone: string; icon?: LucideIcon; spin?: boolean };

const LOOKS: Record<string, Look> = {
  // A run's states.
  queued: { tone: "text-waiting", icon: Clock },
  running: { tone: "text-running", icon: Loader2, spin: true },
  succeeded: { tone: "text-succeeded", icon: CheckCircle2 },
  failed: { tone: "text-failed", icon: XCircle },
  cancelled: { tone: "text-cancelled", icon: Ban },
  // Someone or something waiting on a person.
  waiting: { tone: "text-waiting", icon: Clock },
  pending: { tone: "text-waiting", icon: Clock },
  // A session's states.
  open: { tone: "text-succeeded" },
  active: { tone: "text-succeeded" },
  idle: { tone: "text-waiting" },
  closed: { tone: "text-cancelled" },
  paused: { tone: "text-cancelled" },
};

const UNKNOWN: Look = { tone: "text-muted-foreground", icon: CircleDashed };

export function isLive(status: string): boolean {
  return status === "queued" || status === "running";
}

/**
 * A status as its word, in its tone. `pill` puts it in a round outline, for
 * beside a title; inline, it sits in a row or a sentence.
 */
export function StatusPill({
  status,
  pill = false,
  className,
}: {
  status: string;
  pill?: boolean;
  className?: string;
}) {
  const look = LOOKS[status] ?? UNKNOWN;
  const Icon = look.icon;
  return (
    <span
      data-status={status}
      className={cn(
        "inline-flex items-center gap-1 text-xs whitespace-nowrap",
        pill &&
          "rounded-full px-2 py-0.5 font-medium shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]",
        className,
      )}
    >
      {Icon ? (
        <Icon
          aria-hidden
          className={cn("size-3.5 shrink-0", look.tone, look.spin && "animate-spin")}
        />
      ) : (
        <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full bg-current", look.tone)} />
      )}
      <span>{status}</span>
    </span>
  );
}

/** While a run streams: the one place teal marks a state rather than an action. */
export function LiveBadge() {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full bg-running/10 px-2 py-0.5 text-xs font-medium text-running">
      <span aria-hidden className="size-1.5 rounded-full bg-running" />
      Live
    </span>
  );
}

/**
 * How many are waiting on the reader. Only on a queue that needs action; a
 * count of things that need nothing is decoration. `n` is already a word
 * ("3", "50+"), since a cursor-paged count may be a floor.
 */
export function Count({ n, label = "waiting" }: { n: string; label?: string }) {
  return (
    <span className="rounded-full bg-waiting px-1.5 text-xs font-medium text-sheet tabular-nums">
      <span className="sr-only">, </span>
      {n}
      <span className="sr-only"> {label}</span>
    </span>
  );
}
