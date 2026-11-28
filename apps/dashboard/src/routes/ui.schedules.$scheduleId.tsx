import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import {
  confirmSchedule,
  deleteSchedule,
  getAgent,
  getSchedule,
  listScheduleFires,
  runSchedule,
  takeOverSchedule,
  updateSchedule,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Schedule, UpdateScheduleRequest } from "@/api/types";
import { KeyValue, KeyValues, Time } from "@/components/facts";
import { ScheduleForm } from "@/components/schedule-form";
import { AskStrip, DeleteTrigger, TriggerHeader } from "@/components/schedule-page";
import { ErrorState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LoadMore } from "@/components/ui-bits";
import { SaveBar } from "@/components/unsaved";
import { type ScheduleDraft, scheduleDraftOf, scheduleUpdate } from "@/lib/schedule-draft";
import { fireSays, scheduleFinding, statusWord } from "@/lib/schedules";
import { timeBoth, whenSays } from "@/lib/when";

export const Route = createFileRoute("/ui/schedules/$scheduleId")({
  component: ScheduleScreen,
});

const WRITE = "schedule:write";
const MANAGE = "schedule:manage";

function ScheduleScreen() {
  const { scheduleId } = Route.useParams();
  const { teamId } = useCurrentTeam();
  const schedule = useQuery({
    queryKey: ["schedules", teamId, scheduleId],
    queryFn: ({ signal }) => getSchedule(teamId as string, scheduleId, signal),
    enabled: Boolean(teamId),
  });

  if (!teamId || schedule.isPending) {
    return (
      <div className="space-y-4" role="status" aria-label="Loading the schedule">
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-4 w-80" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }
  if (schedule.isError) {
    return (
      <div className="space-y-4">
        <TriggerHeader crumb="Schedules" title="Schedule" />
        <ErrorState
          what="this schedule"
          error={schedule.error}
          onRetry={() => schedule.refetch()}
          retrying={schedule.isRefetching}
        />
      </div>
    );
  }
  return <Detail teamId={teamId} s={schedule.data} />;
}

function Detail({ teamId, s }: { teamId: string; s: Schedule }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const perms = usePermissions(teamId);
  const canWrite = !perms.isPending && perms.can(WRITE);
  const canManage = !perms.isPending && perms.can(MANAGE);
  const [editing, setEditing] = useState<ScheduleDraft | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const agent = useQuery({
    queryKey: ["agents", teamId, s.agent_id],
    queryFn: () => getAgent(teamId, s.agent_id as string),
    enabled: Boolean(s.agent_id) && !s.hub,
    staleTime: 60_000,
  });

  const saved = (next: Schedule, said: string) => {
    qc.setQueryData(["schedules", teamId, s.id], next);
    void qc.invalidateQueries({ queryKey: ["schedules", teamId, "list"] });
    void qc.invalidateQueries({ queryKey: ["scheduled-news", teamId] });
    toast.success(said);
  };
  const fail = (err: Error) => toast.error(err.message);

  const patch = useMutation({
    mutationFn: (body: UpdateScheduleRequest) => updateSchedule(teamId, s.id, body),
    onSuccess: (next, body) => {
      setEditing(null);
      setProblem(null);
      saved(
        next,
        body.status === "paused"
          ? `Paused ${next.name}`
          : body.status === "active"
            ? `Resumed ${next.name}`
            : `Saved ${next.name}`,
      );
    },
    onError: (err: Error, body) => (body.status ? fail(err) : setProblem(err.message)),
  });
  const confirm = useMutation({
    mutationFn: () => confirmSchedule(teamId, s.id),
    onSuccess: (next) => saved(next, `Confirmed ${next.name}`),
    onError: fail,
  });
  const takeOver = useMutation({
    mutationFn: () => takeOverSchedule(teamId, s.id),
    onSuccess: (next) => saved(next, `${next.name} is yours; it stays paused until you resume it`),
    onError: fail,
  });
  const run = useMutation({
    mutationFn: () => runSchedule(teamId, s.id),
    onSuccess: () => {
      toast.success("Running", {
        action: {
          label: "Open its conversation",
          onClick: () =>
            void navigate({ to: "/ui/sessions/$sessionId", params: { sessionId: s.session_id } }),
        },
      });
      void qc.invalidateQueries({ queryKey: ["schedule-fires", teamId, s.id] });
    },
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: () => deleteSchedule(teamId, s.id),
    onSuccess: async () => {
      void qc.invalidateQueries({ queryKey: ["schedules", teamId, "list"] });
      toast.success(`Deleted ${s.name}`);
      await navigate({ to: "/ui/schedules" });
    },
  });

  const proposed = s.status === "proposed";
  const paused = s.status === "paused";
  const orphaned =
    paused && (s.paused_reason === "maker_gone" || s.paused_reason === "maker_cannot_run");

  const actions = canWrite ? (
    <>
      {proposed ? (
        <Button size="sm" loading={confirm.isPending} onClick={() => confirm.mutate()}>
          Confirm
        </Button>
      ) : null}
      {paused ? (
        <Button
          size="sm"
          loading={patch.isPending}
          onClick={() => patch.mutate({ status: "active" })}
        >
          Resume
        </Button>
      ) : null}
      {s.status === "active" ? (
        <>
          <Button size="sm" variant="outline" loading={run.isPending} onClick={() => run.mutate()}>
            Run now
          </Button>
          <Button
            size="sm"
            variant="outline"
            loading={patch.isPending}
            onClick={() => patch.mutate({ status: "paused" })}
          >
            Pause
          </Button>
        </>
      ) : null}
      {orphaned && canManage ? (
        <Button
          size="sm"
          variant="outline"
          loading={takeOver.isPending}
          onClick={() => takeOver.mutate()}
        >
          Take over
        </Button>
      ) : null}
      {!editing && s.status !== "done" ? (
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setEditing(scheduleDraftOf(s, agent.data?.name))}
        >
          Edit
        </Button>
      ) : null}
      <DeleteTrigger
        name={s.name}
        kept="It stops firing. Its conversation and its runs are kept."
        pending={remove.isPending}
        problem={remove.error?.message ?? null}
        onDelete={() => remove.mutate()}
      />
    </>
  ) : null;

  const finding = scheduleFinding(s);
  return (
    <div className="space-y-6">
      <TriggerHeader
        crumb="Schedules"
        title={s.name}
        actions={actions}
        line={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <StatusPill status={statusWord(s.status)} className="text-sm text-foreground" />
            <span aria-hidden>·</span>
            <span>{whenSays(s.when, s.timezone)}</span>
            <span aria-hidden>·</span>
            <span>in {s.timezone}</span>
          </span>
        }
      />
      {finding ? <p className="text-sm text-foreground">{finding}</p> : null}
      {s.waiting_change ? (
        <AskStrip
          says={s.waiting_change.says}
          since={s.waiting_change.since}
          canConfirm={canWrite}
          pending={confirm.isPending}
          onConfirm={() => confirm.mutate()}
        />
      ) : null}
      {!canWrite && !perms.isPending ? (
        <p className="text-xs text-muted-foreground">
          Changing a schedule needs {WRITE} in the team.
        </p>
      ) : null}

      {editing ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const body = scheduleUpdate(s, editing);
            if (typeof body === "string") return setProblem(body);
            if (Object.keys(body).length === 0) return setEditing(null);
            patch.mutate(body);
          }}
        >
          <ScheduleForm
            teamId={teamId}
            draft={editing}
            set={(p) => setEditing((d) => (d ? { ...d, ...p } : d))}
            editing
          />
          <SaveBar status="Only what you changed is sent." problem={problem}>
            <Button
              type="button"
              variant="ghost"
              onClick={() => {
                setEditing(null);
                setProblem(null);
              }}
            >
              Cancel
            </Button>
            <Button type="submit" loading={patch.isPending}>
              Save
            </Button>
          </SaveBar>
        </form>
      ) : (
        <>
          <section aria-label="What it does" className="space-y-4">
            <p className="max-w-[75ch] text-sm whitespace-pre-wrap">{s.objective}</p>
            <KeyValues>
              <KeyValue label="Runs">
                {s.hub || !s.agent_id ? "the hub" : (agent.data?.name ?? "an agent")}
              </KeyValue>
              <KeyValue label="Answers">
                {s.target.kind === "own"
                  ? "in its own conversation"
                  : (s.target.label ?? s.target.kind)}
                {" · "}
                <Link
                  to="/ui/sessions/$sessionId"
                  params={{ sessionId: s.session_id }}
                  className="inline-flex items-center gap-1 text-primary underline-offset-4 hover:underline"
                >
                  Open its conversation
                  <ArrowRight aria-hidden className="size-3" />
                </Link>
              </KeyValue>
              {s.upcoming.length > 0 ? (
                <KeyValue label="Next">
                  {s.upcoming.map((t, i) => {
                    const { here, there } = timeBoth(t, s.timezone);
                    return (
                      <span key={t} title={there ?? undefined}>
                        {i > 0 ? ", " : ""}
                        {here}
                      </span>
                    );
                  })}
                </KeyValue>
              ) : null}
              {s.last_fired_at ? (
                <KeyValue label="Last">
                  {s.last_outcome ? `${fireSays(s.last_outcome).says}, ` : ""}
                  <Time iso={s.last_fired_at} />
                </KeyValue>
              ) : null}
            </KeyValues>
          </section>
          <Fires teamId={teamId} s={s} />
        </>
      )}
    </div>
  );
}

