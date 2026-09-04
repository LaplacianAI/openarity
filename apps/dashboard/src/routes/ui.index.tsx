import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import {
  getSetupState,
  listChannels,
  listPendingSenders,
  listSessions,
  listTeams,
} from "@/mocks/api";

export const Route = createFileRoute("/ui/")({
  component: Overview,
});

function Overview() {
  const setup = useQuery({ queryKey: ["setup"], queryFn: getSetupState });
  const teams = useQuery({ queryKey: ["teams", null], queryFn: () => listTeams() });
  const channels = useQuery({ queryKey: ["channels", "all"], queryFn: () => listChannels(null) });
  const pending = useQuery({
    queryKey: ["pending", null, null],
    queryFn: () => listPendingSenders({}),
  });
  const sessions = useQuery({ queryKey: ["sessions", null], queryFn: () => listSessions(null) });

  const empty = (teams.data?.items.length ?? 0) === 0;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Overview"
        description="Openarity control plane. Everything here reads from the local API server."
        actions={
          setup.data?.completed ? null : (
            <Button asChild size="sm">
              <Link to="/ui/setup">Continue setup</Link>
            </Button>
          )
        }
      />

      {empty ? (
        <EmptyState
          title="This install is empty"
          body="No teams exist yet, so there is nothing for an agent to answer. Run the first-run setup — it takes four short steps and you can stop at any point."
          action={
            <Button asChild size="sm">
              <Link to="/ui/setup">Start first-run setup</Link>
            </Button>
          }
          hint="Just exploring? Use “Load demo data” in the top bar to populate a realistic install."
        />
      ) : (
        <dl className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Stat label="Teams" value={teams.data?.items.length} to="/ui/teams" />
          <Stat label="Channels" value={channels.data?.items.length} to="/ui/channels" />
          <Stat
            label="Awaiting approval"
            value={pending.data?.items.length}
            more={Boolean(pending.data?.next_cursor)}
            to="/ui/approvals"
          />
          <Stat label="Sessions" value={sessions.data?.items.length} to="/ui/sessions" />
        </dl>
      )}

      <p className="text-xs text-muted-foreground">
        Counts reflect what has been loaded. The API returns cursor pages without totals, so lists
        grow with “Load more” rather than page numbers.
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
  to: string;
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
