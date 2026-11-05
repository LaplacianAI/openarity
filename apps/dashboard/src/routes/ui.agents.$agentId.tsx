import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useRef, useState } from "react";
import { toast } from "sonner";

import { deleteAgent, getAgent, replaceAgent } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Agent, AgentRequest } from "@/api/types";
import { AgentForm } from "@/components/agent-form";
import { Time } from "@/components/facts";
import { ReadOnlyNote, Tag } from "@/components/registry-bits";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui-bits";
import { LeaveGuard, SaveBar } from "@/components/unsaved";
import { type Draft, draftOf, replaceRequest, sameDraft } from "@/lib/agent-draft";

export const Route = createFileRoute("/ui/agents/$agentId")({
  component: AgentPage,
});

const WRITE = "agent:write";

function AgentPage() {
  const { agentId } = Route.useParams();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);

  // The whole agent, read afresh: the PUT replaces it, so the editor starts
  // from what the brain holds now, never from a list row or a cache.
  const detail = useQuery({
    queryKey: ["agents", teamId, agentId],
    queryFn: () => getAgent(teamId as string, agentId),
    enabled: Boolean(teamId),
  });
  const agent = detail.isFetchedAfterMount ? detail.data : undefined;

  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <nav aria-label="Breadcrumb">
          <ol className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
            <li>
              <Link
                to="/ui/agents"
                className="rounded underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
              >
                Agents
              </Link>
            </li>
            <li aria-hidden>
              <ChevronRight className="size-3" />
            </li>
            <li aria-current="page" className="min-w-0 truncate text-foreground">
              {agent?.name ?? "Agent"}
            </li>
          </ol>
        </nav>
        {agent ? (
          <>
            <h1 className="truncate text-xl font-semibold tracking-tight">{agent.name}</h1>
            <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
              {agent.kind !== "custom" ? (
                <>
                  <Tag>{agent.kind}</Tag>
                  <span aria-hidden>·</span>
                </>
              ) : null}
              <span>
                updated <Time iso={agent.updated_at} />
              </span>
            </p>
          </>
        ) : detail.isError ? null : teamId ? (
          <Skeleton className="h-7 w-48" />
        ) : null}
      </header>

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its agents." />
      ) : detail.isError ? (
        <ErrorState
          what="this agent"
          error={detail.error}
          onRetry={() => detail.refetch()}
          retrying={detail.isRefetching}
        />
      ) : !agent ? (
        <div className="space-y-3" role="status" aria-label="Loading the agent">
          <Skeleton className="h-9 w-full max-w-2xl" />
          <Skeleton className="h-9 w-full max-w-2xl" />
          <Skeleton className="h-48 w-full max-w-2xl" />
        </div>
      ) : (
        <>
          {!perms.isPending && !canWrite ? <ReadOnlyNote permission={WRITE} what="agents" /> : null}
          <Editor
            // A save that comes back starts the editor again from what was saved.
            key={agent.updated_at}
            teamId={teamId}
            agent={agent}
            canWrite={canWrite}
          />
        </>
      )}
    </div>
  );
}

function Editor({ teamId, agent, canWrite }: { teamId: string; agent: Agent; canWrite: boolean }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [edited, setEdited] = useState<Draft | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const draft = edited ?? draftOf(agent);
  const dirty = edited !== null && !sameDraft(edited, draftOf(agent));
  // Read by the guard when a move happens, so leaving after a save or a
  // delete is never asked about.
  const unsaved = useRef(false);
  unsaved.current = dirty;

  const save = useMutation({
    mutationFn: (body: AgentRequest) => replaceAgent(teamId, agent.id, body),
    onSuccess: (a) => {
      unsaved.current = false;
      qc.setQueryData(["agents", teamId, agent.id], a);
      void qc.invalidateQueries({ queryKey: ["agents", teamId] });
      void qc.invalidateQueries({ queryKey: ["summary", teamId] });
      setEdited(null);
      setProblem(null);
      toast.success(`Saved ${a.name}`);
    },
    onError: (err: Error) => setProblem(err.message),
  });

  const remove = useMutation({
    mutationFn: () => deleteAgent(teamId, agent.id),
    onSuccess: async () => {
      unsaved.current = false;
      qc.removeQueries({ queryKey: ["agents", teamId, agent.id] });
      void qc.invalidateQueries({ queryKey: ["agents", teamId] });
      void qc.invalidateQueries({ queryKey: ["summary", teamId] });
      toast.success(`Deleted ${agent.name}`);
      await navigate({ to: "/ui/agents" });
    },
  });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const body = replaceRequest(agent, draft);
        if (typeof body === "string") {
          setProblem(body);
          return;
        }
        setProblem(null);
        save.mutate(body);
      }}
    >
      {/* Without agent:write every control is shown as it is set, and none moves. */}
      <fieldset disabled={!canWrite} className="min-w-0">
        <legend className="sr-only">{agent.name}</legend>
        <AgentForm
          teamId={teamId}
          draft={draft}
          set={(patch) => setEdited((d) => ({ ...(d ?? draftOf(agent)), ...patch }))}
          self={agent.id}
        />
      </fieldset>

      {canWrite ? (
        <DeleteAgent
          name={agent.name}
          pending={remove.isPending}
          problem={remove.error?.message ?? null}
          onDelete={() => remove.mutate()}
          onCancel={() => remove.reset()}
        />
      ) : null}

      {canWrite && dirty ? (
        <SaveBar status="Unsaved changes. Saving replaces the whole agent." problem={problem}>
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              setEdited(null);
              setProblem(null);
            }}
          >
            Discard
          </Button>
          <Button type="submit" loading={save.isPending}>
            Save
          </Button>
        </SaveBar>
      ) : null}

      <LeaveGuard dirty={() => unsaved.current} what={agent.name} />
    </form>
  );
}

function DeleteAgent({
  name,
  pending,
  problem,
  onDelete,
  onCancel,
}: {
  name: string;
  pending: boolean;
  problem: string | null;
  onDelete: () => void;
  onCancel: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <section
      aria-labelledby="delete-agent"
      className="mt-2 flex flex-wrap items-center justify-between gap-3 border-t border-rule pt-6"
    >
      <div className="max-w-xl space-y-1">
        <h2 id="delete-agent" className="text-base font-medium">
          Delete this agent
        </h2>
        <p className="text-xs text-muted-foreground">
          Agents that list it lose it from their lists. If it answers as the team's hub, the
          built-in orchestrator answers instead.
        </p>
      </div>
      <Button
        type="button"
        variant="outline"
        className="text-destructive"
        onClick={() => setOpen(true)}
      >
        Delete agent
      </Button>
      <Dialog
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) onCancel();
        }}
      >
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Delete {name}?</DialogTitle>
            <DialogDescription>
              It cannot be restored. Its past runs stay where they are.
            </DialogDescription>
          </DialogHeader>
          {problem ? (
            <p role="alert" className="text-sm text-destructive">
              {problem}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Keep it
            </Button>
            <Button type="button" variant="destructive" loading={pending} onClick={onDelete}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
