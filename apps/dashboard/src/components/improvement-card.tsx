/**
 * A change an agent proposed to itself: who, what kind, why, what it expects,
 * the runs it came from, and exactly what would change. Whether to offer the
 * decision is read from the caller's permissions; the brain's refusal is the
 * last word, and a 409 is shown here with its reason, the card left in place.
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { ArrowRight, Coins, Eye, Timer, TriangleAlert, UserRoundX } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import { decideImprovement } from "@/api";
import { ApiError } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import type {
  Improvement,
  ImprovementChange,
  ImprovementDecision,
  ImprovementWatch,
} from "@/api/types";
import { TriedOnPastRuns } from "@/components/improvement-replay";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Mono } from "@/components/ui-bits";
import { absoluteTime } from "@/lib/format";
import {
  APPROVE,
  costWords,
  fieldWords,
  isScript,
  kindWords,
  listNames,
  MARK_WORDS,
  missingWrites,
  patternWords,
  replayWorse,
  SAVES_WORDS,
  scopeWords,
  timeWords,
  usualWords,
  watchHeadline,
} from "@/lib/improvements";
import { type DiffLine, type Folded, lineDiff, withContext } from "@/lib/line-diff";
import { count } from "@/lib/runs";
import { cn } from "@/lib/utils";

/** A run it came from, linked to its conversation, which the brain gives only when the caller may open it. */
function RunLink({ run }: { run: Improvement["runs"][number] }) {
  const short = run.id.slice(0, 8);
  if (!run.session_id) return <Mono>{short}</Mono>;
  return (
    <Link
      to="/ui/sessions/$sessionId"
      params={{ sessionId: run.session_id }}
      search={{ tab: "runs", run: run.id }}
      aria-label={`Run ${short}`}
      className="inline-flex items-center gap-0.5 rounded text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
    >
      <Mono>{short}</Mono>
      <ArrowRight aria-hidden className="size-3" />
    </Link>
  );
}

/** Marks that give an agent more than it had: read before approving. */
const CAUTION = new Set(["widens_access", "widens_reach", "raises_cost", "changes_code"]);

const LINE = "grid grid-cols-[1.25rem_minmax(0,1fr)] whitespace-pre-wrap break-words pr-2";
const SIGN = "select-none text-center";

function DiffRow({ line }: { line: DiffLine | Folded }) {
  if (line.kind === "skip") {
    return (
      <span className={cn(LINE, "text-muted-foreground")}>
        <span aria-hidden className={SIGN}>
          ⋯
        </span>
        <span>{count(line.count, "unchanged line")}</span>
      </span>
    );
  }
  if (line.kind === "add") {
    return (
      <ins className={cn(LINE, "bg-succeeded/10 no-underline")}>
        <span aria-hidden className={cn(SIGN, "text-succeeded")}>
          +
        </span>
        <span className="sr-only">added: </span>
        <span>{line.text}</span>
      </ins>
    );
  }
  if (line.kind === "del") {
    return (
      <del className={cn(LINE, "bg-failed/10 no-underline [text-decoration:none]")}>
        <span aria-hidden className={cn(SIGN, "text-failed")}>
          −
        </span>
        <span className="sr-only">removed: </span>
        <span>{line.text}</span>
      </del>
    );
  }
  return (
    <span className={cn(LINE, "text-muted-foreground")}>
      <span aria-hidden className={SIGN} />
      <span>{line.text}</span>
    </span>
  );
}

