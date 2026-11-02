import { type InfiniteData, useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { listDeletedTeams, restoreTeam } from "@/api";
import { nextCursorOf } from "@/api/client";
import type { Page, Team } from "@/api/types";
import { day } from "@/components/delete-team-dialog";
import { Button } from "@/components/ui/button";
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/teams/deleted")({
  component: DeletedTeams,
});

const KEY = ["teams", "deleted"];

function DeletedTeams() {
  const qc = useQueryClient();
  const [problem, setProblem] = useState<{ id: string; text: string } | null>(null);
  const [restoring, setRestoring] = useState<string | null>(null);

  const deleted = useInfiniteQuery({
    queryKey: KEY,
    queryFn: ({ pageParam }) => listDeletedTeams(pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });

  // The brain ignores ?deleted for anyone but a super admin and answers with
  // their live teams, so only a row with a deletion date is a deleted team.
  const rows = (deleted.data?.pages.flatMap((p) => p.items) ?? []).filter((t) => t.deleted_at);

  const restore = async (team: Team) => {
    setRestoring(team.id);
    setProblem(null);
    try {
      await restoreTeam(team.id);
    } catch (err) {
      setProblem({ id: team.id, text: err instanceof Error ? err.message : String(err) });
      setRestoring(null);
      return;
    }
    qc.setQueryData<InfiniteData<Page<Team>>>(KEY, (data) =>
      data
        ? {
            ...data,
            pages: data.pages.map((p) => ({
              ...p,
              items: p.items.filter((t) => t.id !== team.id),
            })),
          }
        : data,
    );
    await qc.invalidateQueries({ queryKey: ["teams"], exact: true });
    setRestoring(null);
    toast.success(`Restored ${team.name}`);
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="Deleted teams"
        description="A deleted team is gone for its members but kept whole until it is erased for good. Restore one to bring back everything it held."
      />

      {deleted.isPending ? (
        <p className="text-sm text-muted-foreground">Loading deleted teams…</p>
      ) : deleted.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {deleted.error.message}
        </p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No deleted teams"
          body="A team deleted from its settings waits here until it is erased for good."
          hint="Only a super admin sees deleted teams."
        />
      ) : (
        <div className="overflow-x-auto rounded-md border border-border bg-card">
          <table className="w-full text-sm">
            <thead className="border-b border-border text-left text-xs text-muted-foreground">
              <tr>
                <th className="px-3 py-2 font-medium sm:px-4">Name</th>
                <th className="hidden px-4 py-2 font-medium sm:table-cell">Deleted on</th>
                <th className="px-3 py-2 font-medium sm:px-4">Erased on</th>
                <th className="px-3 py-2 sm:px-4">
                  <span className="sr-only">Restore</span>
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {rows.map((team) => (
                <tr key={team.id}>
                  <td className="px-3 py-2.5 break-words sm:px-4">
                    <div className="font-medium">{team.name}</div>
                    <Mono className="hidden sm:inline">{team.id}</Mono>
                    {problem?.id === team.id ? (
                      <p role="alert" className="mt-1 text-xs text-destructive">
                        {problem.text}
                      </p>
                    ) : null}
                  </td>
                  <td className="hidden px-4 py-2.5 whitespace-nowrap text-muted-foreground sm:table-cell">
                    {team.deleted_at ? day(team.deleted_at) : "—"}
                  </td>
                  <td className="px-3 py-2.5 whitespace-nowrap sm:px-4">
                    {team.purge_at ? day(team.purge_at) : "—"}
                  </td>
                  <td className="px-3 py-2.5 text-right sm:px-4">
                    <Button
                      variant="outline"
                      size="sm"
                      aria-label={`Restore ${team.name}`}
                      disabled={restoring !== null}
                      onClick={() => void restore(team)}
                    >
                      {restoring === team.id ? "Restoring…" : "Restore"}
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {rows.length > 0 ? (
        <LoadMore
          hasMore={Boolean(deleted.hasNextPage)}
          loading={deleted.isFetchingNextPage}
          onClick={() => deleted.fetchNextPage()}
          loadedLabel={`${rows.length} deleted`}
        />
      ) : null}
    </div>
  );
}
