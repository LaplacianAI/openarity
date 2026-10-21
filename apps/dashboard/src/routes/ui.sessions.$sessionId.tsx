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
  listAttachments,
  listMessages,
  listSessionRuns,
  listSessions,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Attachment, Page, Run, Session } from "@/api/types";
import { RunDetail, RunStatus } from "@/components/run-detail";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, LoadMore, Mono, StatusDot } from "@/components/ui-bits";
import { absoluteTime, fileSize, relativeTime, sessionTone } from "@/lib/format";
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

/** How many pages of sessions to read looking for one opened by a link. */
const SESSION_PAGES = 10;

/**
 * The brain has no route that reads one session, so a session is found in
 * the list: at once if the list was just read, otherwise by paging through.
 */
function useSession(teamId: string | null, sessionId: string) {
  const qc = useQueryClient();
  return useQuery({
    queryKey: ["sessions", teamId, sessionId, "meta"],
    queryFn: async (): Promise<Session | null> => {
      let cursor: string | null = null;
      for (let i = 0; i < SESSION_PAGES; i++) {
        const page: Page<Session> = await listSessions(teamId as string, cursor);
        const found = page.items.find((s) => s.id === sessionId);
        if (found) return found;
        cursor = nextCursorOf(page) ?? null;
        if (!cursor) break;
      }
      return null;
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

  return (
    <div className="space-y-5">
      <header className="space-y-2 border-b border-border pb-4">
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
        <h1 className="truncate text-lg font-semibold tracking-tight">{title}</h1>
        {s ? (
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
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
        ) : session.isPending && teamId ? (
          <Skeleton className="h-4 w-64" />
        ) : null}
      </header>

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its sessions." />
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
            className="h-9 border-b border-border w-full justify-start rounded-none"
          >
            <TabsTrigger value="messages" className="flex-none px-3">
              Messages
            </TabsTrigger>
            <TabsTrigger value="runs" className="flex-none px-3">
              Runs
              {runs.isSuccess ? (
                <span className="rounded-full bg-muted px-1.5 text-[11px] font-medium tabular-nums text-muted-foreground">
                  {loadedRuns}
                </span>
              ) : null}
            </TabsTrigger>
          </TabsList>
          <TabsContent value="messages">
            <Messages teamId={teamId} sessionId={sessionId} />
          </TabsContent>
          <TabsContent value="runs">
            <Runs
              teamId={teamId}
              sessionId={sessionId}
              runs={runs}
              selected={search.run ?? null}
              onChanged={() => runs.refetch()}
            />
          </TabsContent>
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
    <section aria-label="Messages" className="max-w-3xl space-y-4">
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
          <ol className="space-y-3">
            {said.map((m) => (
              <li
                key={m.id}
                data-message
                className="rounded-md border border-border bg-card px-4 py-3"
              >
                <div className="flex items-baseline justify-between gap-3 text-xs text-muted-foreground">
                  <Mono>{m.user_id}</Mono>
                  <time dateTime={m.received_at}>{absoluteTime(m.received_at)}</time>
                </div>
                {/* A person typed this: it is shown as text, never parsed. */}
                <p data-text className="mt-1.5 whitespace-pre-wrap break-words text-sm">
                  {m.text}
                </p>
              </li>
            ))}
          </ol>
        </>
      )}

      {messages.isSuccess ? (
        attachments.isError ? (
          <p role="alert" className="text-xs text-destructive">
            {attachments.error.message}
          </p>
        ) : files.length > 0 ? (
          <section aria-label="Attachments" className="space-y-2 border-t border-border pt-4">
            <h2 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Attachments
            </h2>
            <ul className="divide-y divide-border rounded-md border border-border bg-card text-sm">
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
      <div
        className="grid gap-4 lg:grid-cols-[18rem_minmax(0,1fr)]"
        role="status"
        aria-label="Loading runs"
      >
        <div className="space-y-2">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
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
    <div className="grid gap-4 lg:h-[calc(100dvh-15rem)] lg:min-h-[28rem] lg:grid-cols-[18rem_minmax(0,1fr)]">
      <nav
        aria-label="Runs"
        className="flex flex-col rounded-md border border-border bg-card lg:min-h-0 lg:overflow-y-auto"
      >
        <ul className="divide-y divide-border">
          {rows.map((r) => (
            <li key={r.id}>
              <RunLink sessionId={sessionId} run={r} current={r.id === chosenId} />
            </li>
          ))}
        </ul>
        <div className="mt-auto border-t border-border px-3 pb-3">
          <LoadMore
            hasMore={Boolean(runs.hasNextPage)}
            loading={runs.isFetchingNextPage}
            onClick={() => runs.fetchNextPage()}
            loadedLabel={`${rows.length} run${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      </nav>

      <div
        data-testid="run-pane"
        className="rounded-md border border-border bg-card p-5 lg:min-h-0 lg:overflow-y-auto"
      >
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
        "block space-y-1 border-l-2 px-3 py-2.5 transition-colors focus-visible:bg-secondary focus-visible:outline-none",
        current ? "border-l-primary bg-accent/60" : "border-l-transparent hover:bg-secondary/60",
      )}
    >
      <div className="flex items-baseline justify-between gap-2">
        <span data-agent className="truncate text-sm font-medium">
          {run.agent}
        </span>
        <time
          dateTime={run.created_at}
          className="shrink-0 text-xs text-muted-foreground tabular-nums"
        >
          {relativeTime(run.created_at)}
        </time>
      </div>
      <div className="flex items-center justify-between gap-2 text-muted-foreground">
        <RunStatus status={run.status} />
        <span className="text-xs tabular-nums">{total.toLocaleString("en-US")} tokens</span>
      </div>
    </Link>
  );
}
