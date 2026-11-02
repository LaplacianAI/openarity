import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { deleteTeam, getTeam } from "@/api";
import { ApiError } from "@/api/client";
import { forgetTeam } from "@/api/session";
import { Field } from "@/components/registry-bits";
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

/** The brain's default purge window. The real one is the brain's, and it says so on delete. */
const DEFAULT_PURGE_AFTER = 7 * 24 * 60 * 60 * 1000;

/** A date as the brain writes it, without the time: the purge runs hourly anyway. */
export function day(iso: string): string {
  return iso.slice(0, 10);
}

function refusal(err: unknown): string {
  if (err instanceof ApiError && err.status === 409) {
    return "This team has runs in progress. Wait for them to finish, or cancel them, then try again.";
  }
  return err instanceof Error ? err.message : String(err);
}

/**
 * Deleting a team, for a super admin. The name is typed back rather than
 * clicked through, and once the brain has it the dashboard lets go of the
 * team before any screen can ask about it again.
 */
export function DeleteTeamDialog({ teamId, teamName }: { teamId: string; teamName: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  const close = () => {
    setOpen(false);
    setTyped("");
    setProblem(null);
  };

  const confirm = async () => {
    setPending(true);
    setProblem(null);
    try {
      await deleteTeam(teamId);
    } catch (err) {
      setProblem(refusal(err));
      setPending(false);
      return;
    }

    forgetTeam();
    // The purge date is the brain's to say: the window is its setting.
    const gone = await getTeam(teamId).catch(() => null);
    await qc.invalidateQueries({ queryKey: ["teams"] });
    toast.success(
      gone?.purge_at
        ? `Deleted ${teamName}. You can restore it from Deleted teams until ${day(gone.purge_at)}.`
        : `Deleted ${teamName}.`,
    );
    setPending(false);
    close();
    await navigate({ to: "/ui/teams" });
  };

  const byDefault = day(new Date(Date.now() + DEFAULT_PURGE_AFTER).toISOString());

  return (
    <>
      <Button variant="destructive" size="sm" onClick={() => setOpen(true)}>
        Delete team
      </Button>
      <Dialog open={open} onOpenChange={(o) => (o ? setOpen(true) : close())}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Delete {teamName}?</DialogTitle>
            <DialogDescription>
              Its members lose access right away, and its name can be used again. You can restore it
              from Deleted teams until it is erased for good, usually a week after it is deleted,
              around <span className="whitespace-nowrap">{byDefault}</span>.
            </DialogDescription>
          </DialogHeader>
          <form
            className="space-y-3"
            onSubmit={(e) => {
              e.preventDefault();
              if (typed === teamName) void confirm();
            }}
          >
            <Field label="Type the team's name to confirm" htmlFor="delete-team-name">
              <Input
                id="delete-team-name"
                value={typed}
                autoComplete="off"
                spellCheck={false}
                placeholder={teamName}
                onChange={(e) => setTyped(e.target.value)}
              />
            </Field>
            {problem ? (
              <p role="alert" className="text-xs text-destructive">
                {problem}
              </p>
            ) : null}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={close}>
                Cancel
              </Button>
              <Button type="submit" variant="destructive" disabled={typed !== teamName || pending}>
                {pending ? "Deleting…" : `Delete ${teamName}`}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </>
  );
}
