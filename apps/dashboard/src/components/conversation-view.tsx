/**
 * A conversation read off its session's stream: what people said, and each
 * run as it thinks, calls tools, hands work on and answers.
 *
 * A run's working is open while it works and folds to one line when it ends,
 * so the answer stands alone; a reader who opened it keeps it open. The text
 * being written shows as it arrives, and the run's output replaces it once
 * the run has finished.
 */
import { Link } from "@tanstack/react-router";
import {
  ArrowRight,
  Check,
  ChevronRight,
  Clock,
  Loader2,
  MessageSquare,
  Users,
  X,
} from "lucide-react";
import { type ReactNode, useEffect, useState } from "react";

import type { Run } from "@/api/types";
import { Markdown } from "@/components/markdown";
import { duration } from "@/components/run-detail";
import { HubTag } from "@/components/run-table";
import { LiveBadge, StatusPill } from "@/components/status";
import { Mono } from "@/components/ui-bits";
import { replyLine } from "@/lib/conversation";
import { absoluteTime } from "@/lib/format";
import { explainRunError } from "@/lib/run-error";
import {
  type Conversation,
  type RunTurn,
  type Said,
  type Step,
  saying,
  type ToolLine,
  worked,
} from "@/lib/session-stream";
import { cn } from "@/lib/utils";

/** Longer than this, an answer is cut here and read whole in its run. */
const ANSWER_FOLD = 1200;
/** A tool's arguments, shown on one line, cut at this. */
const ARGS_MAX = 80;

export type People = {
  /** A member's email or subject, by user id; null for someone unknown. */
  nameOf: (userId: string) => string | null;
  /** The signed-in user, whose messages read as "you". */
  me: string | null;
};

export function ConversationView({
  conversation: c,
  sessionId,
  rows,
  people,
}: {
  conversation: Conversation;
  sessionId: string;
  /** The session's runs as listed, for what the stream does not carry: the hub, a reply's fate. */
  rows: Run[];
  people: People;
}) {
  const now = useClock(Object.values(c.runs).some((r) => r.status === "running"));
  return (
    <ol className="space-y-5">
      {c.order.map((e) =>
        e.kind === "said" ? (
          <SaidTurn key={e.id} said={c.said[e.id] as Said} conversation={c} people={people} />
        ) : (
          <RunTurnView
            key={e.id}
            run={c.runs[e.id] as RunTurn}
            conversation={c}
            row={rows.find((r) => r.id === e.id)}
            sessionId={sessionId}
            people={people}
            now={now}
          />
        ),
      )}
    </ol>
  );
}

/** Ticks once a second while something is running, for how long it has worked. */
function useClock(running: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!running) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [running]);
  return now;
}

function SaidTurn({
  said: m,
  conversation: c,
  people,
}: {
  said: Said;
  conversation: Conversation;
  people: People;
}) {
  const mine = people.me !== null && m.userId === people.me;
  const name = mine ? "You" : people.nameOf(m.userId);
  const reader = m.readBy ? c.runs[m.readBy] : undefined;
  const working = Object.values(c.runs).some((r) => r.status === "running" && r.parent === null);
  return (
    <li data-message={m.id} className="space-y-1.5">
      <div className="rounded-lg bg-card px-4 py-3">
        <div className="flex items-baseline justify-between gap-3 text-xs text-muted-foreground">
          {name ? (
            <span className="truncate text-sm font-medium text-foreground">{name}</span>
          ) : (
            <Mono className="truncate">{m.userId}</Mono>
          )}
          <time dateTime={m.at} className="shrink-0">
            {absoluteTime(m.at)}
          </time>
        </div>
        {/* A person typed this: it is shown as text, never parsed. */}
        <p data-text className="mt-1 max-w-[75ch] whitespace-pre-wrap break-words text-sm">
          {m.text}
        </p>
      </div>
      {m.pending ? (
        <p
          data-state="pending"
          className="flex items-center gap-1.5 px-1 text-xs text-muted-foreground"
        >
          <Clock aria-hidden className="size-3.5 shrink-0 text-waiting" />
          <span>
            <span className="text-waiting">Waiting</span>
            {working ? " · read before the run's next step" : null}
          </span>
        </p>
      ) : reader ? (
        <p
          data-state="read"
          className="flex items-center gap-1.5 px-1 text-xs text-muted-foreground"
        >
          <Check aria-hidden className="size-3.5 shrink-0" />
          <span>Read by {reader.agent}</span>
        </p>
      ) : null}
    </li>
  );
}

