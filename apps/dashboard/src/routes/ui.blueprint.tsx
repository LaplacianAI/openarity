import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import {
  CircleAlert,
  Folder as FolderIcon,
  FolderPlus,
  Minus,
  PenLine,
  Plus,
  X,
} from "lucide-react";
import { type ChangeEvent, useRef, useState } from "react";
import { toast } from "sonner";

import { type Applied, applyBlueprint, planBlueprint } from "@/api";
import { useCurrentTeam } from "@/api/session";
import type { Blueprint, BlueprintChange, BlueprintPlan } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/input";
import { EmptyState, PageHeader } from "@/components/ui-bits";
import { LeaveGuard, SaveBar } from "@/components/unsaved";
import {
  type Folder,
  folderFrom,
  KIND_WORDS,
  missingFolders,
  readBlueprint,
  tally,
  withSkills,
} from "@/lib/blueprint";

export const Route = createFileRoute("/ui/blueprint")({
  component: BlueprintScreen,
});

const ABOUT =
  "A blueprint is a team's MCP servers, skills and agents in one YAML or JSON file, the same one oa plan and oa apply read. Plan shows what applying it would change; Apply makes exactly that change, all at once. Items it does not name are left alone, and nothing is removed.";

const PLACEHOLDER = `mcp_servers:
  - name: helpdesk
    url: https://helpdesk.example/mcp
    auth_secret: helpdesk#token
skills:
  - path: ./skills/support-faq
agents:
  - name: triage
    model: {name: anthropic/claude-sonnet-5-5, max_tokens: 4096}
    pattern: react
    max_steps: 20
    skills: [support-faq]`;

function BlueprintScreen() {
  const { teamId } = useCurrentTeam();
  return (
    <div className="space-y-6">
      <PageHeader
        title="Blueprint"
        finding="Plan a team's servers, skills and agents from one file, then apply it."
        description={ABOUT}
      />
      {teamId ? (
        <Editor teamId={teamId} />
      ) : (
        <EmptyState title="No team" body="Join or create a team to apply a blueprint to it." />
      )}
    </div>
  );
}

/** A plan, and the exact request it was made from: Apply sends that, never the text as it is now. */
type Shown = { plan: BlueprintPlan; sent: Blueprint; note?: string };

