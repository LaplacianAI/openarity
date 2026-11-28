import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";

import { listSchedules, listWatchers, markScheduledNewsSeen } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { READ_SCHEDULES, useScheduledNews } from "@/api/scheduled";
import { useCurrentTeam } from "@/api/session";
import type { Schedule, ScheduledNewsItem, Watcher } from "@/api/types";
import { Time } from "@/components/facts";
import { ErrorState, ForbiddenState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, LoadMore, PageHeader } from "@/components/ui-bits";
import { checkSays, fireSays, newsSays, statusWord, watchesSays } from "@/lib/schedules";
import { cn } from "@/lib/utils";
import { timeBoth, whenSays } from "@/lib/when";

export type View = "news" | "schedules" | "watchers" | "all";
type ListView = Exclude<View, "news">;
const LIST_VIEWS: ListView[] = ["schedules", "watchers", "all"];

export const Route = createFileRoute("/ui/schedules/")({
  // Which list, in the URL, so a reload keeps it and it can be linked. The
  // news has an address of its own, which Needs you opens.
  validateSearch: (search: Record<string, unknown>): { view?: ListView | undefined } => ({
    view: LIST_VIEWS.includes(search.view as ListView) ? (search.view as ListView) : undefined,
  }),
  component: () => <Schedules view={Route.useSearch().view ?? "schedules"} />,
});

export const WRITE_SCHEDULES = "schedule:write";
export const MANAGE_SCHEDULES = "schedule:manage";

export function Schedules({ view: asked }: { view: View }) {
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canRead = perms.can(READ_SCHEDULES);
  const canManage = !perms.isPending && perms.can(MANAGE_SCHEDULES);
  const news = useScheduledNews(teamId);
  const navigate = useNavigate();
  const view: View = asked === "all" && !perms.isPending && !canManage ? "schedules" : asked;

  const finding =
    news.data && news.data.count > 0
      ? `${news.data.count}${news.data.more ? "+" : ""} thing${news.data.count === 1 ? "" : "s"} your schedules did that you have not seen.`
      : news.isSuccess
        ? "Nothing new from your schedules."
        : null;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Schedules"
        finding={canRead ? finding : null}
        description="A schedule runs an agent at a time, every so often or on a cron rule, as the person who made it. A watcher runs one when a page or a feed changes, or when its address is called. Each answers in a conversation of its own, a chat, a channel or its maker's direct messages."
        actions={
          teamId && canRead ? (
            <NewButton view={view} canWrite={perms.isPending ? null : perms.can(WRITE_SCHEDULES)} />
          ) : null
        }
      />
      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to schedule its agents." />
      ) : perms.isPending ? null : !canRead ? (
        <ForbiddenState what="the team's schedules" permission={READ_SCHEDULES} />
      ) : (
        <Tabs
          value={view}
          onValueChange={(v) =>
            v === "news"
              ? navigate({ to: "/ui/schedules/news", replace: true })
              : navigate({
                  to: "/ui/schedules",
                  search: { view: v === "schedules" ? undefined : (v as ListView) },
                  replace: true,
                })
          }
          className="gap-4"
        >
          <TabsList>
            <TabsTrigger value="news">
              News
              {news.data && news.data.count > 0 ? (
                <span className="ml-1.5 rounded-full bg-surface px-1.5 text-xs tabular-nums">
                  {news.data.count}
                  {news.data.more ? "+" : ""}
                </span>
              ) : null}
            </TabsTrigger>
            <TabsTrigger value="schedules">Schedules</TabsTrigger>
            <TabsTrigger value="watchers">Watchers</TabsTrigger>
            {canManage ? <TabsTrigger value="all">Everyone's</TabsTrigger> : null}
          </TabsList>
          {view === "news" ? (
            <News teamId={teamId} />
          ) : view === "watchers" ? (
            <WatcherList teamId={teamId} all={false} />
          ) : view === "all" ? (
            <div className="space-y-6">
              <ScheduleList teamId={teamId} all />
              <WatcherList teamId={teamId} all />
            </div>
          ) : (
            <ScheduleList teamId={teamId} all={false} />
          )}
        </Tabs>
      )}
    </div>
  );
}

