import {
  type InfiniteData,
  type UseInfiniteQueryResult,
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowDown, ChevronRight, Loader2, Paperclip } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";

import {
  downloadAttachment,
  fetchWhoami,
  getRun,
  getSession,
  listAttachments,
  listChannels,
  listMembers,
  listSessionRuns,
} from "@/api";
import { ApiError, nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Attachment, Page, Run, Session } from "@/api/types";
import { Composer } from "@/components/composer";
import { ConversationView } from "@/components/conversation-view";
import { Time } from "@/components/facts";
import { isLive, RunDetail, RunStatus } from "@/components/run-detail";
import { HandedTo } from "@/components/run-table";
import { useSessionStream } from "@/components/session-stream";
import { ErrorState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, LoadMore } from "@/components/ui-bits";
import { awaitsReply } from "@/lib/conversation";
import { fileSize } from "@/lib/format";
import { useMediaQuery } from "@/lib/media";
import { sessionName } from "@/lib/sessions";
import { cn } from "@/lib/utils";

type Tab = "messages" | "runs";

/**
 * Which tab is open and which run is shown live in the URL, so a run's trace
 * can be linked to. Both keys are always returned: the router lays what this
 * returns over the query string as it was parsed, so a key left out would let
 * a bad value through untouched. A run id that parsed as a number is still an
 * id.
 */
type SessionSearch = { tab?: Tab | undefined; run?: string | undefined };

function sessionSearch(search: Record<string, unknown>): SessionSearch {
  const { tab, run } = search;
  return {
    tab: tab === "messages" || tab === "runs" ? tab : undefined,
    run:
      (typeof run === "string" && run !== "") || typeof run === "number" ? String(run) : undefined,
  };
}

export const Route = createFileRoute("/ui/sessions/$sessionId")({
  validateSearch: sessionSearch,
  component: SessionPage,
});

/**
 * The session's own details. A 404 is not an error to show: the page still
 * has its messages and runs to answer for themselves, and titles with the id.
 */
function useSession(teamId: string | null, sessionId: string) {
  const qc = useQueryClient();
  return useQuery({
    queryKey: ["sessions", teamId, sessionId, "meta"],
    queryFn: async (): Promise<Session | null> => {
      try {
        return await getSession(teamId as string, sessionId);
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return null;
        throw err;
      }
    },
    // Whichever filtered list the reader came from already holds the row.
    placeholderData: () =>
      qc
        .getQueriesData<{ pages: Page<Session>[] }>({ queryKey: ["sessions", teamId, "paged"] })
        .flatMap(([, data]) => data?.pages.flatMap((p) => p.items) ?? [])
        .find((s) => s.id === sessionId),
    enabled: Boolean(teamId),
  });
}

