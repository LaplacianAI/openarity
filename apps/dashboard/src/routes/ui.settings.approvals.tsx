import { createFileRoute } from "@tanstack/react-router";

import { ApprovalSections } from "@/components/settings/approvals";
import { SettingsShell } from "@/components/settings/shell";

export const Route = createFileRoute("/ui/settings/approvals")({
  component: Approvals,
});

/** Which tool calls wait for a person, and the rules that decide. */
function Approvals() {
  return (
    <SettingsShell page="approvals">
      {({ teamId, canWrite }) => <ApprovalSections teamId={teamId} canWrite={canWrite} />}
    </SettingsShell>
  );
}
