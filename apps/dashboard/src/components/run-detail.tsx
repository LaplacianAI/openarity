/**
 * One run, read in full: what it was asked, what it answered, and the steps
 * it took to get there, followed live while it is still running.
 *
 * Everything shown here came from a person, a model or a tool. It is rendered
 * as React text — or, for the answer alone, as markdown with raw HTML off —
 * and never as markup.
 */
import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  AlertTriangle,
  ArrowUpLeft,
  Ban,
  CheckCircle2,
  ChevronRight,
  Circle,
  CircleDashed,
  CircleDot,
  Clock,
  Loader2,
  Users,
  Wrench,
  XCircle,
} from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";

import { cancelRun, followRunEvents, getRun } from "@/api";
import type { Run, RunEvent } from "@/api/types";
import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import {
  argumentPairs,
  delegateReport,
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

const STATUS: Record<string, { icon: typeof Clock; tone: string; spin?: boolean }> = {
  queued: { icon: Clock, tone: "text-warning" },
  running: { icon: Loader2, tone: "text-primary", spin: true },
  succeeded: { icon: CheckCircle2, tone: "text-success" },
  failed: { icon: XCircle, tone: "text-destructive" },
  cancelled: { icon: Ban, tone: "text-muted-foreground" },
};

export function isLive(status: string): boolean {
  return status === "queued" || status === "running";
}

/** A status as an icon and its word: the colour is never the only signal. */
export function RunStatus({ status, pill = false }: { status: string; pill?: boolean }) {
  const s = STATUS[status] ?? { icon: CircleDashed, tone: "text-muted-foreground" };
  const Icon = s.icon;
  return (
    <span
      data-status={status}
      className={cn(
        "inline-flex items-center gap-1 text-xs",
        pill && "rounded-full border border-border bg-card px-2 py-0.5 font-medium",
      )}
    >
      <Icon aria-hidden className={cn("size-3.5 shrink-0", s.tone, s.spin && "animate-spin")} />
      <span>{status}</span>
    </span>
  );
}

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

/**
 * Follows a run's events for as long as this is mounted on that run. Choosing
 * another run or leaving the page aborts the stream: a running run's stream
 * otherwise stays open for as long as the run does.
 */
function useRunEvents(teamId: string, runId: string, onEnded: () => void) {
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [failed, setFailed] = useState<string | null>(null);

  // A ref, because onEnded is a fresh closure every render and restarting the
  // stream for it would replay every event.
  const onEndedRef = useRef(onEnded);
  onEndedRef.current = onEnded;

  useEffect(() => {
    const abort = new AbortController();
    setEvents([]);
    setFailed(null);
    followRunEvents(
      teamId,
      runId,
      (e) => {
        setEvents((prev) => [...prev, e]);
        if (e.kind === "end") onEndedRef.current();
      },
      abort.signal,
    ).catch((err: unknown) => {
      // Aborted by this effect's own cleanup — another run was chosen, or
      // the page went away — which is not a failure to report.
      if (abort.signal.aborted) return;
      setFailed(err instanceof Error ? err.message : String(err));
    });
    return () => abort.abort();
  }, [teamId, runId]);

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
  const trace = useMemo(() => groupTrace(events), [events]);
  const live = isLive(run.status) && !trace.ended && !failed;
  const now = useNow(live);

  const steps = stepsOf(trace).length;
  const spent = trace.usage.total > 0 ? trace.usage.total : run.input_tokens + run.output_tokens;
  const began = run.started_at ? Date.parse(run.started_at) : null;
  const took =
    began === null ? null : (run.finished_at ? Date.parse(run.finished_at) : now) - began;
  const error = run.error ?? (trace.finished?.error || null);

  return (
    <article aria-label={`Run of ${run.agent}`} className="space-y-6">
      <header className="space-y-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <h2 className="truncate text-base font-semibold tracking-tight">{run.agent}</h2>
              <RunStatus status={run.status} pill />
              {run.hub ? (
                <span
                  data-hub
                  title="Started for the team: its hub answered"
                  className="rounded-full border border-border bg-surface px-2 py-0.5 text-xs font-medium"
                >
                  hub
                </span>
              ) : null}
              {live ? <LiveBadge /> : null}
            </div>
            {run.parent_run_id ? (
              <ParentLink teamId={teamId} sessionId={run.session_id} parentId={run.parent_run_id} />
            ) : null}
            <dl className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
              <Meta label="Model">{trace.started?.model || "—"}</Meta>
              <Meta label="Steps">{steps}</Meta>
              <Meta label="Tokens">{spent.toLocaleString("en-US")}</Meta>
              <Meta label="Duration">{took === null ? "—" : duration(took)}</Meta>
            </dl>
          </div>
          {isLive(run.status) && canCancel ? (
            <CancelButton key={run.id} teamId={teamId} run={run} onChanged={onChanged} />
          ) : null}
        </div>
        {trace.started && (trace.started.tools.length > 0 || trace.started.skills.length > 0) ? (
          <div className="flex flex-wrap items-center gap-1.5 text-xs" data-testid="capabilities">
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

      {run.output ? (
        <Section title="Answer">
          <div data-answer className="rounded-md border border-border bg-card px-4 py-3">
            <Markdown>{run.output}</Markdown>
          </div>
        </Section>
      ) : null}

      {error ? (
        <div
          role="alert"
          className="flex gap-2 rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive"
        >
          <XCircle aria-hidden className="mt-0.5 size-4 shrink-0" />
          <p className="whitespace-pre-wrap break-words">{error}</p>
        </div>
      ) : null}

      <Section
        title="Trace"
        aside={
          failed ? null : trace.ended ? (
            <span className="text-xs text-muted-foreground">
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
        <TraceView
          trace={trace}
          live={live}
          empty={events.length === 0 && !failed}
          sessionId={run.session_id}
        />
      </Section>
    </article>
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

function LiveBadge() {
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full bg-success/10 px-2 py-0.5 text-xs font-medium text-success">
      <span aria-hidden className="relative flex size-1.5">
        <span className="absolute inline-flex size-full animate-ping rounded-full bg-success opacity-60" />
        <span className="relative inline-flex size-1.5 rounded-full bg-success" />
      </span>
      Live
    </span>
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
        disabled={cancel.isPending}
        onClick={() => cancel.mutate()}
        aria-label={`Cancel the run of ${run.agent}`}
      >
        {cancel.isPending ? "Cancelling…" : "Cancel"}
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
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {title}
        </h3>
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
        "inline-flex max-w-full items-center gap-1 rounded border border-border px-1.5 py-0.5 font-mono text-[11px]",
        muted ? "bg-transparent text-muted-foreground" : "bg-surface text-foreground",
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
    return (
      <p className="flex items-center gap-2 text-sm text-muted-foreground">
        {live ? <Loader2 aria-hidden className="size-3.5 animate-spin" /> : null}
        Waiting for events…
      </p>
    );
  }
  let numbered = 0;
  return (
    <ol className="relative space-y-4">
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
          <AlertTriangle aria-hidden className="size-3.5 text-warning" />
        ) : (
          <ChevronRight aria-hidden className="size-3.5" />
        )}
      </span>
      <div className="min-w-0 flex-1 break-words">{children}</div>
    </li>
  );
}

function StepView({
  step,
  index,
  live,
  last,
  sessionId,
}: {
  step: Step;
  index: number;
  live: boolean;
  last: boolean;
  sessionId: string;
}) {
  return (
    <li data-step={index} className="relative flex gap-3">
      {/* The timeline's rail, from this marker to the next. */}
      {!last ? (
        <span aria-hidden className="absolute top-7 bottom-[-1rem] left-3 w-px bg-border" />
      ) : null}
      <span
        aria-hidden
        className="relative flex size-6 shrink-0 items-center justify-center rounded-full border border-border-strong bg-card text-[11px] font-semibold tabular-nums"
      >
        {index}
      </span>
      <div className="min-w-0 flex-1 space-y-2">
        <div className="flex h-6 items-center gap-2 text-sm">
          <span className="font-medium">Step {index}</span>
          {step.usage.total > 0 ? (
            <span data-usage className="text-xs text-muted-foreground tabular-nums">
              {tokens(step.usage.total)}
            </span>
          ) : null}
          {step.models.length > 0 ? (
            <span className="truncate text-xs text-muted-foreground">
              · {step.models.join(", ")}
            </span>
          ) : null}
        </div>
        {step.calls.length > 0 ? (
          <ul className="space-y-2">
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

function ToolCallCard({ call, live }: { call: ToolCall; live: boolean }) {
  const result = call.result;
  const failed = Boolean(result?.error);
  const pending = result === null;
  const pairs = call.arguments === null ? null : argumentPairs(call.arguments);

  return (
    <li
      data-call={call.name}
      data-state={pending ? "pending" : failed ? "failed" : "done"}
      className={cn(
        "rounded-md border bg-card",
        failed ? "border-destructive/40" : "border-border",
      )}
    >
      <div className="flex items-start gap-2 px-3 py-2">
        <Wrench aria-hidden className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
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
            <div className="flex flex-wrap gap-1">
              {pairs.map(([k, v]) => (
                <span
                  key={k}
                  data-arg={k}
                  title={v.length > CHIP_MAX ? v : undefined}
                  className="inline-flex max-w-full items-center rounded border border-border bg-surface px-1.5 py-0.5 font-mono text-[11px]"
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
      </div>
      {result ? (
        <div className="border-t border-border px-3 py-2">
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
    <li
      data-call={call.name}
      data-state={state}
      className={cn(
        "rounded-md border bg-card",
        failed ? "border-destructive/40" : "border-border",
      )}
    >
      <div className="flex items-start gap-2 px-3 py-2">
        <Users aria-hidden className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-xs font-medium">delegate</span>
            <span aria-hidden className="text-xs text-muted-foreground">
              →
            </span>
            <span data-target className="truncate text-xs font-medium">
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
        <div className="border-t border-border px-3 py-2">
          {result.error ? (
            <p className="whitespace-pre-wrap break-words font-mono text-xs text-destructive">
              {result.error}
            </p>
          ) : (
            <div className="space-y-1.5">
              <p
                data-preview
                className="whitespace-pre-wrap break-words text-xs text-muted-foreground"
              >
                {clipped(report ? report.body : result.output, PREVIEW) || "No answer."}
              </p>
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
          className="mt-1.5 max-h-80 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-[11px] leading-relaxed"
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
        <ol aria-label="Graph matches" className="divide-y divide-border">
          {hits.map((h, i) => {
            const score = Math.min(1, Math.max(0, h.score));
            return (
              <li
                // biome-ignore lint/suspicious/noArrayIndexKey: ranked, and names may repeat across kinds
                key={i}
                data-hit={h.name}
                className="flex items-start gap-3 py-1.5 first:pt-0 last:pb-0"
              >
                <span className="w-4 shrink-0 pt-0.5 text-right text-[11px] text-muted-foreground tabular-nums">
                  {i + 1}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="rounded border border-border bg-surface px-1 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
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
                    <p data-via className="mt-0.5 text-[11px] text-muted-foreground">
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
                  <span aria-hidden className="h-1.5 flex-1 overflow-hidden rounded-full bg-muted">
                    <span
                      data-bar
                      className="block h-full rounded-full bg-primary"
                      style={{ width: `${(score * 100).toFixed(1)}%` }}
                    />
                  </span>
                  <span className="font-mono text-[11px] tabular-nums">{h.score.toFixed(3)}</span>
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
  in_progress: { icon: CircleDot, word: "in progress", tone: "text-primary" },
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
                <span className="w-4 shrink-0 pt-0.5 text-right text-[11px] text-muted-foreground tabular-nums">
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
                  <p className="truncate font-mono text-[11px] text-muted-foreground">{h.url}</p>
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
        <p className="text-xs text-muted-foreground">The brain cut these results at 16 KiB.</p>
      ) : null}
      <RawToggle raw={raw} />
    </div>
  );
}
