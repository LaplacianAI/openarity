import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { type ReactNode, useMemo } from "react";

import { listMCPServers, listTeamPendingSenders, listTeamRuns } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { TeamSummary } from "@/api/types";
import { RunTable } from "@/components/run-table";
import { ErrorState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar, EmptyState, Mono, PageHeader } from "@/components/ui-bits";
import { waitedFor } from "@/lib/format";
import { count, DAY_MS, RUN_STATUSES } from "@/lib/runs";

export const Route = createFileRoute("/ui/")({
  component: Home,
});

/** How many rows a block on Home shows before "View all". */
const FEW = 5;

/**
 * What needs the reader, in one line: failed runs and people waiting, or
 * that nothing does. A count the reader may not see is not mentioned.
 */
function homeFinding(s: TeamSummary): string {
  const needs: string[] = [];
  if (s.runs_24h.failed > 0) {
    needs.push(`${count(s.runs_24h.failed, "run")} failed in the last 24 hours`);
  }
  if (s.waiting_senders) {
    needs.push(`${count(s.waiting_senders, "person", "people")} waiting to be let in`);
  }
  if (s.servers_failing > 0) {
    needs.push(`${count(s.servers_failing, "server")} failing discovery`);
  }
  if (needs.length === 0) return "Nothing needs you right now.";
  const [last, ...rest] = needs.reverse();
  const said = rest.length ? `${rest.reverse().join(", ")} and ${last}` : (last as string);
  return `${said.charAt(0).toUpperCase()}${said.slice(1)}.`;
}

