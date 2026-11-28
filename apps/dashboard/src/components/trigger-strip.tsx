/**
 * The line over a schedule's or a watcher's own conversation: what made it,
 * when it fires next, and the two things one does from here, pause and run
 * now. Opening the conversation marks what it did as seen.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CalendarClock, Eye, MoreHorizontal } from "lucide-react";
import { useEffect } from "react";
import { toast } from "sonner";

import {
  checkWatcherNow,
  getSchedule,
  getWatcher,
  markScheduledNewsSeen,
  runSchedule,
  updateSchedule,
  updateWatcher,
} from "@/api";
import { ApiError } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import type { Schedule, Session, Watcher } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useMediaQuery } from "@/lib/media";
import { timeBoth } from "@/lib/when";

export function TriggerStrip({ teamId, session }: { teamId: string; session: Session }) {
  const scheduleId = session.schedule_id;
  const watcherId = session.watcher_id;
  const kind = scheduleId ? "schedule" : watcherId ? "watcher" : null;
  const id = scheduleId ?? watcherId ?? "";
  const qc = useQueryClient();
  const perms = usePermissions(teamId);
  const canWrite = !perms.isPending && perms.can("schedule:write");
  const wide = useMediaQuery("(min-width: 48rem)");

  const item = useQuery<Schedule | Watcher>({
    queryKey: [kind === "schedule" ? "schedules" : "watchers", teamId, id],
    queryFn: ({ signal }) =>
      kind === "schedule" ? getSchedule(teamId, id, signal) : getWatcher(teamId, id, signal),
    enabled: kind !== null,
  });

  // What it did is seen once its conversation is open.
  const seen = useMutation({
    mutationFn: () =>
      markScheduledNewsSeen(teamId, kind === "schedule" ? { schedule_id: id } : { watcher_id: id }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ["scheduled-news", teamId] }),
  });
  const mark = seen.mutate;
  useEffect(() => {
    if (kind && item.isSuccess) mark();
  }, [kind, item.isSuccess, mark]);

  const toggle = useMutation<Schedule | Watcher, Error, "active" | "paused">({
    mutationFn: (status) =>
      kind === "schedule"
        ? updateSchedule(teamId, id, { status })
        : updateWatcher(teamId, id, { status }),
    onSuccess: (next) => {
      qc.setQueryData([kind === "schedule" ? "schedules" : "watchers", teamId, id], next);
      void qc.invalidateQueries({
        queryKey: [kind === "schedule" ? "schedules" : "watchers", teamId],
      });
      toast.success(next.status === "paused" ? `Paused ${next.name}` : `Resumed ${next.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });
  const run = useMutation({
    mutationFn: async () => {
      if (kind === "schedule") await runSchedule(teamId, id);
      else await checkWatcherNow(teamId, id);
    },
    onSuccess: () =>
      toast.success(kind === "schedule" ? "Running: it answers here" : "Looking now"),
    onError: (err: Error) => toast.error(err.message),
  });

  if (!kind) return null;
  const to =
    kind === "schedule" ? "/ui/schedules/$scheduleId" : "/ui/schedules/watchers/$watcherId";
  const params = kind === "schedule" ? { scheduleId: id } : { watcherId: id };
  const Icon = kind === "schedule" ? CalendarClock : Eye;
  const word = kind === "schedule" ? "Schedule" : "Watcher";

  // Another's, or deleted: say where it came from, and offer nothing.
  if (item.isError) {
    const gone =
      item.error instanceof ApiError && (item.error.status === 404 || item.error.status === 403);
    return (
      <p
        data-trigger-strip
        className="flex items-center gap-2 rounded-md bg-surface px-3 py-1.5 text-sm text-muted-foreground"
      >
        <Icon aria-hidden className="size-4 shrink-0" />
        {gone ? `From a ${kind} you cannot open, or one that was deleted.` : `From a ${kind}.`}
      </p>
    );
  }
  const x = item.data;
  if (!x) return <div data-trigger-strip className="h-9 rounded-md bg-surface" aria-hidden />;
  const next =
    "next_fire_at" in x && x.next_fire_at
      ? `next ${timeBoth(x.next_fire_at, x.timezone).here}`
      : "next_check_at" in x && x.next_check_at
        ? `looks again ${timeBoth(x.next_check_at, "UTC").here}`
        : x.status;
  const paused = x.status === "paused";
  const canToggle = canWrite && (x.status === "active" || paused);
  const canRun =
    canWrite &&
    x.status === "active" &&
    !(kind === "watcher" && "source" in x && x.source === "webhook");

  return (
    <div
      data-trigger-strip
      className="flex min-h-9 items-center gap-2 rounded-md bg-surface px-3 py-1 text-sm"
    >
      <Icon aria-hidden className="size-4 shrink-0 text-muted-foreground" />
      <p className="min-w-0 flex-1 truncate">
        <span className="text-muted-foreground">{word} · </span>
        <Link
          to={to}
          params={params as never}
          className="font-medium underline-offset-4 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
        >
          {x.name}
        </Link>
        <span className="text-muted-foreground"> · {next}</span>
      </p>
      {canToggle || canRun ? (
        wide ? (
          <div className="flex shrink-0 items-center gap-1">
            {canToggle ? (
              <Button
                size="sm"
                variant="ghost"
                loading={toggle.isPending}
                onClick={() => toggle.mutate(paused ? "active" : "paused")}
              >
                {paused ? "Resume" : "Pause"}
              </Button>
            ) : null}
            {canRun ? (
              <Button
                size="sm"
                variant="ghost"
                loading={run.isPending}
                onClick={() => run.mutate()}
              >
                {kind === "schedule" ? "Run now" : "Look now"}
              </Button>
            ) : null}
          </div>
        ) : (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button size="icon" variant="ghost" aria-label={`What to do with ${x.name}`}>
                <MoreHorizontal aria-hidden className="size-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              {canToggle ? (
                <DropdownMenuItem onSelect={() => toggle.mutate(paused ? "active" : "paused")}>
                  {paused ? "Resume" : "Pause"}
                </DropdownMenuItem>
              ) : null}
              {canRun ? (
                <DropdownMenuItem onSelect={() => run.mutate()}>
                  {kind === "schedule" ? "Run now" : "Look now"}
                </DropdownMenuItem>
              ) : null}
            </DropdownMenuContent>
          </DropdownMenu>
        )
      ) : null}
    </div>
  );
}
