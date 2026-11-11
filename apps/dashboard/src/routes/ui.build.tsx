import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight, Check, CircleAlert, LoaderCircle, Plus } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";

import { listProposals, listSessionRuns, startRun } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Run } from "@/api/types";
import { isSend, Refusal, RUN_WRITE } from "@/components/ask-team";
import { Markdown } from "@/components/markdown";
import { ProposalCard } from "@/components/proposal-card";
import { useRunEvents } from "@/components/run-detail";
import { runLink } from "@/components/run-table";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Textarea } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import { askProblem } from "@/lib/ask";
import { buildRefusal, isLive, liveOf, type Turn, turnsOf } from "@/lib/build";

type BuildSearch = { session?: string };

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function buildSearch(raw: Record<string, unknown>): BuildSearch {
  const s = raw.session;
  return { session: typeof s === "string" && UUID.test(s) ? s : undefined };
}

export const Route = createFileRoute("/ui/build")({
  validateSearch: buildSearch,
  component: Build,
});

const ABOUT =
  "The builder designs agents, MCP servers and skills from what you ask, and proposes them as a blueprint. It reads the team and writes nothing: you see the plan, as it would apply for you, and apply it yourself. It runs on the team's model.";

/** How often a conversation with a run still working is read again. */
export const POLL_MS = 1500;

function Build() {
  const { teamId } = useCurrentTeam();
  const { session } = Route.useSearch();
  const navigate = Route.useNavigate();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Builder"
        finding="Ask for agents, servers or skills in plain words; apply what it proposes."
        description={ABOUT}
        actions={
          session ? (
            <Button variant="outline" size="sm" onClick={() => void navigate({ search: {} })}>
              <Plus aria-hidden className="size-4" />
              New conversation
            </Button>
          ) : null
        }
      />
      {teamId ? (
        <Conversation key={session ?? "new"} teamId={teamId} session={session} />
      ) : (
        <EmptyState title="No team" body="Join or create a team to build in it." />
      )}
    </div>
  );
}

function Conversation({ teamId, session }: { teamId: string; session?: string }) {
  const qc = useQueryClient();
  const navigate = Route.useNavigate();
  const permissions = usePermissions(teamId);
  const box = useRef<HTMLTextAreaElement>(null);

  const runs = useQuery({
    queryKey: ["build", teamId, session, "runs"],
    queryFn: () => listSessionRuns(teamId, session as string),
    enabled: Boolean(session),
    refetchInterval: (q) => (q.state.data?.items.some(isLive) ? POLL_MS : false),
  });
  const live = runs.data?.items.some(isLive) ?? false;
  const proposals = useQuery({
    queryKey: ["proposals", teamId, session],
    queryFn: () => listProposals(teamId, session as string),
    enabled: Boolean(session),
    refetchInterval: live ? POLL_MS : false,
  });

  // A run that just ended may have proposed after the last read.
  const wasLive = useRef(false);
  useEffect(() => {
    if (wasLive.current && !live)
      void qc.invalidateQueries({ queryKey: ["proposals", teamId, session] });
    wasLive.current = live;
  }, [live, qc, teamId, session]);

  const send = useMutation({
    mutationFn: (input: string) => startRun(teamId, { input, builder: true, session_id: session }),
    onSuccess: (run) => {
      void qc.invalidateQueries({ queryKey: ["build", teamId, run.session_id] });
      void qc.invalidateQueries({ queryKey: ["sessions", teamId] });
      void qc.invalidateQueries({ queryKey: ["runs", teamId] });
      if (run.session_id !== session) void navigate({ search: { session: run.session_id } });
    },
  });

  const turns: Turn[] = turnsOf(runs.data?.items ?? [], proposals.data?.items ?? []);
  const loading = Boolean(session) && (runs.isPending || proposals.isPending);

  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-4">
      {loading ? (
        <Skeleton className="h-32 w-full" />
      ) : runs.isError ? (
        <ErrorState
          what="this conversation"
          error={runs.error}
          onRetry={() => void runs.refetch()}
        />
      ) : turns.length === 0 ? (
        <EmptyState
          title="What should the team have?"
          body="Describe an agent, an MCP server or a skill. The builder asks what it needs to know, then proposes; nothing changes until you apply it."
        />
      ) : (
        <ol aria-label="Conversation" className="space-y-4">
          {turns.map((t) => (
            <TurnView
              key={t.run.id}
              teamId={teamId}
              turn={t}
              onChange={() => box.current?.focus()}
            />
          ))}
        </ol>
      )}

      {permissions.isPending ? null : permissions.can(RUN_WRITE) ? (
        <Composer
          box={box}
          busy={send.isPending || live}
          sending={send.isPending}
          error={send.isError ? send.error : null}
          onType={() => send.isError && send.reset()}
          onSend={(text) => send.mutate(text)}
          first={!session}
        />
      ) : (
        <p className="text-sm text-muted-foreground">
          Asking the builder needs {RUN_WRITE} in this team. An admin can grant it.
        </p>
      )}
    </div>
  );
}

