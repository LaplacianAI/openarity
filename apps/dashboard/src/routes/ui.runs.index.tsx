import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useMemo } from "react";

import { listTeamRuns } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { RunStatus } from "@/api/types";
import { Filter } from "@/components/filter";
import { RunTable } from "@/components/run-table";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, LoadMore, PageHeader } from "@/components/ui-bits";
import { DAY_MS, dayFinding, RUN_STATUSES, STATUS_WORDS } from "@/lib/runs";

/**
 * The filters, in the URL so Home can link to "failed in the last day". Every
 * key is always returned, so a value the page does not know is dropped
 * rather than passed through.
 */
type RunsSearch = {
  status?: RunStatus | undefined;
  who?: "hub" | "agents" | undefined;
  day?: true | undefined;
};

function runsSearch(search: Record<string, unknown>): RunsSearch {
  const { status, who, day } = search;
  return {
    status: RUN_STATUSES.includes(status as RunStatus) ? (status as RunStatus) : undefined,
    who: who === "hub" || who === "agents" ? who : undefined,
    day: day === true || day === "true" ? true : undefined,
  };
}

export const Route = createFileRoute("/ui/runs/")({
  validateSearch: runsSearch,
  component: Runs,
});

function Runs() {
  const { teamId } = useCurrentTeam();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const summary = useTeamSummary(teamId);

  // Fixed when the filter is chosen, so every page of one list shares it.
  const since = useMemo(() => (search.day ? new Date(Date.now() - DAY_MS) : null), [search.day]);
  const filtered = Boolean(search.status || search.who || search.day);

  const runs = useInfiniteQuery({
    queryKey: ["runs", teamId, "team", search],
    queryFn: ({ pageParam }) =>
      listTeamRuns(teamId as string, pageParam, {
        status: search.status ? [search.status] : [],
        since,
        hub: search.who === "hub" ? true : search.who === "agents" ? false : null,
      }),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });
  const rows = runs.data?.pages.flatMap((p) => p.items) ?? [];

  const set = (next: Partial<RunsSearch>) =>
    void navigate({ search: (prev) => ({ ...prev, ...next }), replace: true });

  return (
    <div className="space-y-5">
      <PageHeader
        title="Runs"
        finding={summary.data ? dayFinding(summary.data) : null}
        description="Every run the team has started, newest first: the hub's, and those started for a named agent. A run handed to another agent by delegation is part of the run that started it."
      />

      <fieldset className="flex flex-wrap gap-2">
        <legend className="sr-only">Filters</legend>
        <Filter
          label="Status"
          value={search.status ?? "any"}
          onChange={(v) => set({ status: v === "any" ? undefined : (v as RunStatus) })}
          options={[
            ["any", "Any status"],
            ...RUN_STATUSES.map((s): [string, string] => [s, STATUS_WORDS[s]]),
          ]}
        />
        <Filter
          label="Answered by"
          value={search.who ?? "any"}
          onChange={(v) => set({ who: v === "any" ? undefined : (v as "hub" | "agents") })}
          options={[
            ["any", "Anyone"],
            ["hub", "The hub"],
            ["agents", "A named agent"],
          ]}
        />
        <Filter
          label="When"
          value={search.day ? "day" : "any"}
          onChange={(v) => set({ day: v === "day" ? true : undefined })}
          options={[
            ["any", "Any time"],
            ["day", "Last 24 hours"],
          ]}
        />
      </fieldset>

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its runs." />
      ) : runs.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading runs">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : runs.isError ? (
        <ErrorState
          what="the runs"
          error={runs.error}
          onRetry={() => runs.refetch()}
          retrying={runs.isRefetching}
        />
      ) : rows.length === 0 ? (
        filtered ? (
          <EmptyState
            title="No runs match"
            body="Nothing the team ran fits every filter chosen."
            action={
              <Button
                variant="outline"
                size="sm"
                onClick={() => set({ status: undefined, who: undefined, day: undefined })}
              >
                Clear the filters
              </Button>
            }
          />
        ) : (
          <EmptyState
            title="No runs yet"
            body="A run starts when someone asks the team, or one of its agents, something."
          />
        )
      ) : (
        <div>
          <RunTable runs={rows} label="The team's runs" />
          <LoadMore
            hasMore={Boolean(runs.hasNextPage)}
            loading={runs.isFetchingNextPage}
            onClick={() => runs.fetchNextPage()}
            loadedLabel={`${rows.length} run${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}
    </div>
  );
}
