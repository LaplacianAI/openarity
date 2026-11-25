import { BrowserSection } from "@/components/settings/sections/browser";
import { SandboxSection } from "@/components/settings/sections/sandbox";
import { SearchSection } from "@/components/settings/sections/search";
import { type SettingsFormProps, useSettingsDraft } from "@/components/settings/use-settings-draft";
import { LeaveGuard } from "@/components/unsaved";

/** Tools: web search, the sandbox, and the browser. */
export function ToolsForm({ teamId, stored, canWrite }: SettingsFormProps) {
  const s = useSettingsDraft(teamId, stored, canWrite, ["search", "sandbox", "browser"]);
  return (
    <div>
      <LeaveGuard dirty={s.anyDirty} what="the team settings" />
      <SearchSection
        stored={stored}
        draft={s.draft}
        set={(patch) => s.set("search", patch)}
        section={s.section("search")}
      />
      <SandboxSection
        stored={stored}
        draft={s.draft}
        set={(patch) => s.set("sandbox", patch)}
        section={s.section("sandbox")}
      />
      <BrowserSection
        stored={stored}
        draft={s.draft}
        set={(patch) => s.set("browser", patch)}
        section={s.section("browser")}
      />
    </div>
  );
}