/** One change: a line diff for text, names for a list, before and after for a value. */
function Change({ change }: { change: ImprovementChange }) {
  let body: React.ReactNode;
  if (change.type === "text") {
    const all = lineDiff(change.before, change.after);
    // A script is read before it is trusted, so none of it is folded away.
    const lines = isScript(change.field) ? all : withContext(all, 3);
    body = (
      <pre className="overflow-x-auto rounded-md bg-sheet py-2 font-mono text-xs">
        {lines.map((l, i) => (
          // The diff is fixed once drawn, and lines repeat, so position is the key.
          // biome-ignore lint/suspicious/noArrayIndexKey: a static list of lines
          <DiffRow key={i} line={l} />
        ))}
      </pre>
    );
  } else if (change.type === "list") {
    const names = listNames(change);
    body = (
      <ul className="flex flex-wrap gap-x-3 gap-y-1 text-sm">
        {names.map((n) =>
          n.change === "del" ? (
            <li key={`-${n.name}`}>
              <del className="text-failed [text-decoration:none]">
                <span aria-hidden>− </span>
                <span className="sr-only">removed: </span>
                {n.name}
              </del>
            </li>
          ) : n.change === "add" ? (
            <li key={`+${n.name}`}>
              <ins className="text-succeeded no-underline">
                <span aria-hidden>+ </span>
                <span className="sr-only">added: </span>
                {n.name}
              </ins>
            </li>
          ) : (
            <li key={n.name} className="text-muted-foreground">
              {n.name}
            </li>
          ),
        )}
        {names.length === 0 ? (
          <li className="text-muted-foreground">None, before or after.</li>
        ) : null}
      </ul>
    );
  } else {
    body = (
      <p className="text-sm break-words">
        <span className="sr-only">from </span>
        <del className="text-muted-foreground [text-decoration:none]">
          {change.before ?? "none"}
        </del>
        <span aria-hidden> → </span>
        <span className="sr-only"> to </span>
        <ins className="no-underline">{change.after ?? "none"}</ins>
      </p>
    );
  }
  return (
    <div className="space-y-1">
      <p className="text-xs text-muted-foreground break-words">{fieldWords(change.field)}</p>
      {body}
    </div>
  );
}

/**
 * How an applied change went: the headline in words, then the agent's usual
 * rate, cost, the pattern it targeted, votes and what was applied beside it.
 * A flag is said in words and marked with an icon, never by colour alone.
 */
function Watch({
  watch,
  goal,
  saves,
}: {
  watch: ImprovementWatch;
  goal: Improvement["goal"];
  saves: Improvement["saves"];
}) {
  const flagged = watch.status === "flagged";
  const cost = costWords(watch, goal);
  const time = goal === "efficiency" ? timeWords(watch) : null;
  const pattern = patternWords(watch);
  const votes = watch.before.up + watch.before.down + watch.after.up + watch.after.down;
  return (
    <section
      aria-label="How it went"
      data-watch={watch.status}
      className="space-y-1 rounded-md bg-sheet px-3 py-2 text-xs"
    >
      <p
        className={cn(
          "flex items-start gap-1.5 text-sm",
          flagged ? "text-warning" : "text-foreground",
        )}
      >
        {flagged ? (
          <TriangleAlert aria-hidden className="mt-0.5 size-3.5 shrink-0" />
        ) : (
          <Eye aria-hidden className="mt-0.5 size-3.5 shrink-0 text-muted-foreground" />
        )}
        <span>{watchHeadline(watch, saves)}</span>
      </p>
      {watch.status !== "watching" || watch.after.runs > 0 ? (
        <ul className="space-y-0.5 pl-5 text-muted-foreground">
          <li>{usualWords(watch)}</li>
          {cost ? <li>{cost}</li> : null}
          {time ? <li>{time}</li> : null}
          {pattern ? <li>{pattern}</li> : null}
          {votes > 0 ? (
            <li>
              Votes before 👍 {watch.before.up} 👎 {watch.before.down}, after 👍 {watch.after.up} 👎{" "}
              {watch.after.down}
            </li>
          ) : null}
          {watch.overlapping.length > 0 ? (
            <li>
              {count(watch.overlapping.length, "other change")} applied to the same agents meanwhile
            </li>
          ) : null}
        </ul>
      ) : null}
    </section>
  );
}

