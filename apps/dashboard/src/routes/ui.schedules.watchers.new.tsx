import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useRef, useState } from "react";
import { toast } from "sonner";

import { createWatcher } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { CreateWatcherRequest, Watcher } from "@/api/types";
import { ReadOnlyNote } from "@/components/registry-bits";
import { WatcherForm } from "@/components/schedule-form";
import { TriggerHeader } from "@/components/schedule-page";
import { SecretReveal } from "@/components/secret-reveal";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui-bits";
import { LeaveGuard, SaveBar } from "@/components/unsaved";
import { emptyWatcherDraft, type WatcherDraft, watcherRequest } from "@/lib/schedule-draft";

export const Route = createFileRoute("/ui/schedules/watchers/new")({
  component: NewWatcher,
});

const WRITE = "schedule:write";

function NewWatcher() {
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  return (
    <div className="space-y-6">
      <TriggerHeader
        crumb="Watchers"
        title="New watcher"
        line="It runs as you, when what it watches changes."
      />
      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to watch for changes." />
      ) : perms.isPending ? null : !perms.can(WRITE) ? (
        <ReadOnlyNote permission={WRITE} what="watchers" />
      ) : (
        <Maker teamId={teamId} />
      )}
    </div>
  );
}

function Maker({ teamId }: { teamId: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [draft, setDraft] = useState<WatcherDraft>(emptyWatcherDraft);
  const [problem, setProblem] = useState<string | null>(null);
  // A webhook watcher's secret, this once: held here, in no cache, and gone with the page.
  const [made, setMade] = useState<Watcher | null>(null);
  const unsaved = useRef(false);
  unsaved.current =
    made === null && (draft.name !== "" || draft.objective !== "" || draft.url !== "");

  const create = useMutation({
    mutationFn: (body: CreateWatcherRequest) => createWatcher(teamId, body),
    // The answer carries the secret: dropped from the mutation cache once the page goes.
    gcTime: 0,
    onSuccess: async (w) => {
      unsaved.current = false;
      const { secret: _shownOnce, ...kept } = w;
      qc.setQueryData(["watchers", teamId, w.id], kept);
      void qc.invalidateQueries({ queryKey: ["watchers", teamId, "list"] });
      toast.success(`Made ${w.name}`);
      if (w.secret) {
        setMade(w);
        return;
      }
      await navigate({ to: "/ui/schedules/watchers/$watcherId", params: { watcherId: w.id } });
    },
    onError: (err: Error) => setProblem(err.message),
  });

  if (made?.secret) {
    return (
      <div className="max-w-2xl space-y-4">
        <SecretReveal
          url={made.hook_url}
          secret={made.secret}
          onDone={() =>
            void navigate({
              to: "/ui/schedules/watchers/$watcherId",
              params: { watcherId: made.id },
            })
          }
        />
        <Link
          to="/ui/schedules/watchers/$watcherId"
          params={{ watcherId: made.id }}
          className="inline-flex items-center gap-1 text-sm text-primary underline-offset-4 hover:underline"
        >
          Open {made.name}
          <ArrowRight aria-hidden className="size-3" />
        </Link>
      </div>
    );
  }

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const body = watcherRequest(draft);
        if (typeof body === "string") {
          setProblem(body);
          return;
        }
        setProblem(null);
        create.mutate(body);
      }}
    >
      <WatcherForm
        teamId={teamId}
        draft={draft}
        set={(patch) => setDraft((d) => ({ ...d, ...patch }))}
      />
      <SaveBar
        status={
          draft.source === "webhook"
            ? "It shows its address and secret once it is made."
            : "Its first look is kept to compare against; a change after that runs it."
        }
        problem={problem}
      >
        <Button
          type="button"
          variant="ghost"
          onClick={() => void navigate({ to: "/ui/schedules", search: { view: "watchers" } })}
        >
          Cancel
        </Button>
        <Button type="submit" loading={create.isPending}>
          Make watcher
        </Button>
      </SaveBar>
      <LeaveGuard dirty={() => unsaved.current} what="this watcher" />
    </form>
  );
}
