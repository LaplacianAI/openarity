import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { listAgents, listImprovements } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { Choice } from "@/components/choices";
import { ImprovementCard } from "@/components/improvement-card";
import { ImproverPasses } from "@/components/improver-passes";
import { ErrorState } from "@/components/states";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, LoadMore, PageHeader } from "@/components/ui-bits";
import {
  APPROVE,
  TAB_STATUSES,
  TAB_WORDS,
  type Tab,
  tabOf,
  tabsFor,
  waitingFinding,
} from "@/lib/improvements";

type ImprovementsSearch = {
  tab?: Tab | undefined;
  agent?: string | undefined;
  q?: string | undefined;
};

const TABS: Tab[] = ["waiting", "applied", "flagged", "closed", "passes"];

/** How often a list with a change still being tried is read again. */
const TRYING_POLL_MS = 5_000;

export const Route = createFileRoute("/ui/improvements")({
  // The tab, the agent and the words searched for, in the URL so a view of
  // what was rejected for one agent can be linked and survives a reload.
  validateSearch: (search: Record<string, unknown>): ImprovementsSearch => ({
    tab: TABS.includes(search.tab as Tab) ? (search.tab as Tab) : undefined,
    agent: typeof search.agent === "string" && search.agent !== "" ? search.agent : undefined,
    q: typeof search.q === "string" && search.q.trim() !== "" ? search.q : undefined,
  }),
  component: Improvements,
});

const ALL = "all";

/** The agents of the team, for the filter; the first hundred, by name. */
function useAgentOptions(teamId: string | null) {
  const agents = useQuery({
    queryKey: ["agents", teamId, "names", "improvements"],
    queryFn: () => listAgents(teamId as string, null, { limit: 100 }),
    enabled: Boolean(teamId),
    staleTime: 60_000,
  });
  return [
    { value: ALL, label: "All agents" },
    ...(agents.data?.items ?? []).map((a) => ({ value: a.id, label: a.name })),
  ];
}

