/**
 * One run, read in full: what it was asked, what it answered, and the steps
 * it took to get there, followed live while it is still running.
 *
 * Everything shown here came from a person, a model or a tool. It is rendered
 * as React text — or, for the answer alone, as markdown with raw HTML off —
 * and never as markup.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  AlertTriangle,
  ArrowUpLeft,
  CheckCircle2,
  ChevronRight,
  Circle,
  CircleDot,
  type Clock,
  Loader2,
  Users,
  Wrench,
  XCircle,
} from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { cancelRun, followRunEvents, getRun } from "@/api";
import type { Run, RunEvent } from "@/api/types";
import { CopyId } from "@/components/facts";
import { Markdown } from "@/components/markdown";
import { isLive, LiveBadge, StatusPill as RunStatus } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { explainRunError } from "@/lib/run-error";
import {
  argumentPairs,
  delegateReport,
  type FetchedPage,
  fetchedPage,
  type GraphHit,
  graphHits,
  groupTrace,
  type PlanItem,
  type PlanStatus,
  pretty,
  type SearchResults,
  type Step,
  searchResults,
  stepsOf,
  type ToolCall,
  type Trace,
} from "@/lib/run-trace";
import { cn } from "@/lib/utils";

/* ----------------------------------------------------------------- status */

export { isLive, StatusPill as RunStatus } from "@/components/status";

/* ------------------------------------------------------------------ format */

export function tokens(n: number): string {
  return `${n.toLocaleString("en-US")} token${n === 1 ? "" : "s"}`;
}

