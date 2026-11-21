/**
 * How a run was judged, in one line: the verdict as an icon and its word, what
 * was missed, and what members said, kept apart from the judge's own word.
 */
import {
  CircleAlert,
  CircleCheck,
  CircleHelp,
  CircleMinus,
  type LucideIcon,
  ThumbsDown,
  ThumbsUp,
  TriangleAlert,
} from "lucide-react";

import type { RunOutcome, RunVerdict } from "@/api/types";
import { cn } from "@/lib/utils";

const VERDICTS: Record<RunVerdict, { word: string; tone: string; icon: LucideIcon }> = {
  done: { word: "Done", tone: "text-succeeded", icon: CircleCheck },
  partly: { word: "Partly done", tone: "text-waiting", icon: CircleAlert },
  not_done: { word: "Not done", tone: "text-failed", icon: CircleMinus },
  wrong: { word: "Wrong", tone: "text-failed", icon: TriangleAlert },
  cant_tell: { word: "Can't tell", tone: "text-cancelled", icon: CircleHelp },
};

function members(n: number): string {
  return n === 1 ? "1 member" : `${n} members`;
}

export function OutcomeLine({ outcome }: { outcome: RunOutcome }) {
  const v = VERDICTS[outcome.verdict];
  const Icon = v.icon;
  const { up, down } = outcome.feedback;
  const missed = outcome.missed.trim();
  return (
    <p
      data-outcome={outcome.verdict}
      className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm"
    >
      <span className="inline-flex items-center gap-1.5">
        <Icon aria-hidden className={cn("size-3.5 shrink-0", v.tone)} />
        <span className="sr-only">Judged: </span>
        <span className={cn("font-medium", v.tone)}>{v.word}</span>
      </span>
      {missed ? (
        <span className="min-w-0 max-w-[75ch] break-words">
          <span className="text-muted-foreground">Missed: </span>
          {missed}
        </span>
      ) : null}
      {up + down > 0 ? (
        <span className="inline-flex items-center gap-2.5 text-xs text-muted-foreground">
          {up > 0 ? (
            <span
              className="inline-flex items-center gap-1"
              title={`${members(up)} said it was right`}
            >
              <ThumbsUp aria-hidden className="size-3.5" />
              <span className="tabular-nums">{up}</span>
              <span className="sr-only">{members(up)} said it was right</span>
            </span>
          ) : null}
          {down > 0 ? (
            <span
              className="inline-flex items-center gap-1"
              title={`${members(down)} said it was wrong`}
            >
              <ThumbsDown aria-hidden className="size-3.5" />
              <span className="tabular-nums">{down}</span>
              <span className="sr-only">{members(down)} said it was wrong</span>
            </span>
          ) : null}
        </span>
      ) : null}
    </p>
  );
}
