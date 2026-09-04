import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";

import { listChannels, listSessions } from "@/api";
import { useCurrentTeam } from "@/api/session";
import { Button } from "@/components/ui/button";
import { EmptyState, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/")({
  component: Overview,
});

function Overview() {
  const { teamId, teams, isPending } = useCurrentTeam();

  const channels = useQuery({
    queryKey: ["channels", teamId],
    queryFn: () => listChannels(teamId as string),
    enabled: Boolean(teamId),
  });
  const sessions = useQuery({
    queryKey: ["sessions", teamId],
    queryFn: () => listSessions(teamId as string),
    enabled: Boolean(teamId),
  });

  if (isPending) {
    return <p className="text-sm text-muted-foreground">Loading…</p>;
  }

  if (teams.length === 0) {
    return (
      <div className="space-y-5">
        <PageHeader title="Overview" />
        <EmptyState
          title="You are not in any team"
          body="Everything in Openarity is scoped to a team — channels, sessions and the senders waiting for approval all belong to one. Create the first team to begin."
          action={
            <Button asChild size="sm">
              <Link to="/ui/teams">Go to teams</Link>
            </Button>
          }
          hint="Creating a team requires super admin. On a fresh install the first person to sign in is granted it."
        />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Overview"
        description="Everything here is scoped to the current team and read from the brain."
      />
      <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <Stat label="Teams you belong to" value={teams.length} to="/ui/teams" />
        <Stat
          label="Channels"
          value={channels.data?.items.length}
          more={Boolean(channels.data?.next_cursor)}
          to="/ui/channels"
        />
        <Stat
          label="Sessions"
          value={sessions.data?.items.length}
          more={Boolean(sessions.data?.next_cursor)}
          to="/ui/sessions"
        />
      </dl>
      <p className="text-xs text-muted-foreground">
        Counts are of what has been loaded, not of what exists. The API returns cursor pages with no
        totals, so a “+” means there is another page rather than an unknown remainder.
      </p>
    </div>
  );
}

function Stat({
  label,
  value,
  more,
  to,
}: {
  label: string;
  value: number | undefined;
  more?: boolean;
  to: "/ui/teams" | "/ui/channels" | "/ui/sessions";
}) {
  return (
    <Link
      to={to}
      className="rounded-md border border-border bg-card px-4 py-3 transition-colors hover:border-border-strong"
    >
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className="mt-1 font-mono text-2xl tabular-nums">
        {value ?? "—"}
        {more ? <span className="text-base text-muted-foreground">+</span> : null}
      </dd>
    </Link>
  );
}
