import { CompactionSection } from "@/components/settings/sections/compaction";
import { HubSection } from "@/components/settings/sections/hub";
import { ModelSection } from "@/components/settings/sections/model";
import { type SettingsFormProps, useSettingsDraft } from "@/components/settings/use-settings-draft";
import { LeaveGuard } from "@/components/unsaved";
import { compactionOf } from "@/lib/team-settings/draft";

/** General: the model, the hub, and when a run compacts. */
export function GeneralForm({ teamId, stored, canWrite }: SettingsFormProps) {
  const s = useSettingsDraft(teamId, stored, canWrite, ["model", "hub", "compaction"]);
  return (
    <div>
      <LeaveGuard dirty={s.anyDirty} what="the team settings" />
      <ModelSection
        draft={s.draft}
        set={(patch) => s.set("model", patch)}
        section={s.section("model")}
      />
      <HubSection
        teamId={teamId}
        draft={s.draft}
        set={(patch) => s.set("hub", patch)}
        section={s.section("hub")}
      />
      <CompactionSection
        draft={compactionOf(s.draft)}
        onChange={(patch) =>
          s.set("compaction", {
            ...(patch.messages === undefined ? {} : { compaction: patch.messages }),
            ...(patch.least === undefined ? {} : { tokensMin: patch.least }),
            ...(patch.most === undefined ? {} : { tokensMax: patch.most }),
          })
        }
        section={s.section("compaction")}
      />
    </div>
  );
}
