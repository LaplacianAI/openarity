import {
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { ChevronDown } from "lucide-react";
import { Popover } from "radix-ui";
import { useState } from "react";
import { toast } from "sonner";

import {
  createAgent,
  deleteAgent,
  getAgent,
  getMCPServer,
  getSkill,
  listAgents,
  listLoops,
  listMCPServers,
  listMCPTools,
  listSkills,
  replaceAgent,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Agent, AgentRequest, AgentSummary, Delegation, MCPServerGrant } from "@/api/types";
import { type Picked, Picker, type SearchResult } from "@/components/picker";
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
  const loops = useQuery({
    queryKey: ["loops", teamId],
    queryFn: () => listLoops(teamId as string),
    enabled: Boolean(teamId),
  });
  return {
    servers: servers.data?.items ?? [],
    skills: skills.data?.items ?? [],
    agents: agents.data?.items ?? [],
    loops: (loops.data?.items ?? []).map((l) => l.name),
  };
}

/** How many matches a picker shows at once; the rest are a search away. */
const PICK_PAGE = 20;

function searchResult(page: { items: Picked[]; next_cursor?: string | null }): SearchResult {
  return {
    items: page.items.map((i) => ({ id: i.id, name: i.name })),
    more: Boolean(page.next_cursor),
  };
}

/**
 * Names for the ids an agent holds: from what is already known, and the rest
 * read one by one. A team's first page is known; a grant past it, in a team
 * of thousands, is not.
 */
function useNamed(
  ids: string[],
  known: Map<string, string>,
  key: readonly unknown[],
  read: (id: string) => Promise<{ name: string }>,
): Picked[] {
  const missing = ids.filter((id) => !known.has(id));
  const read_ = useQueries({
    queries: missing.map((id) => ({
      queryKey: [...key, id, "name"],
      queryFn: () => read(id),
      staleTime: Number.POSITIVE_INFINITY,
    })),
  });
  const fetched = new Map(missing.map((id, i) => [id, read_[i]?.data?.name]));
  return ids.map((id) => ({ id, name: known.get(id) ?? fetched.get(id) ?? id }));
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
  servers: MCPServerGrant[];
  skills: string[];
  delegation: Delegation;
  /** Kept while another choice is shown, and sent only with listed. */
  delegates: string[];
  web: boolean;
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
  web: false,
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
    mcp_servers: d.servers.map((g) => (g.allow ? { id: g.id, allow: g.allow } : { id: g.id })),
    skills: d.skills,
    ...(budget === undefined ? {} : { token_budget: budget }),
    delegation: d.delegation,
    ...(d.delegation === "listed" ? { delegates: d.delegates } : {}),
    web: d.web,
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
    servers: a.mcp_servers,
    skills: a.skills,
    delegation: a.delegation,
    delegates: a.delegates,
    web: a.web === true,
  };
}

/** What the brain holds beyond the form; the PUT is whole, so they go back as they came. */
const UNSHOWN = ["steer_continuations", "output_schema", "parser", "parser_model"] as const;

/**
 * The PUT for an edit: the agent as it is, with the form laid over it. The
 * form carries each server's tool limit, so a grant limited to some tools
 * stays limited unless the form changed it.
 */
