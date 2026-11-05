import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { approveSender, listMembers, listTeamPendingSenders } from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { useTeamSummary } from "@/api/summary";
import type { Member, TeamPendingSender } from "@/api/types";
import { Time } from "@/components/facts";
import { ErrorState, ForbiddenState } from "@/components/states";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Avatar, EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { waitedFor } from "@/lib/format";
import { count } from "@/lib/runs";

export const Route = createFileRoute("/ui/approvals")({
  component: Approvals,
});

// Seeing the queue and acting on it are separate grants. The shipped roles
// hold both or neither; an operator's own role may hold the first alone.
const READ = "channel:read";
const APPROVE = "sender:approve";

/** "3 people are waiting; the longest, for 2 days." */
function waitingFinding(total: number, oldest: TeamPendingSender | undefined): string {
  if (total === 0 || !oldest) return "No one is waiting.";
  const who =
    total === 1 ? "1 person is waiting" : `${count(total, "person", "people")} are waiting`;
  return total === 1
    ? `${who}, for ${waitedFor(oldest.first_seen)}.`
    : `${who}; the longest, for ${waitedFor(oldest.first_seen)}.`;
}

function Approvals() {
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canRead = perms.can(READ);
  const summary = useTeamSummary(teamId);
  const qc = useQueryClient();

  // Every channel's queue in one, the longest waiting first: whoever has
  // waited longest is the one to see to.
  const queue = useInfiniteQuery({
    queryKey: ["pending", teamId, "team"],
    queryFn: ({ pageParam }) => listTeamPendingSenders(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId) && canRead,
  });
  const rows = queue.data?.pages.flatMap((p) => p.items) ?? [];
  const total = summary.data?.waiting_senders ?? rows.length;

  // Approving binds a sender to a member of this team: the brain accepts no
  // one else. The team's members are readable by every member, where the
  // directory needs user:read, which an approver does not hold.
  const canApprove = perms.can(APPROVE);
  const members = useQuery({
    queryKey: ["members", teamId],
    queryFn: () => listMembers(teamId as string),
    enabled: Boolean(teamId) && canApprove,
  });

  const approve = useMutation({
    mutationFn: ({ row, userId }: { row: TeamPendingSender; userId: string }) =>
      approveSender(teamId as string, row.channel_id, row.sender_ref, userId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["pending", teamId] });
      void qc.invalidateQueries({ queryKey: ["summary", teamId] });
      toast.success("Sender let in");
    },
    onError: (err: Error) => toast.error(err.message),
  });

  return (
    <div className="space-y-5">
      <PageHeader
        title="Approvals"
        finding={canRead && queue.isSuccess ? waitingFinding(total, rows[0]) : null}
        description="People who wrote in on one of the team's channels and have not been let in. Nothing they send reaches an agent until they are bound to a member. There is no reject: someone never let in never reaches an agent."
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team before reviewing senders." />
      ) : perms.isPending ? null : !canRead ? (
        <ForbiddenState what="who is waiting" permission={READ} />
      ) : queue.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading who is waiting">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      ) : queue.isError ? (
        <ErrorState
          what="who is waiting"
          error={queue.error}
          onRetry={() => queue.refetch()}
          retrying={queue.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title="No one is waiting"
          body="Someone who writes in on one of the team's channels before being let in appears here, the longest waiting first."
        />
      ) : (
        <div>
          <Table aria-label="Senders waiting to be let in, the longest waiting first">
            <TableHeader>
              <tr>
                <TableHead>Sender</TableHead>
                <TableHead>Channel</TableHead>
                <TableHead>Waiting</TableHead>
                <TableHead numeric>Messages</TableHead>
                <TableHead>Last seen</TableHead>
                {canApprove ? (
                  <TableHead>
                    <span className="sr-only">Let in</span>
                  </TableHead>
                ) : null}
              </tr>
            </TableHeader>
            <TableBody>
              {rows.map((row) => (
                <TableRow key={`${row.channel_id}/${row.sender_ref}`} data-sender={row.sender_ref}>
                  <TableCell>
                    <span className="flex min-w-0 items-center gap-2.5">
                      <Avatar name={row.sender_name || row.sender_ref} />
                      <span className="min-w-0">
                        {/* A name the stranger typed: text, never markup. */}
                        <span className="block truncate font-medium">
                          {row.sender_name || row.sender_ref}
                        </span>
                        {row.sender_name ? (
                          <Mono className="block truncate">{row.sender_ref}</Mono>
                        ) : null}
                      </span>
                    </span>
                  </TableCell>
                  <TableCell label="Channel" className="text-muted-foreground">
                    {row.channel_name} · {row.channel_provider}
                  </TableCell>
                  <TableCell label="Waiting" className="whitespace-nowrap">
                    {waitedFor(row.first_seen)}
                  </TableCell>
                  <TableCell label="Messages" numeric>
                    {row.seen_count.toLocaleString("en-US")}
                  </TableCell>
                  <TableCell label="Last seen" className="whitespace-nowrap text-muted-foreground">
                    <Time iso={row.last_seen} />
                  </TableCell>
                  {canApprove ? (
                    <TableCell>
                      <BindTo
                        disabled={approve.isPending || members.isPending}
                        members={members.data?.items ?? []}
                        onApprove={(userId) => approve.mutate({ row, userId })}
                      />
                    </TableCell>
                  ) : null}
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <LoadMore
            hasMore={Boolean(queue.hasNextPage)}
            loading={queue.isFetchingNextPage}
            onClick={() => queue.fetchNextPage()}
            loadedLabel={`${rows.length} sender${rows.length === 1 ? "" : "s"} loaded`}
          />
        </div>
      )}
    </div>
  );
}

function BindTo({
  members,
  disabled,
  onApprove,
}: {
  members: Member[];
  disabled: boolean;
  onApprove: (userId: string) => void;
}) {
  const [userId, setUserId] = useState<string>("");

  return (
    <div className="flex items-center justify-end gap-1.5 max-sm:justify-start">
      <Select value={userId} onValueChange={setUserId}>
        <SelectTrigger className="h-8 w-48 text-xs" aria-label="Bind this sender to">
          <SelectValue placeholder="Bind to a member…" />
        </SelectTrigger>
        <SelectContent>
          {members.map((m) => (
            <SelectItem key={m.user_id} value={m.user_id}>
              {m.email ?? m.subject}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        variant="outline"
        size="sm"
        disabled={disabled || !userId}
        onClick={() => userId && onApprove(userId)}
      >
        Approve
      </Button>
    </div>
  );
}
