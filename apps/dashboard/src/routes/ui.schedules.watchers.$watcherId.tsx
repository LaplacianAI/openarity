import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import {
  checkWatcherNow,
  confirmWatcher,
  deleteWatcher,
  getAgent,
  getWatcher,
  listWatcherChecks,
  rollWatcherSecret,
  takeOverWatcher,
  updateWatcher,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { UpdateWatcherRequest, Watcher } from "@/api/types";
import { CopyId, KeyValue, KeyValues, Time } from "@/components/facts";
import { WatcherForm } from "@/components/schedule-form";
import { AskStrip, DeleteTrigger, TriggerHeader } from "@/components/schedule-page";
import { SecretReveal } from "@/components/secret-reveal";
import { ErrorState } from "@/components/states";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { LoadMore } from "@/components/ui-bits";
import { SaveBar } from "@/components/unsaved";
import { type WatcherDraft, watcherDraftOf, watcherUpdate } from "@/lib/schedule-draft";
import { checkSays, statusWord, watcherFinding, watchesSays } from "@/lib/schedules";
import { lengthSays } from "@/lib/when";

export const Route = createFileRoute("/ui/schedules/watchers/$watcherId")({
  component: WatcherScreen,
});

const WRITE = "schedule:write";
const MANAGE = "schedule:manage";

function WatcherScreen() {
  const { watcherId } = Route.useParams();
  const { teamId } = useCurrentTeam();
  const watcher = useQuery({
    queryKey: ["watchers", teamId, watcherId],
    queryFn: ({ signal }) => getWatcher(teamId as string, watcherId, signal),
    enabled: Boolean(teamId),
  });
  if (!teamId || watcher.isPending) {
    return (
      <div className="space-y-4" role="status" aria-label="Loading the watcher">
        <Skeleton className="h-6 w-48" />
        <Skeleton className="h-4 w-80" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }
  if (watcher.isError) {
    return (
      <div className="space-y-4">
        <TriggerHeader crumb="Watchers" title="Watcher" />
        <ErrorState
          what="this watcher"
          error={watcher.error}
          onRetry={() => watcher.refetch()}
          retrying={watcher.isRefetching}
        />
      </div>
    );
  }
  return <Detail teamId={teamId} w={watcher.data} />;
}

function Detail({ teamId, w }: { teamId: string; w: Watcher }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const perms = usePermissions(teamId);
  const canWrite = !perms.isPending && perms.can(WRITE);
  const canManage = !perms.isPending && perms.can(MANAGE);
  const [editing, setEditing] = useState<WatcherDraft | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const agent = useQuery({
    queryKey: ["agents", teamId, w.agent_id],
    queryFn: () => getAgent(teamId, w.agent_id as string),
    enabled: Boolean(w.agent_id) && !w.hub,
    staleTime: 60_000,
  });

  // A secret is kept out of the query cache: an answer carrying one is stored without it.
  const saved = (next: Watcher, said: string) => {
    const { secret: _once, ...kept } = next;
    qc.setQueryData(["watchers", teamId, w.id], kept);
    void qc.invalidateQueries({ queryKey: ["watchers", teamId, "list"] });
    void qc.invalidateQueries({ queryKey: ["scheduled-news", teamId] });
    toast.success(said);
  };
  const fail = (err: Error) => toast.error(err.message);

  const patch = useMutation({
    mutationFn: (body: UpdateWatcherRequest) => updateWatcher(teamId, w.id, body),
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
    mutationFn: () => confirmWatcher(teamId, w.id),
    onSuccess: (next) => saved(next, `Confirmed ${next.name}`),
    onError: fail,
  });
  const look = useMutation({
    mutationFn: () => checkWatcherNow(teamId, w.id),
    onSuccess: (next) => {
      saved(next, "Looking now: a change runs it");
      void qc.invalidateQueries({ queryKey: ["watcher-checks", teamId, w.id] });
    },
    onError: fail,
  });
  // The secret this page was given, this once: in this mutation alone, dropped when the page goes.
  const secret = useMutation({
    mutationFn: () => rollWatcherSecret(teamId, w.id),
    gcTime: 0,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["watchers", teamId, w.id] });
      void qc.invalidateQueries({ queryKey: ["watchers", teamId, "list"] });
    },
    onError: fail,
  });
  const takeOver = useMutation({
    mutationFn: () => takeOverWatcher(teamId, w.id),
    gcTime: 0,
    onSuccess: (next) => saved(next, `${next.name} is yours; it stays paused until you resume it`),
    onError: fail,
  });
  const remove = useMutation({
    mutationFn: () => deleteWatcher(teamId, w.id),
    onSuccess: async () => {
      void qc.invalidateQueries({ queryKey: ["watchers", teamId, "list"] });
      toast.success(`Deleted ${w.name}`);
      await navigate({ to: "/ui/schedules", search: { view: "watchers" } });
    },
  });

  const hook = w.source === "webhook";
  const paused = w.status === "paused";
  const orphaned =
    paused && (w.paused_reason === "maker_gone" || w.paused_reason === "maker_cannot_run");
  const shown =
    secret.data ??
    (takeOver.data?.secret ? { secret: takeOver.data.secret, url: takeOver.data.hook_url } : null);

  const actions = canWrite ? (
    <>
      {w.status === "proposed" && !hook ? (
        <Button size="sm" loading={confirm.isPending} onClick={() => confirm.mutate()}>
          Confirm
        </Button>
      ) : null}
      {hook && (w.status === "waiting_secret" || w.status === "proposed") ? (
        <Button size="sm" loading={secret.isPending} onClick={() => secret.mutate()}>
          Generate secret
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
      {w.status === "active" ? (
        <>
          {hook ? null : (
            <Button
              size="sm"
              variant="outline"
              loading={look.isPending}
              onClick={() => look.mutate()}
            >
              Look now
            </Button>
          )}
          <Button
            size="sm"
            variant="outline"
            loading={patch.isPending}
            onClick={() => patch.mutate({ status: "paused" })}
          >
            Pause
          </Button>
          {hook ? (
            <Button
              size="sm"
              variant="ghost"
              loading={secret.isPending}
              onClick={() => secret.mutate()}
            >
              New secret
            </Button>
          ) : null}
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
      {editing ? null : (
        <Button
          size="sm"
          variant="ghost"
          onClick={() => setEditing(watcherDraftOf(w, agent.data?.name))}
        >
          Edit
        </Button>
      )}
      <DeleteTrigger
        name={w.name}
        kept={
          hook
            ? "Its address stops answering at once. Its conversation and runs are kept."
            : "It stops looking. Its conversation and runs are kept."
        }
        pending={remove.isPending}
        problem={remove.error?.message ?? null}
        onDelete={() => remove.mutate()}
      />
    </>
  ) : null;

  const finding = watcherFinding(w);
  return (
    <div className="space-y-6">
      <TriggerHeader
        crumb="Watchers"
        title={w.name}
        actions={actions}
        line={
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <StatusPill status={statusWord(w.status)} className="text-sm text-foreground" />
            <span aria-hidden>·</span>
            <span>watches {watchesSays(w)}</span>
          </span>
        }
      />
      {finding ? <p className="text-sm text-foreground">{finding}</p> : null}
      {shown ? (
        <div className="max-w-2xl">
          <SecretReveal
            url={shown.url ?? w.hook_url}
            secret={shown.secret}
            onDone={() => {
              secret.reset();
              takeOver.reset();
            }}
          />
        </div>
      ) : null}
      {w.waiting_change ? (
        <AskStrip
          says={w.waiting_change.says}
          since={w.waiting_change.since}
          canConfirm={canWrite}
          pending={confirm.isPending}
          onConfirm={() => confirm.mutate()}
        />
      ) : null}
      {!canWrite && !perms.isPending ? (
        <p className="text-xs text-muted-foreground">
          Changing a watcher needs {WRITE} in the team.
        </p>
      ) : null}

      {editing ? (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const body = watcherUpdate(w, editing);
            if (typeof body === "string") return setProblem(body);
            if (Object.keys(body).length === 0) return setEditing(null);
            patch.mutate(body);
          }}
        >
          <WatcherForm
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
            <p className="max-w-[75ch] text-sm whitespace-pre-wrap">{w.objective}</p>
            <KeyValues>
              {hook && w.hook_url ? (
                <KeyValue label="Address">
                  <CopyId value={w.hook_url} label="address" wrap />
                </KeyValue>
              ) : null}
              {w.url ? <KeyValue label="Looks at">{w.url}</KeyValue> : null}
              {w.selector ? (
                <KeyValue label="Only">
                  <span className="font-mono text-xs">{w.selector}</span>
                </KeyValue>
              ) : null}
              <KeyValue label="Cooldown">{lengthSays(w.cooldown_seconds)}</KeyValue>
              <KeyValue label="Runs">
                {w.hub || !w.agent_id ? "the hub" : (agent.data?.name ?? "an agent")}
              </KeyValue>
              <KeyValue label="Answers">
                {w.target.kind === "own"
                  ? "in its own conversation"
                  : (w.target.label ?? w.target.kind)}
                {" · "}
                <Link
                  to="/ui/sessions/$sessionId"
                  params={{ sessionId: w.session_id }}
                  className="inline-flex items-center gap-1 text-primary underline-offset-4 hover:underline"
                >
                  Open its conversation
                  <ArrowRight aria-hidden className="size-3" />
                </Link>
              </KeyValue>
              {w.next_check_at ? (
                <KeyValue label="Looks next">
                  <Time iso={w.next_check_at} />
                </KeyValue>
              ) : null}
            </KeyValues>
          </section>
          <Checks teamId={teamId} w={w} />
        </>
      )}
    </div>
  );
}

function Checks({ teamId, w }: { teamId: string; w: Watcher }) {
  const checks = useInfiniteQuery({
    queryKey: ["watcher-checks", teamId, w.id],
    queryFn: ({ pageParam, signal }) => listWatcherChecks(teamId, w.id, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = checks.data?.pages.flatMap((p) => p.items) ?? [];
  const hook = w.source === "webhook";
  return (
    <section aria-labelledby="checks" className="space-y-2">
      <h2 id="checks" className="text-base font-medium">
        {hook ? "Deliveries" : "Looks"}
      </h2>
      {checks.isPending ? (
        <Skeleton className="h-24 w-full" />
      ) : checks.isError ? (
        <ErrorState
          what="what it found"
          error={checks.error}
          onRetry={() => checks.refetch()}
          retrying={checks.isRefetching}
        />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          {hook ? "Nothing has called it yet." : "It has not looked yet."}
        </p>
      ) : (
        <>
          <ul className="divide-y divide-rule">
            {rows.map((c) => {
              const o = checkSays(c.outcome);
              return (
                <li
                  key={`${c.checked_at}-${c.outcome}`}
                  className="grid grid-cols-[auto_auto_1fr] items-center gap-x-3 gap-y-0.5 px-3 py-2 text-sm md:grid-cols-[9rem_7rem_minmax(0,1fr)_auto]"
                >
                  <Time iso={c.checked_at} />
                  <StatusPill status={o.look} />
                  <span className="col-span-3 row-start-2 min-w-0 text-muted-foreground md:col-span-1 md:col-start-3 md:row-start-1">
                    {o.says}
                    {c.detail ? <span className="block truncate text-xs">{c.detail}</span> : null}
                  </span>
                  {c.run_id ? (
                    <Link
                      to="/ui/sessions/$sessionId"
                      params={{ sessionId: w.session_id }}
                      search={{ tab: "runs", run: c.run_id }}
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
            hasMore={Boolean(checks.hasNextPage)}
            loading={checks.isFetchingNextPage}
            onClick={() => checks.fetchNextPage()}
            loadedLabel={`${rows.length} loaded`}
          />
        </>
      )}
    </section>
  );
}
