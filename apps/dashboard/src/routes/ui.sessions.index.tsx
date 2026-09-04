import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { listSessions } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { EmptyState, LoadMore, Mono, PageHeader, StatusDot } from "@/components/ui-bits";
import { relativeTime } from "@/lib/format";

export const Route = createFileRoute("/ui/sessions/")({
  component: Sessions,
});

// The brain's status vocabulary, mapped to the three tones the dot knows.
// Anything unrecognised is drawn as closed rather than guessed at.
function toneOf(status: string): "active" | "idle" | "closed" {
  if (status === "active" || status === "open") return "active";
  if (status === "idle" || status === "waiting") return "idle";
  return "closed";
}

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
        <p className="text-sm text-muted-foreground">Loading sessions…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No sessions yet"
          body="A session begins the first time an approved sender writes in on a channel. An empty list means nothing has been received, or nobody has been approved to send."
        />
      ) : (
        <>
          <table className="w-full overflow-hidden rounded-md border border-border bg-card text-left">
            <caption className="sr-only">Sessions in this team</caption>
            <tbody className="divide-y divide-border">
              {rows.map((s) => (
                <tr key={s.id} className="hover:bg-secondary/60">
                  <td className="w-6 py-2 pl-3">
                    <StatusDot tone={toneOf(s.status)} />
                  </td>
                  <td className="min-w-0 py-2">
                    <div className="truncate text-sm font-medium">{s.provider_ref}</div>
                    <p className="text-xs text-muted-foreground">
                      {s.kind} · started {relativeTime(s.started_at)} · last message{" "}
                      {relativeTime(s.last_message_at)}
                    </p>
                  </td>
                  <td className="py-2 pr-3 text-right">
                    <Mono>{s.status}</Mono>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
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