/** The one filled button, following the view; shown with what it takes when the reader cannot. */
function NewButton({ view, canWrite }: { view: View; canWrite: boolean | null }) {
  const watcher = view === "watchers";
  if (canWrite === null) return null;
  if (!canWrite) {
    return (
      <Button size="sm" variant="outline" disabled title={`Making one takes ${WRITE_SCHEDULES}`}>
        {watcher ? "New watcher" : "New schedule"} · needs {WRITE_SCHEDULES}
      </Button>
    );
  }
  return (
    <Button size="sm" asChild>
      <Link to={watcher ? "/ui/schedules/watchers/new" : "/ui/schedules/new"}>
        {watcher ? "New watcher" : "New schedule"}
      </Link>
    </Button>
  );
}

function Loading({ what }: { what: string }) {
  return (
    <div className="space-y-1" role="status" aria-label={`Loading ${what}`}>
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-12 w-full" />
    </div>
  );
}

const ROW =
  "grid gap-x-4 gap-y-0.5 px-3 py-2.5 text-sm outline-none hover:bg-surface/60 focus-visible:bg-accent-wash focus-visible:shadow-[inset_3px_0_0_0_var(--primary)] md:grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_7.5rem] md:items-center";

function News({ teamId }: { teamId: string }) {
  const news = useScheduledNews(teamId);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const seen = useMutation({
    mutationFn: (item: ScheduledNewsItem) =>
      markScheduledNewsSeen(
        teamId,
        item.watcher_id
          ? { watcher_id: item.watcher_id }
          : { schedule_id: item.schedule_id as string },
      ),
    onSettled: () => void qc.invalidateQueries({ queryKey: ["scheduled-news", teamId] }),
  });
  if (news.isPending) return <Loading what="the news" />;
  if (news.isError)
    return (
      <ErrorState
        what="the news"
        error={news.error}
        onRetry={() => news.refetch()}
        retrying={news.isRefetching}
      />
    );
  const items = news.data.items;
  if (items.length === 0)
    return (
      <EmptyState
        title="Nothing new"
        body="A schedule or watcher of yours tells you here when a run fails, is skipped or is missed, when a page cannot be read, and when it answers in its own conversation."
      />
    );
  return (
    <div className="space-y-2">
      <ul aria-label="News" className="divide-y divide-rule">
        {items.map((item) => {
          const o = newsSays(item);
          const key = `${item.schedule_id ?? item.watcher_id}-${item.fired_at}`;
          return (
            <li
              key={key}
              className="grid grid-cols-[auto_1fr_auto] items-center gap-x-3 gap-y-0.5 px-3 py-2.5 text-sm md:grid-cols-[7rem_minmax(0,1fr)_auto_auto]"
            >
              <StatusPill status={o.look} />
              <span className="col-span-3 row-start-2 min-w-0 md:col-span-1 md:col-start-2 md:row-start-1">
                <span className="font-medium">{item.name}</span>
                <span className="text-muted-foreground"> · {o.says}</span>
              </span>
              <Time
                iso={item.fired_at}
                className="text-xs text-muted-foreground max-md:justify-self-start"
              />
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  seen.mutate(item);
                  void navigate({
                    to: "/ui/sessions/$sessionId",
                    params: { sessionId: item.session_id },
                  });
                }}
              >
                Open
                <ArrowRight aria-hidden className="size-3" />
              </Button>
            </li>
          );
        })}
      </ul>
      {news.data.more ? (
        <p className="px-3 text-xs text-muted-foreground">
          The newest {items.length} are shown. Opening one marks it seen, and older ones come up.
        </p>
      ) : null}
    </div>
  );
}

