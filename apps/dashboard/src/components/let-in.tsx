import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { listApprovedSenders, listChannels, listMembers, removeSender } from "@/api";
import { nextCursorOf } from "@/api/client";
import type { ApprovedSender, ListedChannel, Member } from "@/api/types";
import { Time } from "@/components/facts";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { EmptyState, LoadMore, Mono } from "@/components/ui-bits";

/**
 * Who has been let in, channel by channel, and the way to stop letting
 * someone in. The brain keeps the list per channel, so each is read alone.
 */
export function LetIn({ teamId, canRemove }: { teamId: string; canRemove: boolean }) {
  // Every member may read the team's members, so who each sender is bound to
  // is known whether or not the reader may approve.
  const members = useQuery({
    queryKey: ["members", teamId],
    queryFn: () => listMembers(teamId),
  });
  const channels = useQuery({
    queryKey: ["channels", teamId, "let-in"],
    queryFn: () => listChannels(teamId),
  });

  if (channels.isPending) {
    return <Skeleton className="h-12 w-full" />;
  }
  if (channels.isError) {
    return (
      <ErrorState
        what="the team's channels"
        error={channels.error}
        onRetry={() => channels.refetch()}
      />
    );
  }
  if (channels.data.items.length === 0) {
    return (
      <EmptyState
        title="No channels yet"
        body="Someone is let in on a channel, so with none there is no one to list."
      />
    );
  }
  return (
    <div className="space-y-8">
      {channels.data.items.map((c) => (
        <OnChannel
          key={c.id}
          teamId={teamId}
          channel={c}
          members={members.data?.items ?? null}
          canRemove={canRemove}
        />
      ))}
    </div>
  );
}

function OnChannel({
  teamId,
  channel,
  members,
  canRemove,
}: {
  teamId: string;
  channel: ListedChannel;
  members: Member[] | null;
  canRemove: boolean;
}) {
  const [removing, setRemoving] = useState<ApprovedSender | null>(null);
  const senders = useInfiniteQuery({
    queryKey: ["approved", teamId, channel.id],
    queryFn: ({ pageParam }) => listApprovedSenders(teamId, channel.id, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = senders.data?.pages.flatMap((p) => p.items) ?? [];
  // Until the members are read, nobody is called gone.
  const who = (userId: string) => {
    if (members === null) return "…";
    const m = members.find((x) => x.user_id === userId);
    return m ? (m.email ?? m.subject) : "Someone no longer in the team";
  };
  const id = `let-in-${channel.id}`;

  return (
    <section aria-labelledby={id} className="space-y-3">
      <h2 id={id} className="text-base font-medium">
        {channel.name}
      </h2>
      {senders.isPending ? (
        <Skeleton className="h-10 w-full" />
      ) : senders.isError ? (
        <ErrorState
          what={`who is let in on ${channel.name}`}
          error={senders.error}
          onRetry={() => senders.refetch()}
        />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">No one has been let in on this channel.</p>
      ) : (
        <div>
          <Table aria-label={`Let in on ${channel.name}`}>
            <TableHeader>
              <tr>
                <TableHead>Sender</TableHead>
                <TableHead>Bound to</TableHead>
                <TableHead>Let in</TableHead>
                {canRemove ? (
                  <TableHead>
                    <span className="sr-only">Remove</span>
                  </TableHead>
                ) : null}
              </tr>
            </TableHeader>
            <TableBody>
              {rows.map((s) => (
                <TableRow key={s.sender_ref} data-sender={s.sender_ref}>
                  <TableCell>
                    <Mono className="block truncate">{s.sender_ref}</Mono>
                  </TableCell>
                  <TableCell label="Bound to">{who(s.user_id)}</TableCell>
                  <TableCell label="Let in" className="whitespace-nowrap text-muted-foreground">
                    <Time iso={s.created_at} />
                  </TableCell>
                  {canRemove ? (
                    <TableCell className="text-right stacked:justify-end">
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`Remove ${s.sender_ref}`}
                        onClick={() => setRemoving(s)}
                      >
                        Remove
                      </Button>
                    </TableCell>
                  ) : null}
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <LoadMore
            hasMore={Boolean(senders.hasNextPage)}
            loading={senders.isFetchingNextPage}
            onClick={() => senders.fetchNextPage()}
            loadedLabel={`${rows.length} let in`}
          />
        </div>
      )}
      {canRemove ? (
        <RemoveSender
          teamId={teamId}
          channel={channel}
          sender={removing}
          onClose={() => setRemoving(null)}
        />
      ) : null}
    </section>
  );
}

function RemoveSender({
  teamId,
  channel,
  sender,
  onClose,
}: {
  teamId: string;
  channel: ListedChannel;
  sender: ApprovedSender | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const remove = useMutation({
    mutationFn: (s: ApprovedSender) => removeSender(teamId, channel.id, s.sender_ref),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["approved", teamId, channel.id] });
      toast.success(`No longer let in on ${channel.name}`);
      close();
    },
  });
  const close = () => {
    remove.reset();
    onClose();
  };

  return (
    <Dialog open={sender !== null} onOpenChange={(o) => (o ? null : close())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            Stop letting {sender?.sender_ref} in on {channel.name}?
          </DialogTitle>
          <DialogDescription>
            What they send stops reaching the team's agents. If they write again, they wait in
            Approvals to be let in.
          </DialogDescription>
        </DialogHeader>
        {remove.isError ? (
          <p role="alert" className="text-sm text-destructive">
            {remove.error.message}
          </p>
        ) : null}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={close}>
            Keep them
          </Button>
          <Button
            type="button"
            variant="destructive"
            loading={remove.isPending}
            onClick={() => (sender ? remove.mutate(sender) : null)}
          >
            Remove
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
