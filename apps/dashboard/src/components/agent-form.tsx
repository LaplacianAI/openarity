import { useQueries, useQuery } from "@tanstack/react-query";
import { ChevronDown } from "lucide-react";
import { Popover } from "radix-ui";
import { type ReactNode, useId, useState } from "react";

import {
  getAgent,
  getMCPServer,
  getSkill,
  getTeamSettings,
  listAgents,
  listLoops,
  listMCPServers,
  listMCPTools,
  listSkills,
} from "@/api";
import type { Delegation, MCPServerGrant } from "@/api/types";
import { type Picked, Picker, type SearchResult } from "@/components/picker";
import { Field, TextArea } from "@/components/registry-bits";
import { Input } from "@/components/ui/input";
import { SwitchField } from "@/components/ui/switch";
import type { Draft } from "@/lib/agent-draft";

/** Names for the ids an agent's grants carry, from the team's own first pages. */
function useNames(teamId: string) {
  const servers = useQuery({
    queryKey: ["servers", teamId, "names"],
    queryFn: () => listMCPServers(teamId),
  });
  const skills = useQuery({
    queryKey: ["skills", teamId, "names"],
    queryFn: () => listSkills(teamId),
  });
  const agents = useQuery({
    queryKey: ["agents", teamId, "names"],
    queryFn: () => listAgents(teamId),
  });
  const loops = useQuery({
    queryKey: ["loops", teamId],
    queryFn: () => listLoops(teamId),
  });
  return {
    servers: servers.data?.items ?? [],
    skills: skills.data?.items ?? [],
    agents: agents.data?.items ?? [],
    loops: (loops.data?.items ?? []).map((l) => l.name),
    // Settled either way: until then an id missing from a list is only not
    // read yet, and asking for it by itself would be a read wasted.
    settled: {
      servers: !servers.isPending,
      skills: !skills.isPending,
      agents: !agents.isPending,
    },
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
 * read one by one once the team's first page has settled without them. A
 * team's first page is known; a grant past it, in a team of thousands, is not.
 */
function useNamed(
  ids: string[],
  known: Map<string, string>,
  settled: boolean,
  key: readonly unknown[],
  read: (id: string) => Promise<{ name: string }>,
): Picked[] {
  const missing = ids.filter((id) => !known.has(id));
  const read_ = useQueries({
    queries: missing.map((id) => ({
      queryKey: [...key, id, "name"],
      queryFn: () => read(id),
      enabled: settled,
      staleTime: Number.POSITIVE_INFINITY,
    })),
  });
  const fetched = new Map(missing.map((id, i) => [id, read_[i]?.data?.name]));
  return ids.map((id) => ({ id, name: known.get(id) ?? fetched.get(id) ?? id }));
}

/**
 * One part of the agent: its name and what it covers on the left from 1024px,
 * the fields on the right. Parts are divided by a rule, not boxed.
 */
function Section({
  title,
  about,
  children,
}: {
  title: string;
  about: ReactNode;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section
      aria-labelledby={id}
      className="grid gap-x-8 gap-y-3 border-t border-rule py-6 first:border-t-0 first:pt-0 lg:grid-cols-[13rem_minmax(0,1fr)]"
    >
      <div className="space-y-1">
        <h2 id={id} className="text-base font-medium">
          {title}
        </h2>
        <p className="text-xs text-muted-foreground">{about}</p>
      </div>
      <div className="min-w-0 max-w-2xl space-y-4">{children}</div>
    </section>
  );
}

/**
 * Every part of an agent a person may set, in the order they decide them.
 * The draft is the caller's: the form only changes it, so the same form
 * defines a new agent and edits one.
 */
export function AgentForm({
  teamId,
  draft,
  set,
  self,
}: {
  teamId: string;
  draft: Draft;
  set: (patch: Partial<Draft>) => void;
  /** The agent being edited, which is never offered as its own delegate. */
  self?: string;
}) {
  const names = useNames(teamId);
  const settings = useQuery({
    queryKey: ["settings", teamId],
    queryFn: () => getTeamSettings(teamId),
  });
  // Suggested, not imposed: the team's own model first, then those its agents
  // run on. A name the gateway knows that nobody uses yet can still be typed.
  const teamModel = settings.data?.model?.name ?? null;
  const models = [
    ...new Set([teamModel, ...names.agents.map((a) => a.model?.name)].filter(Boolean)),
  ] as string[];
  const modelsId = useId();
  // Names learned from a pick, so a chip says what it is before any read.
  const [learned, setLearned] = useState<Map<string, string>>(new Map());
  const learn = (picked: Picked[]) =>
    setLearned((m) => new Map([...m, ...picked.map((p): [string, string] => [p.id, p.name])]));
  const known = (list: { id: string; name: string }[]) =>
    new Map([...list.map((i): [string, string] => [i.id, i.name]), ...learned]);

  const servers = useNamed(
    draft.servers.map((g) => g.id),
    known(names.servers),
    names.settled.servers,
    ["mcp-servers", teamId],
    (id) => getMCPServer(teamId, id),
  );
  const skills = useNamed(
    draft.skills,
    known(names.skills),
    names.settled.skills,
    ["skills", teamId],
    (id) => getSkill(teamId, id),
  );
  const delegates = useNamed(
    draft.delegates,
    known(names.agents),
    names.settled.agents,
    ["agents", teamId],
    (id) => getAgent(teamId, id),
  );
  // A pattern the brain no longer lists is still the agent's: shown, not changed.
  const patterns =
    draft.pattern === "" || names.loops.includes(draft.pattern)
      ? names.loops
      : [draft.pattern, ...names.loops];

  return (
    <div>
      <Section
        title="Identity"
        about="What it is called, how other agents find it, and what it is told."
      >
        <Field label="Name" htmlFor="agent-name">
          <Input
            id="agent-name"
            value={draft.name}
            autoComplete="off"
            onChange={(e) => set({ name: e.target.value })}
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
            className="min-h-48"
            onChange={(e) => set({ instructions: e.target.value })}
          />
        </Field>
      </Section>

      <Section
        title="Model and loop"
        about="The model it runs on, and how it works through a task."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field
            label="Model"
            htmlFor="agent-model"
            hint={
              teamModel
                ? `The team's model is ${teamModel}. The models its agents use are suggested.`
                : "As the model gateway names it. The models the team's agents use are suggested."
            }
          >
            <Input
              id="agent-model"
              list={modelsId}
              value={draft.model}
              placeholder={teamModel ?? undefined}
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => set({ model: e.target.value })}
            />
            <datalist id={modelsId}>
              {models.map((m) => (
                <option key={m} value={m} />
              ))}
            </datalist>
          </Field>
          <Field label="Pattern" htmlFor="agent-pattern" hint="How it works through a task.">
            <select
              id="agent-pattern"
              value={draft.pattern}
              onChange={(e) => set({ pattern: e.target.value })}
              className="h-9 w-full rounded-lg border border-input bg-transparent px-2 text-base outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:opacity-50 md:text-sm"
            >
              {patterns.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </Field>
        </div>
      </Section>

      <Section
        title="Tools and skills"
        about="The MCP servers and skills it may use. A server is granted every tool unless limited to some."
      >
        <div>
          <p className="mb-1.5 text-sm font-medium">MCP servers</p>
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
          <p className="mb-1.5 text-sm font-medium">Skills</p>
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
      </Section>

      <Section
        title="Delegation"
        about="Whether it hands parts of a task to other agents, and to which."
      >
        <Delegates
          teamId={teamId}
          draft={draft}
          set={set}
          chosen={delegates}
          learn={learn}
          self={self}
        />
      </Section>

      <Section title="The web" about="Searching the web and reading pages, granted together.">
        <SwitchField
          label="Can search the web and read pages"
          description="It is given the web search and page reading tools. The hub has them either way."
          checked={draft.web}
          onCheckedChange={(web) => set({ web })}
        />
      </Section>

      <Section
        title="Limits"
        about="How long one answer may be, and how many steps and tokens one run may take."
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Max tokens" htmlFor="agent-tokens" hint="The longest answer, per step.">
            <Input
              id="agent-tokens"
              inputMode="numeric"
              value={draft.maxTokens}
              onChange={(e) => set({ maxTokens: e.target.value })}
            />
          </Field>
          <Field label="Max steps" htmlFor="agent-steps" hint="Steps before it must answer.">
            <Input
              id="agent-steps"
              inputMode="numeric"
              value={draft.maxSteps}
              onChange={(e) => set({ maxSteps: e.target.value })}
            />
          </Field>
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
      </Section>
    </div>
  );
}

const DELEGATION: [Delegation, string, string][] = [
  ["none", "Works alone", "It is not given the delegate tool."],
  ["team", "Any agent in the team", "It finds the right one through the graph."],
  ["listed", "Only these agents", "It may hand work only to the agents chosen below."],
];

/**
 * Whom the agent may hand work to. A list stays in the draft while another
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
  const name = useId();
  return (
    <>
      <fieldset className="space-y-2">
        <legend className="sr-only">Hands work to</legend>
        {DELEGATION.map(([value, word, said]) => (
          <label key={value} className="flex cursor-pointer items-start gap-2.5">
            <input
              type="radio"
              name={name}
              value={value}
              checked={draft.delegation === value}
              onChange={() => set({ delegation: value })}
              className="mt-0.5 size-4 accent-primary"
            />
            <span>
              <span className="block text-sm">{word}</span>
              <span className="block text-xs text-muted-foreground">{said}</span>
            </span>
          </label>
        ))}
      </fieldset>
      {draft.delegation === "listed" ? (
        <div className="pl-6.5">
          <p className="mb-1.5 text-sm font-medium">Agents it may hand work to</p>
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
    </>
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
          className="inline-flex items-center gap-0.5 rounded px-1 text-muted-foreground hover:bg-secondary hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring disabled:pointer-events-none"
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
          className="z-50 max-h-72 w-64 overflow-y-auto origin-(--radix-popover-content-transform-origin) rounded-lg bg-popover p-2 text-xs text-popover-foreground shadow-lg ring-1 ring-sheet-edge duration-(--dur-pop) ease-out data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-96"
        >
          <label className="flex items-center gap-1.5 py-0.5 font-medium">
            <input
              type="checkbox"
              checked={!only}
              onChange={() => onChange({ id: grant.id })}
              className="accent-primary"
            />
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
                  className="accent-primary"
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
