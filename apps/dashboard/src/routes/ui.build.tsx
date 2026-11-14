import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowUpRight, Check, CircleAlert, LoaderCircle, Plus } from "lucide-react";
import { useEffect, useId, useRef } from "react";

import {
  downloadAttachment,
  listAttachments,
  listProposals,
  listSessionRuns,
  listTeamRuns,
} from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Run, SessionEventFile } from "@/api/types";
import { RUN_WRITE } from "@/components/ask-team";
import { Composer } from "@/components/composer";
import { type SentFiles, SentFilesView } from "@/components/conversation-view";
import { saveDownload } from "@/components/file-chip";
import { Markdown } from "@/components/markdown";
import { ProposalCard } from "@/components/proposal-card";
import { useRunEvents } from "@/components/run-detail";
import { runLink } from "@/components/run-table";
import { ErrorState } from "@/components/states";
import { StatusPill as RunStatus } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import { conversationsOf, isLive, liveOf, saidOf, type Turn, turnsOf } from "@/lib/build";
import { relativeTime } from "@/lib/format";

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
  const permissions = usePermissions(teamId);
  const composerId = useId();

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

  // What was sent with each message, by the message: a turn shows its own.
  const attachments = useQuery({
    queryKey: ["build", teamId, session, "attachments"],
    queryFn: () => listAttachments(teamId, session as string),
    enabled: Boolean(session),
  });
  const sentWith = (messageId: string | null | undefined): SessionEventFile[] =>
    (attachments.data?.items ?? [])
      .filter((a) => messageId && a.message_id === messageId)
      .map((a) => ({
        id: a.id,
        filename: a.filename,
        media_type: a.media_type,
        size: a.size_bytes,
      }));
  const files: SentFiles | undefined = session
    ? {
        open: (f) =>
          void downloadAttachment(teamId, session, f.id).then((b) => saveDownload(b, f.filename)),
        load: (f) => downloadAttachment(teamId, session, f.id),
      }
    : undefined;

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
              sent={sentWith(t.run.message_id)}
              files={files}
              onChange={() => document.getElementById(composerId)?.focus()}
            />
          ))}
        </ol>
      )}

      {permissions.isPending ? null : permissions.can(RUN_WRITE) ? (
        <Composer
          teamId={teamId}
          writes={
            session ? { kind: "reply", sessionId: session } : { kind: "start", builder: true }
          }
          label={session ? "Reply to the builder" : "What do you want?"}
          placeholder={
            session
              ? "Sonnet, and yes to the helpdesk server."
              : "An agent that triages support email, using our FAQ."
          }
          className="rounded-lg bg-card p-4"
          id={composerId}
        />
      ) : (
        <p className="text-sm text-muted-foreground">
          Asking the builder needs {RUN_WRITE} in this team. An admin can grant it.
        </p>
      )}
      {session ? null : <RecentConversations teamId={teamId} />}
    </div>
  );
}

/**
 * The builder conversations the caller can carry on, so one is found again
 * after leaving it: by the sidebar, a new tab, another day.
 */
function RecentConversations({ teamId }: { teamId: string }) {
  const recent = useQuery({
    queryKey: ["build", teamId, "recent"],
    queryFn: () => listTeamRuns(teamId, null, { builder: true, limit: 50 }),
  });
  const conversations = conversationsOf(recent.data?.items ?? []);
  if (recent.isPending || (recent.isSuccess && conversations.length === 0)) return null;
  return (
    <section aria-labelledby="recent-heading" className="space-y-2">
      <h2 id="recent-heading" className="text-sm font-medium">
        Recent conversations
      </h2>
      {recent.isError ? (
        <ErrorState
          what="recent conversations"
          error={recent.error}
          onRetry={() => void recent.refetch()}
        />
      ) : (
        <ul className="divide-y divide-rule rounded-lg border border-border">
          {conversations.map((r) => (
            <li key={r.session_id}>
              <Link
                to="/ui/build"
                search={{ session: r.session_id }}
                className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm hover:bg-muted"
              >
                <span className="min-w-0 flex-1 truncate">{r.input}</span>
                <RunStatus status={r.status} />
                <span className="text-xs text-muted-foreground">{relativeTime(r.created_at)}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function TurnView({
  teamId,
  turn,
  sent,
  files,
  onChange,
}: {
  teamId: string;
  turn: Turn;
  /** The files sent with the message the run answers. */
  sent: SessionEventFile[];
  files?: SentFiles;
  onChange: () => void;
}) {
  const { run, proposal } = turn;
  const said = saidOf(run, sent);
  return (
    <li className="space-y-3">
      <div className="ml-auto w-fit max-w-[85%] rounded-lg bg-muted px-3 py-2 text-sm">
        {said ? <p className="whitespace-pre-wrap break-words">{said}</p> : null}
        <SentFilesView sent={sent} files={files} />
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
