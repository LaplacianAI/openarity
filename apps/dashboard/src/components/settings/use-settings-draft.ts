import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { saveTeamSettings } from "@/api";
import type { TeamSettings } from "@/api/types";
import type { SectionProps } from "@/components/settings/section";
import {
  draftOf,
  FIELDS,
  type Group,
  isDirty,
  type SettingsDraft,
  withFields,
} from "@/lib/team-settings/draft";
import { groupRequest } from "@/lib/team-settings/request";

/**
 * One page's settings as typed: the draft, and each group's own Save. The
 * brain takes the settings whole, so a Save sends its group as typed and every
 * other group as the brain holds it; after it, only that group reads what the
 * brain now holds, and another group's unsaved edits stay.
 */
export function useSettingsDraft(
  teamId: string,
  stored: TeamSettings,
  canWrite: boolean,
  groups: readonly Group[],
) {
  const qc = useQueryClient();
  const [draft, setDraft] = useState<SettingsDraft>(() => draftOf(stored));
  const [problem, setProblem] = useState<{ group: Group; text: string } | null>(null);
  const [pending, setPending] = useState<Group | null>(null);
  const [saved, setSaved] = useState<Group | null>(null);
  // Once a save of the improvement section is refused, each efficiency
  // field shows its own error until a save goes through: fixing one clears
  // only that one's.
  const [effChecked, setEffChecked] = useState(false);

  const set = (group: Group, patch: Partial<SettingsDraft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    if (problem?.group === group) setProblem(null);
    if (saved === group) setSaved(null);
  };

  const dirty = (g: Group) => isDirty(g, draft, stored);

  // A plain call rather than useMutation: a mutation keeps its variables in
  // the query client's cache, and they can hold the key.
  const save = async (group: Group) => {
    const body = groupRequest(group, draft, stored);
    if (typeof body === "string") {
      setProblem({ group, text: body });
      if (group === "improvement") setEffChecked(true);
      return;
    }
    setPending(group);
    setProblem(null);
    try {
      const next = await saveTeamSettings(teamId, body);
      qc.setQueryData(["settings", teamId], next);
      // This group now reads what the brain holds, the typed key gone; the
      // others keep what was typed in them.
      const fresh = draftOf(next);
      setDraft((d) => withFields(d, fresh, FIELDS[group]));
      setSaved(group);
      if (group === "improvement") setEffChecked(false);
    } catch (err) {
      setProblem({ group, text: err instanceof Error ? err.message : String(err) });
    } finally {
      setPending(null);
    }
  };

  const section = (group: Group): SectionProps => ({
    canWrite,
    dirty: dirty(group),
    saved: saved === group,
    problem: problem?.group === group ? problem.text : null,
    pending: pending === group,
    busy: pending !== null && pending !== group,
    onSave: () => void save(group),
  });

  /** What a section is handed: the draft, its group's edits, and its group's Save. */
  const bind = (group: Group) => ({
    draft,
    set: (patch: Partial<SettingsDraft>) => set(group, patch),
    section: section(group),
  });

  return {
    draft,
    bind,
    /** Changes a group's fields; its own refusal and "Saved" go with the edit. */
    set,
    section,
    /** Whether any of this page's groups holds an unsaved change. */
    anyDirty: () => groups.some(dirty),
    effChecked,
  };
}

/** What each page's form is given. */
export type SettingsFormProps = { teamId: string; stored: TeamSettings; canWrite: boolean };
