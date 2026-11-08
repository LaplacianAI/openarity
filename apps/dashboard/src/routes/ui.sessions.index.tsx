import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";

import { listChannels, listSessions } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { ListedSession, RunStatus } from "@/api/types";
import { Time } from "@/components/facts";
import { Filter } from "@/components/filter";
import { ErrorState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { EmptyState, LoadMore, PageHeader } from "@/components/ui-bits";
import { count, RUN_STATUSES, STATUS_WORDS } from "@/lib/runs";
import { sessionName } from "@/lib/sessions";

type SessionsSearch = { channel?: string | undefined; run?: RunStatus | undefined };

function sessionsSearch(search: Record<string, unknown>): SessionsSearch {
  const { channel, run } = search;
  return {
    channel: typeof channel === "string" && channel !== "" ? channel : undefined,
    run: RUN_STATUSES.includes(run as RunStatus) ? (run as RunStatus) : undefined,
  };
}

export const Route = createFileRoute("/ui/sessions/")({
  validateSearch: sessionsSearch,
  component: Sessions,
});

/**
 * The total, and how many sessions' latest run failed: the sessions to open
 * first. A page of failures that has more after it is "N or more".
 */
export function sessionsFinding(
  total: number,
  failed: { n: number; more: boolean } | null,
): string {
  const all = `${count(total, "session")} in all`;
  if (!failed || failed.n === 0) return `${all}.`;
  const n = `${failed.n.toLocaleString("en-US")}${failed.more ? " or more" : ""}`;
  return `${all}, ${n} whose latest run failed.`;
}

function Sessions() {
  const { teamId } = useCurrentTeam();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const summary = useTeamSummary(teamId);
  const filtered = Boolean(search.channel || search.run);

  const channels = useQuery({
    queryKey: ["channels", teamId],
    queryFn: () => listChannels(teamId as string),
    enabled: Boolean(teamId),
  });

  const failing = useQuery({
    queryKey: ["sessions", teamId, "failing"],
    queryFn: () => listSessions(teamId as string, null, { runStatus: ["failed"] }),
    enabled: Boolean(teamId),
  });

  const sessions = useInfiniteQuery({
    queryKey: ["sessions", teamId, "paged", search],
    queryFn: ({ pageParam }) =>
      listSessions(teamId as string, pageParam, {
        channelId: search.channel ?? null,
        runStatus: search.run ? [search.run] : [],
      }),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });
  const rows = sessions.data?.pages.flatMap((p) => p.items) ?? [];

  const set = (next: Partial<SessionsSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...next }), replace: true });

  return (
    <div className="space-y-5">
      <PageHeader
        title="Sessions"
        finding={
          summary.data
            ? sessionsFinding(
                summary.data.sessions,
                failing.data
                  ? { n: failing.data.items.length, more: Boolean(failing.data.next_cursor) }
                  : null,
              )
            : null
        }
        description="A conversation between someone on a channel and the team. Sessions you are not part of and may not read are left out."
      />

      <fieldset className="flex flex-wrap gap-2">
        <legend className="sr-only">Filters</legend>
        <Filter
          label="Channel"
          value={search.channel ?? "any"}
          onChange={(v) => set({ channel: v === "any" ? undefined : v })}
          options={[
            ["any", "Every channel"],
            ...(channels.data?.items ?? []).map((c): [string, string] => [c.id, c.name]),
          ]}
        />
        <Filter
          label="Latest run"
          value={search.run ?? "any"}
          onChange={(v) => set({ run: v === "any" ? undefined : (v as RunStatus) })}
          options={[
            ["any", "Any latest run"],
            ...RUN_STATUSES.map((s): [string, string] => [
              s,
              `Latest run ${STATUS_WORDS[s].toLowerCase()}`,
            ]),
          ]}
        />
      </fieldset>

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its sessions." />
      ) : sessions.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading sessions">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : sessions.isError ? (
        <ErrorState
          what="the sessions"
          error={sessions.error}
          onRetry={() => sessions.refetch()}
          retrying={sessions.isRefetching}
        />
      ) : rows.length === 0 ? (
        filtered ? (
          <EmptyState
            title="No sessions match"
            body="No session you can read fits every filter chosen."
            action={
              <Button
                variant="outline"
                size="sm"
                onClick={() => set({ channel: undefined, run: undefined })}
              >
                Clear the filters
              </Button>
            }
          />
        ) : (
          <EmptyState
            title="No sessions yet"
            body="A session begins the first time an approved sender writes in on a channel, or someone asks the team from here."
          />
        )
      ) : (
        <div>
          <SessionTable sessions={rows} />
          <LoadMore
            hasMore={Boolean(sessions.hasNextPage)}
            loading={sessions.isFetchingNextPage}
            onClick={() => sessions.fetchNextPage()}
            loadedLabel={`${rows.length} session${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}
    </div>
  );
}

function SessionTable({ sessions }: { sessions: ListedSession[] }) {
  const navigate = useNavigate();
  return (
    <Table stack="md" aria-label="Sessions in this team">
      <TableHeader>
        <tr>
          <TableHead>Session</TableHead>
          <TableHead>Channel</TableHead>
          <TableHead>With</TableHead>
          <TableHead>Last message</TableHead>
          <TableHead>Latest run</TableHead>
          <TableHead>Last active</TableHead>
        </tr>
      </TableHeader>
      <TableBody>
        {sessions.map((s) => (
          <TableRow
            key={s.id}
            onOpen={() =>
              void navigate({ to: "/ui/sessions/$sessionId", params: { sessionId: s.id } })
            }
          >
            {/* The name and the last message share the width the others leave,
                three parts to two, each cut to a line: a long first question
                must not push the row past the frame. */}
            <TableCell className="sm:w-3/5 sm:max-w-0 stacked:w-auto stacked:max-w-none">
              <span className="flex min-w-0 items-baseline gap-2">
                <Link
                  to="/ui/sessions/$sessionId"
                  params={{ sessionId: s.id }}
                  tabIndex={-1}
                  title={sessionName(s)}
                  className="truncate font-medium hover:underline"
                >
                  {sessionName(s)}
                </Link>
                <span className="shrink-0 text-xs text-muted-foreground">{s.kind}</span>
              </span>
            </TableCell>
            {/* Bounded on the span: a cell's own max-width is not honoured in an
                auto-layout table. */}
            <TableCell label="Channel" className="text-muted-foreground">
              <span className="block max-w-[16ch] truncate stacked:max-w-none">
                {s.channel_name ?? "None"}
              </span>
            </TableCell>
            <TableCell label="With">
              <span className="block max-w-[16ch] truncate stacked:max-w-none">
                {s.participant ?? "—"}
              </span>
            </TableCell>
            <TableCell
              label="Last message"
              className="text-muted-foreground sm:w-2/5 sm:max-w-0 stacked:w-auto stacked:max-w-none"
            >
              {/* A stranger's words: text, cut to a line. */}
              <span className="block min-w-0 truncate">{s.last_message ?? "Nothing said yet"}</span>
            </TableCell>
            <TableCell label="Latest run" className="whitespace-nowrap">
              {s.latest_run_status ? (
                <StatusPill status={s.latest_run_status} />
              ) : (
                <span className="text-xs text-muted-foreground">No run</span>
              )}
            </TableCell>
            <TableCell label="Last active" className="whitespace-nowrap text-muted-foreground">
              <Time iso={s.last_message_at} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
