import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import {
  createAgent,
  deleteAgent,
  getAgent,
  listAgents,
  listMCPServers,
  listSkills,
  replaceAgent,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Agent, AgentRequest, AgentSummary, Delegation, MCPServerGrant } from "@/api/types";
import { Field, ReadOnlyNote, Tag, TextArea } from "@/components/registry-bits";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { relativeTime } from "@/lib/format";

export const Route = createFileRoute("/ui/agents/")({
  component: Agents,
});

const WRITE = "agent:write";

/** Names for the ids an agent's grants carry, from the team's own lists. */
function useNames(teamId: string | null) {
  const servers = useQuery({
    queryKey: ["servers", teamId, "names"],
    queryFn: () => listMCPServers(teamId as string),
    enabled: Boolean(teamId),
  });
  const skills = useQuery({
    queryKey: ["skills", teamId, "names"],
    queryFn: () => listSkills(teamId as string),
    enabled: Boolean(teamId),
  });
  const agents = useQuery({
    queryKey: ["agents", teamId, "names"],
    queryFn: () => listAgents(teamId as string),
    enabled: Boolean(teamId),
  });
  return {
    servers: servers.data?.items ?? [],
    skills: skills.data?.items ?? [],
    agents: agents.data?.items ?? [],
  };
}

