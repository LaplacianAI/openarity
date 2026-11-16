import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import {
  approveSender,
  listApprovedSenders,
  listChannels,
  listMembers,
  removeSender,
  updateMember,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import type { ApprovedSender, ListedChannel, Member } from "@/api/types";
import { Time } from "@/components/facts";
import { Field } from "@/components/registry-bits";
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
import { Input } from "@/components/ui/input";
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
import { EmptyState, LoadMore, Mono } from "@/components/ui-bits";
import { ROLES } from "@/lib/roles";

/**
 * Who has been let in, channel by channel, as whom, and the way to change
 * that or stop letting someone in. The brain keeps the list per channel, so
 * each is read alone. Moving a sender is sender:approve, like letting them in;
 * a role or a channel user's name is membership:write.
 */
export function LetIn({
  teamId,
  canRemove,
  canManage = false,
}: {
  teamId: string;
  canRemove: boolean;
  canManage?: boolean;
}) {
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
          canManage={canManage}
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
  canManage,
}: {
  teamId: string;
  channel: ListedChannel;
  members: Member[] | null;
  canRemove: boolean;
  canManage: boolean;
}) {
  const [removing, setRemoving] = useState<ApprovedSender | null>(null);
  const [editing, setEditing] = useState<ApprovedSender | null>(null);
  const senders = useInfiniteQuery({
    queryKey: ["approved", teamId, channel.id],
    queryFn: ({ pageParam }) => listApprovedSenders(teamId, channel.id, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = senders.data?.pages.flatMap((p) => p.items) ?? [];
  // Until the members are read, nobody is called gone.
  const who = (s: ApprovedSender) => {
    if (s.name) return s.name;
    if (members === null) return "…";
    const m = members.find((x) => x.user_id === s.user_id);
    return m ? (m.email ?? m.name ?? m.subject) : "Someone no longer in the team";
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
                <TableHead>Speaks as</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Let in</TableHead>
                {canRemove ? (
                  <TableHead>
                    <span className="sr-only">Edit or remove</span>
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
                  <TableCell label="Speaks as">
                    {who(s)}
                    {s.channel_user ? (
                      <span className="ml-1.5 text-xs text-muted-foreground">their own</span>
                    ) : null}
                  </TableCell>
                  <TableCell label="Role" className="whitespace-nowrap">
                    {s.role || <span className="text-muted-foreground">not in the team</span>}
                  </TableCell>
                  <TableCell label="Let in" className="whitespace-nowrap text-muted-foreground">
                    <Time iso={s.created_at} />
                  </TableCell>
                  {canRemove ? (
                    <TableCell className="text-right stacked:justify-end">
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`Edit ${s.sender_ref}`}
                        onClick={() => setEditing(s)}
                      >
                        Edit
                      </Button>
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
        <>
          <RemoveSender
            teamId={teamId}
            channel={channel}
            sender={removing}
            onClose={() => setRemoving(null)}
          />
          <EditSender
            key={editing?.sender_ref ?? "none"}
            teamId={teamId}
            channel={channel}
            sender={editing}
            members={members ?? []}
            canManage={canManage}
            onClose={() => setEditing(null)}
          />
        </>
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

/** Who the sender speaks as when that is a user of their own. */
const THEIR_OWN = "their-own";

/**
 * Editing an approval: moving the sender to another member, as approving
 * again does, or changing the role and name of the user they speak as, in
 * place. Moving a sender off a user of their own deletes that user, and the
 * sheet says so before it saves.
 */
function EditSender({
  teamId,
  channel,
  sender,
  members,
  canManage,
  onClose,
}: {
  teamId: string;
  channel: ListedChannel;
  sender: ApprovedSender | null;
  members: Member[];
  canManage: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const current = sender?.channel_user ? THEIR_OWN : (sender?.user_id ?? "");
  const [speaksAs, setSpeaksAs] = useState(current);
  const [role, setRole] = useState(sender?.role ?? "");
  const [name, setName] = useState(sender?.name ?? "");
  const moving = speaksAs !== current;
  const people = members.filter((m) => !m.channel_user);

  const save = useMutation({
    mutationFn: async (s: ApprovedSender) => {
      if (moving) {
        await approveSender(teamId, channel.id, s.sender_ref, { user_id: speaksAs });
        return;
      }
      const change: { role?: string; name?: string } = {};
      if (role && role !== s.role) change.role = role;
      if (s.channel_user && name.trim() && name.trim() !== s.name) change.name = name.trim();
      if (change.role || change.name) await updateMember(teamId, s.user_id, change);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["approved", teamId, channel.id] });
      void qc.invalidateQueries({ queryKey: ["members", teamId] });
      toast.success("Saved");
      onClose();
    },
  });

  return (
    <Dialog open={sender !== null} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            Edit {sender?.sender_ref} on {channel.name}
          </DialogTitle>
          <DialogDescription>
            Who their messages are from, and what that person may do in the team.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <Field label="Speaks as" htmlFor="edit-speaks-as">
            <Select value={speaksAs} onValueChange={setSpeaksAs}>
              <SelectTrigger id="edit-speaks-as" aria-label="Speaks as" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sender?.channel_user ? (
                  <SelectItem value={THEIR_OWN}>Their own: {sender.name}</SelectItem>
                ) : null}
                {people.map((m) => (
                  <SelectItem key={m.user_id} value={m.user_id}>
                    {m.email ?? m.subject}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          {moving && sender?.channel_user ? (
            <p role="alert" className="text-sm text-failed">
              {sender.name}'s own identity is deleted, with anything they own.
            </p>
          ) : null}
          {canManage && !moving ? (
            <>
              <Field label="Role" htmlFor="edit-role">
                <Select value={role} onValueChange={setRole}>
                  <SelectTrigger id="edit-role" aria-label="Role" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {ROLES.map((r) => (
                      <SelectItem key={r} value={r}>
                        {r}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </Field>
              {sender?.channel_user ? (
                <Field label="Name" htmlFor="edit-name">
                  <Input
                    id="edit-name"
                    value={name}
                    maxLength={64}
                    onChange={(e) => setName(e.target.value)}
                  />
                </Field>
              ) : null}
            </>
          ) : null}
        </div>
        {save.isError ? (
          <p role="alert" className="text-sm text-destructive">
            {save.error.message}
          </p>
        ) : null}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            loading={save.isPending}
            onClick={() => (sender ? save.mutate(sender) : null)}
          >
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