export function duration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)} s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m < 60) return `${m}m ${rest}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/**
 * The line under a run's title that answers what the screen is for: where the
 * run failed, or which step its tokens went to. Null while it runs, and when
 * there is nothing the trace would not show at a glance.
 */
export function runFinding(run: Run, trace: Trace): string | null {
  const status = trace.finished?.status || run.status;
  const steps = stepsOf(trace);
  // As the trace numbers them: the agent's count, else the position.
  const numberOf = (i: number) => steps[i]?.number ?? i + 1;

  if (status === "failed") {
    const at = steps.findIndex((st) => st.calls.some((c) => c.result?.error));
    if (at >= 0) {
      const call = steps[at]?.calls.find((c) => c.result?.error);
      return `Failed at step ${numberOf(at)}, in ${call?.name}.`;
    }
    return steps.length > 0
      ? `Failed at step ${numberOf(steps.length - 1)}.`
      : "Failed before its first step.";
  }
  if (status !== "succeeded" || steps.length < 2) return null;

  const total = steps.reduce((n, st) => n + st.usage.total, 0);
  if (total === 0) return null;
  let most = 0;
  steps.forEach((st, i) => {
    if (st.usage.total > (steps[most]?.usage.total ?? 0)) most = i;
  });
  const share = Math.round(((steps[most]?.usage.total ?? 0) / total) * 100);
  return `Step ${numberOf(most)} spent ${share}% of the tokens.`;
}

/** Ticks once a second while `on`, so a live duration counts up. */
function useNow(on: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!on) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [on]);
  return now;
}

/* ----------------------------------------------------------------- stream */

/** What has been read of a run's stream, kept across mounts. */
type Followed = { events: RunEvent[]; ended: boolean };

/**
 * Follows a run's events for as long as this is mounted on that run. Choosing
 * another run or leaving the page aborts the stream: a running run's stream
 * otherwise stays open for as long as the run does.
 *
 * What was read is kept in the query cache under the run, so mounting the
 * same run again does not start its trace over: a run whose stream has ended
 * is shown from the cache and not followed again, and one still going is
 * followed on from the last event seen.
 */
export function useRunEvents(teamId: string, runId: string, onEnded: () => void) {
  const qc = useQueryClient();
  const key = useMemo(() => ["run-events", teamId, runId], [teamId, runId]);
  const [events, setEvents] = useState<RunEvent[]>(
    () => qc.getQueryData<Followed>(key)?.events ?? [],
  );
  const [failed, setFailed] = useState<string | null>(null);

  // A ref, because onEnded is a fresh closure every render and restarting the
  // stream for it would replay every event.
  const onEndedRef = useRef(onEnded);
  onEndedRef.current = onEnded;

  useEffect(() => {
    const held = qc.getQueryData<Followed>(key) ?? { events: [], ended: false };
    setEvents(held.events);
    setFailed(null);
    if (held.ended) return;

    const abort = new AbortController();
    let read = held.events;
    const last = read.at(-1)?.seq ?? null;
    followRunEvents(
      teamId,
      runId,
      (e) => {
        read = [...read, e];
        qc.setQueryData<Followed>(key, { events: read, ended: e.kind === "end" });
        setEvents(read);
        if (e.kind === "end") onEndedRef.current();
      },
      abort.signal,
      last,
    ).catch((err: unknown) => {
      // Aborted by this effect's own cleanup — another run was chosen, or
      // the page went away — which is not a failure to report.
      if (abort.signal.aborted) return;
      setFailed(err instanceof Error ? err.message : String(err));
    });
    return () => abort.abort();
  }, [qc, key, teamId, runId]);

  return { events, failed };
}

/* ----------------------------------------------------------------- detail */

export function RunDetail({
  teamId,
  run,
  canCancel,
  onChanged,
}: {
  teamId: string;
  run: Run;
  canCancel: boolean;
  /** The run's row is stale: it was cancelled, or its stream ended. */
  onChanged: () => void;
}) {
  const { events, failed } = useRunEvents(teamId, run.id, onChanged);
  return (
    <RunView
      teamId={teamId}
      run={run}
      canCancel={canCancel}
      onChanged={onChanged}
      events={events}
      failed={failed}
    />
  );
}

/**
 * One run, drawn from the events read so far. Kept apart from the stream so
 * the states gallery can show every state of a run from fixtures.
 */
export function RunView({
  teamId,
  run,
  canCancel,
  onChanged,
  events,
  failed,
}: {
  teamId: string;
  run: Run;
  canCancel: boolean;
  onChanged: () => void;
  events: RunEvent[];
  /** Why the stream could not be followed, if it could not. */
  failed: string | null;
}) {
  const trace = useMemo(() => groupTrace(events), [events]);
  const live = isLive(run.status) && !trace.ended && !failed;
  const now = useNow(live);

  const steps = stepsOf(trace).length;
  // Until the stream has answered, the trace's figures are unknown, not zero.
  const reading = events.length === 0 && !failed;
  const delegates = [
    ...new Set(
      stepsOf(trace).flatMap((st) =>
        st.calls.flatMap((c) =>
          c.name === "delegate" && c.delegated?.agent ? [c.delegated.agent] : [],
        ),
      ),
    ),
  ];
  const spent = trace.usage.total > 0 ? trace.usage.total : run.input_tokens + run.output_tokens;
  const began = run.started_at ? Date.parse(run.started_at) : null;
  const took =
    began === null ? null : (run.finished_at ? Date.parse(run.finished_at) : now) - began;
  const error = run.error ?? (trace.finished?.error || null);
  const finding = runFinding(run, trace);

  return (
    <article aria-label={`Run of ${run.agent}`} className="space-y-6">
      <header className="space-y-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="truncate text-lg font-semibold tracking-tight">{run.agent}</h2>
              <RunStatus status={run.status} pill />
              {run.hub ? (
                <span
                  data-hub
                  title="Started for the team: its hub answered"
                  className="rounded-full bg-card px-2 py-0.5 text-xs font-medium"
                >
                  hub
                </span>
              ) : null}
              {live ? <LiveBadge /> : null}
            </div>
            {finding ? (
              <p data-finding className="text-sm">
                {finding}
              </p>
            ) : null}
            {delegates.length > 0 ? (
              <p
                data-delegates
                className="flex flex-wrap items-center gap-1 text-sm text-muted-foreground"
              >
                <Users aria-hidden className="size-3.5" />
                handed work to{" "}
                <span className="font-medium text-foreground">{delegates.join(", ")}</span>
              </p>
            ) : null}
            {run.parent_run_id ? (
              <ParentLink teamId={teamId} sessionId={run.session_id} parentId={run.parent_run_id} />
            ) : null}
            <dl className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-muted-foreground">
              <Meta label="Model">{reading ? "…" : trace.started?.model || "—"}</Meta>
              <Meta label="Steps">{reading ? "…" : steps}</Meta>
              <Meta label="Tokens">{spent.toLocaleString("en-US")}</Meta>
              <Meta label="Duration">{took === null ? "—" : duration(took)}</Meta>
              <Meta label="Run">
                <CopyId value={run.id} label="run id" />
              </Meta>
            </dl>
          </div>
          {isLive(run.status) && canCancel ? (
            <CancelButton key={run.id} teamId={teamId} run={run} onChanged={onChanged} />
          ) : null}
        </div>
        {trace.started && (trace.started.tools.length > 0 || trace.started.skills.length > 0) ? (
          <div className="flex flex-wrap items-center gap-1.5" data-testid="capabilities">
            {trace.started.pattern ? <Chip muted>{trace.started.pattern}</Chip> : null}
            {trace.started.tools.map((t) => (
              <Chip key={`t-${t}`} icon={<Wrench aria-hidden className="size-3" />}>
                {t}
              </Chip>
            ))}
            {trace.started.skills.map((s) => (
              <Chip key={`s-${s}`} muted>
                skill · {s}
              </Chip>
            ))}
          </div>
        ) : null}
      </header>

      <Section title="Input">
        <p data-input className="whitespace-pre-wrap break-words text-sm">
          {run.input}
        </p>
      </Section>

      {trace.plan ? (
        <Section title="Plan">
          <PlanList items={trace.plan} />
        </Section>
      ) : null}

      {run.output ? <Answer text={run.output} /> : null}

      {error ? (
        <div
          role="alert"
          className="flex gap-2 rounded-lg bg-destructive/8 px-3 py-2 text-sm text-destructive"
        >
          <XCircle aria-hidden className="mt-0.5 size-4 shrink-0" />
          <RunErrorText raw={error} />
        </div>
      ) : null}

      <Section
        title="Trace"
        aside={
          failed ? null : trace.ended ? (
            <span className="sr-only">
              Ended{trace.ended.status ? `: ${trace.ended.status}` : ""}
            </span>
          ) : null
        }
      >
        {failed ? (
          <p role="alert" className="mb-3 text-sm text-destructive">
            Could not follow this run: {failed}
          </p>
        ) : null}
        <TraceView trace={trace} live={live} empty={reading} sessionId={run.session_id} />
      </Section>
    </article>
  );
}

/** Longer than this, an answer starts folded so the trace stays in reach. */
const ANSWER_FOLD = 1500;

function Answer({ text }: { text: string }) {
  const long = text.length > ANSWER_FOLD;
  const [whole, setWhole] = useState(false);
  const folded = long && !whole;
  return (
    <Section title="Answer">
      <div
        data-answer
        data-folded={String(folded)}
        className={cn(
          "rounded-lg bg-card px-4 py-3",
          // Fades out at the fold, so the cut reads as more to come.
          folded &&
            "max-h-80 overflow-hidden [mask-image:linear-gradient(to_bottom,black_70%,transparent)]", // taste-skill-ignore: a mask fade on a fold, not a fill
        )}
      >
        <Markdown>{text}</Markdown>
      </div>
      {long ? (
        <Button variant="ghost" size="sm" className="mt-1 -ml-2" onClick={() => setWhole(!whole)}>
          <ChevronRight
            aria-hidden
            className={cn(
              "size-3.5 transition-transform",
              whole && "-rotate-90",
              !whole && "rotate-90",
            )}
          />
          {whole ? "Show less of the answer" : "Show the whole answer"}
        </Button>
      ) : null}
    </Section>
  );
}

/** A delegate's run names the run that started it, read for its agent's name. */
function ParentLink({
  teamId,
  sessionId,
  parentId,
}: {
  teamId: string;
  sessionId: string;
  parentId: string;
}) {
  const parent = useQuery({
    queryKey: ["runs", teamId, parentId],
    queryFn: () => getRun(teamId, parentId),
  });
  return (
    <Link
      to="/ui/sessions/$sessionId"
      params={{ sessionId }}
      search={{ tab: "runs", run: parentId }}
      className="inline-flex items-center gap-1 rounded text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
    >
      <ArrowUpLeft aria-hidden className="size-3" />
      delegated by {parent.data?.agent ?? "another run"}
    </Link>
  );
}

function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center gap-1">
      <dt>{label}</dt>
      <dd className="font-medium text-foreground tabular-nums">{children}</dd>
    </div>
  );
}

function CancelButton({
  teamId,
  run,
  onChanged,
}: {
  teamId: string;
  run: Run;
  onChanged: () => void;
}) {
  const cancel = useMutation({
    mutationFn: () => cancelRun(teamId, run.id),
    onSuccess: onChanged,
  });
  return (
    <div className="flex flex-col items-end gap-1">
      <Button
        variant="outline"
        size="sm"
        loading={cancel.isPending}
        onClick={() => cancel.mutate()}
        aria-label={`Cancel the run of ${run.agent}`}
      >
        Cancel
      </Button>
      {cancel.isError ? (
        <p role="alert" className="text-xs text-destructive">
          {cancel.error.message}
        </p>
      ) : null}
    </div>
  );
}

function Section({
  title,
  aside,
  children,
}: {
  title: string;
  aside?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-label={title}>
      <div className="mb-2 flex items-center justify-between gap-3">
        <h3 className="text-sm font-medium">{title}</h3>
        {aside}
      </div>
      {children}
    </section>
  );
}

function Chip({
  children,
  icon,
  muted = false,
  title,
}: {
  children: ReactNode;
  icon?: ReactNode;
  muted?: boolean;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex max-w-full items-center gap-1 rounded-md px-1.5 py-0.5 font-mono text-xs",
        muted
          ? "text-muted-foreground shadow-[inset_0_0_0_1px_var(--color-rule)]"
          : "bg-card text-foreground",
      )}
    >
      {icon}
      <span className="truncate">{children}</span>
    </span>
  );
}

/* ------------------------------------------------------------------ trace */

function TraceView({
  trace,
  live,
  empty,
  sessionId,
}: {
  trace: Trace;
  live: boolean;
  empty: boolean;
  sessionId: string;
}) {
  if (empty) {
    return live ? (
      <p className="flex items-center gap-2 text-sm text-muted-foreground" role="status">
        <Loader2 aria-hidden className="size-3.5 animate-spin" />
        Waiting for the first step…
      </p>
    ) : (
      <div className="space-y-3" role="status" aria-label="Reading the run's steps">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-10 w-full" />
        <Skeleton className="h-10 w-4/5" />
      </div>
    );
  }
  // Each step's bar is its tokens against the run's largest step.
  const most = Math.max(1, ...stepsOf(trace).map((st) => st.usage.total));
  let numbered = 0;
  return (
    <ol className="relative space-y-5">
      {trace.items.map((item, i) => {
        const key = `${item.type}-${i}`;
        switch (item.type) {
          case "step":
            numbered += 1;
            return (
              <StepView
                key={key}
                step={item.step}
                index={item.step.number ?? numbered}
                share={item.step.usage.total / most}
                model={trace.started?.model ?? ""}
                live={live}
                last={i === trace.items.length - 1}
                sessionId={sessionId}
              />
            );
          case "resumed":
            return (
              <Note key={key} kind="resumed">
                Resumed with {item.messages} earlier message{item.messages === 1 ? "" : "s"}
              </Note>
            );
          case "plan":
            return (
              <Note key={key} kind="plan">
                <span className="font-medium text-foreground">Plan</span>
                <div className="mt-1">
                  <PlanList items={item.items} />
                </div>
              </Note>
            );
          case "unavailable":
            return (
              <Note key={key} kind="tool_unavailable" warn>
                <span className="font-medium text-foreground">{item.name}</span> was unavailable:{" "}
                {item.reason}
              </Note>
            );
          default:
            return (
              <Note key={key} kind={item.event.kind}>
                <Unknown event={item.event} />
              </Note>
            );
        }
      })}
      {trace.finished ? (
        <Note kind="finished" warn={Boolean(trace.finished.error)}>
          Finished: <span className="font-medium text-foreground">{trace.finished.status}</span>
        </Note>
      ) : null}
    </ol>
  );
}

/** The failure in a sentence; the brain's own words behind a disclosure, when they differ. */
function RunErrorText({ raw }: { raw: string }) {
  const { said, details } = explainRunError(raw);
  return (
    <div className="min-w-0 space-y-1">
      <p className="whitespace-pre-wrap break-words">{said}</p>
      {details ? (
        <details className="text-xs">
          <summary className="cursor-pointer select-none underline-offset-4 hover:underline">
            Show the details
          </summary>
          <p className="mt-1 whitespace-pre-wrap break-words text-foreground/80">{details}</p>
        </details>
      ) : null}
    </div>
  );
}

function Note({
  kind,
  warn = false,
  children,
}: {
  kind: string;
  warn?: boolean;
  children: ReactNode;
}) {
  return (
    <li data-event={kind} className="flex gap-3 text-xs text-muted-foreground">
      <span className="flex w-6 shrink-0 justify-center pt-0.5">
        {warn ? (
          <AlertTriangle aria-hidden className="size-3.5 text-waiting" />
        ) : (
          <span aria-hidden className="mt-1.5 size-1.5 rounded-full bg-border-strong" />
        )}
      </span>
      <div className="min-w-0 flex-1 break-words">{children}</div>
    </li>
  );
}

function StepView({
  step,
  index,
  share,
  model,
  live,
  last,
  sessionId,
}: {
  step: Step;
  index: number;
  /** The model the run started on; a step names its own only when it differs. */
  model: string;
  /** This step's tokens as a share of the run's largest step, 0 to 1. */
  share: number;
  live: boolean;
  last: boolean;
  sessionId: string;
}) {
  return (
    <li
      data-step={index}
      className={cn(
        "relative flex gap-3",
        // A step that arrives while the run streams fades in; nothing else moves.
        live && "motion-safe:animate-in motion-safe:fade-in-0 motion-safe:duration-120",
      )}
    >
      {/* The timeline's rail, from this marker to the next. */}
      {!last ? (
        <span aria-hidden className="absolute top-7 bottom-[-1.25rem] left-3 w-px bg-rule" />
      ) : null}
      <span
        aria-hidden
        className="relative flex size-6 shrink-0 items-center justify-center rounded-full bg-card text-xs font-medium tabular-nums shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]"
      >
        {index}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex min-h-6 flex-wrap items-center gap-x-3 gap-y-1 text-sm">
          <span className="font-medium">Step {index}</span>
          {step.usage.total > 0 ? (
            <span className="flex items-center gap-2">
              {/* One track for every step, so the bars compare. */}
              <span aria-hidden className="h-1.5 w-28 overflow-hidden rounded-full bg-rule sm:w-48">
                <span
                  data-bar
                  className={cn(
                    "block h-full rounded-full",
                    live && last ? "bg-running" : "bg-foreground/45",
                  )}
                  style={{ width: `${Math.max(2, Math.round(share * 100))}%` }}
                />
              </span>
              <span data-usage className="text-xs text-muted-foreground tabular-nums">
                {tokens(step.usage.total)}
              </span>
            </span>
          ) : null}
          {step.models.some((m) => m !== model) ? (
            <span className="truncate text-xs text-muted-foreground">{step.models.join(", ")}</span>
          ) : null}
        </div>
        {step.calls.length > 0 ? (
          <ul className="divide-y divide-rule rounded-lg bg-card">
            {step.calls.map((c, i) =>
              c.name === "delegate" ? (
                <DelegateCard key={c.id ?? `n${i}`} call={c} live={live} sessionId={sessionId} />
              ) : (
                <ToolCallCard key={c.id ?? `n${i}`} call={c} live={live} />
              ),
            )}
          </ul>
        ) : step.other.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            {live && last ? "Thinking…" : "No tools called."}
          </p>
        ) : null}
        {step.other.map((e, i) => (
          <div key={e.seq ?? `o${i}`} data-event={e.kind} className="text-xs text-muted-foreground">
            <Unknown event={e} />
          </div>
        ))}
      </div>
    </li>
  );
}

function Unknown({ event }: { event: RunEvent }) {
  const raw = typeof event.data === "string" ? event.data : JSON.stringify(event.data);
  return (
    <div className="space-y-1">
      <span>
        <span className="font-mono">{event.kind}</span> event
      </span>
      <RawToggle raw={raw ?? ""} />
    </div>
  );
}

/* -------------------------------------------------------------- tool call */

const CHIP_MAX = 48;

function clipped(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

/** The first `max` characters, cut where a line ends so no word or mark is split. */
function atLineEnd(s: string, max: number): string {
  if (s.length <= max) return s;
  const cut = s.lastIndexOf("\n", max);
  const head = cut > 0 ? s.slice(0, cut) : s.slice(0, s.lastIndexOf(" ", max));
  // A stop already there, even inside closing emphasis, is not doubled.
  return `${head.trimEnd().replace(/[.…]+([*_]*)$/, "$1")}…`;
}

function ToolCallCard({ call, live }: { call: ToolCall; live: boolean }) {
  const result = call.result;
  const failed = Boolean(result?.error);
  const pending = result === null;
  const pairs = call.arguments === null ? null : argumentPairs(call.arguments);

  return (
    <li
      data-call={call.name}
      data-state={pending ? "pending" : failed ? "failed" : "done"}
      className="first:rounded-t-lg last:rounded-b-lg"
    >
      {/* Folded to its one line unless it failed or has not answered: the
          reader opens the step that matters, not every step. */}
      <details open={failed || pending} className="group">
        <summary className="flex cursor-pointer list-none items-start gap-2 rounded-lg px-3 py-2 hover:bg-sheet/50 focus-visible:outline-2 focus-visible:outline-offset-[-2px] [&::-webkit-details-marker]:hidden">
          <ChevronRight
            aria-hidden
            className="mt-0.5 size-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-90"
          />
          <div className="min-w-0 flex-1 space-y-1.5">
            <div className="flex items-center gap-2">
              <span className="font-mono text-xs font-medium">{call.name}</span>
              {failed ? (
                <span className="inline-flex items-center gap-1 text-xs text-destructive">
                  <XCircle aria-hidden className="size-3" /> failed
                </span>
              ) : null}
              {call.truncated ? <span className="text-xs text-muted-foreground">(cut)</span> : null}
            </div>
            {call.arguments === null ? null : pairs === null ? (
              <Chip muted title={call.arguments}>
                {clipped(call.arguments, CHIP_MAX * 2)}
              </Chip>
            ) : pairs.length > 0 ? (
              <div className="flex flex-wrap gap-1 group-[:not([open])]:max-h-6 group-[:not([open])]:overflow-hidden">
                {pairs.map(([k, v]) => (
                  <span
                    key={k}
                    data-arg={k}
                    title={v.length > CHIP_MAX ? v : undefined}
                    className="inline-flex max-w-full items-center rounded-md px-1.5 py-0.5 font-mono text-xs shadow-[inset_0_0_0_1px_var(--color-rule)]"
                  >
                    <span className="text-muted-foreground">{k}:</span>
                    <span className="ml-1 truncate">{clipped(v, CHIP_MAX)}</span>
                  </span>
                ))}
              </div>
            ) : null}
          </div>
          <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
            {pending ? (
              live ? (
                <span className="inline-flex items-center gap-1" role="status">
                  <Loader2 aria-hidden className="size-3 animate-spin" />
                  running
                </span>
              ) : (
                "no result"
              )
            ) : result.durationMs !== null ? (
              duration(result.durationMs)
            ) : null}
          </span>
        </summary>
        {result ? (
          <div className="pr-3 pb-2.5 pl-8.5">
            {failed ? (
              <p className="whitespace-pre-wrap break-words font-mono text-xs text-destructive">
                {result.error}
              </p>
            ) : (
              <ResultView name={call.name} output={result.output} />
            )}
            {result.truncated ? (
              <p className="mt-1 text-xs text-muted-foreground">
                The brain cut this output at its size limit.
              </p>
            ) : null}
          </div>
        ) : null}
      </details>
    </li>
  );
}

const PREVIEW = 300;

/**
 * A `delegate` call: whom it went to and what it was asked, the child's run as
 * soon as the brain has started it, and the child's report once it is back.
 */
function DelegateCard({
  call,
  live,
  sessionId,
}: {
  call: ToolCall;
  live: boolean;
  sessionId: string;
}) {
  const result = call.result;
  const brief = call.arguments === null ? null : argumentPairs(call.arguments);
  const field = (k: string) => brief?.find(([key]) => key === k)?.[1] ?? "";
  const target = field("agent") || call.delegated?.agent || "";
  const objective = field("objective");
  const report = result && !result.error ? delegateReport(result.output) : null;
  const failed = Boolean(result?.error) || (report !== null && report.status !== "succeeded");
  const state = result === null ? "pending" : failed ? "failed" : "done";

  return (
    <li data-call={call.name} data-state={state} className="first:rounded-t-lg last:rounded-b-lg">
      <div className="flex items-start gap-2 px-3 py-2">
        <Users
          aria-hidden
          className={cn(
            "mt-0.5 size-3.5 shrink-0",
            failed ? "text-failed" : "text-muted-foreground",
          )}
        />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-xs font-medium">delegate</span>
            <span aria-hidden className="text-xs text-muted-foreground">
              →
            </span>
            <span data-target className="truncate text-sm font-medium">
              {target || "an agent it did not name"}
            </span>
            {report ? <RunStatus status={report.status} /> : null}
            {call.truncated ? <span className="text-xs text-muted-foreground">(cut)</span> : null}
          </div>
          {objective ? (
            <p data-objective className="line-clamp-2 break-words text-xs text-muted-foreground">
              {clipped(objective, PREVIEW)}
            </p>
          ) : null}
          {call.delegated ? (
            <Link
              to="/ui/sessions/$sessionId"
              params={{ sessionId }}
              search={{ tab: "runs", run: call.delegated.runId }}
              className="inline-flex items-center gap-1 rounded text-xs text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
            >
              Open {call.delegated.agent || target}'s run
              <ChevronRight aria-hidden className="size-3" />
            </Link>
          ) : null}
        </div>
        <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
          {result === null ? (
            live ? (
              <span className="inline-flex items-center gap-1" role="status">
                <Loader2 aria-hidden className="size-3 animate-spin" />
                working
              </span>
            ) : (
              "no result"
            )
          ) : result.durationMs !== null ? (
            duration(result.durationMs)
          ) : null}
        </span>
      </div>
      {result ? (
        <div className="pr-3 pb-2.5 pl-8.5">
          {result.error ? (
            <p className="whitespace-pre-wrap break-words font-mono text-xs text-destructive">
              {result.error}
            </p>
          ) : (
            <div className="space-y-1.5">
              {/* The child answers in markdown; raw HTML in it stays text. */}
              <div data-preview className="max-w-[75ch] text-muted-foreground">
                {(report ? report.body : result.output).trim() ? (
                  <Markdown
                    htmlAsText
                    className="text-xs [&_code]:text-xs [&_h1]:text-sm [&_h2]:text-sm [&_h3]:text-sm [&_h1,&_h2,&_h3]:mt-1 [&_h1,&_h2,&_h3]:mb-1 [&_h1,&_h2,&_h3]:font-medium [&_h1,&_h2,&_h3]:text-foreground"
                  >
                    {atLineEnd(report ? report.body : result.output, PREVIEW * 2)}
                  </Markdown>
                ) : (
                  <p className="text-xs">No answer.</p>
                )}
              </div>
              <RawToggle raw={result.output} />
            </div>
          )}
        </div>
      ) : null}
    </li>
  );
}

function ResultView({ name, output }: { name: string; output: string }) {
  const hits = name === "graph" ? graphHits(output) : null;
  if (hits) return <GraphResults hits={hits} raw={output} />;
  const found = name === "web_search" ? searchResults(output) : null;
  if (found) return <SearchList results={found} raw={output} />;
  const page = name === "web_fetch" ? fetchedPage(output) : null;
  if (page) return <PageView page={page} raw={output} />;
  if (output === "") return <p className="text-xs text-muted-foreground">No output.</p>;
  return (
    <div className="space-y-1.5">
      <p data-preview className="whitespace-pre-wrap break-words text-xs text-muted-foreground">
        {clipped(output, PREVIEW)}
      </p>
      <RawToggle raw={output} />
    </div>
  );
}

function RawToggle({ raw }: { raw: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        className="inline-flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        <ChevronRight
          aria-hidden
          className={cn("size-3 transition-transform", open && "rotate-90")}
        />
        {open ? "Hide raw" : "Show raw"}
      </button>
      {open ? (
        <pre
          data-raw
          className="mt-1.5 max-h-80 overflow-auto rounded-md bg-sheet p-3 font-mono text-xs leading-relaxed shadow-[inset_0_0_0_1px_var(--color-rule)]"
        >
          {pretty(raw)}
        </pre>
      ) : null}
    </div>
  );
}

/* ------------------------------------------------------------ graph result */

function GraphResults({ hits, raw }: { hits: GraphHit[]; raw: string }) {
  return (
    <div className="space-y-2">
      {hits.length === 0 ? (
        <p className="text-xs text-muted-foreground">Nothing in reach matched.</p>
      ) : (
        <ol aria-label="Graph matches" className="divide-y divide-rule">
          {hits.map((h, i) => {
            const score = Math.min(1, Math.max(0, h.score));
            return (
              <li
                // biome-ignore lint/suspicious/noArrayIndexKey: ranked, and names may repeat across kinds
                key={i}
                data-hit={h.name}
                className="flex items-start gap-3 py-1.5 first:pt-0 last:pb-0"
              >
                <span className="w-4 shrink-0 pt-0.5 text-right text-xs text-muted-foreground tabular-nums">
                  {i + 1}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="rounded-md px-1.5 text-xs text-muted-foreground shadow-[inset_0_0_0_1px_var(--color-rule)]">
                      {h.kind}
                    </span>
                    <span className="truncate text-xs font-medium">{h.name}</span>
                  </div>
                  {h.description ? (
                    <p className="mt-0.5 line-clamp-2 text-xs text-muted-foreground">
                      {h.description}
                    </p>
                  ) : null}
                  {h.via.length > 0 ? (
                    <p data-via className="mt-0.5 text-xs text-muted-foreground">
                      via{" "}
                      {h.via.map((v, j) => (
                        <span key={`${v.kind}:${v.name}`}>
                          {j > 0 ? " › " : ""}
                          {v.kind} <span className="font-medium text-foreground">{v.name}</span>
                        </span>
                      ))}
                    </p>
                  ) : null}
                </div>
                <div className="flex w-24 shrink-0 items-center gap-2 pt-1">
                  <span aria-hidden className="h-1.5 flex-1 overflow-hidden rounded-full bg-rule">
                    <span
                      data-bar
                      className="block h-full rounded-full bg-foreground/45"
                      style={{ width: `${(score * 100).toFixed(1)}%` }}
                    />
                  </span>
                  <span className="font-mono text-xs tabular-nums">{h.score.toFixed(3)}</span>
                </div>
              </li>
            );
          })}
        </ol>
      )}
      <RawToggle raw={raw} />
    </div>
  );
}

/* ------------------------------------------------------------------- plan */

const PLAN: Record<PlanStatus, { icon: typeof Clock; word: string; tone: string }> = {
  pending: { icon: Circle, word: "pending", tone: "text-muted-foreground" },
  in_progress: { icon: CircleDot, word: "in progress", tone: "text-running" },
  done: { icon: CheckCircle2, word: "done", tone: "text-success" },
};

/** A plan as a checklist: each status an icon and its word, never colour alone. */
function PlanList({ items }: { items: PlanItem[] }) {
  if (items.length === 0) {
    return <p className="text-xs text-muted-foreground">The plan is empty.</p>;
  }
  return (
    <ul className="space-y-1">
      {items.map((item, i) => {
        const s = PLAN[item.status];
        const Icon = s.icon;
        return (
          <li
            // biome-ignore lint/suspicious/noArrayIndexKey: a plan is a list in order, and its steps may repeat
            key={i}
            data-status={item.status}
            className="flex items-start gap-2 text-sm"
          >
            <Icon aria-hidden className={cn("mt-0.5 size-3.5 shrink-0", s.tone)} />
            <span className="w-20 shrink-0 text-xs text-muted-foreground">{s.word}</span>
            <span
              className={cn(
                "min-w-0 break-words",
                item.status === "done" && "text-muted-foreground line-through",
              )}
            >
              {item.text}
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/* ----------------------------------------------------------- search result */

/** The address a hit may link to: the web's only. Anything else is shown, never followed. */
function webURL(raw: string): string | null {
  try {
    const u = new URL(raw);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : null;
  } catch {
    return null;
  }
}

function SearchList({ results, raw }: { results: SearchResults; raw: string }) {
  return (
    <div className="space-y-2">
      {results.hits.length === 0 ? (
        <p className="text-xs text-muted-foreground">No results.</p>
      ) : (
        <ol aria-label="Search results" className="space-y-2">
          {results.hits.map((h, i) => {
            const href = webURL(h.url);
            const title = h.title || h.url;
            return (
              <li
                // biome-ignore lint/suspicious/noArrayIndexKey: ranked, and two pages may share a title
                key={i}
                data-result
                className="flex items-start gap-3"
              >
                <span className="w-4 shrink-0 pt-0.5 text-right text-xs text-muted-foreground tabular-nums">
                  {i + 1}
                </span>
                <div className="min-w-0 flex-1">
                  {href ? (
                    <a
                      href={href}
                      target="_blank"
                      rel="noopener noreferrer nofollow"
                      className="break-words text-xs font-medium text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
                    >
                      {title}
                    </a>
                  ) : (
                    <span className="break-words text-xs font-medium">{title}</span>
                  )}
                  <p className="truncate font-mono text-xs text-muted-foreground">{h.url}</p>
                  {h.snippet ? (
                    <p className="mt-0.5 line-clamp-2 break-words text-xs text-muted-foreground">
                      {h.snippet}
                    </p>
                  ) : null}
                </div>
              </li>
            );
          })}
        </ol>
      )}
      {results.cut ? (
        <p className="text-xs text-muted-foreground">
          The brain cut these results at their size limit.
        </p>
      ) : null}
      <RawToggle raw={raw} />
    </div>
  );
}

/* ------------------------------------------------------------ fetched page */

function sizeOf(text: string): string {
  const bytes = new TextEncoder().encode(text).length;
  return bytes < 1024 ? `${bytes} B` : `${Math.round(bytes / 1024)} KiB`;
}

/** A page web_fetch read: a link only to the web, and its markdown as text, folded away. */
function PageView({ page, raw }: { page: FetchedPage; raw: string }) {
  const [open, setOpen] = useState(false);
  const href = webURL(page.url);
  const title = page.title || page.url;
  return (
    <div className="space-y-1.5">
      <div className="min-w-0">
        {href ? (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer nofollow"
            className="break-words text-xs font-medium text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          >
            {title}
          </a>
        ) : (
          <span className="break-words text-xs font-medium">{title}</span>
        )}
        <p className="truncate font-mono text-xs text-muted-foreground">{page.url}</p>
        <p className="text-xs text-muted-foreground tabular-nums">
          {page.cut ? `${sizeOf(page.markdown)} · cut by the brain` : sizeOf(page.markdown)}
        </p>
      </div>
      <div>
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="inline-flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRight
            aria-hidden
            className={cn("size-3 transition-transform", open && "rotate-90")}
          />
          {open ? "Hide the page" : "Show the page"}
        </button>
        {open ? (
          <pre
            data-page-body
            className="mt-1.5 max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-md bg-sheet p-3 font-mono text-xs leading-relaxed shadow-[inset_0_0_0_1px_var(--color-rule)]"
          >
            {page.markdown}
          </pre>
        ) : null}
      </div>
      <RawToggle raw={raw} />
    </div>
  );
}
