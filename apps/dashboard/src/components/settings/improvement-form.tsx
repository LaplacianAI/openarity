import { ImprovementSection } from "@/components/settings/sections/improvement";
import { type SettingsFormProps, useSettingsDraft } from "@/components/settings/use-settings-draft";
import { LeaveGuard } from "@/components/unsaved";

/** Improvement: whether, and how, agents improve from their runs. */
export function ImprovementForm({ teamId, stored, canWrite }: SettingsFormProps) {
  const s = useSettingsDraft(teamId, stored, canWrite, ["improvement"]);
  return (
    <div>
      <LeaveGuard dirty={s.anyDirty} what="the team settings" />
      <ImprovementSection {...s.bind("improvement")} effChecked={s.effChecked} />
    </div>
  );
}
