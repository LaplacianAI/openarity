import { useQuery } from "@tanstack/react-query";
import { FlaskConical, TriangleAlert } from "lucide-react";
import { useId, useState } from "react";
import { getImprovementReplay } from "@/api";
import type { ImprovementReplay, ImprovementReplayRun } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Mono } from "@/components/ui-bits";
import {
  replayCost,
  replayOutcome,
  replaySummary,
  replayWorse,
  SERVED_WORDS,
} from "@/lib/improvements";
import { cn } from "@/lib/utils";

/** How much of a call's output a step shows. */
const OUTPUT_SHOWN = 200;

/** s cut to n characters (never inside one), with an ellipsis when it was cut. */
function clipped(s: string, n: number): string {
  const chars = Array.from(s);
  return chars.length <= n ? s : `${chars.slice(0, n).join("")}…`;
}

const TONE = {
  good: "text-success",
  bad: "text-warning",
  neutral: "text-muted-foreground",
} as const;

/** One try's steps, read when asked for: each call and how it was served. */
function Steps({
  teamId,
  proposalId,
  runId,
}: {
  teamId: string;
  proposalId: string;
  runId: string;
}) {
  const q = useQuery({
    queryKey: ["improvement-replay", teamId, proposalId, runId],
    queryFn: ({ signal }) => getImprovementReplay(teamId, proposalId, runId, signal),
  });
  if (q.isPending) return <p className="text-muted-foreground">Reading the steps…</p>;
  if (q.isError)
    return <p className="text-warning">The steps could not be read: {q.error.message}</p>;
  const t = q.data;
  return (
    <div className="space-y-1.5">
      {t.calls.length === 0 ? (
        <p className="text-muted-foreground">No tool call was kept for this try.</p>
      ) : (
        <ol className="space-y-1">
          {t.calls.map((c, i) => (
            // A try's calls are in the order made; one may repeat another.
            // biome-ignore lint/suspicious/noArrayIndexKey: the order is the identity
            <li key={i} className="grid gap-x-2 sm:grid-cols-[10rem_minmax(0,1fr)]">
              <span className={cn(c.how === "not_made" ? "text-warning" : "text-muted-foreground")}>
                {SERVED_WORDS[c.how]}
              </span>
              <span className="min-w-0 break-words">
                <Mono>{c.name}</Mono>
                {c.error ? (
                  <span className="text-muted-foreground"> · {c.error}</span>
                ) : c.output ? (
                  <span className="text-muted-foreground">
                    {" "}
                    · {clipped(c.output, OUTPUT_SHOWN)}
                  </span>
                ) : null}
              </span>
            </li>
          ))}
        </ol>
      )}
      {t.answer ? (
        <p className="break-words whitespace-pre-wrap">
          <span className="text-muted-foreground">Answered: </span>
          {t.answer}
        </p>
      ) : null}
    </div>
  );
}

function Run({
  teamId,
  proposalId,
  run,
}: {
  teamId: string;
  proposalId: string;
  run: ImprovementReplayRun;
}) {
  const [open, setOpen] = useState(false);
  const stepsId = useId();
  const outcome = replayOutcome(run);
  const cost = replayCost(run);
  return (
    <li
      data-replay={run.status}
      className="space-y-1 border-t border-rule pt-2 first:border-t-0 first:pt-0"
    >
      <p className="flex flex-wrap items-baseline gap-x-2">
        <span className={cn("font-medium", TONE[outcome.tone])}>{outcome.words}</span>
        <span className="min-w-0 truncate text-foreground" title={run.request ?? undefined}>
          {run.request ?? "A conversation you cannot read"}
        </span>
      </p>
      {cost ? <p className="text-muted-foreground tabular-nums">{cost}</p> : null}
      {run.diverged ? (
        <p className="text-muted-foreground">
          Went a different way: {run.not_made === 1 ? "1 step was" : `${run.not_made} steps were`}{" "}
          skipped because it would have changed or sent something.
        </p>
      ) : null}
      {run.skip_why ? (
        <p className="text-muted-foreground">
          {run.skip_why.charAt(0).toUpperCase()}
          {run.skip_why.slice(1)}.
        </p>
      ) : null}
      {run.status === "failed" && run.error ? (
        <p className="text-muted-foreground">
          {run.withheld ? "The try failed." : `The try failed: ${run.error}`}
        </p>
      ) : null}
      {run.status === "done" && run.why ? (
        <p className="text-muted-foreground">Judged: {run.why}</p>
      ) : null}
      {run.status === "done" && !run.withheld ? (
        <>
          <Button
            variant="ghost"
            size="sm"
            className="-ml-2 h-7 px-2 text-xs"
            aria-expanded={open}
            aria-controls={stepsId}
            onClick={() => setOpen((o) => !o)}
          >
            {open ? "Hide the steps" : "See the steps"}
          </Button>
          {open ? (
            <div id={stepsId}>
              <Steps teamId={teamId} proposalId={proposalId} runId={run.run.id} />
            </div>
          ) : null}
        </>
      ) : null}
    </li>
  );
}

/**
 * How a proposal fared on a few of its agent's past runs, tried before anyone
 * decided on it. Shown, never acted on: a run that went well and now goes
 * wrong is said in the warning tone, and the approver decides.
 */
export function TriedOnPastRuns({
  teamId,
  proposalId,
  replay,
  stalled = false,
}: {
  teamId: string;
  proposalId: string;
  replay: ImprovementReplay;
  /** Still trying, with no try moving for a while: the page stopped checking. */
  stalled?: boolean;
}) {
  const worse = replayWorse(replay);
  return (
    <section
      aria-label="Tried on past runs"
      data-tried={replay.status}
      className="space-y-2 rounded-md bg-sheet px-3 py-2 text-xs"
    >
      <p
        className={cn(
          "flex items-start gap-1.5 text-sm",
          worse ? "text-warning" : "text-foreground",
        )}
      >
        {worse ? (
          <TriangleAlert aria-hidden className="mt-0.5 size-3.5 shrink-0" />
        ) : (
          <FlaskConical aria-hidden className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span>{replaySummary(replay)}</span>
      </p>
      {stalled ? (
        <p className="text-muted-foreground">
          No try has moved for a while, so this page stopped checking. Reload to look again.
        </p>
      ) : null}
      {replay.runs.length > 0 ? (
        <ul className="space-y-2 pl-5">
          {replay.runs.map((r) => (
            <Run key={r.run.id} teamId={teamId} proposalId={proposalId} run={r} />
          ))}
        </ul>
      ) : null}
    </section>
  );
}
