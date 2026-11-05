import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";

import { listAgents } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { ListedAgent } from "@/api/types";
import { Time } from "@/components/facts";
import { ReadOnlyNote, Tag } from "@/components/registry-bits";
import { ErrorState } from "@/components/states";
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
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { delegationWords, grantWords } from "@/lib/agent-draft";
import { count } from "@/lib/runs";

export const Route = createFileRoute("/ui/agents/")({
  component: Agents,
});

const WRITE = "agent:write";

function Agents() {
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const summary = useTeamSummary(teamId);

  const agents = useInfiniteQuery({
    queryKey: ["agents", teamId, "paged"],
    queryFn: ({ pageParam }) => listAgents(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });
  const rows = agents.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Agents"
        finding={summary.data ? `${count(summary.data.agents, "agent")} in the team.` : null}
        description="A model, a way of working through a task, instructions, and the MCP servers and skills it is granted. The hub hands work to them."
        actions={
          canWrite ? (
            <Button asChild size="sm">
              <Link to="/ui/agents/new">Define agent</Link>
            </Button>
          ) : null
        }
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="agents" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its agents." />
      ) : agents.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading agents">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : agents.isError ? (
        <ErrorState
          what="the agents"
          error={agents.error}
          onRetry={() => agents.refetch()}
          retrying={agents.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No agents yet"
          body="The hub answers on its own until there are agents to hand work to. Define one with a model, then grant it the servers and skills it may use."
          action={
            canWrite ? (
              <Button asChild variant="outline" size="sm">
                <Link to="/ui/agents/new">Define agent</Link>
              </Button>
            ) : null
          }
        />
      ) : (
        <div>
          <AgentTable agents={rows} />
          <LoadMore
            hasMore={Boolean(agents.hasNextPage)}
            loading={agents.isFetchingNextPage}
            onClick={() => agents.fetchNextPage()}
            loadedLabel={`${rows.length} agent${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}
    </div>
  );
}

function AgentTable({ agents }: { agents: ListedAgent[] }) {
  const navigate = useNavigate();
  return (
    <Table aria-label="Agents in this team">
      <TableHeader>
        <tr>
          <TableHead>Agent</TableHead>
          {/* Read on the agent's page; between a phone and a wide screen there is no room. */}
          <TableHead className="sm:max-lg:hidden">Model</TableHead>
          <TableHead>Grants</TableHead>
          <TableHead>Web</TableHead>
          <TableHead>Hands work to</TableHead>
          <TableHead className="sm:max-lg:hidden">Updated</TableHead>
        </tr>
      </TableHeader>
      <TableBody>
        {agents.map((a) => (
          <TableRow
            key={a.id}
            onOpen={() => void navigate({ to: "/ui/agents/$agentId", params: { agentId: a.id } })}
          >
            <TableCell className="sm:w-full sm:max-w-0">
              {/* One block, so the name stays over its description when the row stacks. */}
              <div className="min-w-0 flex-1">
                <span className="flex min-w-0 items-baseline gap-2">
                  <Link
                    to="/ui/agents/$agentId"
                    params={{ agentId: a.id }}
                    tabIndex={-1}
                    className="truncate font-medium hover:underline"
                  >
                    {a.name}
                  </Link>
                  {a.kind !== "custom" ? <Tag>{a.kind}</Tag> : null}
                </span>
                <span className="block min-w-0 truncate text-xs text-muted-foreground">
                  {a.description || "No description: other agents cannot find it by what it does."}
                </span>
              </div>
            </TableCell>
            <TableCell label="Model" className="whitespace-nowrap sm:max-lg:hidden">
              <Mono>{a.model.name}</Mono>
            </TableCell>
            <TableCell label="Grants" className="whitespace-nowrap">
              {grantWords(a)}
            </TableCell>
            <TableCell label="Web" className="whitespace-nowrap">
              {a.web ? "On" : "Off"}
            </TableCell>
            <TableCell label="Hands work to" className="whitespace-nowrap">
              {delegationWords(a)}
            </TableCell>
            <TableCell
              label="Updated"
              className="whitespace-nowrap text-muted-foreground sm:max-lg:hidden"
            >
              <Time iso={a.updated_at} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
