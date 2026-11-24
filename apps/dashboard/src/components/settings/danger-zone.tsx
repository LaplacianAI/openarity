import { DeleteTeamDialog } from "@/components/delete-team-dialog";

/** Super admins only: the brain refuses a delete from anyone else. */
export function DangerZone({ teamId, teamName }: { teamId: string; teamName: string }) {
  return (
    <section
      aria-labelledby="danger-zone"
      className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-destructive/40 bg-card p-4"
    >
      <div className="space-y-1">
        <h2 id="danger-zone" className="text-sm font-medium text-destructive">
          Delete this team
        </h2>
        <p className="max-w-xl text-xs text-muted-foreground">
          Its members lose access right away, and its name can be used again. A super admin can
          restore it from Deleted teams until it is erased for good. Not possible while a run is
          waiting or running.
        </p>
      </div>
      <DeleteTeamDialog teamId={teamId} teamName={teamName} />
    </section>
  );
}
