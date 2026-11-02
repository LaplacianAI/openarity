import {
  type InfiniteData,
  type UseInfiniteQueryResult,
  useInfiniteQuery,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ChevronRight, Paperclip } from "lucide-react";
import { useState } from "react";

import {
  downloadAttachment,
  getRun,
  getSession,
  listAttachments,
  listMembers,
  listMessages,
  listSessionRuns,
} from "@/api";
import { ApiError, nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Attachment, Page, Run, Session } from "@/api/types";
import { RunDetail, RunStatus } from "@/components/run-detail";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, LoadMore, Mono, StatusDot } from "@/components/ui-bits";
import { absoluteTime, fileSize, relativeTime, sessionTone } from "@/lib/format";
import { useMediaQuery } from "@/lib/media";
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
    placeholderData: () =>
      qc
        .getQueryData<{ pages: Page<Session>[] }>(["sessions", teamId, "paged"])
        ?.pages.flatMap((p) => p.items)
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
  });

  const tab: Tab = search.tab ?? (search.run ? "runs" : "messages");
  const s = session.data ?? null;
  const title = s?.provider_ref ?? sessionId;
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
            <span className="inline-flex items-center gap-1.5">
              <StatusDot tone={sessionTone(s.status)} />
              <span className="text-foreground">{s.status}</span>
            </span>
            <span aria-hidden>·</span>
            <span>{s.kind}</span>
            <span aria-hidden>·</span>
            <span>
              started <time dateTime={s.started_at}>{relativeTime(s.started_at)}</time>
            </span>
            <span aria-hidden>·</span>
            <span>
              last message{" "}
              <time dateTime={s.last_message_at}>{relativeTime(s.last_message_at)}</time>
            </span>
          </p>
        ) : session.isError ? (
          <p role="alert" className="text-xs text-destructive">
            {session.error.message}
          </p>
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
            <Messages teamId={teamId} sessionId={sessionId} />
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
          <TabsList
            variant="line"
            className="h-10 w-full justify-start rounded-none border-b border-rule"
          >
            <TabsTrigger value="messages" className="flex-none px-3">
              Messages
            </TabsTrigger>
            <TabsTrigger value="runs" className="flex-none px-3">
              Runs
              {runs.isSuccess ? (
                <span className="rounded-full bg-card px-1.5 text-xs font-medium tabular-nums text-muted-foreground">
                  {loadedRuns}
                </span>
              ) : null}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="messages">
            <Messages teamId={teamId} sessionId={sessionId} />
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

/**
 * The conversation. The brain pages messages newest first, so every page it
 * hands over is older than the last: the whole list is reversed to read top
 * to bottom like a chat, and "Load more" sits above it because what it brings
 * back is earlier.
 */
function Messages({ teamId, sessionId }: { teamId: string; sessionId: string }) {
  const messages = useInfiniteQuery({
    queryKey: ["sessions", teamId, sessionId, "messages"],
    queryFn: ({ pageParam }) => listMessages(teamId, sessionId, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
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

  const [failed, setFailed] = useState<string | null>(null);
  const download = async (f: Attachment) => {
    setFailed(null);
    try {
      save(await downloadAttachment(teamId, sessionId, f.id), f.filename);
    } catch (err) {
      setFailed(`${f.filename}: ${err instanceof Error ? err.message : String(err)}`);
    }
  };

  const said = (messages.data?.pages.flatMap((p) => p.items) ?? []).slice().reverse();
  const files = attachments.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <section aria-label="Messages" className="space-y-4">
      {messages.isPending ? (
        <div className="space-y-3" role="status" aria-label="Loading messages">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-4/5" />
        </div>
      ) : messages.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {messages.error.message}
        </p>
      ) : said.length === 0 ? (
        <p className="text-sm text-muted-foreground">Nothing has been said here.</p>
      ) : (
        <>
          {messages.hasNextPage ? (
            <LoadMore
              hasMore
              loading={messages.isFetchingNextPage}
              onClick={() => messages.fetchNextPage()}
              loadedLabel={`${said.length} message${said.length === 1 ? "" : "s"} loaded`}
            />
          ) : null}
          <ol className="space-y-2">
            {said.map((m) => {
              const name = nameOf(m.user_id);
              return (
                <li key={m.id} data-message className="rounded-lg bg-card px-4 py-3">
                  <div className="flex items-baseline justify-between gap-3 text-xs text-muted-foreground">
                    {name ? (
                      <span className="truncate text-sm font-medium text-foreground">{name}</span>
                    ) : (
                      <Mono className="truncate">{m.user_id}</Mono>
                    )}
                    <time dateTime={m.received_at} className="shrink-0">
                      {absoluteTime(m.received_at)}
                    </time>
                  </div>
                  {/* A person typed this: it is shown as text, never parsed. */}
                  <p
                    data-text
                    className="mt-1 max-w-[75ch] whitespace-pre-wrap break-words text-sm"
                  >
                    {m.text}
                  </p>
                </li>
              );
            })}
          </ol>
        </>
      )}

      {messages.isSuccess ? (
        attachments.isError ? (
          <p role="alert" className="text-xs text-destructive">
            {attachments.error.message}
          </p>
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
                disabled={attachments.isFetchingNextPage}
                onClick={() => attachments.fetchNextPage()}
              >
                {attachments.isFetchingNextPage ? "Loading…" : "More attachments"}
              </Button>
            ) : null}
          </section>
        ) : null
      ) : null}
    </section>
  );
}

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
      <p role="alert" className="text-sm text-destructive">
        {runs.error.message}
      </p>
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
          <p role="alert" className="text-sm text-destructive">
            {linked.error.message}
          </p>
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
      <span data-agent className="min-w-0 flex-1 truncate text-sm font-medium">
        {run.agent}
      </span>
      <RunStatus status={run.status} />
      <span className="flex basis-full gap-3 text-xs text-muted-foreground tabular-nums sm:basis-auto">
        <span className="sm:w-24 sm:text-right">{total.toLocaleString("en-US")} tokens</span>
        <time dateTime={run.created_at} className="sm:w-16 sm:text-right">
          {relativeTime(run.created_at)}
        </time>
      </span>
    </Link>
  );
}
