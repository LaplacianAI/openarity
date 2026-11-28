import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { toast } from "sonner";

import { createSchedule, getScheduleSettings } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { CreateScheduleRequest } from "@/api/types";
import { ReadOnlyNote } from "@/components/registry-bits";
import { ScheduleForm } from "@/components/schedule-form";
import { TriggerHeader } from "@/components/schedule-page";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui-bits";
import { LeaveGuard, SaveBar } from "@/components/unsaved";
import { emptyScheduleDraft, type ScheduleDraft, scheduleRequest } from "@/lib/schedule-draft";
import { readerZone, timeBoth } from "@/lib/when";

export const Route = createFileRoute("/ui/schedules/new")({
  component: NewSchedule,
});

const WRITE = "schedule:write";

function NewSchedule() {
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const settings = useQuery({
    queryKey: ["schedule-settings", teamId],
    queryFn: ({ signal }) => getScheduleSettings(teamId as string, signal),
    enabled: Boolean(teamId),
  });
  return (
    <div className="space-y-6">
      <TriggerHeader
        crumb="Schedules"
        title="New schedule"
        line="It runs as you, and starts at once."
      />
      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to schedule its agents." />
      ) : perms.isPending || settings.isPending ? null : !perms.can(WRITE) ? (
        <ReadOnlyNote permission={WRITE} what="schedules" />
      ) : (
        <Maker teamId={teamId} zone={settings.data?.timezone ?? readerZone()} />
      )}
    </div>
  );
}

function Maker({ teamId, zone }: { teamId: string; zone: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const empty = emptyScheduleDraft(zone);
  const [draft, setDraft] = useState<ScheduleDraft>(empty);
  const [problem, setProblem] = useState<string | null>(null);
  const unsaved = useRef(false);
  unsaved.current = draft.name !== "" || draft.objective !== "";

  const create = useMutation({
    mutationFn: (body: CreateScheduleRequest) => createSchedule(teamId, body),
    onSuccess: async (s) => {
      unsaved.current = false;
      qc.setQueryData(["schedules", teamId, s.id], s);
      void qc.invalidateQueries({ queryKey: ["schedules", teamId, "list"] });
      const next = s.next_fire_at ? ` · next ${timeBoth(s.next_fire_at, s.timezone).here}` : "";
      toast.success(`Made ${s.name}${next}`);
      await navigate({ to: "/ui/schedules/$scheduleId", params: { scheduleId: s.id } });
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const body = scheduleRequest(draft);
        if (typeof body === "string") {
          setProblem(body);
          return;
        }
        setProblem(null);
        create.mutate(body);
      }}
    >
      <ScheduleForm
        teamId={teamId}
        draft={draft}
        set={(patch) => setDraft((d) => ({ ...d, ...patch }))}
      />
      <SaveBar status="It starts as soon as it is made. Pause it any time." problem={problem}>
        <Button
          type="button"
          variant="ghost"
          onClick={() => void navigate({ to: "/ui/schedules" })}
        >
          Cancel
        </Button>
        <Button type="submit" loading={create.isPending}>
          Make schedule
        </Button>
      </SaveBar>
      <LeaveGuard dirty={() => unsaved.current} what="this schedule" />
    </form>
  );
}
