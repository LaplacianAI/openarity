import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { addMember, createTeam, listMembers, listTeams, listUsers, removeMember } from "@/api";
import { ApiError } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Member, Page, Team } from "@/api/types";
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Avatar, EmptyState, Mono, PageHeader } from "@/components/ui-bits";
import { count } from "@/lib/runs";

export const Route = createFileRoute("/ui/teams/")({
  component: Teams,
});

/** The roles the brain ships, nested: each can do what the one before it can. */
const ROLES = ["member", "approver", "admin"];

/**
 * Whether a team's members, read whole, include no admin. A page with more
 * after it cannot say so: the admin may be on the next one.
 */
export function noAdmin(members: Page<Member> | undefined): boolean {
  return Boolean(members && !members.next_cursor && !members.items.some((m) => m.role === "admin"));
}

/** The sentence at the top: which teams no one can manage but a super admin. */
export function adminFinding(teams: Team[]): string | null {
  if (teams.length === 0) return null;
  if (teams.length === 1) return `${teams[0]?.name} has no admin.`;
  return `${count(teams.length, "team")} have no admin.`;
}

function Teams() {
  const { teamId, choose, isPending: choosing } = useCurrentTeam();
  const { superAdmin } = usePermissions(teamId);
  const [creating, setCreating] = useState(false);

  const teams = useQuery({ queryKey: ["teams"], queryFn: () => listTeams() });
  const rows = teams.data?.items ?? [];
  // The same reads each team's table makes, so the finding costs nothing more.
  const members = useQueries({
    queries: rows.map((t) => ({
      queryKey: ["members", t.id],
      queryFn: () => listMembers(t.id),
    })),
  });
  const adminless = rows.filter((_, i) => noAdmin(members[i]?.data));

  // Creating a team is for super admins. Someone in no team yet is offered
  // it too: on a fresh install, the first person to sign in is one, and has
  // no team to be told so by.
  const canCreate = superAdmin || (!choosing && !teamId);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Teams"
        finding={adminFinding(adminless)}
        description="Every channel, session and sender belongs to a team. Who is in each, and what they may do there."
        actions={
          canCreate ? (
            <Button size="sm" onClick={() => setCreating(true)}>
              New team
            </Button>
          ) : null
        }
      />

      {teams.isPending ? (
        <div className="space-y-1" role="status" aria-label="Loading teams">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      ) : teams.isError ? (
        <ErrorState
          what="your teams"
          error={teams.error}
          onRetry={() => teams.refetch()}
          retrying={teams.isRefetching}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title="You are not in any team"
          body="A team is the unit everything else hangs from. Create one, and you are its first member."
          hint="Creating a team requires super admin. On a fresh install, the first person to sign in is granted it."
        />
      ) : (
        rows.map((team) => (
          <TeamSection
            key={team.id}
            team={team}
            current={team.id === teamId}
            onSwitch={() => choose(team.id)}
          />
        ))
      )}

      <CreateTeam open={creating} onClose={() => setCreating(false)} onCreated={choose} />
    </div>
  );
}

