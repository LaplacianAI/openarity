import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect } from "react";

import { getRun, listToolApprovals } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { ApprovalStatus } from "@/api/types";
import { RequestCard } from "@/components/request-card";
import { ErrorState, ForbiddenState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { EmptyState, LoadMore, PageHeader } from "@/components/ui-bits";
import { fromApproval } from "@/lib/approvals";
import { count } from "@/lib/runs";

const STATUSES: { value: ApprovalStatus; label: string }[] = [
  { value: "pending", label: "Waiting" },
  { value: "approved", label: "Approved" },
  { value: "denied", label: "Declined" },
  { value: "expired", label: "Expired" },
  { value: "cancelled", label: "Cancelled" },
];

type RequestsSearch = { status?: ApprovalStatus | undefined; request?: string | undefined };

export const Route = createFileRoute("/ui/requests")({
  // Which tab, in the URL so a link can open on what was declined; and one
  // request, as a notification opens it.
  validateSearch: (search: Record<string, unknown>): RequestsSearch => ({
    status: STATUSES.some((s) => s.value === search.status)
      ? (search.status as ApprovalStatus)
      : undefined,
    request:
      typeof search.request === "string" && search.request !== "" ? search.request : undefined,
  }),
  component: Requests,
});

const READ = "approval:read";

/** Where the run that asked is read: its conversation, at that run. */
function RunLink({ teamId, runId }: { teamId: string; runId: string }) {
  const run = useQuery({
    queryKey: ["run", teamId, runId],
    queryFn: () => getRun(teamId, runId),
    staleTime: 60_000,
  });
  if (!run.data) return null;
  return (
    <Link
      to="/ui/sessions/$sessionId"
      params={{ sessionId: run.data.session_id }}
      search={{ tab: "runs", run: runId }}
      className="inline-flex items-center gap-1 rounded text-primary underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
    >
      Open its conversation
      <ArrowRight aria-hidden className="size-3" />
    </Link>
  );
}

function Requests() {
  const { teamId } = useCurrentTeam();
  const search = Route.useSearch();
  const status: ApprovalStatus = search.status ?? "pending";
  const opened = search.request;
  const navigate = Route.useNavigate();
  const perms = usePermissions(teamId);
  const canRead = perms.can(READ);

  const list = useInfiniteQuery({
    queryKey: ["approvals", teamId, status],
    queryFn: ({ pageParam, signal }) =>
      listToolApprovals(teamId as string, { status: [status] }, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId) && canRead,
  });
  const rows = list.data?.pages.flatMap((p) => p.items) ?? [];
  const found = opened !== undefined && rows.some((a) => a.id === opened);

  // The request a link names is brought to the eye and to the keyboard, once
  // it has arrived; one already answered, or past the first page, leaves the
  // list as it is.
  useEffect(() => {
    if (!found) return;
    const card = document.getElementById(`request-${opened}`);
    card?.scrollIntoView?.({ block: "center" });
    card?.focus({ preventScroll: true });
  }, [found, opened]);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Tool approvals"
        finding={
          canRead && list.isSuccess && status === "pending"
            ? rows.length === 0
              ? "Nothing is waiting for an answer."
              : `${count(rows.length, "request")} waiting for an answer${list.hasNextPage ? " or more" : ""}.`
            : null
        }
        description="Calls the team's agents asked a person about before making them. The agent does not make the call; it says what it wants to do and why, then its run waits. Approving lets the brain make exactly that call, and a request nobody answers expires."
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its requests." />
      ) : perms.isPending ? null : !canRead ? (
        <ForbiddenState what="the requests" permission={READ} />
      ) : (
        <Tabs
          value={status}
          onValueChange={(v) =>
            navigate({
              search: { status: v === "pending" ? undefined : (v as ApprovalStatus) },
              replace: true,
            })
          }
          className="gap-4"
        >
          <TabsList>
            {STATUSES.map((s) => (
              <TabsTrigger key={s.value} value={s.value}>
                {s.label}
              </TabsTrigger>
            ))}
          </TabsList>
          {list.isPending ? (
            <div className="space-y-2" role="status" aria-label="Loading requests">
              <Skeleton className="h-24 w-full" />
              <Skeleton className="h-24 w-full" />
            </div>
          ) : list.isError ? (
            <ErrorState
              what="the requests"
              error={list.error}
              onRetry={() => list.refetch()}
              retrying={list.isRefetching}
            />
          ) : rows.length === 0 ? (
            <EmptyState
              title={status === "pending" ? "Nothing is waiting" : "Nothing here"}
              body={
                status === "pending"
                  ? "When an agent needs a person's answer before a call, the request appears here, and in the conversation it came from."
                  : "No request has ended this way."
              }
            />
          ) : (
            <div>
              {opened !== undefined && !found && !list.hasNextPage ? (
                <p className="mb-2 text-sm text-muted-foreground" role="status">
                  The request you opened is not {status === "pending" ? "waiting" : "here"}: it may
                  have been answered, or expired.
                </p>
              ) : null}
              <ul aria-label="Tool approvals" className="space-y-2">
                {rows.map((a) => (
                  <li
                    key={a.id}
                    id={`request-${a.id}`}
                    tabIndex={a.id === opened ? -1 : undefined}
                    aria-current={a.id === opened ? "true" : undefined}
                    className="rounded-lg outline-none aria-[current=true]:ring-2 aria-[current=true]:ring-primary"
                  >
                    <RequestCard
                      teamId={teamId}
                      request={fromApproval(a)}
                      link={<RunLink teamId={teamId} runId={a.run_id} />}
                    />
                  </li>
                ))}
              </ul>
              <LoadMore
                hasMore={Boolean(list.hasNextPage)}
                loading={list.isFetchingNextPage}
                onClick={() => list.fetchNextPage()}
                loadedLabel={`${rows.length} request${rows.length === 1 ? "" : "s"} loaded`}
              />
            </div>
          )}
        </Tabs>
      )}
    </div>
  );
}