export function ImprovementCard({ teamId, item }: { teamId: string; item: Improvement }) {
  const qc = useQueryClient();
  const perms = usePermissions(teamId);

  // Reject and Revert ask once more, in place: they are not undone by the next click.
  const [confirming, setConfirming] = useState<ImprovementDecision["action"] | null>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const cardRef = useRef<HTMLDivElement>(null);
  // Set when the question is dismissed, so focus goes back to the button that asked it.
  const returnTo = useRef<string | null>(null);
  useEffect(() => {
    if (confirming) {
      confirmRef.current?.focus();
    } else if (returnTo.current) {
      cardRef.current?.querySelector<HTMLElement>(`[data-act="${returnTo.current}"]`)?.focus();
      returnTo.current = null;
    }
  }, [confirming]);

  const decide = useMutation({
    mutationFn: (d: ImprovementDecision) => decideImprovement(teamId, item.id, d),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["improvements", teamId] }),
    // A refusal means what the card shows is out of date: another approval
    // withdrew it, or its agent changed. The list is read again; a card still
    // in this tab keeps the reason under it.
    onError: (err) => {
      if (err instanceof ApiError && err.status === 409) {
        qc.invalidateQueries({ queryKey: ["improvements", teamId] });
      }
    },
    onSettled: () => setConfirming(null),
  });
  // The button pressed is gone or disabled once a decision fails, so focus
  // goes to the reason rather than to the page.
  const errorRef = useRef<HTMLParagraphElement>(null);
  useEffect(() => {
    if (decide.error) errorRef.current?.focus();
  }, [decide.error]);
  const askId = useId();

  const action: {
    label: string;
    do: ImprovementDecision["action"];
    primary: boolean;
    ask?: string;
  }[] =
    item.status === "waiting"
      ? [
          { label: "Reject", do: "reject", primary: false, ask: "Reject this change?" },
          { label: "Approve", do: "approve", primary: true },
        ]
      : item.status === "applied"
        ? [
            {
              label: "Revert",
              do: "revert",
              primary: false,
              ask: "Put the old version back?",
            },
            // A flag asks a person to look; keeping the change is a choice too.
            ...(item.watch?.status === "flagged"
              ? [{ label: "Clear flag", do: "clear" as const, primary: true }]
              : []),
          ]
        : [];
  const asking = action.find((a) => a.do === confirming && a.ask);
  const canDecide = !perms.isPending && perms.can(APPROVE);
  const lacking = canDecide ? missingWrites(item.kind, perms.can) : [];
  // Rejecting and clearing a flag write nothing, so they need only the
  // approve permission; the rest need the write the change makes.
  const offered =
    lacking.length > 0 ? action.filter((a) => a.do === "reject" || a.do === "clear") : action;
  const withheld = action.find((a) => !offered.includes(a));
  const readable = item.runs.filter((r) => r.readable);
  const hidden = item.runs.length - readable.length;
  const decidedAt = item.status === "reverted" ? item.reverted_at : (item.decided_at ?? null);
  // An efficiency change says what it aims to save, before what kind of change it is.
  const saves = item.goal === "efficiency" ? (item.saves ?? []) : [];
  const worse = replayWorse(item.replay);

  return (
    <div
      ref={cardRef}
      data-improvement={item.id}
      data-status={item.status}
      className="max-w-[75ch] space-y-3 rounded-lg bg-card px-4 py-3"
    >
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="min-w-0 text-sm font-medium break-words">{item.agent.name}</span>
        <span className="text-xs text-muted-foreground">
          {kindWords(item.kind)}
          {item.skill ? ` of ${item.skill.name}` : ""} · {scopeWords(item.scope)}
        </span>
        {item.status !== "waiting" ? <StatusPill status={item.status} /> : null}
        {decidedAt ? (
          <time dateTime={decidedAt} className="text-xs text-muted-foreground">
            {absoluteTime(decidedAt)}
          </time>
        ) : null}
      </div>

      {item.outsider ? (
        <p className="flex items-start gap-1.5 text-xs text-warning">
          <UserRoundX aria-hidden className="mt-px size-3.5 shrink-0" />
          <span>
            Outside sender: someone who is not a member spoke in a conversation this came from.
          </span>
        </p>
      ) : null}

      {item.marks.length > 0 || item.reaches.length > 0 || saves.length > 0 || worse ? (
        <ul aria-label="What kind of change" className="flex flex-wrap gap-1.5">
          {worse ? (
            <li
              data-worse
              className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs text-warning shadow-[inset_0_0_0_1px_var(--sheet-edge)]"
            >
              <TriangleAlert aria-hidden className="size-3 shrink-0" />
              Worse on past runs
            </li>
          ) : null}
          {saves.map((v) => {
            const Icon = v === "cost" ? Coins : Timer;
            return (
              <li
                key={v}
                data-saves={v}
                className="inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs shadow-[inset_0_0_0_1px_var(--sheet-edge)]"
              >
                <Icon aria-hidden className="size-3 shrink-0 text-muted-foreground" />
                {SAVES_WORDS[v]}
              </li>
            );
          })}
          {item.marks.map((m) => (
            <li
              key={m}
              className={cn(
                "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-xs shadow-[inset_0_0_0_1px_var(--sheet-edge)]",
                CAUTION.has(m) && "text-warning",
              )}
            >
              {CAUTION.has(m) ? <TriangleAlert aria-hidden className="size-3 shrink-0" /> : null}
              {MARK_WORDS[m] ?? m.replace(/_/g, " ")}
            </li>
          ))}
          {item.reaches.length > 0 ? (
            <li className="self-center text-xs text-muted-foreground">
              Reaches {count(item.reaches.length, "agent")}:{" "}
              {item.reaches.map((a) => a.name).join(", ")}
            </li>
          ) : null}
        </ul>
      ) : null}

      <div className="space-y-1">
        <p className="text-sm break-words whitespace-pre-wrap">{item.why}</p>
        <p className="text-sm text-muted-foreground break-words whitespace-pre-wrap">
          Expects: {item.hypothesis}
        </p>
      </div>

      <div className="space-y-3">
        {item.changes.map((c) => (
          <Change key={c.field} change={c} />
        ))}
      </div>

      {item.replay && item.replay.status !== "off" ? (
        <TriedOnPastRuns teamId={teamId} proposalId={item.id} replay={item.replay} />
      ) : null}

      {item.watch ? <Watch watch={item.watch} goal={item.goal} saves={item.saves} /> : null}

      {item.runs.length > 0 ? (
        <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-muted-foreground">
          <span>From {count(item.runs.length, "run")}</span>
          {readable.map((r) => (
            <RunLink key={r.id} run={r} />
          ))}
          {hidden > 0 ? (
            <span>{hidden === 1 ? "a run you cannot open" : `${hidden} runs you cannot open`}</span>
          ) : null}
        </p>
      ) : null}

      {item.pass_id && canDecide ? (
        <p className="text-xs">
          <Link
            to="/ui/improvements"
            search={{ tab: "passes", agent: item.agent.id }}
            className="inline-flex items-center gap-0.5 rounded text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
          >
            From a pass
            <ArrowRight aria-hidden className="size-3" />
          </Link>
        </p>
      ) : null}

      {action.length > 0 && canDecide ? (
        <div className="space-y-2 border-t border-rule pt-3">
          {withheld ? (
            <p data-no-answer className="text-xs text-muted-foreground">
              {withheld.do === "approve" ? "Approving" : "Reverting"} this takes{" "}
              {lacking.map((p, i) => (
                <span key={p}>
                  {i > 0 ? " and " : null}
                  <Mono className="text-foreground">{p}</Mono>
                </span>
              ))}
              , which a team admin can grant.
            </p>
          ) : null}
          {offered.length > 0 ? (
            <div className="flex flex-wrap items-center justify-end gap-2">
              {asking ? (
                <>
                  <span id={askId} className="mr-auto text-sm" role="status">
                    {asking.ask}
                  </span>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={decide.isPending}
                    onClick={() => {
                      returnTo.current = asking.do;
                      setConfirming(null);
                    }}
                  >
                    Keep it
                  </Button>
                  <Button
                    ref={confirmRef}
                    size="sm"
                    variant="destructive"
                    aria-describedby={askId}
                    loading={decide.isPending}
                    onClick={() => decide.mutate({ action: asking.do })}
                  >
                    {asking.label}
                  </Button>
                </>
              ) : (
                offered.map((a) => (
                  <Button
                    key={a.do}
                    size="sm"
                    variant={a.primary ? "default" : "outline"}
                    loading={decide.isPending && a.primary}
                    disabled={decide.isPending}
                    data-act={a.do}
                    onClick={() => {
                      if (a.ask) setConfirming(a.do);
                      else decide.mutate({ action: a.do });
                    }}
                  >
                    {a.label}
                  </Button>
                ))
              )}
            </div>
          ) : null}
        </div>
      ) : null}

      {decide.error ? (
        <p ref={errorRef} tabIndex={-1} role="alert" className="text-xs text-failed outline-none">
          {decide.error.message}
        </p>
      ) : null}
    </div>
  );
}