/** One team: who is in it, and, for those who may, adding and removing them. */
function TeamSection({
  team,
  current,
  onSwitch,
}: {
  team: Team;
  current: boolean;
  onSwitch: () => void;
}) {
  // Adding and removing people is membership:write in that team. Without it
  // the list is still worth seeing; the controls are not offered.
  const canManage = usePermissions(team.id).can("membership:write");
  const [adding, setAdding] = useState(false);
  const [removing, setRemoving] = useState<Member | null>(null);

  const members = useQuery({
    queryKey: ["members", team.id],
    queryFn: () => listMembers(team.id),
  });
  const rows = members.data?.items ?? [];
  const heading = `team-${team.id}`;

  return (
    <section aria-labelledby={heading} className="space-y-2">
      <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
        <div className="flex min-w-0 flex-wrap items-baseline gap-x-2">
          <h2 id={heading} className="truncate text-base font-semibold">
            {team.name}
          </h2>
          {team.role ? (
            <span className="text-xs text-muted-foreground">You are {team.role}</span>
          ) : null}
          {noAdmin(members.data) ? <span className="text-xs text-failed">No admin</span> : null}
        </div>
        <div className="flex items-center gap-2">
          {current ? (
            <span className="text-xs text-muted-foreground">Current team</span>
          ) : (
            <Button
              variant="ghost"
              size="sm"
              aria-label={`Switch to ${team.name}`}
              onClick={onSwitch}
            >
              Switch to
            </Button>
          )}
          {canManage ? (
            <Button variant="outline" size="sm" onClick={() => setAdding(true)}>
              Add member
            </Button>
          ) : null}
        </div>
      </div>

      {members.isPending ? (
        <Skeleton className="h-10 w-full" />
      ) : members.isError ? (
        <ErrorState
          what={`the members of ${team.name}`}
          error={members.error}
          onRetry={() => members.refetch()}
          retrying={members.isRefetching}
        />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">No members yet.</p>
      ) : (
        <Table aria-label={`Members of ${team.name}`}>
          <TableHeader>
            <tr>
              <TableHead>Member</TableHead>
              <TableHead>Role</TableHead>
              {canManage ? (
                <TableHead>
                  <span className="sr-only">Remove</span>
                </TableHead>
              ) : null}
            </tr>
          </TableHeader>
          <TableBody>
            {rows.map((m) => (
              <TableRow key={m.user_id}>
                <TableCell className="sm:w-full sm:max-w-0">
                  <div className="flex min-w-0 items-center gap-3">
                    <Avatar name={m.email ?? m.subject} />
                    <div className="min-w-0">
                      <div className="truncate">{m.email ?? m.subject}</div>
                      {m.email ? <Mono className="block truncate">{m.subject}</Mono> : null}
                    </div>
                  </div>
                </TableCell>
                <TableCell label="Role" className="whitespace-nowrap">
                  {m.role}
                </TableCell>
                {canManage ? (
                  <TableCell className="text-right max-sm:justify-end">
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={`Remove ${m.email ?? m.subject}`}
                      onClick={() => setRemoving(m)}
                    >
                      Remove
                    </Button>
                  </TableCell>
                ) : null}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {canManage ? (
        <>
          <AddMember team={team} members={rows} open={adding} onClose={() => setAdding(false)} />
          <RemoveMember team={team} member={removing} onClose={() => setRemoving(null)} />
        </>
      ) : null}
    </section>
  );
}

/**
 * Adding someone, in a sheet beside the list. They are picked from the
 * directory when this person may read it, and named by their sign-in subject
 * when they may not: the brain lets anyone who manages members add a person
 * they can name.
 */
function AddMember({
  team,
  members,
  open,
  onClose,
}: {
  team: Team;
  members: Member[];
  open: boolean;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const [userId, setUserId] = useState("");
  const [subject, setSubject] = useState("");
  const [role, setRole] = useState("member");

  const users = useQuery({ queryKey: ["users"], queryFn: () => listUsers(), enabled: open });
  const inTeam = new Set(members.map((m) => m.user_id));
  const offered = (users.data?.items ?? []).filter((u) => !inTeam.has(u.id));
  // Without the directory, the person is named instead of picked.
  const byName = users.isError;

  const add = useMutation({
    mutationFn: () =>
      addMember(team.id, byName ? { subject: subject.trim() } : { user_id: userId }, role),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", team.id] });
      toast.success(`Added to ${team.name}`);
      close();
    },
  });

  const close = () => {
    setUserId("");
    setSubject("");
    setRole("member");
    add.reset();
    onClose();
  };

  const ready = byName ? subject.trim() !== "" : userId !== "";

  return (
    <Sheet open={open} onOpenChange={(o) => (o ? null : close())}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>Add a member to {team.name}</SheetTitle>
          <SheetDescription>
            They must have signed in once, so the brain knows them.
          </SheetDescription>
        </SheetHeader>
        <form
          className="flex flex-1 flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) add.mutate();
          }}
        >
          {byName ? (
            <Field
              label="Their sign-in name"
              htmlFor="member-subject"
              hint="The subject their identity provider gives them. You may not read the directory, so name them instead of picking them."
            >
              <Input
                id="member-subject"
                value={subject}
                spellCheck={false}
                autoComplete="off"
                onChange={(e) => setSubject(e.target.value)}
              />
            </Field>
          ) : (
            <Field label="Person" htmlFor="member-user">
              <Select value={userId} onValueChange={setUserId}>
                <SelectTrigger id="member-user" aria-label="Person" className="w-full">
                  <SelectValue
                    placeholder={
                      users.isPending
                        ? "Loading people…"
                        : offered.length === 0
                          ? "Everyone is already in this team"
                          : "Pick someone"
                    }
                  />
                </SelectTrigger>
                <SelectContent>
                  {offered.map((u) => (
                    <SelectItem key={u.id} value={u.id}>
                      {u.email ?? u.subject}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </Field>
          )}
          <Field
            label="Role"
            htmlFor="member-role"
            hint="An approver also lets waiting senders in; an admin also manages the team."
          >
            <Select value={role} onValueChange={setRole}>
              <SelectTrigger id="member-role" aria-label="Role" className="w-full">
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
          {add.isError ? (
            <p role="alert" className="text-sm text-destructive">
              {add.error.message}
            </p>
          ) : null}
          <SheetFooter>
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={!ready} loading={add.isPending}>
              Add
            </Button>
          </SheetFooter>
        </form>
      </SheetContent>
    </Sheet>
  );
}

/** Removing someone asks first: they lose the team's work at once. */
function RemoveMember({
  team,
  member,
  onClose,
}: {
  team: Team;
  member: Member | null;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const remove = useMutation({
    mutationFn: (m: Member) => removeMember(team.id, m.user_id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", team.id] });
      toast.success(`Removed from ${team.name}`);
      close();
    },
  });
  const close = () => {
    remove.reset();
    onClose();
  };
  const who = member ? (member.email ?? member.subject) : "";

  return (
    <Dialog open={member !== null} onOpenChange={(o) => (o ? null : close())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>
            Remove {who} from {team.name}?
          </DialogTitle>
          <DialogDescription>
            They lose its channels, sessions and agents at once. Adding them back gives it all back.
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
            onClick={() => (member ? remove.mutate(member) : null)}
          >
            Remove
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

/** A new team, named. Its creator becomes its first member, and it becomes current. */
function CreateTeam({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: (teamId: string) => void;
}) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const create = useMutation({
    mutationFn: () => createTeam(name.trim()),
    onSuccess: (team) => {
      qc.invalidateQueries({ queryKey: ["teams"] });
      onCreated(team.id);
      toast.success(`Created ${team.name}`);
      close();
    },
  });
  const close = () => {
    setName("");
    create.reset();
    onClose();
  };

  // 403 here has one cause and one remedy, and the raw message says neither.
  const problem = create.isError
    ? create.error instanceof ApiError && create.error.status === 403
      ? "Creating a team requires super admin."
      : create.error.message
    : null;

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : close())}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New team</DialogTitle>
          <DialogDescription>
            You become its first member, and it becomes current.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (name.trim()) create.mutate();
          }}
        >
          <Field label="Name" htmlFor="team-name">
            <Input id="team-name" value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          {problem ? (
            <p role="alert" className="text-sm text-destructive">
              {problem}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={!name.trim()} loading={create.isPending}>
              Create
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