function Home() {
  const { teamId, teams, isPending } = useCurrentTeam();
  const summary = useTeamSummary(teamId);

  if (isPending) {
    return (
      <div className="space-y-5" role="status" aria-label="Loading">
        <Skeleton className="h-7 w-32" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }

  if (teams.length === 0 || !teamId) {
    return (
      <div className="space-y-5">
        <PageHeader title="Home" />
        <EmptyState
          title="You are not in any team"
          body="Everything in openarity belongs to a team: its agents, its channels, its sessions and the people waiting to be let in. Create the first team to begin."
          action={
            <Button asChild size="sm">
              <Link to="/ui/teams">Go to teams</Link>
            </Button>
          }
          hint="Creating a team needs a super admin. On a fresh install the first person to sign in is one."
        />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Home"
        finding={summary.data ? homeFinding(summary.data) : null}
        description="Whether the team is working, and what needs you: runs that failed, people waiting to be let in, and servers whose tools could not be read."
      />

      {summary.isError ? (
        <ErrorState
          what="the team's summary"
          error={summary.error}
          onRetry={() => summary.refetch()}
          retrying={summary.isRefetching}
        />
      ) : (
        <DayCounts summary={summary.data} />
      )}

      <div className="grid gap-8 min-[80rem]:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
        <div className="min-w-0 space-y-8">
          {summary.data && summary.data.runs_24h.failed > 0 ? <Failures teamId={teamId} /> : null}
          <HubRuns teamId={teamId} />
        </div>
        <div className="min-w-0 space-y-8">
          <Waiting teamId={teamId} waiting={summary.data?.waiting_senders ?? null} />
          {summary.data && summary.data.servers_failing > 0 ? (
            <FailingServers teamId={teamId} />
          ) : null}
        </div>
      </div>
    </div>
  );
}

/** The last day's runs by status, each a way into the runs it counts. */
function DayCounts({ summary }: { summary: TeamSummary | undefined }) {
  return (
    <section aria-labelledby="day">
      <h2 id="day" className="sr-only">
        Runs in the last 24 hours
      </h2>
      {/* Each count links to its runs, and a dl may not wrap its pairs in links. */}
      <ul className="grid grid-cols-2 gap-px overflow-hidden rounded-lg bg-rule sm:grid-cols-5">
        {RUN_STATUSES.map((status) => (
          <li key={status} className="bg-card last:col-span-2 sm:last:col-span-1">
            <Link
              to="/ui/runs"
              search={{ status, day: true }}
              className="flex flex-col gap-1 px-4 py-3 transition-colors hover:bg-foreground/3 focus-visible:outline-2 focus-visible:outline-offset-[-2px]"
            >
              <StatusPill status={status} />
              <span className="text-xl font-semibold tabular-nums" data-count={status}>
                {summary ? summary.runs_24h[status].toLocaleString("en-US") : "—"}
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Block({
  id,
  title,
  more,
  children,
}: {
  id: string;
  title: string;
  more?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section aria-labelledby={id} className="space-y-3">
      <div className="flex items-baseline justify-between gap-3">
        <h2 id={id} className="text-base font-medium">
          {title}
        </h2>
        {more}
      </div>
      {children}
    </section>
  );
}

const viewAll =
  "inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline";

function Rows() {
  return (
    <div className="space-y-1" role="status" aria-label="Loading">
      <Skeleton className="h-10 w-full" />
      <Skeleton className="h-10 w-full" />
    </div>
  );
}

/** The last day's failures: the first thing to open when something is wrong. */
function Failures({ teamId }: { teamId: string }) {
  const since = useMemo(() => new Date(Date.now() - DAY_MS), []);
  const failed = useQuery({
    queryKey: ["runs", teamId, "home", "failed"],
    queryFn: () => listTeamRuns(teamId, null, { status: ["failed"], since, limit: FEW }),
  });
  return (
    <Block
      id="failed"
      title="Failed in the last 24 hours"
      more={
        failed.data?.next_cursor ? (
          <Link to="/ui/runs" search={{ status: "failed", day: true }} className={viewAll}>
            View all
            <ArrowRight aria-hidden className="size-3" />
          </Link>
        ) : null
      }
    >
      {failed.isPending ? (
        <Rows />
      ) : failed.isError ? (
        <ErrorState what="the failed runs" error={failed.error} onRetry={() => failed.refetch()} />
      ) : (
        <RunTable runs={failed.data.items} label="Runs that failed in the last 24 hours" />
      )}
    </Block>
  );
}

/** What the team was last asked, as the hub answered it. */
function HubRuns({ teamId }: { teamId: string }) {
  const runs = useQuery({
    queryKey: ["runs", teamId, "home", "hub"],
    queryFn: () => listTeamRuns(teamId, null, { hub: true, limit: FEW }),
  });
  return (
    <Block
      id="hub"
      title="Asked of the team"
      more={
        runs.data?.next_cursor ? (
          <Link to="/ui/runs" search={{ who: "hub" }} className={viewAll}>
            View all
            <ArrowRight aria-hidden className="size-3" />
          </Link>
        ) : null
      }
    >
      {runs.isPending ? (
        <Rows />
      ) : runs.isError ? (
        <ErrorState what="the hub's runs" error={runs.error} onRetry={() => runs.refetch()} />
      ) : runs.data.items.length === 0 ? (
        <EmptyState
          title="Nobody has asked the team yet"
          body="A question for the team goes to its hub, which answers or hands the work to the agent that fits."
        />
      ) : (
        <RunTable runs={runs.data.items} label="The hub's latest runs" />
      )}
    </Block>
  );
}

/** Who has waited longest, for a reader who may see the queue. */
function Waiting({ teamId, waiting }: { teamId: string; waiting: number | null }) {
  const canRead = usePermissions(teamId).can("channel:read");
  const oldest = useQuery({
    queryKey: ["pending", teamId, "home"],
    queryFn: () => listTeamPendingSenders(teamId, null, 3),
    enabled: canRead && Boolean(waiting),
  });
  if (!canRead) return null;

  return (
    <Block
      id="waiting"
      title="Waiting to be let in"
      more={
        waiting ? (
          <Link to="/ui/approvals" className={viewAll}>
            Review
            <ArrowRight aria-hidden className="size-3" />
          </Link>
        ) : null
      }
    >
      {!waiting ? (
        <p className="text-sm text-muted-foreground">No one is waiting.</p>
      ) : oldest.isPending ? (
        <Rows />
      ) : oldest.isError ? (
        <ErrorState
          what="who is waiting"
          permission="channel:read"
          error={oldest.error}
          onRetry={() => oldest.refetch()}
        />
      ) : (
        <ul className="divide-y divide-rule rounded-lg bg-card">
          {oldest.data.items.map((s) => (
            <li
              key={`${s.channel_id}/${s.sender_ref}`}
              className="flex items-center gap-3 px-3 py-2.5 text-sm"
            >
              <Avatar name={s.sender_name || s.sender_ref} />
              <span className="min-w-0 flex-1">
                <span className="block truncate font-medium">{s.sender_name || s.sender_ref}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  on {s.channel_name}
                </span>
              </span>
              <span className="text-xs whitespace-nowrap text-muted-foreground">
                {waitedFor(s.first_seen)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Block>
  );
}

/** Servers whose tools could not be read the last time anyone asked. */
function FailingServers({ teamId }: { teamId: string }) {
  const servers = useQuery({
    queryKey: ["mcp-servers", teamId, "home"],
    queryFn: () => listMCPServers(teamId),
  });
  const failing = (servers.data?.items ?? []).filter((s) => s.last_discovery_error);
  return (
    <Block
      id="servers"
      title="Servers that need attention"
      more={
        <Link to="/ui/servers" className={viewAll}>
          Open servers
          <ArrowRight aria-hidden className="size-3" />
        </Link>
      }
    >
      {servers.isPending ? (
        <Rows />
      ) : servers.isError ? (
        <ErrorState what="the servers" error={servers.error} onRetry={() => servers.refetch()} />
      ) : failing.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          A server's last discovery failed. Open the servers to see which.
        </p>
      ) : (
        <ul className="divide-y divide-rule rounded-lg bg-card">
          {failing.slice(0, FEW).map((s) => (
            <li key={s.id} className="space-y-0.5 px-3 py-2.5">
              <span className="block truncate text-sm font-medium">{s.name}</span>
              {/* What the server or the network said: shown as text. */}
              <Mono className="block truncate text-failed">{s.last_discovery_error}</Mono>
            </li>
          ))}
        </ul>
      )}
    </Block>
  );
}
