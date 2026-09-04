import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { addMember, createTeam, listMembers, listTeams, listUsers, removeMember } from "@/api";
import { ApiError } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Avatar, EmptyState, Mono, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/teams/")({
  component: Teams,
});

function Teams() {
  const qc = useQueryClient();
  const { teamId, choose } = useCurrentTeam();
  const [name, setName] = useState("");

  const teams = useQuery({ queryKey: ["teams"], queryFn: () => listTeams() });

  const create = useMutation({
    mutationFn: () => createTeam(name.trim()),
    onSuccess: (team) => {
      setName("");
      qc.invalidateQueries({ queryKey: ["teams"] });
      choose(team.id);
      toast.success(`Created ${team.name}`);
    },
    onError: (err: Error) => {
      // 403 here has one cause and one remedy, and the raw message does not
      // say either: creating a team is super-admin only.
      toast.error(
        err instanceof ApiError && err.status === 403
          ? "Creating a team requires super admin"
          : err.message,
      );
    },
  });

  const rows = teams.data?.items ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Teams"
        description="Every channel, session and sender belongs to a team."
        actions={
          <form
            className="flex items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim()) create.mutate();
            }}
          >
            <Input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="New team name"
              aria-label="New team name"
              className="h-8 w-48 text-xs"
            />
            <Button type="submit" size="sm" disabled={!name.trim() || create.isPending}>
              Create
            </Button>
          </form>
        }
      />

      {teams.isPending ? (
        <p className="text-sm text-muted-foreground">Loading teams…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="You are not in any team"
          body="A team is the unit everything else hangs from. Create one above — you will be its first member."
          hint="Creating a team requires super admin. On a fresh install, the first person to sign in is granted it."
        />
      ) : (
        <div className="space-y-3">
          {rows.map((team) => (
            <section key={team.id} className="rounded-md border border-border bg-card">
              <header className="flex items-center justify-between gap-3 border-b border-border px-4 py-2.5">
                <div className="flex items-baseline gap-2">
                  <h2 className="text-sm font-semibold">{team.name}</h2>
                  {team.role ? <Mono>{team.role}</Mono> : null}
                </div>
                {team.id === teamId ? (
                  <span className="text-xs text-muted-foreground">current</span>
                ) : (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="text-xs"
                    onClick={() => choose(team.id)}
                  >
                    Switch to
                  </Button>
                )}
              </header>
              <Members teamId={team.id} />
            </section>
          ))}
        </div>
      )}
    </div>
  );
}

function Members({ teamId }: { teamId: string }) {
  const qc = useQueryClient();
  const [userId, setUserId] = useState("");
  const [role, setRole] = useState("member");

  const members = useQuery({
    queryKey: ["members", teamId],
    queryFn: () => listMembers(teamId),
  });
  const users = useQuery({ queryKey: ["users"], queryFn: () => listUsers() });

  const add = useMutation({
    mutationFn: () => addMember(teamId, { user_id: userId }, role),
    onSuccess: () => {
      setUserId("");
      qc.invalidateQueries({ queryKey: ["members", teamId] });
      toast.success("Member added");
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const remove = useMutation({
    mutationFn: (id: string) => removeMember(teamId, id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["members", teamId] });
      toast.success("Member removed");
    },
    onError: (err: Error) => toast.error(err.message),
  });

  const rows = members.data?.items ?? [];

  return (
    <div className="px-4 py-3">
      <ul className="divide-y divide-border">
        {rows.map((m) => (
          <li key={m.user_id} className="flex items-center gap-3 py-2">
            <Avatar name={m.email ?? m.subject} />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm">{m.email ?? m.subject}</div>
              {m.email ? <Mono>{m.subject}</Mono> : null}
            </div>
            <span className="text-xs text-muted-foreground">{m.role}</span>
            <Button
              variant="ghost"
              size="sm"
              className="text-xs"
              disabled={remove.isPending}
              onClick={() => remove.mutate(m.user_id)}
            >
              Remove
            </Button>
          </li>
        ))}
        {rows.length === 0 && !members.isPending ? (
          <li className="py-2 text-xs text-muted-foreground">No members yet.</li>
        ) : null}
      </ul>

      <form
        className="flex items-center gap-2 pt-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (userId) add.mutate();
        }}
      >
        <Select value={userId} onValueChange={setUserId}>
          <SelectTrigger className="h-8 w-56 text-xs" aria-label="User to add">
            <SelectValue placeholder="Add a user…" />
          </SelectTrigger>
          <SelectContent>
            {(users.data?.items ?? []).map((u) => (
              <SelectItem key={u.id} value={u.id}>
                {u.email ?? u.subject}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={role} onValueChange={setRole}>
          <SelectTrigger className="h-8 w-28 text-xs" aria-label="Role">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="member">member</SelectItem>
            <SelectItem value="admin">admin</SelectItem>
          </SelectContent>
        </Select>
        <Button type="submit" size="sm" disabled={!userId || add.isPending}>
          Add
        </Button>
      </form>
    </div>
  );
}