function Editor({ teamId }: { teamId: string }) {
  const qc = useQueryClient();
  const [text, setText] = useState("");
  const [folders, setFolders] = useState<Folder[]>([]);
  const [shown, setShown] = useState<Shown | null>(null);
  const [applied, setApplied] = useState<BlueprintChange[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const folderInput = useRef<HTMLInputElement>(null);
  const dirty = useRef(false);
  dirty.current = Boolean(text.trim()) && applied === null;

  // Any edit makes the shown plan someone else's: it goes, so Apply can
  // never send a plan for text that is no longer on the screen.
  const edited = () => {
    setShown(null);
    setApplied(null);
    setProblem(null);
  };

  const plan = useMutation({
    mutationFn: async () => {
      const read = readBlueprint(text);
      if (!read.ok) throw new Error(read.problem);
      const missing = missingFolders(read.skillPaths, folders);
      if (missing.length > 0) {
        throw new Error(
          `Add the folder for ${missing.join(", ")}: a browser cannot open a path itself.`,
        );
      }
      const sent = await withSkills(read.blueprint, folders);
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
        setApplied(out.changes);
        setShown(null);
        for (const key of ["agents", "skills", "servers", "summary", "graph"]) {
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

  const onFolder = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files ? folderFrom(e.target.files) : null;
    e.target.value = "";
    if (!f) return;
    setFolders((all) => [...all.filter((x) => x.name !== f.name), f]);
    edited();
  };

  const t = shown ? tally(shown.plan.changes) : null;
  const refused = (shown?.plan.refusals.length ?? 0) > 0;
  const canApply = Boolean(shown && t && !refused && t.create + t.update > 0);

  return (
    <div className="space-y-6">
      <section className="space-y-2">
        <label htmlFor="blueprint" className="text-sm font-medium">
          Blueprint
        </label>
        <Textarea
          id="blueprint"
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            edited();
          }}
          placeholder={PLACEHOLDER}
          spellCheck={false}
          className="min-h-72 font-mono text-sm leading-relaxed"
        />
        <p className="text-xs text-muted-foreground">
          YAML or JSON. Agents name their skills, MCP servers and delegates; a secret is its
          name#key in the team, never the secret.
        </p>
      </section>

      <section className="space-y-2" aria-labelledby="folders-heading">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 id="folders-heading" className="text-sm font-medium">
            Skill folders
          </h2>
          <Button variant="outline" size="sm" onClick={() => folderInput.current?.click()}>
            <FolderPlus aria-hidden />
            Add skill folder
          </Button>
          {/* webkitdirectory is not in React's types, and every browser supports it. */}
          <input
            ref={folderInput}
            type="file"
            className="hidden"
            aria-label="Skill folder"
            onChange={onFolder}
            {...({ webkitdirectory: "", directory: "" } as Record<string, string>)}
          />
        </div>
        {folders.length === 0 ? (
          <p className="text-xs text-muted-foreground">
            Each folder holds a SKILL.md. A skill's path: in the blueprint is matched to a folder by
            its name.
          </p>
        ) : (
          <ul className="divide-y divide-rule rounded-lg border border-border">
            {folders.map((f) => (
              <li key={f.name} className="flex items-center gap-2 px-3 py-2 text-sm">
                <FolderIcon aria-hidden className="size-4 text-muted-foreground" />
                <span className="min-w-0 flex-1 truncate">{f.name}</span>
                <span className="text-xs text-muted-foreground">
                  {f.files.length === 1 ? "1 file" : `${f.files.length} files`}
                </span>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={`Remove ${f.name}`}
                  onClick={() => {
                    setFolders((all) => all.filter((x) => x.name !== f.name));
                    edited();
                  }}
                >
                  <X aria-hidden />
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {shown ? <PlanView shown={shown} /> : null}
      {applied ? <AppliedView changes={applied} /> : null}

      <SaveBar
        status={
          applied
            ? "Applied. Edit the blueprint to plan again."
            : t
              ? refused
                ? "Fix the refusals; nothing can be applied until then."
                : t.create + t.update === 0
                  ? "Nothing to change."
                  : `${t.create} to create, ${t.update} to change, ${t.same} unchanged.`
              : "Plan to see what applying would change. Nothing is written until Apply."
        }
        problem={problem}
      >
        <Button
          variant={canApply ? "outline" : "default"}
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
      <LeaveGuard dirty={() => dirty.current} what="this blueprint" />
    </div>
  );
}

function PlanView({ shown }: { shown: Shown }) {
  const { changes, refusals } = shown.plan;
  return (
    <section aria-labelledby="plan-heading" className="space-y-2">
      <h2 id="plan-heading" className="text-sm font-medium">
        Plan
      </h2>
      {shown.note ? (
        <p role="status" className="text-sm text-muted-foreground">
          {shown.note}
        </p>
      ) : null}
      <ul className="divide-y divide-rule rounded-lg border border-border">
        {changes.map((c) => (
          <ChangeRow key={`${c.kind}/${c.name}`} change={c} />
        ))}
        {refusals.map((r) => (
          <li
            key={`${r.kind}/${r.name}/${r.message}`}
            className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2 text-sm"
          >
            <span className="flex w-24 shrink-0 items-center gap-1.5 text-destructive">
              <CircleAlert aria-hidden className="size-4 self-center" />
              Refused
            </span>
            <span className="w-24 shrink-0 text-muted-foreground">{KIND_WORDS[r.kind]}</span>
            <span className="font-medium">{r.name}</span>
            <span className="basis-full text-destructive sm:basis-auto">{r.message}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

const ACTION = {
  create: { word: "Create", icon: Plus, tone: "text-success" },
  update: { word: "Update", icon: PenLine, tone: "text-warning" },
  unchanged: { word: "Unchanged", icon: Minus, tone: "text-muted-foreground" },
} as const;

function ChangeRow({ change: c }: { change: BlueprintChange }) {
  const a = ACTION[c.action] ?? ACTION.unchanged;
  const Icon = a.icon;
  return (
    <li className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-3 py-2 text-sm">
      <span className={`flex w-24 shrink-0 items-center gap-1.5 ${a.tone}`}>
        <Icon aria-hidden className="size-4 self-center" />
        {a.word}
      </span>
      <span className="w-24 shrink-0 text-muted-foreground">{KIND_WORDS[c.kind]}</span>
      <span className={c.action === "unchanged" ? "text-muted-foreground" : "font-medium"}>
        {c.name}
      </span>
      {c.fields?.length ? (
        <span className="text-muted-foreground">{c.fields.join(", ")}</span>
      ) : null}
    </li>
  );
}

function AppliedView({ changes }: { changes: BlueprintChange[] }) {
  const made = changes.filter((c) => c.action !== "unchanged");
  const newServers = made.filter((c) => c.kind === "mcp_server" && c.action === "create");
  return (
    <section aria-labelledby="applied-heading" className="space-y-2">
      <h2 id="applied-heading" className="text-sm font-medium">
        Applied
      </h2>
      <ul className="divide-y divide-rule rounded-lg border border-border">
        {made.map((c) => (
          <ChangeRow key={`${c.kind}/${c.name}`} change={c} />
        ))}
      </ul>
      {newServers.length > 0 ? (
        <p className="text-xs text-muted-foreground">
          A new MCP server has no tools until it is discovered: open{" "}
          {newServers.map((s) => s.name).join(", ")} on MCP servers and discover it.
        </p>
      ) : null}
    </section>
  );
}
