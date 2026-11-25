/**
 * A call an agent asked a person about before making it: what it wants to
 * call, with what, how long the request lasts, and the person's answer.
 *
 * One card for the conversation and the Requests page. The stream says a
 * request was made and answered; the API adds the arguments and whom it was
 * asked for, so a card in a conversation reads it too. Whether to offer the
 * answer is decided from the caller's permissions, and the brain's refusal is
 * the last word: a 403 shows as it came, a 409 shows the request as it now is.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight } from "lucide-react";
import { useEffect, useId, useState } from "react";

import { decideToolApproval, getToolApproval } from "@/api";
import { ApiError } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useMe } from "@/api/session";
import type { ApprovalDecision, ToolApproval } from "@/api/types";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Mono } from "@/components/ui-bits";
import {
  answeredBy,
  mayAnswer,
  merged,
  outcome,
  type RequestView,
  timeLeft,
  toolParts,
  whyNot,
} from "@/lib/approvals";
import { absoluteTime } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Ticks every 30 seconds while a request waits, for the time it has left. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, [active]);
  return now;
}

/** The request a 409 carries, or null when its body is not one. */
function requestOf(err: ApiError): ToolApproval | null {
  try {
    const parsed = JSON.parse(err.message) as Partial<ToolApproval>;
    return typeof parsed.id === "string" && typeof parsed.status === "string"
      ? (parsed as ToolApproval)
      : null;
  } catch {
    return null;
  }
}

