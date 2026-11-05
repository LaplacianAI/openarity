import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { useRef, useState } from "react";
import { toast } from "sonner";

import { createAgent } from "@/api";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { AgentRequest } from "@/api/types";
import { AgentForm } from "@/components/agent-form";
import { ReadOnlyNote } from "@/components/registry-bits";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui-bits";
import { LeaveGuard, SaveBar } from "@/components/unsaved";
import { agentRequest, type Draft, EMPTY, sameDraft } from "@/lib/agent-draft";

export const Route = createFileRoute("/ui/agents/new")({
  component: NewAgent,
});

const WRITE = "agent:write";

function NewAgent() {
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);

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
            <li aria-current="page" className="text-foreground">
              New
            </li>
          </ol>
        </nav>
        <h1 className="text-xl font-semibold tracking-tight">Define an agent</h1>
        <p className="text-sm text-muted-foreground">
          It works alone and off the web until it is told otherwise.
        </p>
      </header>

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to define its agents." />
      ) : perms.isPending ? null : !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="agents" />
      ) : (
        <Definer teamId={teamId} />
      )}
    </div>
  );
}

function Definer({ teamId }: { teamId: string }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [problem, setProblem] = useState<string | null>(null);
  const unsaved = useRef(false);
  unsaved.current = !sameDraft(draft, EMPTY);

  const create = useMutation({
    mutationFn: (body: AgentRequest) => createAgent(teamId, body),
    onSuccess: async (a) => {
      unsaved.current = false;
      qc.setQueryData(["agents", teamId, a.id], a);
      void qc.invalidateQueries({ queryKey: ["agents", teamId] });
      void qc.invalidateQueries({ queryKey: ["summary", teamId] });
      toast.success(`Defined ${a.name}`);
      await navigate({ to: "/ui/agents/$agentId", params: { agentId: a.id } });
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        const body = agentRequest(draft);
        if (typeof body === "string") {
          setProblem(body);
          return;
        }
        setProblem(null);
        create.mutate(body);
      }}
    >
      <AgentForm
        teamId={teamId}
        draft={draft}
        set={(patch) => setDraft((d) => ({ ...d, ...patch }))}
      />
      <SaveBar
        status="Defined with its grants in one step. Every part can be changed later."
        problem={problem}
      >
        <Button type="button" variant="ghost" onClick={() => void navigate({ to: "/ui/agents" })}>
          Cancel
        </Button>
        <Button type="submit" loading={create.isPending}>
          Define agent
        </Button>
      </SaveBar>
      <LeaveGuard dirty={() => unsaved.current} what="this agent" />
    </form>
  );
}
