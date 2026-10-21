import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";

import { listSessions } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, LoadMore, Mono, PageHeader, StatusDot } from "@/components/ui-bits";
import { relativeTime, sessionTone } from "@/lib/format";

export const Route = createFileRoute("/ui/sessions/")({
  component: Sessions,
});

function Sessions() {
  const { teamId } = useCurrentTeam();

  const sessions = useInfiniteQuery({
    queryKey: ["sessions", teamId, "paged"],
    queryFn: ({ pageParam }) => listSessions(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const rows = sessions.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Sessions"
        description="A conversation between someone on a channel and an agent."
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its sessions." />
      ) : sessions.isPending ? (
        <div className="space-y-2" role="status" aria-label="Loading sessions">
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-14 w-full" />
        </div>
      ) : sessions.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {sessions.error.message}
        </p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No sessions yet"
          body="A session begins the first time an approved sender writes in on a channel. An empty list means nothing has been received, or nobody has been approved to send."
        />
      ) : (
        <>
          <ul
            aria-label="Sessions in this team"
            className="divide-y divide-border overflow-hidden rounded-md border border-border bg-card"
          >
            {rows.map((s) => (
              <li key={s.id}>
                <Link
                  to="/ui/sessions/$sessionId"
                  params={{ sessionId: s.id }}
                  className="group flex items-center gap-3 px-3 py-2.5 transition-colors hover:bg-secondary/60 focus-visible:bg-secondary focus-visible:outline-none"
                >
                  <StatusDot tone={sessionTone(s.status)} />
                  <span className="min-w-0 flex-1">
                    <span className="block truncate text-sm font-medium group-hover:underline group-focus-visible:underline">
                      {s.provider_ref}
                    </span>
                    <span className="block text-xs text-muted-foreground">
                      {s.kind} · started {relativeTime(s.started_at)} · last message{" "}
                      {relativeTime(s.last_message_at)}
                    </span>
                  </span>
                  <Mono>{s.status}</Mono>
                  <ChevronRight aria-hidden className="size-4 text-muted-foreground" />
                </Link>
              </li>
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(sessions.hasNextPage)}
            loading={sessions.isFetchingNextPage}
            onClick={() => sessions.fetchNextPage()}
            loadedLabel={`${rows.length} session${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </div>
  );
}