function SessionPage() {
  const { sessionId } = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const { teamId } = useCurrentTeam();
  const session = useSession(teamId, sessionId);

  const runs = useInfiniteQuery({
    queryKey: ["sessions", teamId, sessionId, "runs"],
    queryFn: ({ pageParam }) => listSessionRuns(teamId as string, sessionId, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
    // While the newest run works, its answer is read again until it lands:
    // the conversation shows it whether or not its trace is open. A reply
    // still on its way to the channel is read again too, more slowly: its
    // next try can be an hour off.
    refetchInterval: (q) => {
      const newest = q.state.data?.pages[0]?.items[0];
      if (newest && isLive(newest.status)) return LIVE_EVERY_MS;
      return awaitsReply(q.state.data?.pages.flatMap((p) => p.items) ?? [])
        ? REPLY_EVERY_MS
        : false;
    },
  });

  // A failed or running latest run is what the reader came for, so a phone
  // opens on it; a tab the reader chose always wins.
  const latest = runs.data?.pages[0]?.items[0];
  const needsLook = latest?.status === "failed" || latest?.status === "running";
  const tab: Tab = search.tab ?? (search.run || needsLook ? "runs" : "messages");
  const s = session.data ?? null;
  const title = s ? sessionName(s) : sessionId;
  const loadedRuns = runs.data?.pages.reduce((n, p) => n + p.items.length, 0) ?? 0;
  // Wide enough for both: the conversation beside the run it started.
  const sideBySide = useMediaQuery("(min-width: 80rem)");
  const runsPanel = teamId ? (
    <Runs
      teamId={teamId}
      sessionId={sessionId}
      runs={runs}
      selected={search.run ?? null}
      onChanged={() => runs.refetch()}
    />
  ) : null;

  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <nav aria-label="Breadcrumb">
          <ol className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
            <li>
              <Link
                to="/ui/sessions"
                className="rounded underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
              >
                Sessions
              </Link>
            </li>
            <li aria-hidden>
              <ChevronRight className="size-3" />
            </li>
            <li aria-current="page" className="min-w-0 truncate text-foreground">
              {title}
            </li>
          </ol>
        </nav>
        <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
        {s ? (
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
            <StatusPill status={s.status} className="text-sm text-foreground" />
            <span aria-hidden>·</span>
            <span>{s.kind}</span>
            <span aria-hidden>·</span>
            <span>
              started <Time iso={s.started_at} />
            </span>
            <span aria-hidden>·</span>
            <span>
              last message <Time iso={s.last_message_at} />
            </span>
          </p>
        ) : session.isError ? (
          <ErrorState
            what="this session's details"
            error={session.error}
            onRetry={() => session.refetch()}
            retrying={session.isRefetching}
          />
        ) : session.isPending && teamId ? (
          <Skeleton className="h-4 w-64" />
        ) : null}
      </header>

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its sessions." />
      ) : sideBySide ? (
        <div className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] items-start gap-8">
          <section aria-labelledby="conversation" className="space-y-3">
            <h2 id="conversation" className="text-base font-medium">
              Conversation
            </h2>
            <Conversation teamId={teamId} sessionId={sessionId} session={s} runs={runs} />
          </section>
          <section aria-labelledby="runs" className="space-y-3">
            <h2 id="runs" className="flex items-center gap-2 text-base font-medium">
              Runs
              {runs.isSuccess ? (
                <span className="text-sm font-normal text-muted-foreground tabular-nums">
                  {loadedRuns}
                </span>
              ) : null}
            </h2>
            {runsPanel}
          </section>
        </div>
      ) : (
        <Tabs
          value={tab}
          onValueChange={(v) =>
            navigate({ search: (prev) => ({ ...prev, tab: v as Tab }), replace: true })
          }
          className="gap-4"
        >
          <TabsList>
            <TabsTrigger value="messages">Conversation</TabsTrigger>
            <TabsTrigger value="runs">
              Runs
              {runs.isSuccess ? (
                <span className="rounded-full bg-card px-1.5 text-xs font-medium tabular-nums text-muted-foreground">
                  {loadedRuns}
                </span>
              ) : null}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="messages">
            <Conversation teamId={teamId} sessionId={sessionId} session={s} runs={runs} />
          </TabsContent>
          <TabsContent value="runs">{runsPanel}</TabsContent>
        </Tabs>
      )}
    </div>
  );
}

/* --------------------------------------------------------------- messages */

/**
 * Saves what a stranger may have sent. Retyped as plain bytes and handed over
 * as a download, so the browser never renders it on this origin, whatever
 * type it claims.
 */
