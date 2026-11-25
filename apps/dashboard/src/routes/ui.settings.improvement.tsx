import { createFileRoute } from "@tanstack/react-router";
import { ImprovementForm } from "@/components/settings/improvement-form";
import { SettingsBody, SettingsShell } from "@/components/settings/shell";

export const Route = createFileRoute("/ui/settings/improvement")({
  component: Improvement,
});

/** Whether agents propose changes to themselves, the efficiency pass, and tries on past runs. */
function Improvement() {
  return (
    <SettingsShell page="improvement">
      {(ready) => <SettingsBody ready={ready} form={ImprovementForm} />}
    </SettingsShell>
  );
}
