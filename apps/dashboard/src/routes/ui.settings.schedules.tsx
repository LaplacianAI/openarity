import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useId, useState } from "react";

import { getScheduleSettings, saveScheduleSettings } from "@/api";
import type { ScheduleSettings } from "@/api/types";
import { Field } from "@/components/registry-bits";
import { ZoneField } from "@/components/schedule-fields";
import { Section } from "@/components/settings/section";
import { SettingsShell } from "@/components/settings/shell";
import { ErrorState } from "@/components/states";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { SwitchField } from "@/components/ui/switch";
import {
  type ScheduleSettingsDraft,
  scheduleSettingsRequest,
  settingsDraftOf,
} from "@/lib/schedule-settings";

export const Route = createFileRoute("/ui/settings/schedules")({
  component: Schedules,
});

/** Whether agents may propose schedules, the team's zone, and its limits. */
function Schedules() {
  return (
    <SettingsShell page="schedules">
      {({ teamId, canWrite }) => <Body teamId={teamId} canWrite={canWrite} />}
    </SettingsShell>
  );
}

function Body({ teamId, canWrite }: { teamId: string; canWrite: boolean }) {
  const settings = useQuery({
    queryKey: ["schedule-settings", teamId],
    queryFn: ({ signal }) => getScheduleSettings(teamId, signal),
  });
  if (settings.isPending) {
    return (
      <div className="space-y-2" role="status" aria-label="Loading settings">
        <Skeleton className="h-24 w-full" />
        <Skeleton className="h-24 w-full" />
      </div>
    );
  }
  if (settings.isError) {
    return (
      <ErrorState
        what="the schedule settings"
        error={settings.error}
        onRetry={() => settings.refetch()}
        retrying={settings.isRefetching}
      />
    );
  }
  return (
    <Form
      key={JSON.stringify(settings.data)}
      teamId={teamId}
      canWrite={canWrite}
      stored={settings.data}
    />
  );
}

function Form({
  teamId,
  canWrite,
  stored,
}: {
  teamId: string;
  canWrite: boolean;
  stored: ScheduleSettings;
}) {
  const qc = useQueryClient();
  const id = useId();
  const [draft, setDraft] = useState<ScheduleSettingsDraft>(() => settingsDraftOf(stored));
  const [problem, setProblem] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const set = (patch: Partial<ScheduleSettingsDraft>) => {
    setDraft((d) => ({ ...d, ...patch }));
    setSaved(false);
    setProblem(null);
  };
  const dirty = JSON.stringify(draft) !== JSON.stringify(settingsDraftOf(stored));
  const save = useMutation({
    mutationFn: (body: ScheduleSettings) => saveScheduleSettings(teamId, body),
    onSuccess: (next) => {
      qc.setQueryData(["schedule-settings", teamId], next);
      setSaved(true);
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const number = (key: keyof ScheduleSettingsDraft, label: string, hint: string) => (
    <Field label={label} htmlFor={`${id}-${key}`} hint={hint}>
      <Input
        id={`${id}-${key}`}
        inputMode="numeric"
        autoComplete="off"
        className="w-28 tabular-nums"
        value={draft[key] as string}
        onChange={(e) => set({ [key]: e.target.value })}
      />
    </Field>
  );
  return (
    <Section
      title="Schedules and watchers"
      about="Schedules already made keep their rules; these limits hold from their next fire or change."
      canWrite={canWrite}
      dirty={dirty}
      saved={saved}
      problem={problem}
      pending={save.isPending}
      busy={false}
      onSave={() => {
        const body = scheduleSettingsRequest(draft);
        if (typeof body === "string") return setProblem(body);
        save.mutate(body);
      }}
    >
      <SwitchField
        label="Agents may propose schedules and watchers"
        description="An agent with the grant may propose one in a chat. It starts only once the person confirms it, with the code the agent gives them."
        checked={draft.agents}
        onCheckedChange={(agents: boolean) => set({ agents })}
      />
      <ZoneField
        id={`${id}-zone`}
        value={draft.zone}
        onChange={(zone) => set({ zone })}
        hint="What a schedule made without a zone reads its rule in."
      />
      {number(
        "interval",
        "Shortest repeat, in minutes",
        "5 minutes to 7 days (10,080). A watcher looks no more often either.",
      )}
      <div className="grid gap-4 sm:grid-cols-3">
        {number("perMember", "Per person", "1 to 100, schedules and watchers together.")}
        {number("perTeam", "Per team", "1 to 1,000, and no fewer than per person.")}
        {number("firesPerHour", "Runs an hour", "1 to 1,000, across the team.")}
      </div>
      <p className="text-xs text-muted-foreground">
        Saved whole: a change someone else saves while this page is open is replaced by what is
        here.
      </p>
    </Section>
  );
}