function RunTurnView({
  run,
  conversation: c,
  row,
  sessionId,
  people,
  now,
}: {
  run: RunTurn;
  conversation: Conversation;
  row: Run | undefined;
  sessionId: string;
  people: People;
  now: number;
}) {
  const running = run.status === "running";
  const w = worked(c, run, now);
  const live = saying(run);
  // The step whose text is the answer is shown as the answer, not again in the working.
  const answerStep = [...run.steps].reverse().find((s) => s.text !== "")?.step ?? null;
  return (
    <li data-turn={run.id} data-status={run.status} className="space-y-2">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span className="min-w-0 truncate text-sm font-medium text-foreground">{run.agent}</span>
        {row?.hub ? <HubTag /> : null}
        {running ? <LiveBadge /> : <StatusPill status={run.status} />}
        {w.ms !== null && !running ? <span className="tabular-nums">{duration(w.ms)}</span> : null}
        <time dateTime={run.startedAt} className="ml-auto shrink-0">
          {absoluteTime(run.startedAt)}
        </time>
      </div>

      {w.steps > 0 || w.handed.length > 0 ? (
        <Working
          run={run}
          conversation={c}
          answerStep={answerStep}
          people={people}
          sessionId={sessionId}
          now={now}
        />
      ) : null}

      <div data-answer className="max-w-[75ch] text-sm">
        {running ? (
          live ? (
            <p data-live className="whitespace-pre-wrap break-words">
              {live}
              {/* Teal marks what is running; it holds still, as nothing but the Live dot breathes. */}
              <span
                aria-hidden
                className="ml-0.5 inline-block h-[1.1em] w-0.5 translate-y-[0.15em] bg-running"
              />
            </p>
          ) : (
            <p className="text-muted-foreground">Working on it…</p>
          )
        ) : (
          <Answer run={run} />
        )}
      </div>

      {row ? <ReplyStatus run={row} /> : null}
      <Link
        to="/ui/sessions/$sessionId"
        params={{ sessionId }}
        search={{ tab: "runs", run: run.id }}
        className="inline-flex items-center gap-1 rounded text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
      >
        {run.output.trim().length > ANSWER_FOLD
          ? "Read the whole answer in its trace"
          : "Open its trace"}
        <ArrowRight aria-hidden className="size-3" />
      </Link>
    </li>
  );
}

function Answer({ run }: { run: RunTurn }) {
  const output = run.output.trim();
  if (output) {
    const folded = output.length > ANSWER_FOLD;
    return (
      <div
        className={cn(
          folded &&
            "max-h-60 overflow-hidden [mask-image:linear-gradient(to_bottom,black_70%,transparent)]", // taste-skill-ignore: a mask fade on a fold, not a fill
        )}
      >
        <Markdown>{output}</Markdown>
      </div>
    );
  }
  if (run.status === "failed") {
    return (
      <p className="rounded-lg bg-destructive/8 px-3 py-2 text-destructive">
        {run.error ? explainRunError(run.error).said : "It failed before answering."}
      </p>
    );
  }
  return (
    <p className="text-muted-foreground">
      {run.status === "cancelled"
        ? "Cancelled before it answered."
        : "It finished without an answer."}
    </p>
  );
}

/**
 * What a run did on its way to the answer, folded to one line. Open while
 * the run works; once it ends it folds, unless the reader opened it.
 */
function Working({
  run,
  conversation: c,
  answerStep,
  people,
  sessionId,
  now,
}: {
  run: RunTurn;
  conversation: Conversation;
  answerStep: number | null;
  people: People;
  sessionId: string;
  now: number;
}) {
  const running = run.status === "running";
  const [chosen, setChosen] = useState<boolean | null>(null);
  const open = chosen ?? running;
  const w = worked(c, run, now);
  const id = `working-${run.id}`;
  return (
    <div data-working={open ? "open" : "folded"}>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setChosen(!open)}
        className="-ml-1 inline-flex min-h-7 max-w-full items-start gap-1 rounded px-1 py-1 text-left text-xs text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        <ChevronRight
          aria-hidden
          className={cn(
            "mt-px size-3.5 shrink-0 transition-transform duration-(--dur-press) ease-(--ease-out) motion-reduce:transition-none",
            open && "rotate-90",
          )}
        />
        <span className="min-w-0 break-words">{summary(w, running)}</span>
      </button>
      {/* 0fr to 1fr folds the height without measuring it; reduced motion skips the travel. */}
      <div
        id={id}
        className={cn(
          "grid transition-[grid-template-rows] duration-(--dur-dialog) ease-(--ease-out) motion-reduce:transition-none",
          open ? "grid-rows-[1fr]" : "grid-rows-[0fr]",
        )}
      >
        <div className="min-h-0 overflow-hidden" inert={!open}>
          <ol className="mt-1 ml-1.5 space-y-2.5 border-l border-rule pl-3">
            {run.steps.map((s) => (
              <StepLines
                key={s.step}
                step={s}
                answer={s.step === answerStep}
                conversation={c}
                people={people}
              />
            ))}
            {run.children.map((id) => {
              const child = c.runs[id];
              return child ? (
                <Handed key={id} child={child} sessionId={sessionId} now={now} conversation={c} />
              ) : null;
            })}
          </ol>
        </div>
      </div>
    </div>
  );
}

/** The line a working folds to. Its duration is in the run's header already. */
function summary(w: ReturnType<typeof worked>, running: boolean): string {
  const parts: string[] = [];
  if (running) parts.push(w.steps > 0 ? `Working · step ${w.steps}` : "Working");
  else if (w.steps > 0) parts.push(count(w.steps, "step"));
  if (w.tools > 0) parts.push(count(w.tools, "tool"));
  if (w.handed.length > 0) parts.push(`handed to ${w.handed.join(", ")}`);
  if (w.read > 0) parts.push(`read ${count(w.read, "message")}`);
  return parts.join(" · ") || "Worked";
}

