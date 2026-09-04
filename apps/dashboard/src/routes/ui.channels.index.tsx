import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";

import { listChannels } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/channels/")({
  component: Channels,
});

function Channels() {
  const { teamId } = useCurrentTeam();

  const channels = useInfiniteQuery({
    queryKey: ["channels", teamId, "paged"],
    queryFn: ({ pageParam }) => listChannels(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const rows = channels.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Channels"
        description="Where messages arrive from. Each belongs to one team and one provider."
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its channels." />
      ) : channels.isPending ? (
        <p className="text-sm text-muted-foreground">Loading channels…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No channels yet"
          body="A channel is a provider the brain listens to — an inbox, a Slack workspace, a webhook. Creating one needs a signing secret, so it is done with the CLI rather than here."
          hint="oa channels create --provider slack --name '#support'"
        />
      ) : (
        <>
          <table className="w-full overflow-hidden rounded-md border border-border bg-card text-left">
            <caption className="sr-only">Channels in this team</caption>
            <tbody className="divide-y divide-border">
              {rows.map((c) => (
                <tr key={c.id} className="hover:bg-secondary/60">
                  <td className="py-2 pl-3 text-sm font-medium">{c.name}</td>
                  <td className="py-2 text-xs text-muted-foreground">{c.provider}</td>
                  <td className="py-2 pr-3 text-right">
                    <Mono>{c.id}</Mono>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <LoadMore
            hasMore={Boolean(channels.hasNextPage)}
            loading={channels.isFetchingNextPage}
            onClick={() => channels.fetchNextPage()}
            loadedLabel={`${rows.length} channel${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </div>
  );
}