function save(bytes: Blob, filename: string): void {
  const url = URL.createObjectURL(bytes.slice(0, bytes.size, "application/octet-stream"));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.rel = "noopener";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

/** How often a working run is read again for its answer. */
const LIVE_EVERY_MS = 3000;
const REPLY_EVERY_MS = 15_000;

/**
 * The conversation: what people said, and each run with what it was asked
 * and what it answered, oldest first like a chat. Both are paged newest
 * first, so "Load more" sits above and brings back what was earlier.
 */
function Conversation({
  teamId,
  sessionId,
  session,
  runs,
}: {
  teamId: string;
  sessionId: string;
  session: Session | null;
  runs: RunsQuery;
}) {
  const stream = useSessionStream(teamId, sessionId);
  const c = stream.conversation;
  const attachments = useInfiniteQuery({
    queryKey: ["sessions", teamId, sessionId, "attachments"],
    queryFn: ({ pageParam }) => listAttachments(teamId, sessionId, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });

  // Who a message's sender was bound to, by the team's members.
  const members = useQuery({
    queryKey: ["members", teamId],
    queryFn: () => listMembers(teamId),
    retry: false,
  });
  const nameOf = (userId: string) => {
    const m = members.data?.items.find((x) => x.user_id === userId);
    return m ? m.email || m.subject : null;
  };
  const me = useMe();

  const [failed, setFailed] = useState<string | null>(null);
  const download = async (f: Attachment) => {
    setFailed(null);
    try {
      save(await downloadAttachment(teamId, sessionId, f.id), f.filename);
    } catch (err) {
      setFailed(`${f.filename}: ${err instanceof Error ? err.message : String(err)}`);
    }
  };
  const files = attachments.data?.pages.flatMap((p) => p.items) ?? [];
  const follow = useFollowBottom(c);

  return (
    <section aria-label="Messages" className="space-y-4">
      {stream.failed !== null ? (
        <ErrorState
          what="the conversation"
          error={stream.failed}
          onRetry={stream.retry}
          retrying={false}
        />
      ) : stream.loading ? (
        <div className="space-y-3" role="status" aria-label="Loading messages">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-4/5" />
        </div>
      ) : c.order.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing has been said here.</p>
      ) : (
        <ConversationView
          conversation={c}
          sessionId={sessionId}
          rows={runs.data?.pages.flatMap((p) => p.items) ?? []}
          people={{ nameOf, me }}
        />
      )}
      {stream.reconnecting ? (
        <p role="status" className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 aria-hidden className="size-3 animate-spin" />
          The connection dropped. Following the conversation again…
        </p>
      ) : null}

      {attachments.isError ? (
        <ErrorState
          what="the attachments"
          error={attachments.error}
          onRetry={() => attachments.refetch()}
          retrying={attachments.isRefetching}
        />
      ) : files.length > 0 ? (
        <section aria-label="Attachments" className="space-y-2 pt-2">
          <h3 className="text-sm font-medium">Attachments</h3>
          <ul className="divide-y divide-rule rounded-lg bg-card text-sm">
            {files.map((f) => (
              <li key={f.id} className="flex items-center justify-between gap-3 px-3 py-2">
                <button
                  type="button"
                  className="inline-flex min-w-0 items-center gap-2 truncate rounded text-left underline-offset-2 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
                  aria-label={`Download ${f.filename}`}
                  onClick={() => void download(f)}
                >
                  <Paperclip aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
                  <span className="truncate">{f.filename}</span>
                </button>
                <span className="shrink-0 text-xs text-muted-foreground tabular-nums">
                  {f.media_type} · {fileSize(f.size_bytes)}
                </span>
              </li>
            ))}
          </ul>
          {failed ? (
            <p role="alert" className="text-xs text-destructive">
              {failed}
            </p>
          ) : null}
          {attachments.hasNextPage ? (
            <Button
              variant="outline"
              size="sm"
              loading={attachments.isFetchingNextPage}
              onClick={() => attachments.fetchNextPage()}
            >
              More attachments
            </Button>
          ) : null}
        </section>
      ) : null}

      <Writing
        teamId={teamId}
        sessionId={sessionId}
        session={session}
        me={me}
        behind={follow.behind}
        onJump={follow.jump}
      />
    </section>
  );
}

/** The signed-in user's id, from /whoami, which the shell reads already. */
function useMe(): string | null {
  const whoami = useQuery({ queryKey: ["whoami"], queryFn: ({ signal }) => fetchWhoami(signal) });
  const id = (whoami.data as { id?: unknown } | undefined)?.id;
  return typeof id === "string" ? id : null;
}

const pageEnd = () => document.documentElement.scrollHeight;

/** How close to the end of the page still counts as reading the end. */
const AT_END_PX = 96;

/**
 * Keeps the end of the conversation in view while the reader is at it: what
 * arrives scrolls into sight, at once, since it arrives many times a minute.
 * A reader who scrolled up to read is left where they are, and told there is
 * more below.
 */
function useFollowBottom(changed: unknown) {
  const atEnd = useRef(true);
  const [behind, setBehind] = useState(false);
  useEffect(() => {
    const read = () => {
      atEnd.current = window.innerHeight + window.scrollY >= pageEnd() - AT_END_PX;
      if (atEnd.current) setBehind(false);
    };
    window.addEventListener("scroll", read, { passive: true });
    return () => window.removeEventListener("scroll", read);
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs for each change to what is shown
  useLayoutEffect(() => {
    if (atEnd.current) window.scrollTo({ top: pageEnd() });
    else setBehind(true);
  }, [changed]);
  return {
    behind,
    jump: () => {
      window.scrollTo({ top: pageEnd() });
      setBehind(false);
    },
  };
}

/**
 * Where the reader writes: a reply in their own conversation on the
 * dashboard, or, anywhere else, why not and a way to start one of their own.
 * Pinned to the bottom of the view, above the phone's bar.
 */
function Writing({
  teamId,
  sessionId,
  session,
  me,
  behind,
  onJump,
}: {
  teamId: string;
  sessionId: string;
  session: Session | null;
  me: string | null;
  behind: boolean;
  onJump: () => void;
}) {
  const canWrite = usePermissions(teamId).can(RUN_WRITE);
  const channels = useQuery({
    queryKey: ["channels", teamId, "all"],
    queryFn: () => listChannels(teamId),
    enabled: Boolean(session?.channel_id),
  });
  const [starting, setStarting] = useState(false);
  const channel = channels.data?.items.find((ch) => ch.id === session?.channel_id);
  if (!session || !channel || me === null) return null;
  const onDashboard = channel.provider === DASHBOARD;
  const mine = onDashboard && session.user_id === me;

  return (
    <div className="sticky bottom-[calc(3.5rem+env(safe-area-inset-bottom))] z-10 -mx-4 space-y-2 border-t border-rule bg-sheet px-4 pt-3 pb-3 md:bottom-0 md:-mx-0 md:px-0">
      {behind ? (
        <div className="flex justify-center">
          <Button variant="outline" size="sm" onClick={onJump}>
            New activity
            <ArrowDown aria-hidden className="size-3.5" />
          </Button>
        </div>
      ) : null}
      {mine && canWrite ? (
        <Composer
          teamId={teamId}
          writes={{ kind: "reply", sessionId }}
          label="Reply"
          placeholder="Write a reply…"
        />
      ) : starting ? (
        <Composer
          teamId={teamId}
          writes={{ kind: "start" }}
          label="Start a conversation"
          placeholder="Ask the team something…"
          autoFocus
        />
      ) : (
        <div className="flex flex-col gap-2 text-sm text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
          <p>
            {onDashboard
              ? mine
                ? `Replying takes ${RUN_WRITE}.`
                : "Only the person this conversation is with writes in it."
              : `Replies to this conversation go through ${channel.name}.`}
          </p>
          {canWrite ? (
            <Button variant="outline" size="sm" onClick={() => setStarting(true)}>
              Start one on the dashboard
            </Button>
          ) : null}
        </div>
      )}
    </div>
  );
}

/** The dashboard's own channel: every team has one, made with the team. */
const DASHBOARD = "dashboard";

/* ------------------------------------------------------------------- runs */

const RUN_WRITE = "run:write";

// Owned by the page rather than the tab, so the tab can show the count.
type RunsQuery = UseInfiniteQueryResult<InfiniteData<Page<Run>, unknown>, Error>;

/**
 * Every run started in the session on the left, newest first, and the chosen
 * one on the right — the newest unless the URL names another. On a narrow
 * screen the two stack, list first.
 */
function Runs({
  teamId,
  sessionId,
  runs,
  selected,
  onChanged,
}: {
  teamId: string;
  sessionId: string;
  runs: RunsQuery;
  selected: string | null;
  onChanged: () => void;
}) {
  const canCancel = usePermissions(teamId).can(RUN_WRITE);
  const rows = runs.data?.pages.flatMap((p) => p.items) ?? [];
  const chosenId = selected ?? rows[0]?.id ?? null;
  const listed = rows.find((r) => r.id === chosenId);

  // A linked run that is not in the pages read so far is read on its own.
  const linked = useQuery({
    queryKey: ["runs", teamId, chosenId],
    queryFn: () => getRun(teamId, chosenId as string),
    enabled: runs.isSuccess && chosenId !== null && !listed,
  });
  const chosen: Run | undefined = listed ?? linked.data;
  const qc = useQueryClient();
  const changed = () => {
    onChanged();
    void qc.invalidateQueries({ queryKey: ["runs", teamId, chosenId] });
  };

  if (runs.isPending) {
    return (
      <div className="space-y-4" role="status" aria-label="Loading runs">
        <div className="space-y-1">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <Skeleton className="h-64 w-full" />
      </div>
    );
  }
  if (runs.isError) {
    return (
      <ErrorState
        what="the runs"
        error={runs.error}
        onRetry={() => runs.refetch()}
        retrying={runs.isRefetching}
      />
    );
  }
  if (rows.length === 0) {
    return (
      <EmptyState
        title="No agent has run in this session."
        body="A run starts when an agent is asked to answer something said here."
      />
    );
  }

  return (
    <div className="space-y-6">
      <nav aria-label="Runs" className="rounded-lg bg-card">
        <ul className="max-h-64 divide-y divide-rule overflow-y-auto rounded-lg">
          {rows.map((r) => (
            <li key={r.id}>
              <RunLink sessionId={sessionId} run={r} current={r.id === chosenId} />
            </li>
          ))}
        </ul>
        {runs.hasNextPage ? (
          <div className="border-t border-rule px-3 pb-3">
            <LoadMore
              hasMore
              loading={runs.isFetchingNextPage}
              onClick={() => runs.fetchNextPage()}
              loadedLabel={`${rows.length} run${rows.length === 1 ? "" : "s"} loaded`}
            />
          </div>
        ) : null}
      </nav>

      <div data-testid="run-pane">
        {chosen && chosen.session_id === sessionId ? (
          <RunDetail teamId={teamId} run={chosen} canCancel={canCancel} onChanged={changed} />
        ) : chosen ? (
          <p className="text-sm text-muted-foreground">That run belongs to another session.</p>
        ) : linked.isError ? (
          <ErrorState
            what="that run"
            error={linked.error}
            onRetry={() => linked.refetch()}
            retrying={linked.isRefetching}
          />
        ) : (
          <div className="space-y-3">
            <Skeleton className="h-6 w-48" />
            <Skeleton className="h-32 w-full" />
          </div>
        )}
      </div>
    </div>
  );
}

function RunLink({ sessionId, run, current }: { sessionId: string; run: Run; current: boolean }) {
  const total = run.input_tokens + run.output_tokens;
  return (
    <Link
      to="/ui/sessions/$sessionId"
      params={{ sessionId }}
      search={{ tab: "runs", run: run.id }}
      aria-current={current ? "page" : undefined}
      data-run={run.id}
      className={cn(
        "flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2.5 transition-colors focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
        current ? "bg-sheet shadow-[inset_0_0_0_1px_var(--color-primary)]" : "hover:bg-sheet/60",
      )}
    >
      <span className="min-w-0 flex-1">
        <span data-agent className="block truncate text-sm font-medium">
          {run.agent}
        </span>
        <HandedTo run={run} />
      </span>
      <RunStatus status={run.status} />
      <span className="flex basis-full gap-3 text-xs text-muted-foreground tabular-nums sm:basis-auto">
        <span className="sm:w-24 sm:text-right">{total.toLocaleString("en-US")} tokens</span>
        <Time iso={run.created_at} className="sm:w-16 sm:text-right" />
      </span>
    </Link>
  );
}