export function RequestCard({
  teamId,
  request,
  fetchFull = false,
  nameOf = () => null,
  link,
}: {
  teamId: string;
  request: RequestView;
  /** Read the request from the API too, for the arguments and whom it was asked for. */
  fetchFull?: boolean;
  nameOf?: (userId: string) => string | null;
  /** Where to read the run that asked, when the card is not already in its conversation. */
  link?: React.ReactNode;
}) {
  const qc = useQueryClient();
  const perms = usePermissions(teamId);
  const me = useMe();
  const [answered, setAnswered] = useState<ToolApproval | null>(null);
  const [conflict, setConflict] = useState(false);
  const [refused, setRefused] = useState<string | null>(null);
  const [reason, setReason] = useState("");
  const [open, setOpen] = useState(false);
  const argsId = useId();
  const reasonId = useId();

  const full = useQuery({
    queryKey: ["tool-approval", teamId, request.id],
    queryFn: ({ signal }) => getToolApproval(teamId, request.id, signal),
    enabled: fetchFull,
  });
  const base = merged(request, full.data);
  const view: RequestView = answered
    ? { ...merged(base, answered), superseded: base.superseded }
    : base;

  const pending = view.status === "pending";
  const now = useNow(pending);
  const left = pending ? timeLeft(view.expiresAt, now) : null;
  const lapsed = pending && left === null;

  const decide = useMutation({
    mutationFn: (d: ApprovalDecision) => decideToolApproval(teamId, request.id, d),
    onSuccess: (a) => {
      setAnswered(a);
      setRefused(null);
      void qc.invalidateQueries({ queryKey: ["approvals", teamId] });
      void qc.invalidateQueries({ queryKey: ["tool-approval", teamId, request.id] });
    },
    onError: (err: Error) => {
      if (err instanceof ApiError && err.status === 403) {
        setRefused(err.message || "you may not answer this request");
        return;
      }
      if (err instanceof ApiError && err.status === 409) {
        const now = requestOf(err);
        if (now) {
          setAnswered(now);
          setConflict(true);
          void qc.invalidateQueries({ queryKey: ["approvals", teamId] });
          return;
        }
      }
    },
  });

  const can = mayAnswer(view, perms.can, me);
  const offer = pending && !lapsed && !perms.isPending && can && refused === null;
  const tool = toolParts(view.tool);
  const who = answeredBy(view, nameOf);
  const args = view.arguments;
  const failed =
    decide.error instanceof ApiError && ![403, 409].includes(decide.error.status)
      ? decide.error.message
      : decide.error && !(decide.error instanceof ApiError)
        ? decide.error.message
        : null;

  const send = (approve: boolean, scope?: "run") =>
    decide.mutate({
      approve,
      ...(scope ? { scope } : {}),
      ...(reason.trim() ? { reason: reason.trim() } : {}),
      via: "dashboard",
    });

  return (
    <div
      data-request={view.id}
      data-status={view.status}
      className="max-w-[75ch] space-y-2.5 rounded-lg bg-card px-4 py-3"
    >
      <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
        <span className="min-w-0 text-sm font-medium break-words">
          {tool.server ? (
            <>
              <span className="text-muted-foreground">{tool.server}</span>
              <span aria-hidden className="text-muted-foreground">
                {" · "}
              </span>
            </>
          ) : null}
          <span>{tool.name}</span>
        </span>
        <span className="text-xs text-muted-foreground">{view.class}</span>
        {pending ? (
          <StatusPill status={lapsed ? "expired" : "pending"} />
        ) : (
          <StatusPill status={view.status} />
        )}
        <span className="ml-auto flex items-baseline gap-2 text-xs text-muted-foreground">
          <span className="sr-only">Code</span>
          <Mono className="text-foreground">{view.code}</Mono>
          {left ? <span className="tabular-nums">{left}</span> : null}
        </span>
      </div>

      {view.summary ? (
        // The call's own values: text, never parsed.
        <p className="text-sm break-words whitespace-pre-wrap">{view.summary}</p>
      ) : null}

      {!pending || lapsed ? (
        <p data-outcome className="text-xs text-muted-foreground">
          <span className="font-medium text-foreground">
            {lapsed ? "Expired: nobody answered in time" : outcome(view)}
          </span>
          {who ? ` ${who}` : null}
          {view.reason ? <span className="block">“{view.reason}”</span> : null}
        </p>
      ) : null}

      {conflict ? (
        <p role="status" className="text-xs text-muted-foreground">
          This was already settled, so your answer was not used.
        </p>
      ) : null}

      {offer ? (
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" disabled={decide.isPending} onClick={() => send(true)}>
              Approve
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={decide.isPending}
              onClick={() => send(true, "run")}
            >
              Approve for this run
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={decide.isPending}
              onClick={() => send(false)}
            >
              Decline
            </Button>
          </div>
          <label htmlFor={reasonId} className="sr-only">
            Reason, optional
          </label>
          <Input
            id={reasonId}
            value={reason}
            maxLength={1000}
            placeholder="Reason, optional: told to the agent"
            onChange={(e) => setReason(e.target.value)}
            className="max-w-sm"
          />
        </div>
      ) : pending && !lapsed && !perms.isPending ? (
        <p data-no-answer className="text-xs text-muted-foreground">
          {refused ?? whyNot(view, perms.can, me)}
        </p>
      ) : null}

      {failed ? (
        <p role="alert" className="text-xs text-failed">
          {failed}
        </p>
      ) : null}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={argsId}
          onClick={() => setOpen(!open)}
          className="-ml-1 inline-flex min-h-7 items-center gap-1 rounded px-1 hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          <ChevronRight
            aria-hidden
            className={cn(
              "size-3.5 transition-transform duration-(--dur-press) ease-(--ease-out) motion-reduce:transition-none",
              open && "rotate-90",
            )}
          />
          Arguments
        </button>
        {link}
        <time dateTime={view.expiresAt} className="ml-auto">
          {pending ? "Expires" : "Was to expire"} {absoluteTime(view.expiresAt)}
        </time>
      </div>
      {open ? (
        <pre
          id={argsId}
          className="max-h-60 relative overflow-auto rounded-md bg-sheet px-3 py-2 font-mono text-xs break-words whitespace-pre-wrap"
        >
          {args
            ? JSON.stringify(args, null, 2)
            : full.isPending && fetchFull
              ? "Reading the arguments…"
              : "The arguments are not available to you."}
        </pre>
      ) : null}
    </div>
  );
}