function Fires({ teamId, s }: { teamId: string; s: Schedule }) {
  const fires = useInfiniteQuery({
    queryKey: ["schedule-fires", teamId, s.id],
    queryFn: ({ pageParam, signal }) => listScheduleFires(teamId, s.id, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = fires.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section aria-labelledby="fires" className="space-y-2">
      <h2 id="fires" className="text-base font-medium">
        Fires
      </h2>
      {fires.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : fires.isError ? (
        <ErrorState
          what="its fires"
          error={fires.error}
          onRetry={() => fires.refetch()}
          retrying={fires.isRefetching}
        />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">It has not fired yet.</p>
      ) : (
        <>
          <ul className="divide-y divide-rule">
            {rows.map((f) => {
              const o = fireSays(f.outcome);
              const { here, there } = timeBoth(f.scheduled_for, s.timezone);
              const late = Math.round(
                (Date.parse(f.fired_at) - Date.parse(f.scheduled_for)) / 60_000,
              );
              return (
                <li
                  key={`${f.scheduled_for}-${f.fired_at}`}
                  className="grid grid-cols-[auto_auto_1fr] items-center gap-x-3 gap-y-0.5 px-3 py-2 text-sm md:grid-cols-[9rem_7rem_minmax(0,1fr)_auto]"
                >
                  <span className="tabular-nums" title={there ?? undefined}>
                    {here}
                  </span>
                  <StatusPill status={o.look} />
                  <span className="col-span-3 row-start-2 min-w-0 text-muted-foreground md:col-span-1 md:col-start-3 md:row-start-1">
                    {o.says}
                    {late >= 2 ? `, ${late} min after it was due` : ""}
                  </span>
                  {f.run_id ? (
                    <Link
                      to="/ui/sessions/$sessionId"
                      params={{ sessionId: s.session_id }}
                      search={{ tab: "runs", run: f.run_id }}
                      className="justify-self-end text-xs text-primary underline-offset-4 hover:underline md:col-start-4 md:row-start-1"
                    >
                      Its run
                    </Link>
                  ) : null}
                </li>
              );
            })}
          </ul>
          <LoadMore
            hasMore={Boolean(fires.hasNextPage)}
            loading={fires.isFetchingNextPage}
            onClick={() => fires.fetchNextPage()}
            loadedLabel={`${rows.length} fire${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </section>
  );
}