function ScheduleList({ teamId, all }: { teamId: string; all: boolean }) {
  const list = useInfiniteQuery({
    queryKey: ["schedules", teamId, "list", all],
    queryFn: ({ pageParam, signal }) =>
      listSchedules(
        teamId,
        { all, status: ["proposed", "active", "paused", "done"] },
        pageParam,
        signal,
      ),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = list.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section aria-label={all ? "Everyone's schedules" : "Your schedules"} className="space-y-2">
      {all ? <h2 className="text-base font-medium">Schedules</h2> : null}
      {list.isPending ? (
        <Loading what="schedules" />
      ) : list.isError ? (
        <ErrorState
          what="the schedules"
          error={list.error}
          onRetry={() => list.refetch()}
          retrying={list.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title={all ? "Nobody has a schedule" : "No schedules yet"}
          body="A schedule runs an agent at a time you set, and answers where you choose: its own conversation, a chat, a channel, or your direct messages."
        />
      ) : (
        <>
          <ul className="divide-y divide-rule">
            {rows.map((s) => (
              <li key={s.id}>
                <ScheduleRow s={s} />
              </li>
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(list.hasNextPage)}
            loading={list.isFetchingNextPage}
            onClick={() => list.fetchNextPage()}
            loadedLabel={`${rows.length} schedule${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </section>
  );
}

function ScheduleRow({ s }: { s: Schedule }) {
  const next = s.next_fire_at ? timeBoth(s.next_fire_at, s.timezone) : null;
  const last = s.last_outcome ? fireSays(s.last_outcome) : null;
  return (
    <Link to="/ui/schedules/$scheduleId" params={{ scheduleId: s.id }} className={ROW}>
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate font-medium">{s.name}</span>
        {s.waiting_change ? <StatusPill status="needs your yes" pill className="shrink-0" /> : null}
      </span>
      <span className="truncate text-muted-foreground">{whenSays(s.when, s.timezone)}</span>
      <span
        className={cn(
          "truncate text-muted-foreground",
          !next && s.status !== "proposed" && "max-md:hidden",
        )}
        title={next?.there ?? undefined}
      >
        {next ? `next ${next.here}` : s.status === "proposed" ? "once confirmed" : "—"}
      </span>
      <span className="truncate text-muted-foreground">
        {s.target.kind === "own" ? "its own conversation" : (s.target.label ?? s.target.kind)}
        {last ? ` · last ${last.says.toLowerCase()}` : ""}
      </span>
      <StatusPill status={statusWord(s.status)} className="md:justify-self-end" />
    </Link>
  );
}

function WatcherList({ teamId, all }: { teamId: string; all: boolean }) {
  const list = useInfiniteQuery({
    queryKey: ["watchers", teamId, "list", all],
    queryFn: ({ pageParam, signal }) =>
      listWatchers(
        teamId,
        { all, status: ["proposed", "waiting_secret", "active", "paused"] },
        pageParam,
        signal,
      ),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = list.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section aria-label={all ? "Everyone's watchers" : "Your watchers"} className="space-y-2">
      {all ? <h2 className="text-base font-medium">Watchers</h2> : null}
      {list.isPending ? (
        <Loading what="watchers" />
      ) : list.isError ? (
        <ErrorState
          what="the watchers"
          error={list.error}
          onRetry={() => list.refetch()}
          retrying={list.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title={all ? "Nobody has a watcher" : "No watchers yet"}
          body="A watcher runs an agent when a web page or a feed changes, or when a service such as GitHub calls its address."
        />
      ) : (
        <>
          <ul className="divide-y divide-rule">
            {rows.map((w) => (
              <li key={w.id}>
                <WatcherRow w={w} />
              </li>
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(list.hasNextPage)}
            loading={list.isFetchingNextPage}
            onClick={() => list.fetchNextPage()}
            loadedLabel={`${rows.length} watcher${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </section>
  );
}

function WatcherRow({ w }: { w: Watcher }) {
  const last = w.last_outcome ? checkSays(w.last_outcome) : null;
  return (
    <Link to="/ui/schedules/watchers/$watcherId" params={{ watcherId: w.id }} className={ROW}>
      <span className="flex min-w-0 items-center gap-2">
        <span className="truncate font-medium">{w.name}</span>
        {w.waiting_change ? <StatusPill status="needs your yes" pill className="shrink-0" /> : null}
      </span>
      <span className="truncate text-muted-foreground md:col-span-2">{watchesSays(w)}</span>
      <span className="truncate text-muted-foreground">
        {w.last_check_at ? (
          <>
            {last ? `${last.says} · ` : ""}
            <Time iso={w.last_check_at} />
          </>
        ) : w.source === "webhook" ? (
          "not called yet"
        ) : (
          "not looked yet"
        )}
      </span>
      <StatusPill status={statusWord(w.status)} className="md:justify-self-end" />
    </Link>
  );
}
