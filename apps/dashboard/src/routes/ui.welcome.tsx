import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { createTeam } from "@/api";
import { ApiError } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";

export const Route = createFileRoute("/ui/welcome")({
  component: Welcome,
});

// Remembered so that skipping is permanent. It is a preference about this
// browser rather than a fact about the install, which is why it is not asked
// of the brain.
const DISMISSED = "openarity.welcome.dismissed";

export function dismissWelcome() {
  try {
    window.localStorage.setItem(DISMISSED, "1");
  } catch {
    // A browser refusing storage means the welcome appears again, which is
    // annoying rather than broken.
  }
}

export function welcomeDismissed(): boolean {
  try {
    return window.localStorage.getItem(DISMISSED) === "1";
  } catch {
    return false;
  }
}

function Welcome() {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const { choose } = useCurrentTeam();
  const [name, setName] = useState("");

  const create = useMutation({
    mutationFn: () => createTeam(name.trim()),
    onSuccess: (team) => {
      dismissWelcome();
      qc.invalidateQueries({ queryKey: ["teams"] });
      choose(team.id);
      toast.success(`Created ${team.name}`);
      navigate({ to: "/ui" });
    },
    onError: (err: Error) => {
      // The one 403 worth translating: creating a team is super-admin only,
      // and the first person to sign in is promoted automatically. Anyone
      // seeing this is not that person.
      toast.error(
        err instanceof ApiError && err.status === 403
          ? "Creating a team requires super admin — ask whoever set this up"
          : err.message,
      );
    },
  });

  const skip = () => {
    dismissWelcome();
    navigate({ to: "/ui" });
  };

  return (
    <div className="mx-auto max-w-lg space-y-6 py-10">
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Welcome to Openarity</h1>
        <p className="text-sm text-muted-foreground">
          You do not belong to a team yet, and everything here belongs to one — channels, sessions,
          and the people who can see them.
        </p>
      </div>

      <form
        className="space-y-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (name.trim()) {
            create.mutate();
          }
        }}
      >
        <label className="block space-y-1.5" htmlFor="team-name">
          <span className="text-sm font-medium">Name your first team</span>
          <Input
            id="team-name"
            autoFocus
            value={name}
            placeholder="my-team"
            onChange={(e) => setName(e.target.value)}
          />
        </label>

        <div className="flex items-center gap-2">
          <Button type="submit" disabled={!name.trim() || create.isPending}>
            {create.isPending ? "Creating…" : "Create team"}
          </Button>
          <Button type="button" variant="ghost" onClick={skip}>
            Skip
          </Button>
        </div>
      </form>

      <p className="text-xs text-muted-foreground">
        Channels come next — the places messages arrive from. You can add one once your team exists.
      </p>
    </div>
  );
}