function TurnView({
  teamId,
  turn,
  onChange,
}: {
  teamId: string;
  turn: Turn;
  onChange: () => void;
}) {
  const { run, proposal } = turn;
  return (
    <li className="space-y-3">
      <div className="ml-auto w-fit max-w-[85%] whitespace-pre-wrap rounded-lg bg-muted px-3 py-2 text-sm">
        {run.input}
      </div>
      {isLive(run) ? (
        <LiveTurn teamId={teamId} run={run} />
      ) : run.status === "succeeded" ? (
        run.output ? (
          <div className="max-w-[85%] text-sm">
            <Markdown>{run.output}</Markdown>
          </div>
        ) : null
      ) : (
        <p role="alert" className="text-sm text-destructive">
          The builder {run.status === "cancelled" ? "was stopped" : "couldn't finish"}
          {run.error ? `: ${run.error}` : "."}{" "}
          <Link {...runLink(run)} className="underline underline-offset-4">
            View the trace
          </Link>
        </p>
      )}
      {proposal ? <ProposalCard teamId={teamId} proposal={proposal} onChange={onChange} /> : null}
    </li>
  );
}

/**
 * A run still working: what it is doing, as it does it, and its reply as it
 * is written. When its stream ends, the conversation is read again for the
 * run's output and its proposal.
 */
function LiveTurn({ teamId, run }: { teamId: string; run: Run }) {
  const qc = useQueryClient();
  const { events, failed } = useRunEvents(teamId, run.id, () => {
    void qc.invalidateQueries({ queryKey: ["build", teamId, run.session_id] });
    void qc.invalidateQueries({ queryKey: ["proposals", teamId, run.session_id] });
  });
  const { activity, text } = liveOf(events);
  return (
    <div className="max-w-[85%] space-y-2 text-sm">
      <ul aria-label="What the builder is doing" aria-live="polite" className="space-y-1">
        {activity.map((a) => (
          <li key={a.id} className="flex items-center gap-2 text-muted-foreground">
            {a.state === "running" ? (
              <LoaderCircle aria-hidden className="size-4 animate-spin" />
            ) : a.state === "failed" ? (
              <CircleAlert aria-hidden className="size-4 text-warning" />
            ) : (
              <Check aria-hidden className="size-4 text-success" />
            )}
            <span>
              {a.label}
              {a.state === "failed" ? " (it failed; the builder carries on)" : ""}
            </span>
          </li>
        ))}
      </ul>
      {text ? (
        <div aria-busy="true">
          <Markdown>{text}</Markdown>
        </div>
      ) : (
        <p role="status" className="flex items-center gap-2 text-muted-foreground">
          {activity.length === 0 ? (
            <LoaderCircle aria-hidden className="size-4 animate-spin" />
          ) : null}
          {activity.length === 0 ? "The builder is starting…" : null}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        {failed ? `Lost the live view (${failed}); the reply still arrives. ` : null}
        <Link
          {...runLink(run)}
          className="inline-flex items-center gap-1 underline underline-offset-4"
        >
          View the trace
          <ArrowUpRight aria-hidden className="size-3" />
        </Link>
      </p>
    </div>
  );
}

function Composer({
  box,
  busy,
  sending,
  error,
  onType,
  onSend,
  first,
}: {
  box: React.RefObject<HTMLTextAreaElement | null>;
  busy: boolean;
  sending: boolean;
  error: unknown;
  onType: () => void;
  onSend: (text: string) => void;
  first: boolean;
}) {
  const [input, setInput] = useState("");
  const id = useId();
  const text = input.trim();
  const problem = askProblem(text);
  const send = () => {
    if (text === "" || problem || busy) return;
    onSend(text);
    setInput("");
  };
  return (
    <form
      aria-label="Message the builder"
      onSubmit={(e) => {
        e.preventDefault();
        send();
      }}
      className="space-y-3 rounded-lg bg-card p-4"
    >
      <Field
        label={first ? "What do you want?" : "Reply to the builder"}
        htmlFor={id}
        hint={
          busy && !sending
            ? "The builder is still working on the last message."
            : "Ctrl+Enter or ⌘+Enter sends it."
        }
        error={problem}
      >
        <Textarea
          id={id}
          ref={box}
          value={input}
          rows={2}
          placeholder={
            first
              ? "An agent that triages support email, using our FAQ."
              : "Sonnet, and yes to the helpdesk server."
          }
          onChange={(e) => {
            setInput(e.target.value);
            onType();
          }}
          onKeyDown={(e) => {
            if (isSend(e)) {
              e.preventDefault();
              send();
            }
          }}
          className="bg-background"
        />
      </Field>
      <div className="flex flex-col-reverse items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">{error ? <Refusal refusal={buildRefusal(error)} /> : null}</div>
        <Button
          type="submit"
          size="sm"
          loading={sending}
          disabled={text === "" || problem !== null || busy}
        >
          Send
        </Button>
      </div>
    </form>
  );
}