function count(n: number, what: string): string {
  return `${n} ${what}${n === 1 ? "" : "s"}`;
}

function StepLines({
  step: s,
  answer,
  conversation: c,
  people,
}: {
  step: Step;
  answer: boolean;
  conversation: Conversation;
  people: People;
}) {
  const tools = s.tools.filter((t) => t.name !== "delegate");
  const said = answer ? "" : s.text.trim();
  if (!s.reasoning && !said && tools.length === 0 && s.read.length === 0) return null;
  return (
    <li data-step={s.step} className="space-y-1.5">
      {s.read.map((id) => {
        const m = c.said[id];
        const who =
          m && people.me !== null && m.userId === people.me ? "your message" : "a message";
        return (
          <Line key={id} icon={<MessageSquare aria-hidden className="size-3.5" />}>
            Read {who}
            {m ? <span className="text-foreground">: “{clip(m.text, ARGS_MAX)}”</span> : null}
          </Line>
        );
      })}
      {s.reasoning ? (
        <p
          data-reasoning
          className="max-w-[75ch] whitespace-pre-wrap break-words text-sm text-muted-foreground"
        >
          {s.reasoning}
        </p>
      ) : null}
      {tools.length > 0 ? (
        <ul className="space-y-1">
          {tools.map((t) => (
            <ToolLineView key={t.id} tool={t} />
          ))}
        </ul>
      ) : null}
      {said ? <p className="max-w-[75ch] whitespace-pre-wrap break-words text-sm">{said}</p> : null}
    </li>
  );
}

function ToolLineView({ tool: t }: { tool: ToolLine }) {
  const failed = Boolean(t.error);
  return (
    <li data-tool={t.name} className="flex min-w-0 items-center gap-2 text-xs">
      <span className="font-mono font-medium">{t.name}</span>
      {t.args && t.args !== "{}" ? (
        <span className="min-w-0 truncate rounded-md px-1.5 py-0.5 font-mono text-muted-foreground shadow-[inset_0_0_0_1px_var(--color-rule)]">
          {clip(t.args, ARGS_MAX)}
        </span>
      ) : null}
      <span className="ml-auto flex shrink-0 items-center gap-1 text-muted-foreground tabular-nums">
        {!t.done ? (
          <>
            <Loader2 aria-hidden className="size-3 animate-spin text-running" />
            <span>running</span>
          </>
        ) : failed ? (
          <>
            <X aria-hidden className="size-3 text-failed" />
            <span className="text-failed">failed</span>
          </>
        ) : t.ms !== undefined ? (
          duration(t.ms)
        ) : null}
      </span>
    </li>
  );
}

function Handed({
  child,
  conversation: c,
  sessionId,
  now,
}: {
  child: RunTurn;
  conversation: Conversation;
  sessionId: string;
  now: number;
}) {
  const w = worked(c, child, now);
  return (
    <li
      data-handed={child.agent}
      className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs"
    >
      <Users aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
      <span className="text-muted-foreground">Handed to</span>
      <span className="text-sm font-medium">{child.agent}</span>
      <StatusPill status={child.status} />
      {w.ms !== null && child.status !== "running" ? (
        <span className="text-muted-foreground tabular-nums">{duration(w.ms)}</span>
      ) : null}
      <Link
        to="/ui/sessions/$sessionId"
        params={{ sessionId }}
        search={{ tab: "runs", run: child.id }}
        className="inline-flex items-center gap-1 rounded text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
      >
        Open its run
        <ChevronRight aria-hidden className="size-3" />
      </Link>
    </li>
  );
}

function Line({ icon, children }: { icon: ReactNode; children: ReactNode }) {
  return (
    <p className="flex min-w-0 items-start gap-1.5 text-xs text-muted-foreground">
      <span className="mt-px shrink-0">{icon}</span>
      <span className="min-w-0 break-words">{children}</span>
    </p>
  );
}

/** Whether the answer reached the person on the channel; nothing for a run that answers none. */
function ReplyStatus({ run }: { run: Run }) {
  const line = replyLine(run.reply);
  if (!line) return null;
  const Icon = line.tone === "sent" ? Check : line.tone === "failed" ? X : Clock;
  return (
    <p
      data-reply-status={line.tone}
      className={cn(
        "flex items-start gap-1.5 text-xs",
        line.tone === "failed" ? "text-failed" : "text-muted-foreground",
      )}
    >
      <Icon aria-hidden className="mt-px size-3.5 shrink-0" />
      <span>
        {line.text}
        {line.when ? (
          <>
            {line.when.lead}
            <time dateTime={line.when.iso}>{absoluteTime(line.when.iso)}</time>
          </>
        ) : null}
      </span>
    </p>
  );
}

function clip(s: string, max: number): string {
  const one = s.replace(/\s+/g, " ").trim();
  return one.length > max ? `${one.slice(0, max - 1)}…` : one;
}