export function replaceRequest(a: Agent, d: Draft): AgentRequest | string {
  const form = agentRequest(d);
  if (typeof form === "string") return form;
  const kept: Partial<AgentRequest> = {};
  for (const k of UNSHOWN) {
    if (a[k] !== undefined && a[k] !== null) (kept as Record<string, unknown>)[k] = a[k];
  }
  const body: AgentRequest = {
    ...form,
    ...kept,
    model: { ...a.model, ...form.model },
  };
  return body;
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
  // Names learned from a pick, so a chip says what it is before any read.
  const [learned, setLearned] = useState<Map<string, string>>(new Map());
  const learn = (picked: Picked[]) =>
    setLearned((m) => new Map([...m, ...picked.map((p): [string, string] => [p.id, p.name])]));
  const known = (list: { id: string; name: string }[]) =>
    new Map([...list.map((i): [string, string] => [i.id, i.name]), ...learned]);

  const servers = useNamed(
    draft.servers.map((g) => g.id),
    known(names.servers),
    ["mcp-servers", teamId],
    (id) => getMCPServer(teamId, id),
  );
  const skills = useNamed(draft.skills, known(names.skills), ["skills", teamId], (id) =>
    getSkill(teamId, id),
  );
  const delegates = useNamed(draft.delegates, known(names.agents), ["agents", teamId], (id) =>
    getAgent(teamId, id),
  );
  const patterns =
    draft.pattern === "" || names.loops.includes(draft.pattern)
      ? names.loops
      : [draft.pattern, ...names.loops];

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
        <Field label="Pattern" htmlFor="agent-pattern" hint="How the agent works through a task.">
          <select
            id="agent-pattern"
            value={draft.pattern}
            onChange={(e) => set({ pattern: e.target.value })}
            className="h-9 w-full rounded-md border border-input bg-transparent px-2 text-sm"
          >
            {patterns.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
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
          <Picker
            label="MCP servers"
            noun="server"
            value={servers}
            onChange={(next) => {
              learn(next);
              set({
                servers: next.map((p) => draft.servers.find((g) => g.id === p.id) ?? { id: p.id }),
              });
            }}
            search={(q) => listMCPServers(teamId, null, { q, limit: PICK_PAGE }).then(searchResult)}
            searchKey={["mcp-servers", teamId]}
            extra={(p) => (
              <ToolLimit
                teamId={teamId}
                server={p}
                grant={draft.servers.find((g) => g.id === p.id) ?? { id: p.id }}
                onChange={(g) =>
                  set({ servers: draft.servers.map((old) => (old.id === g.id ? g : old)) })
                }
              />
            )}
          />
        </div>
        <div>
          <legend className="mb-1 text-xs font-medium">Skills</legend>
          <Picker
            label="Skills"
            noun="skill"
            value={skills}
            onChange={(next) => {
              learn(next);
              set({ skills: next.map((p) => p.id) });
            }}
            search={(q) => listSkills(teamId, null, { q, limit: PICK_PAGE }).then(searchResult)}
            searchKey={["skills", teamId]}
          />
        </div>
      </fieldset>
      <fieldset>
        <legend className="mb-1 text-xs font-medium">Web</legend>
        <label className="flex items-center gap-1.5 text-xs">
          <input type="checkbox" checked={draft.web} onChange={() => set({ web: !draft.web })} />
          Can search the web and read pages — web_search and web_fetch
        </label>
      </fieldset>
      <Delegates
        teamId={teamId}
        draft={draft}
        set={set}
        chosen={delegates}
        learn={learn}
        self={self}
      />
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
  teamId,
  draft,
  set,
  chosen,
  learn,
  self,
}: {
  teamId: string;
  draft: Draft;
  set: (patch: Partial<Draft>) => void;
  chosen: Picked[];
  learn: (picked: Picked[]) => void;
  self?: string;
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
          <Picker
            label="Agents it may hand work to"
            noun="agent"
            value={chosen}
            onChange={(next) => {
              learn(next);
              set({ delegates: next.map((p) => p.id) });
            }}
            search={(q) => listAgents(teamId, null, { q, limit: PICK_PAGE }).then(searchResult)}
            searchKey={["agents", teamId]}
            exclude={self ? [self] : []}
          />
        </div>
      ) : null}
    </fieldset>
  );
}

/**
 * A granted server's tools: every one, or only those ticked. Unticking the
 * last goes back to every tool, since a grant of no tools grants nothing.
 */
function ToolLimit({
  teamId,
  server,
  grant,
  onChange,
}: {
  teamId: string;
  server: Picked;
  grant: MCPServerGrant;
  onChange: (g: MCPServerGrant) => void;
}) {
  const [open, setOpen] = useState(false);
  const tools = useQuery({
    queryKey: ["mcp-servers", teamId, server.id, "tools"],
    queryFn: () => listMCPTools(teamId, server.id),
    enabled: open,
  });
  const only = grant.allow;
  const pick = (name: string) => {
    const next = only?.includes(name) ? only.filter((t) => t !== name) : [...(only ?? []), name];
    onChange(next.length === 0 ? { id: grant.id } : { id: grant.id, allow: next });
  };

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={`Tools of ${server.name}`}
          className="inline-flex items-center gap-0.5 rounded px-1 text-muted-foreground hover:bg-secondary hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
        >
          <span className="max-w-40 truncate">
            {only ? `only ${only.join(", ")}` : "every tool"}
          </span>
          <ChevronDown aria-hidden className="size-3" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={4}
          aria-label={`Tools of ${server.name}`}
          className="z-50 max-h-72 w-64 overflow-y-auto rounded-md border border-border bg-popover p-2 text-xs text-popover-foreground shadow-md"
        >
          <label className="flex items-center gap-1.5 py-0.5 font-medium">
            <input type="checkbox" checked={!only} onChange={() => onChange({ id: grant.id })} />
            Every tool
          </label>
          {tools.isPending ? (
            <p className="py-1 text-muted-foreground">Loading tools…</p>
          ) : tools.isError ? (
            <p className="py-1 text-destructive">{tools.error.message}</p>
          ) : tools.data.items.length === 0 ? (
            <p className="py-1 text-muted-foreground">
              No tools discovered yet. Every tool it offers is granted.
            </p>
          ) : (
            tools.data.items.map((t) => (
              <label key={t.name} className="flex items-center gap-1.5 py-0.5">
                <input
                  type="checkbox"
                  checked={Boolean(only?.includes(t.name))}
                  onChange={() => pick(t.name)}
                />
                <span className="truncate font-mono">{t.name}</span>
              </label>
            ))
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
