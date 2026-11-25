import { CompactionSection } from "@/components/settings/sections/compaction";
import { HubSection } from "@/components/settings/sections/hub";
import { ModelSection } from "@/components/settings/sections/model";
import { type SettingsFormProps, useSettingsDraft } from "@/components/settings/use-settings-draft";
import { LeaveGuard } from "@/components/unsaved";
import { compactionOf, compactionPatch } from "@/lib/team-settings/draft";

/** General: the model, the hub, and when a run compacts. */
export function GeneralForm({ teamId, stored, canWrite }: SettingsFormProps) {
  const s = useSettingsDraft(teamId, stored, canWrite, ["model", "hub", "compaction"]);
  const compaction = s.bind("compaction");
  return (
    <div>
      <LeaveGuard dirty={s.anyDirty} what="the team settings" />
      <ModelSection {...s.bind("model")} />
      <HubSection teamId={teamId} {...s.bind("hub")} />
      <CompactionSection
        draft={compactionOf(compaction.draft)}
        onChange={(patch) => compaction.set(compactionPatch(patch))}
        section={compaction.section}
      />
    </div>
  );
}
