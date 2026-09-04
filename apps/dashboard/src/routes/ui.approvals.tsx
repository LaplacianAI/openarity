import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { approveSender, listChannels, listPendingSenders, listUsers } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import type { PendingSender } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Avatar, EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { relativeTime } from "@/lib/format";

export const Route = createFileRoute("/ui/approvals")({
  component: Approvals,
});

function Approvals() {
  const { teamId } = useCurrentTeam();
  const [channelId, setChannelId] = useState<string | null>(null);

  const channels = useQuery({
    queryKey: ["channels", teamId],
    queryFn: () => listChannels(teamId as string),
    enabled: Boolean(teamId),
  });

  // Pending senders are counted per channel, not per install: the brain has no
  // endpoint that spans them. So the channel is a choice the operator makes
  // rather than a filter over something already fetched.
  const options = channels.data?.items ?? [];
  const selected = channelId ?? options[0]?.id ?? null;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Approval queue"
        description="Senders nobody has vouched for yet. Nothing they send reaches an agent until they are bound to a user."
        actions={
          options.length > 0 ? (
            <Select value={selected ?? undefined} onValueChange={setChannelId}>
              <SelectTrigger className="h-8 w-56 text-xs" aria-label="Channel">
                <SelectValue placeholder="Choose a channel" />
              </SelectTrigger>
              <SelectContent>
                {options.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name} · {c.provider}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          ) : null
        }
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team before reviewing senders." />
      ) : channels.isPending ? (
        <p className="text-sm text-muted-foreground">Loading channels…</p>
      ) : options.length === 0 ? (
        <EmptyState
          title="This team has no channels"
          body="Senders arrive through a channel — an inbox, a Slack workspace, a webhook. Until one exists there is nothing that could be waiting."
        />
      ) : selected ? (
        <Queue teamId={teamId} channelId={selected} />
      ) : null}
    </div>
  );
}

function Queue({ teamId, channelId }: { teamId: string; channelId: string }) {
  const qc = useQueryClient();

  const queue = useInfiniteQuery({
    queryKey: ["pending", teamId, channelId],
    queryFn: ({ pageParam }) => listPendingSenders(teamId, channelId, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });

  // Approving binds a sender to a user, so the directory has to be loaded
  // before any row can be acted on — the brain will not accept an approval
  // that does not say whose messages these are.
  const users = useQuery({ queryKey: ["users"], queryFn: () => listUsers() });

  const rows: PendingSender[] = queue.data?.pages.flatMap((p) => p.items) ?? [];

  const approve = useMutation({
    mutationFn: ({ ref, userId }: { ref: string; userId: string }) =>
      approveSender(teamId, channelId, ref, userId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["pending", teamId, channelId] });
      toast.success("Sender approved");
    },
    onError: (err: Error) => toast.error(err.message),
  });

  if (queue.isPending) return <p className="text-sm text-muted-foreground">Loading senders…</p>;
  if (queue.isError) {
    return (
      <div role="alert" className="text-sm text-destructive">
        {queue.error.message}
      </div>
    );
  }

  if (rows.length === 0) {
    return (
      <EmptyState
        title="Nothing waiting for a decision"
        body="Every sender seen on this channel has already been bound to a user. New ones appear here the first time they write in."
      />
    );
  }

  return (
    <>
      <table className="w-full overflow-hidden rounded-md border border-border bg-card text-left">
        <caption className="sr-only">Senders waiting to be bound to a user</caption>
        <tbody className="divide-y divide-border">
          {rows.map((row) => (
            <tr key={row.sender_ref} className="transition-colors hover:bg-secondary/60">
              <td className="w-9 py-2 pl-3">
                <Avatar name={row.sender_name || row.sender_ref} />
              </td>
              <td className="min-w-0 py-2">
                <div className="flex items-baseline gap-2">
                  <span className="truncate text-sm font-medium">
                    {row.sender_name || row.sender_ref}
                  </span>
                  {row.sender_name ? <Mono>{row.sender_ref}</Mono> : null}
                </div>
                <p className="text-xs text-muted-foreground">
                  {row.seen_count} message{row.seen_count === 1 ? "" : "s"} · first seen{" "}
                  {relativeTime(row.first_seen)} · last {relativeTime(row.last_seen)}
                </p>
              </td>
              <td className="py-2 pr-3 pl-3">
                <BindTo
                  disabled={approve.isPending || users.isPending}
                  users={users.data?.items ?? []}
                  onApprove={(userId) => approve.mutate({ ref: row.sender_ref, userId })}
                />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <LoadMore
        hasMore={Boolean(queue.hasNextPage)}
        loading={queue.isFetchingNextPage}
        onClick={() => queue.fetchNextPage()}
        loadedLabel={`${rows.length} sender${rows.length === 1 ? "" : "s"} loaded`}
      />
      <p className="text-xs text-muted-foreground">
        There is no “reject”. A sender that is never approved never reaches an agent, so declining
        is what happens by leaving them here.
      </p>
    </>
  );
}

function BindTo({
  users,
  disabled,
  onApprove,
}: {
  users: Array<{ id: string; subject: string; email?: string }>;
  disabled: boolean;
  onApprove: (userId: string) => void;
}) {
  const [userId, setUserId] = useState<string>("");

  return (
    <div className="flex items-center justify-end gap-1.5">
      <Select value={userId} onValueChange={setUserId}>
        <SelectTrigger className="h-7 w-52 text-xs" aria-label="Bind this sender to">
          <SelectValue placeholder="Bind to user…" />
        </SelectTrigger>
        <SelectContent>
          {users.map((u) => (
            <SelectItem key={u.id} value={u.id}>
              {u.email ?? u.subject}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <Button
        size="sm"
        className="h-7 px-2.5 text-xs"
        disabled={disabled || !userId}
        onClick={() => userId && onApprove(userId)}
      >
        Approve
      </Button>
    </div>
  );
}