function Improvements() {
  const { teamId } = useCurrentTeam();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const perms = usePermissions(teamId);
  const canApprove = perms.can(APPROVE);
  const tab = tabOf(search.tab, canApprove);
  const agent = search.agent;
  const q = search.q;
  const agents = useAgentOptions(teamId);
  const passes = tab === "passes";

  // The box answers at once; the address and the brain follow once typing pauses.
  const [typed, setTyped] = useState(q ?? "");
  // And the box follows the address when it changes from outside, as Back
  // does, so a stale box never writes its old words back.
  // The box's own words reaching the address (trimmed) change nothing in it,
  // so a trailing space or a keystroke still on its way is kept.
  const [lastQ, setLastQ] = useState(q);
  if (q !== lastQ) {
    setLastQ(q);
    if ((q ?? "") !== typed.trim()) setTyped(q ?? "");
  }
  useEffect(() => {
    if (typed.trim() === (q ?? "")) return;
    const t = setTimeout(
      () =>
        navigate({ search: (prev) => ({ ...prev, q: typed.trim() || undefined }), replace: true }),
      300,
    );
    return () => clearTimeout(t);
  }, [typed, q, navigate]);

  const list = useInfiniteQuery({
    queryKey: ["improvements", teamId, tab, agent ?? null, q ?? null],
    queryFn: ({ pageParam, signal }) =>
      listImprovements(
        teamId as string,
        {
          status: tab === "passes" ? [] : TAB_STATUSES[tab],
          ...(agent ? { agent } : {}),
          ...(q ? { q } : {}),
        },
        pageParam,
        signal,
      ),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId) && !perms.isPending && !passes,
    // A change being tried on past runs is read again until its tries are done.
    refetchInterval: (query) =>
      query.state.data?.pages.some((p) => p.items.some((i) => i.replay?.status === "trying"))
        ? TRYING_POLL_MS
        : false,
  });
  const rows = list.data?.pages.flatMap((p) => p.items) ?? [];

  // What waits, whichever tab is open: the finding does not change with the filter.
  const waiting = useQuery({
    queryKey: ["improvements", teamId, "waiting-finding"],
    queryFn: ({ signal }) =>
      listImprovements(teamId as string, { status: ["waiting"], limit: 100 }, null, signal),
    enabled: Boolean(teamId) && canApprove,
  });

  // The flagged count, on its tab whichever is open.
  const flaggedCount = useQuery({
    queryKey: ["improvements", teamId, "flagged-count"],
    queryFn: ({ signal }) =>
      listImprovements(teamId as string, { status: ["flagged"], limit: 100 }, null, signal),
    enabled: Boolean(teamId) && !perms.isPending,
  });
  const flaggedN = flaggedCount.data?.items.length ?? 0;
  const flaggedMore = Boolean(flaggedCount.data?.next_cursor);

  const filtered = agent !== undefined || q !== undefined;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Improvements"
        finding={
          canApprove && waiting.isSuccess
            ? waitingFinding(waiting.data.items, Boolean(waiting.data.next_cursor))
            : null
        }
        description="Changes the team's agents propose to their own instructions, skills and tools from their runs. Nothing changes until someone who may approve accepts it. An approved change is watched over its agent's next runs, and can be reverted while its agent is as it left it."
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its improvements." />
      ) : perms.isPending ? (
        <Loading />
      ) : (
        <Tabs
          value={tab}
          onValueChange={(v) =>
            navigate({
              search: (prev) => ({
                ...prev,
                tab: v === tabsFor(canApprove)[0] ? undefined : (v as Tab),
              }),
              replace: true,
            })
          }
          className="gap-4"
        >
          <div className="flex flex-wrap items-center justify-between gap-3">
            <TabsList className="min-w-0 max-w-full overflow-x-auto">
              {tabsFor(canApprove).map((t) => (
                <TabsTrigger key={t} value={t}>
                  {TAB_WORDS[t]}
                  {t === "flagged" && flaggedN > 0 ? (
                    <span className="ml-1 tabular-nums text-warning">
                      {flaggedN}
                      {flaggedMore ? "+" : ""}
                      <span className="sr-only"> flagged</span>
                    </span>
                  ) : null}
                </TabsTrigger>
              ))}
            </TabsList>
            <div className="flex w-full flex-wrap items-center gap-2 sm:w-auto">
              <div className="w-full sm:w-48">
                <Choice
                  id="improvements-agent"
                  value={agent ?? ALL}
                  options={agents}
                  onChange={(v) =>
                    navigate({
                      search: (prev) => ({ ...prev, agent: v === ALL ? undefined : v }),
                      replace: true,
                    })
                  }
                />
              </div>
              {passes ? null : (
                <Input
                  type="search"
                  aria-label="Search the reasons"
                  placeholder="Search…"
                  value={typed}
                  onChange={(e) => setTyped(e.target.value)}
                  className="w-full sm:w-48"
                />
              )}
            </div>
          </div>
          {passes ? (
            <ImproverPasses teamId={teamId} agent={agent} />
          ) : list.isPending ? (
            <Loading />
          ) : list.isError ? (
            <ErrorState
              what="the improvements"
              error={list.error}
              onRetry={() => list.refetch()}
              retrying={list.isRefetching}
            />
          ) : rows.length === 0 ? (
            <EmptyState
              title={
                filtered
                  ? "Nothing matches"
                  : tab === "waiting"
                    ? "Nothing is waiting"
                    : tab === "flagged"
                      ? "Nothing is flagged"
                      : "Nothing here"
              }
              body={
                filtered
                  ? "No change in this tab fits the agent and words chosen."
                  : tab === "waiting"
                    ? "When an agent proposes a change to itself from its runs, it appears here for someone who may approve it."
                    : tab === "applied"
                      ? "No proposed change has been applied."
                      : tab === "flagged"
                        ? "An applied change is watched over its agent's next ten judged runs. One whose runs went clearly worse than usual, or cost more with no fewer failures, appears here for someone to clear or revert, as does a change meant to save whose good runs did not get cheaper or quicker."
                        : "No proposed change has been rejected, reverted or withdrawn."
              }
            />
          ) : (
            <div>
              <ul aria-label="Improvements" className="space-y-2">
                {rows.map((i) => (
                  <li key={i.id}>
                    <ImprovementCard teamId={teamId} item={i} />
                  </li>
                ))}
              </ul>
              <LoadMore
                hasMore={Boolean(list.hasNextPage)}
                loading={list.isFetchingNextPage}
                onClick={() => list.fetchNextPage()}
                loadedLabel={`${rows.length} change${rows.length === 1 ? "" : "s"} loaded`}
              />
            </div>
          )}
        </Tabs>
      )}
    </div>
  );
}

/** Cards being read: the shape of one, so the page does not jump when they come. */
function Loading() {
  return (
    <div className="space-y-2" role="status" aria-label="Loading improvements">
      {[0, 1].map((i) => (
        <div key={i} className="max-w-[75ch] space-y-3 rounded-lg bg-card px-4 py-3">
          <Skeleton className="h-4 w-48" />
          <Skeleton className="h-4 w-full" />
          <Skeleton className="h-20 w-full" />
        </div>
      ))}
    </div>
  );
}
