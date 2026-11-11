import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowRight, Bot, FolderPlus, Pencil, Server, X } from "lucide-react";
import { type ChangeEvent, useRef, useState } from "react";
import { toast } from "sonner";

import {
  type Applied,
  applyBlueprint,
  getAgent,
  getMCPServer,
  getSkill,
  listAgents,
  listMCPServers,
  listTeamProposals,
  planBlueprint,
} from "@/api";
import { useCurrentTeam } from "@/api/session";
import type { Agent, Blueprint, BlueprintPlan, MCPServer } from "@/api/types";
import { AgentForm } from "@/components/agent-form";
import { AppliedView, PlanView } from "@/components/blueprint-plan";
import { Picker, type SearchResult } from "@/components/picker";
import { ServerFields } from "@/components/server-fields";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import { LeaveGuard, SaveBar } from "@/components/unsaved";
import { agentRequest, type Draft, draftOf, EMPTY, replaceRequest } from "@/lib/agent-draft";
import { folderFrom, KIND_WORDS, tally } from "@/lib/blueprint";
import {
  blueprintOf,
  type Drafted,
  draftedOf,
  draftId,
  type Item,
  put,
  skillName,
} from "@/lib/compose";
import { relativeTime } from "@/lib/format";
import {
  EMPTY_SERVER,
  editRequest,
  type ServerDraft,
  draftOf as serverDraftOf,
  serverRequest,
} from "@/lib/server-draft";

export const Route = createFileRoute("/ui/blueprint")({
  component: BlueprintPage,
});

const ABOUT =
  "Blueprint gathers new and changed MCP servers, skills and agents into one draft. Nothing is written while you build it. Plan shows what applying it would change, and Apply makes exactly that change, all at once. It is a blueprint, the same thing oa plan and oa apply take from a file.";

function BlueprintPage() {
  const { teamId } = useCurrentTeam();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Blueprint"
        finding="Draft servers, skills and agents together, see the plan, then apply it at once."
        description={ABOUT}
      />
      {teamId ? (
        <>
          <BuilderProposals teamId={teamId} />
          <Composer teamId={teamId} />
        </>
      ) : (
        <EmptyState title="No team" body="Join or create a team to compose changes to it." />
      )}
    </div>
  );
}

/**
 * What the team's builder has proposed and nobody has applied yet. Each is
 * read and applied in its conversation, where its plan is made for the
 * reader; here it is only found.
 */
