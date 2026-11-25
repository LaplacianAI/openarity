import { createFileRoute } from "@tanstack/react-router";

import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import { DangerZone } from "@/components/settings/danger-zone";
import { GeneralForm } from "@/components/settings/general-form";
import { SettingsBody, SettingsShell } from "@/components/settings/shell";

export const Route = createFileRoute("/ui/settings/general")({
  component: General,
});

/** The model, the hub and compaction; and, for a super admin, deleting the team. */
function General() {
  const { teamId, teams } = useCurrentTeam();
  const teamName = teams.find((t) => t.id === teamId)?.name;
  const superAdmin = usePermissions(teamId).superAdmin;
  return (
    <SettingsShell page="general">
      {(ready) => (
        <div className="space-y-6">
          <SettingsBody ready={ready} form={GeneralForm} />
          {teamName && superAdmin ? <DangerZone teamId={ready.teamId} teamName={teamName} /> : null}
        </div>
      )}
    </SettingsShell>
  );
}
