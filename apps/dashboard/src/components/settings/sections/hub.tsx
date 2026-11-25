import { listAgents } from "@/api";
import { Picker } from "@/components/picker";
import { Section, type SectionProps } from "@/components/settings/section";
import { PICK_PAGE } from "@/lib/team-settings/catalogue";
import type { SettingsDraft } from "@/lib/team-settings/draft";

/** The agent every message to the team reaches first. */
export function HubSection({
  teamId,
  draft,
  set,
  section,
}: {
  teamId: string;
  draft: SettingsDraft;
  set: (patch: Partial<SettingsDraft>) => void;
  section: SectionProps;
}) {
  return (
    <Section
      title="The hub"
      about="Every message to the team reaches the hub first, and the hub answers it or hands the work to the agent best placed to. Leave empty for the built-in orchestrator, which does this for you."
      {...section}
    >
      {draft.hub === null ? <p className="text-sm">The built-in orchestrator</p> : null}
      <Picker
        label="Hub"
        noun="agent"
        max={1}
        value={draft.hub ? [draft.hub] : []}
        onChange={(next) => set({ hub: next[0] ?? null })}
        search={(q) =>
          listAgents(teamId, null, { q, limit: PICK_PAGE }).then((page) => ({
            items: page.items.map((a) => ({ id: a.id, name: a.name })),
            more: Boolean(page.next_cursor),
          }))
        }
        searchKey={["agents", teamId]}
      />
    </Section>
  );
}