function BuilderProposals({ teamId }: { teamId: string }) {
  const proposals = useQuery({
    queryKey: ["proposals", teamId, "open"],
    queryFn: () => listTeamProposals(teamId),
  });
  const open = (proposals.data?.items ?? []).filter((p) => p.state === "open");
  if (proposals.isPending || proposals.isError || open.length === 0) return null;
  return (
    <section aria-labelledby="proposed-heading" className="space-y-2">
      <h2 id="proposed-heading" className="text-sm font-medium">
        Proposed by the builder
      </h2>
      <ul className="divide-y divide-rule rounded-lg border border-border">
        {open.map((p) => (
          <li key={p.id}>
            <Link
              to="/ui/build"
              search={{ session: p.session_id }}
              className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2 text-sm hover:bg-muted"
            >
              <span className="min-w-0 flex-1">{p.summary}</span>
              <span className="text-xs text-muted-foreground">{relativeTime(p.created_at)}</span>
              <span className="inline-flex items-center gap-1 text-xs underline underline-offset-4">
                Review and apply
                <ArrowRight aria-hidden className="size-3" />
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** A plan, and the exact request it was made from: Apply sends that, never the draft as it is now. */
type Shown = { plan: BlueprintPlan; sent: Blueprint; note?: string };

/** The form open over the page: which kind, and the item or team's own it starts from. */
type Editing =
  | { kind: "agent"; item?: Item & { kind: "agent" }; existing?: Agent }
  | { kind: "mcp_server"; item?: Item & { kind: "mcp_server" }; existing?: MCPServer };

function Composer({ teamId }: { teamId: string }) {
  const qc = useQueryClient();
  const [items, setItems] = useState<Item[]>([]);
  const [shown, setShown] = useState<Shown | null>(null);
  const [applied, setApplied] = useState<Extract<Applied, { kind: "applied" }> | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [choosing, setChoosing] = useState(false);
  const folderInput = useRef<HTMLInputElement>(null);
  const dirty = useRef(false);
  dirty.current = items.length > 0;

  // Any change to the draft makes the shown plan someone else's: it goes, so
  // Apply never sends a plan for a draft that is no longer on the screen.
  const change = (next: (all: Item[]) => Item[]) => {
    setItems(next);
    setShown(null);
    setApplied(null);
    setProblem(null);
  };

  const nameOf = (kind: keyof Drafted, id: string): Promise<string> => {
    const read: (team: string, id: string) => Promise<{ name: string }> = {
      servers: getMCPServer,
      skills: getSkill,
      agents: getAgent,
    }[kind];
    return qc
      .fetchQuery<{ name: string }>({
        queryKey: [kind === "servers" ? "mcp-servers" : kind, teamId, id, "name"],
        queryFn: () => read(teamId, id),
        staleTime: Number.POSITIVE_INFINITY,
      })
      .then((x) => x.name);
  };

  const plan = useMutation({
    mutationFn: async () => {
      const sent = await blueprintOf(items, nameOf);
      return { plan: await planBlueprint(teamId, sent), sent };
    },
    onMutate: () => setProblem(null),
    onSuccess: (s) => setShown(s),
    onError: (err: Error) => setProblem(err.message),
  });

  const apply = useMutation({
    mutationFn: (s: Shown) => applyBlueprint(teamId, s.sent, s.plan.digest),
    onSuccess: (out: Applied, s) => {
      if (out.kind === "applied") {
        const t = tally(out.changes);
        setItems([]);
        setShown(null);
        setApplied(out);
        for (const key of ["agents", "skills", "servers", "mcp-servers", "summary", "graph"]) {
          void qc.invalidateQueries({ queryKey: [key, teamId] });
        }
        toast.success(`Applied: ${t.create} created, ${t.update} changed`);
        return;
      }
      setShown({
        plan: out.plan,
        sent: s.sent,
        note:
          out.kind === "changed"
            ? "The team changed since that plan, so nothing was applied. This is what applying would do now."
            : "Nothing was applied: the plan has refusals.",
      });
    },
    onError: (err: Error) => setProblem(err.message),
  });

  const onFolder = async (e: ChangeEvent<HTMLInputElement>) => {
    const folder = e.target.files ? folderFrom(e.target.files) : null;
    e.target.value = "";
    if (!folder) return;
    const name = await skillName(folder);
    change((all) => put(all, { kind: "skill", name, folder }));
  };

  const edit = async (kind: "agent" | "mcp_server", id: string) => {
    setChoosing(false);
    try {
      if (kind === "agent") setEditing({ kind, existing: await getAgent(teamId, id) });
      else setEditing({ kind, existing: await getMCPServer(teamId, id) });
    } catch (err) {
      setProblem((err as Error).message);
    }
  };

  const t = shown ? tally(shown.plan.changes) : null;
  const refused = (shown?.plan.refusals.length ?? 0) > 0;
  const canApply = Boolean(shown && t && !refused && t.create + t.update > 0);

  return (
    <div className="space-y-6">
      <section aria-labelledby="draft-heading" className="space-y-3">
        <h2 id="draft-heading" className="text-sm font-medium">
          Draft
        </h2>
        {items.length === 0 ? (
          <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
            Nothing drafted yet. Add a server, a skill or an agent; nothing is written until Apply.
          </p>
        ) : (
          <ul className="divide-y divide-rule rounded-lg border border-border">
            {items.map((item) => (
              <li
                key={`${item.kind}/${item.name}`}
                className="flex items-center gap-3 px-3 py-2 text-sm"
              >
                <span className="w-24 shrink-0 text-muted-foreground">{KIND_WORDS[item.kind]}</span>
                <span className="min-w-0 flex-1 truncate font-medium">{item.name}</span>
                <span className="hidden text-xs text-muted-foreground sm:inline">{said(item)}</span>
                {item.kind === "skill" ? null : (
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`Edit ${item.name}`}
                    onClick={() =>
                      setEditing(
                        item.kind === "agent"
                          ? { kind: "agent", item, existing: item.existing }
                          : { kind: "mcp_server", item, existing: item.existing },
                      )
                    }
                  >
                    <Pencil aria-hidden />
                  </Button>
                )}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  aria-label={`Remove ${item.name}`}
                  onClick={() => change((all) => all.filter((x) => x !== item))}
                >
                  <X aria-hidden />
                </Button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={() => setEditing({ kind: "mcp_server" })}>
            <Server aria-hidden />
            MCP server
          </Button>
          <Button variant="outline" size="sm" onClick={() => folderInput.current?.click()}>
            <FolderPlus aria-hidden />
            Skill folder
          </Button>
          <Button variant="outline" size="sm" onClick={() => setEditing({ kind: "agent" })}>
            <Bot aria-hidden />
            Agent
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setChoosing(true)}>
            <Pencil aria-hidden />
            Change an existing one
          </Button>
          {/* webkitdirectory is not in React's types, and every browser supports it. */}
          <input
            ref={folderInput}
            type="file"
            className="hidden"
            aria-label="Skill folder"
            onChange={(e) => void onFolder(e)}
            {...({ webkitdirectory: "", directory: "" } as Record<string, string>)}
          />
        </div>
      </section>

      {shown ? <PlanView plan={shown.plan} note={shown.note} /> : null}
      {applied ? <AppliedView changes={applied.changes} discovered={applied.discovered} /> : null}

      <SaveBar
        status={
          applied
            ? "Applied. The draft is empty again."
            : t
              ? refused
                ? "Fix the refusals; nothing can be applied until then."
                : t.create + t.update === 0
                  ? "Nothing to change."
                  : `${t.create} to create, ${t.update} to change, ${t.same} unchanged.`
              : "Plan to see what applying the draft would change. Nothing is written until Apply."
        }
        problem={problem}
      >
        <Button
          variant={canApply ? "outline" : "default"}
          disabled={items.length === 0}
          loading={plan.isPending}
          onClick={() => plan.mutate()}
        >
          Plan
        </Button>
        {canApply && shown ? (
          <Button loading={apply.isPending} onClick={() => apply.mutate(shown)}>
            Apply
          </Button>
        ) : null}
      </SaveBar>

      {editing?.kind === "agent" ? (
        <AgentSheet
          teamId={teamId}
          editing={editing}
          drafted={draftedOf(items)}
          onClose={() => setEditing(null)}
          onAdd={(item) => {
            change((all) => put(all, item, editing.item));
            setEditing(null);
          }}
        />
      ) : null}
      {editing?.kind === "mcp_server" ? (
        <ServerSheet
          editing={editing}
          onClose={() => setEditing(null)}
          onAdd={(item) => {
            change((all) => put(all, item, editing.item));
            setEditing(null);
          }}
        />
      ) : null}
      <ChooseExisting
        teamId={teamId}
        open={choosing}
        onClose={() => setChoosing(false)}
        onPick={(kind, id) => void edit(kind, id)}
      />
      <LeaveGuard dirty={() => dirty.current} what="this draft" />
    </div>
  );
}

function said(item: Item): string {
  if (item.kind === "skill") {
    const n = item.folder.files.length;
    return `From a folder, ${n === 1 ? "1 file" : `${n} files`}`;
  }
  return item.existing ? "Changes the existing one" : "New";
}

/**
 * A blueprint finds an item by its name, so renaming the team's own here
 * would make a second one rather than rename the first. Renaming is done on
 * the item's own screen.
 */
function renamed(existing: { name: string } | undefined, name: string): string | null {
  return existing && existing.name !== name
    ? `A draft finds ${existing.name} by its name, so it cannot be renamed here. Rename it on its own screen.`
    : null;
}

function AgentSheet({
  teamId,
  editing,
  drafted,
  onClose,
  onAdd,
}: {
  teamId: string;
  editing: Editing & { kind: "agent" };
  drafted: Drafted;
  onClose: () => void;
  onAdd: (item: Item) => void;
}) {
  const { item, existing } = editing;
  const [form, setForm] = useState<Draft>(
    () => item?.form ?? (existing ? draftOf(existing) : EMPTY),
  );
  const [problem, setProblem] = useState<string | null>(null);
  // Never offered as its own delegate, whether it exists yet or not.
  const self = existing?.id ?? (item ? draftId("agents", item.name) : undefined);
  const others = { ...drafted, agents: drafted.agents.filter((n) => n !== item?.name) };

  const add = () => {
    const request = existing ? replaceRequest(existing, form) : agentRequest(form);
    if (typeof request === "string") return setProblem(request);
    const stop = renamed(existing, request.name);
    if (stop) return setProblem(stop);
    onAdd({ kind: "agent", name: request.name, form, request, ...(existing ? { existing } : {}) });
  };

  return (
    <Sheet open onOpenChange={(o) => (o ? null : onClose())}>
      <SheetContent className="w-full overflow-y-auto sm:max-w-3xl">
        <SheetHeader>
          <SheetTitle>
            {existing ? `Change ${existing.name}` : item ? `Edit ${item.name}` : "Draft an agent"}
          </SheetTitle>
          <SheetDescription>
            Added to the draft, not saved. Its servers, skills and delegates may be ones the draft
            creates.
          </SheetDescription>
        </SheetHeader>
        <div className="px-4">
          <AgentForm
            teamId={teamId}
            draft={form}
            set={(patch) => setForm((d) => ({ ...d, ...patch }))}
            self={self}
            drafted={others}
          />
        </div>
        <SheetFooter className="flex-row flex-wrap items-center justify-end gap-2">
          {problem ? (
            <p role="alert" className="mr-auto text-sm text-destructive">
              {problem}
            </p>
          ) : null}
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={add}>Add to draft</Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

function ServerSheet({
  editing,
  onClose,
  onAdd,
}: {
  editing: Editing & { kind: "mcp_server" };
  onClose: () => void;
  onAdd: (item: Item) => void;
}) {
  const { item, existing } = editing;
  const [form, setForm] = useState<ServerDraft>(
    () => item?.form ?? (existing ? serverDraftOf(existing) : EMPTY_SERVER),
  );
  const [problem, setProblem] = useState<string | null>(null);

  const add = () => {
    const request = existing ? editRequest(existing, form) : serverRequest(form);
    if (typeof request === "string") return setProblem(request);
    const stop = renamed(existing, request.name);
    if (stop) return setProblem(stop);
    onAdd({
      kind: "mcp_server",
      name: request.name,
      form,
      request,
      ...(existing ? { existing } : {}),
    });
  };

  return (
    <Dialog open onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {existing
              ? `Change ${existing.name}`
              : item
                ? `Edit ${item.name}`
                : "Draft an MCP server"}
          </DialogTitle>
          <DialogDescription>
            Added to the draft, not saved. A secret is named as a reference, never written here.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            add();
          }}
        >
          <ServerFields draft={form} set={(patch) => setForm((d) => ({ ...d, ...patch }))} />
          {problem ? (
            <p role="alert" className="text-sm text-destructive">
              {problem}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit">Add to draft</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function searchOf(page: {
  items: { id: string; name: string }[];
  next_cursor?: string | null;
}): SearchResult {
  return {
    items: page.items.map((i) => ({ id: i.id, name: i.name })),
    more: Boolean(page.next_cursor),
  };
}

/** Picks one of the team's agents or servers to change in the draft. */
function ChooseExisting({
  teamId,
  open,
  onClose,
  onPick,
}: {
  teamId: string;
  open: boolean;
  onClose: () => void;
  onPick: (kind: "agent" | "mcp_server", id: string) => void;
}) {
  return (
    <Dialog open={open} onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Change an existing one</DialogTitle>
          <DialogDescription>
            It opens in its form; what you change is added to the draft, not saved.
          </DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div>
            <p className="mb-1.5 text-sm font-medium">An agent</p>
            <Picker
              label="Agent to change"
              noun="agent"
              value={[]}
              max={1}
              onChange={(next) => (next[0] ? onPick("agent", next[0].id) : null)}
              search={(q) => listAgents(teamId, null, { q, limit: 20 }).then(searchOf)}
              searchKey={["agents", teamId, "compose"]}
            />
          </div>
          <div>
            <p className="mb-1.5 text-sm font-medium">An MCP server</p>
            <Picker
              label="MCP server to change"
              noun="server"
              value={[]}
              max={1}
              onChange={(next) => (next[0] ? onPick("mcp_server", next[0].id) : null)}
              search={(q) => listMCPServers(teamId, null, { q, limit: 20 }).then(searchOf)}
              searchKey={["mcp-servers", teamId, "compose"]}
            />
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