function Agents() {
  const qc = useQueryClient();
  const { teamId } = useCurrentTeam();
  const perms = usePermissions(teamId);
  const canWrite = perms.can(WRITE);
  const [open, setOpen] = useState<string | null>(null);
  const [adding, setAdding] = useState(false);

  const agents = useInfiniteQuery({
    queryKey: ["agents", teamId, "paged"],
    queryFn: ({ pageParam }) => listAgents(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const changed = () => qc.invalidateQueries({ queryKey: ["agents", teamId] });
  const rows = agents.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Agents"
        description="A model, a way of working through a task, instructions, and the MCP servers and skills it is granted."
        actions={
          canWrite ? (
            <Button size="sm" onClick={() => setAdding(true)}>
              Define agent
            </Button>
          ) : null
        }
      />

      {teamId && !perms.isPending && !canWrite ? (
        <ReadOnlyNote permission={WRITE} what="agents" />
      ) : null}

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its agents." />
      ) : agents.isPending ? (
        <p className="text-sm text-muted-foreground">Loading agents…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No agents yet"
          body="Define one with a model and a pattern, then grant it the servers and skills it may use."
          hint="oa agents create <team> -f agent.yaml"
        />
      ) : (
        <>
          <ul className="divide-y divide-border overflow-hidden rounded-md border border-border bg-card">
            {rows.map((a) => (
              <AgentRow
                key={a.id}
                agent={a}
                teamId={teamId}
                canWrite={canWrite}
                open={open === a.id}
                onToggle={() => setOpen(open === a.id ? null : a.id)}
                onChanged={changed}
              />
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(agents.hasNextPage)}
            loading={agents.isFetchingNextPage}
            onClick={() => agents.fetchNextPage()}
            loadedLabel={`${rows.length} agent${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}

      {teamId && canWrite ? (
        <DefineDialog
          teamId={teamId}
          open={adding}
          onClose={() => setAdding(false)}
          onCreated={(a) => {
            changed();
            setOpen(a.id);
          }}
        />
      ) : null}
    </div>
  );
}

function AgentRow({
  agent,
  teamId,
  canWrite,
  open,
  onToggle,
  onChanged,
}: {
  agent: AgentSummary;
  teamId: string;
  canWrite: boolean;
  open: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState(false);
  const remove = useMutation({
    mutationFn: () => deleteAgent(teamId, agent.id),
    onSuccess: () => {
      onChanged();
      toast.success(`Deleted ${agent.name}`);
    },
    onError: (err: Error) => toast.error(err.message),
  });

  return (
    <li>
      <div className="flex items-center gap-3 px-3 py-2 hover:bg-secondary/60">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="min-w-0 flex-1 text-left"
        >
          <div className="flex items-center gap-2">
            <span className="text-sm font-medium">{agent.name}</span>
            <Tag>{agent.pattern}</Tag>
            {agent.kind !== "custom" ? <Tag>{agent.kind}</Tag> : null}
          </div>
          <p className="truncate text-xs text-muted-foreground">
            {agent.description || agent.model.name}
          </p>
        </button>
        <Mono className="hidden sm:inline">{agent.model.name}</Mono>
        <span className="hidden text-xs text-muted-foreground md:inline">
          {relativeTime(agent.updated_at)}
        </span>
        {canWrite ? (
          <>
            <Button variant="ghost" size="sm" className="text-xs" onClick={() => setEditing(true)}>
              Edit
            </Button>
            <Button
              variant="ghost"
              size="sm"
              className="text-xs"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              Delete
            </Button>
          </>
        ) : null}
      </div>
      {open ? <AgentDetail teamId={teamId} agentId={agent.id} /> : null}
      {canWrite && editing ? (
        <EditDialog
          teamId={teamId}
          agentId={agent.id}
          onClose={() => setEditing(false)}
          onSaved={onChanged}
        />
      ) : null}
    </li>
  );
}

function AgentDetail({ teamId, agentId }: { teamId: string; agentId: string }) {
  const names = useNames(teamId);
  const detail = useQuery({
    queryKey: ["agents", teamId, agentId],
    queryFn: () => getAgent(teamId, agentId),
  });

  if (detail.isPending) return <p className="px-3 pb-3 text-xs text-muted-foreground">Loading…</p>;
  if (detail.isError)
    return <p className="px-3 pb-3 text-xs text-destructive">{detail.error.message}</p>;

  const a: Agent = detail.data;
  const serverName = (id: string) => names.servers.find((s) => s.id === id)?.name ?? id;
  const skillName = (id: string) => names.skills.find((s) => s.id === id)?.name ?? id;

  return (
    <div className="grid gap-4 border-t border-border bg-surface px-3 py-3 text-xs md:grid-cols-[minmax(0,1fr)_16rem]">
      <div className="min-w-0 space-y-2">
        <p className="text-muted-foreground">
          {a.model.name}, up to {a.model.max_tokens} tokens · {a.pattern}, up to {a.max_steps} steps
          ·{" "}
          {a.token_budget === undefined
            ? "the default budget"
            : `${a.token_budget.toLocaleString("en")} tokens a run`}
        </p>
        <pre className="max-h-64 overflow-auto rounded-md border border-border bg-card p-3 whitespace-pre-wrap">
          {a.instructions || "No instructions."}
        </pre>
      </div>
      <div className="space-y-3">
        <div>
          <h3 className="mb-1 font-medium">MCP servers</h3>
          {a.mcp_servers.length === 0 ? (
            <p className="text-muted-foreground">None granted.</p>
          ) : (
            <ul className="space-y-0.5">
              {a.mcp_servers.map((g) => (
                <li key={g.id}>
                  <span className="font-medium">{serverName(g.id)}</span>{" "}
                  <span className="text-muted-foreground">
                    {g.allow ? g.allow.join(", ") : "every tool"}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </div>
        <div>
          <h3 className="mb-1 font-medium">Skills</h3>
          {a.skills.length === 0 ? (
            <p className="text-muted-foreground">None granted.</p>
          ) : (
            <ul className="space-y-0.5">
              {a.skills.map((id) => (
                <li key={id}>{skillName(id)}</li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

type Draft = {
  name: string;
  description: string;
  instructions: string;
  model: string;
  maxTokens: string;
  pattern: string;
  maxSteps: string;
  tokenBudget: string;
  servers: string[];
  skills: string[];
  delegation: Delegation;
  /** Kept while another choice is shown, and sent only with listed. */
  delegates: string[];
};

/** The brain refuses more; this only says so before the round trip. */
const MAX_TOKEN_BUDGET = 100_000_000;

const EMPTY: Draft = {
  name: "",
  description: "",
  instructions: "",
  model: "",
  maxTokens: "1024",
  pattern: "react",
  maxSteps: "5",
  tokenBudget: "",
  servers: [],
  skills: [],
  delegation: "none",
  delegates: [],
};

/**
 * The form as the API takes it. Only what cannot be sent is refused here;
 * which patterns exist and what a model name means are the brain's to say.
 */
export function agentRequest(d: Draft): AgentRequest | string {
  if (!d.name.trim()) return "An agent needs a name.";
  if (!d.model.trim()) return "An agent needs a model, as the gateway names it.";
  const maxTokens = Number(d.maxTokens);
  const maxSteps = Number(d.maxSteps);
  if (!Number.isInteger(maxTokens) || maxTokens < 1)
    return "Max tokens is a whole number above zero.";
  if (!Number.isInteger(maxSteps) || maxSteps < 1) return "Max steps is a whole number above zero.";
  const budget = d.tokenBudget.trim() === "" ? undefined : Number(d.tokenBudget);
  if (
    budget !== undefined &&
    (!Number.isInteger(budget) || budget < 1 || budget > MAX_TOKEN_BUDGET)
  )
    return "Token budget is a whole number from 1 to 100,000,000, or empty for the default.";

  return {
    name: d.name.trim(),
    description: d.description.trim(),
    instructions: d.instructions,
    model: { name: d.model.trim(), max_tokens: maxTokens },
    pattern: d.pattern.trim(),
    max_steps: maxSteps,
    mcp_servers: d.servers.map((id) => ({ id })),
    skills: d.skills,
    ...(budget === undefined ? {} : { token_budget: budget }),
    delegation: d.delegation,
    ...(d.delegation === "listed" ? { delegates: d.delegates } : {}),
  };
}

/** The agent as the form shows it. */
export function draftOf(a: Agent): Draft {
  return {
    name: a.name,
    description: a.description,
    instructions: a.instructions,
    model: a.model.name,
    maxTokens: String(a.model.max_tokens),
    pattern: a.pattern,
    maxSteps: String(a.max_steps),
    tokenBudget: a.token_budget === undefined ? "" : String(a.token_budget),
    servers: a.mcp_servers.map((g) => g.id),
    skills: a.skills,
    delegation: a.delegation,
    delegates: a.delegates,
  };
}

/** What the brain holds beyond the form; the PUT is whole, so they go back as they came. */
const UNSHOWN = ["steer_continuations", "output_schema", "parser", "parser_model"] as const;

/**
 * The PUT for an edit: the agent as it is, with the form laid over it. A
 * server still ticked keeps the grant it had, so a grant limited to some
 * tools stays limited; only a newly ticked server is granted whole.
 */
export function replaceRequest(a: Agent, d: Draft): AgentRequest | string {
  const form = agentRequest(d);
  if (typeof form === "string") return form;
  const kept: Partial<AgentRequest> = {};
  for (const k of UNSHOWN) {
    if (a[k] !== undefined && a[k] !== null) (kept as Record<string, unknown>)[k] = a[k];
  }
  const grant = (id: string): MCPServerGrant => {
    const old = a.mcp_servers.find((g) => g.id === id);
    return old?.allow ? { id, allow: old.allow } : { id };
  };
  const body: AgentRequest = {
    ...form,
    ...kept,
    model: { ...a.model, ...form.model },
    mcp_servers: d.servers.map(grant),
  };
  return body;
}

function toggle(list: string[], id: string): string[] {
  return list.includes(id) ? list.filter((x) => x !== id) : [...list, id];
}

function DefineDialog({
  teamId,
  open,
  onClose,
  onCreated,
}: {
  teamId: string;
  open: boolean;
  onClose: () => void;
  onCreated: (a: Agent) => void;
}) {
  const [problem, setProblem] = useState<string | null>(null);
  const [draft, setDraft] = useState<Draft>(EMPTY);

  const create = useMutation({
    mutationFn: (body: AgentRequest) => createAgent(teamId, body),
    onSuccess: (a) => {
      setDraft(EMPTY);
      onCreated(a);
      onClose();
      toast.success(`Defined ${a.name}`);
    },
    onError: (err: Error) => setProblem(err.message),
  });

  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Define an agent</DialogTitle>
          <DialogDescription>
            Written with its grants in one step. A server granted here is granted every tool; the
            CLI can grant some.
          </DialogDescription>
        </DialogHeader>
        <AgentForm
          teamId={teamId}
          draft={draft}
          set={(patch) => setDraft((d) => ({ ...d, ...patch }))}
          toRequest={agentRequest}
          submit="Define"
          submitting="Defining…"
          pending={create.isPending}
          problem={problem}
          setProblem={setProblem}
          onSubmit={(body) => create.mutate(body)}
          onCancel={onClose}
        />
      </DialogContent>
    </Dialog>
  );
}

function EditDialog({
  teamId,
  agentId,
  onClose,
  onSaved,
}: {
  teamId: string;
  agentId: string;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [problem, setProblem] = useState<string | null>(null);
  // The whole agent, fetched afresh: the PUT replaces it, so the form must
  // start from what the brain holds now, not from a list row or a cache.
  const detail = useQuery({
    queryKey: ["agents", teamId, agentId],
    queryFn: () => getAgent(teamId, agentId),
  });

  const save = useMutation({
    mutationFn: (body: AgentRequest) => replaceAgent(teamId, agentId, body),
    onSuccess: (a) => {
      onSaved();
      onClose();
      toast.success(`Saved ${a.name}`);
    },
    onError: (err: Error) => setProblem(err.message),
  });

  const agent = detail.isFetchedAfterMount ? detail.data : undefined;
  // Untouched, the form is the agent as fetched.
  const [edited, setEdited] = useState<Draft | null>(null);

  return (
    <Dialog open onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Edit agent</DialogTitle>
          <DialogDescription>
            Saved whole: a server or skill left unticked is revoked. A server already limited to
            some tools keeps that limit; a newly ticked one is granted every tool.
          </DialogDescription>
        </DialogHeader>
        {detail.isError ? (
          <p className="text-xs text-destructive">{detail.error.message}</p>
        ) : !agent ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : (
          <AgentForm
            teamId={teamId}
            draft={edited ?? draftOf(agent)}
            set={(patch) => setEdited((d) => ({ ...(d ?? draftOf(agent)), ...patch }))}
            limits={agent.mcp_servers}
            self={agent.id}
            toRequest={(d) => replaceRequest(agent, d)}
            submit="Save"
            submitting="Saving…"
            pending={save.isPending}
            problem={problem}
            setProblem={setProblem}
            onSubmit={(body) => save.mutate(body)}
            onCancel={onClose}
          />
        )}
      </DialogContent>
    </Dialog>
  );
}

function AgentForm({
  teamId,
  draft,
  set,
  limits,
  self,
  toRequest,
  submit,
  submitting,
  pending,
  problem,
  setProblem,
  onSubmit,
  onCancel,
}: {
  teamId: string;
  draft: Draft;
  set: (patch: Partial<Draft>) => void;
  /** The grants the agent has now, to say which are limited to some tools. */
  limits?: MCPServerGrant[];
  /** The agent being edited, which is never offered as its own delegate. */
  self?: string;
  toRequest: (d: Draft) => AgentRequest | string;
  submit: string;
  submitting: string;
  pending: boolean;
  problem: string | null;
  setProblem: (p: string | null) => void;
  onSubmit: (body: AgentRequest) => void;
  onCancel: () => void;
}) {
  const names = useNames(teamId);

  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        const body = toRequest(draft);
        if (typeof body === "string") {
          setProblem(body);
          return;
        }
        setProblem(null);
        onSubmit(body);
      }}
    >
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Name" htmlFor="agent-name">
          <Input
            id="agent-name"
            value={draft.name}
            onChange={(e) => set({ name: e.target.value })}
          />
        </Field>
        <Field label="Model" htmlFor="agent-model" hint="As the model gateway names it.">
          <Input
            id="agent-model"
            value={draft.model}
            placeholder="anthropic/claude-sonnet-5-5"
            onChange={(e) => set({ model: e.target.value })}
          />
        </Field>
        <Field
          label="Pattern"
          htmlFor="agent-pattern"
          hint="react, plan, rewoo, reflection, code or custom."
        >
          <Input
            id="agent-pattern"
            value={draft.pattern}
            onChange={(e) => set({ pattern: e.target.value })}
          />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Max tokens" htmlFor="agent-tokens">
            <Input
              id="agent-tokens"
              inputMode="numeric"
              value={draft.maxTokens}
              onChange={(e) => set({ maxTokens: e.target.value })}
            />
          </Field>
          <Field label="Max steps" htmlFor="agent-steps">
            <Input
              id="agent-steps"
              inputMode="numeric"
              value={draft.maxSteps}
              onChange={(e) => set({ maxSteps: e.target.value })}
            />
          </Field>
        </div>
      </div>
      <Field
        label="Token budget"
        htmlFor="agent-budget"
        hint="What one run may spend, input and output together. Empty for the default, 1,000,000."
      >
        <Input
          id="agent-budget"
          inputMode="numeric"
          value={draft.tokenBudget}
          placeholder="1000000"
          onChange={(e) => set({ tokenBudget: e.target.value })}
        />
      </Field>
      <Field
        label="Description"
        htmlFor="agent-description"
        hint="Other agents find this one by its description. Say what it does and when to use it."
      >
        <Input
          id="agent-description"
          value={draft.description}
          onChange={(e) => set({ description: e.target.value })}
        />
      </Field>
      <Field label="Instructions" htmlFor="agent-instructions">
        <TextArea
          id="agent-instructions"
          value={draft.instructions}
          onChange={(e) => set({ instructions: e.target.value })}
        />
      </Field>
      <fieldset className="grid gap-3 sm:grid-cols-2">
        <div>
          <legend className="mb-1 text-xs font-medium">MCP servers</legend>
          {names.servers.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">The team has none.</p>
          ) : (
            names.servers.map((s) => {
              const only = limits?.find((g) => g.id === s.id)?.allow;
              return (
                <label key={s.id} className="flex items-center gap-1.5 text-xs">
                  <input
                    type="checkbox"
                    checked={draft.servers.includes(s.id)}
                    onChange={() => set({ servers: toggle(draft.servers, s.id) })}
                  />
                  {s.name}
                  {only ? (
                    <span className="text-muted-foreground">only {only.join(", ")}</span>
                  ) : null}
                </label>
              );
            })
          )}
        </div>
        <div>
          <legend className="mb-1 text-xs font-medium">Skills</legend>
          {names.skills.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">The team has none.</p>
          ) : (
            names.skills.map((s) => (
              <label key={s.id} className="flex items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  checked={draft.skills.includes(s.id)}
                  onChange={() => set({ skills: toggle(draft.skills, s.id) })}
                />
                {s.name}
              </label>
            ))
          )}
        </div>
      </fieldset>
      <Delegates draft={draft} set={set} agents={names.agents.filter((a) => a.id !== self)} />
      {problem ? (
        <p role="alert" className="text-xs text-destructive">
          {problem}
        </p>
      ) : null}
      <DialogFooter>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? submitting : submit}
        </Button>
      </DialogFooter>
    </form>
  );
}

/**
 * Whom the agent may hand work to with the delegate tool. Ticked, it works
 * alone and is not given the tool. A list stays in the draft while another
 * choice is shown, so changing one's mind does not lose it, but only listed
 * sends it.
 */
function Delegates({
  draft,
  set,
  agents,
}: {
  draft: Draft;
  set: (patch: Partial<Draft>) => void;
  agents: AgentSummary[];
}) {
  const alone = draft.delegation === "none";
  return (
    <fieldset className="space-y-2">
      <legend className="mb-1 text-xs font-medium">Delegation</legend>
      <label className="flex items-center gap-1.5 text-xs">
        <input
          type="checkbox"
          checked={alone}
          onChange={() => set({ delegation: alone ? "team" : "none" })}
        />
        Works alone — never hands work to other agents
      </label>
      {alone ? null : (
        <Field label="Hands work to" htmlFor="agent-delegation">
          <select
            id="agent-delegation"
            value={draft.delegation}
            onChange={(e) => set({ delegation: e.target.value as Delegation })}
            className="h-8 w-full rounded-md border border-input bg-transparent px-2 text-sm"
          >
            <option value="team">Any agent in the team</option>
            <option value="listed">Only these agents</option>
          </select>
        </Field>
      )}
      {draft.delegation === "listed" ? (
        <div>
          <p className="mb-1 text-xs font-medium">Agents it may hand work to</p>
          {agents.length === 0 ? (
            <p className="text-[11px] text-muted-foreground">The team has no other agents.</p>
          ) : (
            agents.map((a) => (
              <label key={a.id} className="flex items-center gap-1.5 text-xs">
                <input
                  type="checkbox"
                  checked={draft.delegates.includes(a.id)}
                  onChange={() => set({ delegates: toggle(draft.delegates, a.id) })}
                />
                {a.name}
              </label>
            ))
          )}
        </div>
      ) : null}
    </fieldset>
  );
}
