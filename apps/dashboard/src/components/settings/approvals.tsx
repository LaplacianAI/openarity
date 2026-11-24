import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Ban, Check, Clock, Info, TriangleAlert } from "lucide-react";
import { RadioGroup } from "radix-ui";
import { useId, useState } from "react";

import {
  createApprovalRule,
  deleteApprovalRule,
  getApprovalSettings,
  listApprovalRules,
  listMCPServers,
  listMCPTools,
  saveApprovalSettings,
} from "@/api";
import type { ApprovalMode, ApprovalRuleMode, ApprovalSettings } from "@/api/types";
import { Choice, type ChoiceGroup, chosen, Segments } from "@/components/choices";
import { type Picked, Picker } from "@/components/picker";
import { Field } from "@/components/registry-bits";
import { Section } from "@/components/settings/section";
import { ErrorState } from "@/components/states";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { SwitchField } from "@/components/ui/switch";
import { PICK_PAGE } from "@/lib/team-settings";
import { cn } from "@/lib/utils";
import { waitWords } from "@/lib/wait";

/* ---------------------------------------------------------- tool approvals */

/** The modes from loosest to strictest, each with what it asks before. */
const APPROVAL_MODES: { value: ApprovalMode; label: string; about: string }[] = [
  { value: "off", label: "Off", about: "Agents never ask, and the rules below are off too." },
  {
    value: "destructive",
    label: "Destructive calls only",
    about:
      "Agents ask before a call that may delete or send something. Opening a page, clicking and typing are not asked.",
  },
  {
    value: "writes",
    label: "Writes and destructive calls",
    about:
      "Agents ask before any call that changes something, clicking or typing in a web page included.",
  },
  {
    value: "all",
    label: "Every call",
    about: "Agents ask before every call, reading a page or searching the web too.",
  },
];

const TTL_MIN = 5;
const TTL_MAX = 1440;

/**
 * When the team's agents ask a person, and the standing answers that decide
 * for one tool or one server. Both have routes of their own, apart from the
 * rest of the settings, so they save on their own.
 */
export function ApprovalSections({ teamId, canWrite }: { teamId: string; canWrite: boolean }) {
  const stored = useQuery({
    queryKey: ["approval-settings", teamId],
    queryFn: () => getApprovalSettings(teamId),
  });
  return (
    <>
      {stored.isError ? (
        <section className="border-t border-rule py-6">
          <ErrorState
            what="the approval settings"
            error={stored.error}
            onRetry={() => stored.refetch()}
            retrying={stored.isRefetching}
          />
        </section>
      ) : stored.data ? (
        <ApprovalModeSection teamId={teamId} stored={stored.data} canWrite={canWrite} />
      ) : (
        <Skeleton className="mt-6 h-24 w-full" role="status" aria-label="Loading approvals" />
      )}
      <ApprovalRulesSection teamId={teamId} canWrite={canWrite} mode={stored.data?.mode} />
    </>
  );
}

function ApprovalModeSection({
  teamId,
  stored,
  canWrite,
}: {
  teamId: string;
  stored: ApprovalSettings;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const [mode, setMode] = useState<ApprovalMode>(stored.mode);
  const [ttl, setTtl] = useState(String(stored.ttl_minutes));
  const [saved, setSaved] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const minutes = Number(ttl);
  const valid = Number.isInteger(minutes) && minutes >= TTL_MIN && minutes <= TTL_MAX;
  const dirty =
    mode !== stored.mode ||
    (valid ? minutes !== stored.ttl_minutes : ttl !== String(stored.ttl_minutes));
  const labelId = useId();

  const save = useMutation({
    mutationFn: (s: ApprovalSettings) => saveApprovalSettings(teamId, s),
    onSuccess: (next) => {
      qc.setQueryData(["approval-settings", teamId], next);
      setSaved(true);
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const edited = () => {
    setSaved(false);
    setProblem(null);
  };

  return (
    <Section
      title="Tool approvals"
      about="When the team's agents ask a person before making a call. Runs already started keep the settings they began with."
      canWrite={canWrite}
      dirty={dirty}
      saved={saved && !dirty}
      problem={problem}
      pending={save.isPending}
      busy={false}
      onSave={() => {
        if (!valid) {
          setProblem(`Wait between ${TTL_MIN} and ${TTL_MAX} minutes.`);
          return;
        }
        setProblem(null);
        save.mutate({ mode, ttl_minutes: minutes });
      }}
    >
      <div className="space-y-1.5">
        <p id={labelId} className="text-sm font-medium">
          Ask before
        </p>
        <RadioGroup.Root
          aria-labelledby={labelId}
          value={mode}
          onValueChange={(v) => {
            setMode(v as ApprovalMode);
            edited();
          }}
          className="divide-y divide-rule overflow-hidden rounded-md border border-border"
        >
          {APPROVAL_MODES.map((m) => {
            const id = `approval-mode-${m.value}`;
            return (
              <label
                key={m.value}
                htmlFor={id}
                className="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors duration-(--dur-press) ease-out hover:bg-muted has-[:disabled]:cursor-default has-[:disabled]:hover:bg-transparent"
              >
                <RadioGroup.Item
                  id={id}
                  value={m.value}
                  aria-labelledby={`${id}-label`}
                  aria-describedby={`${id}-about`}
                  className="mt-0.5 flex size-4 shrink-0 items-center justify-center rounded-full border border-input transition-colors duration-(--dur-press) ease-out outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 data-[state=checked]:border-primary"
                >
                  <RadioGroup.Indicator className="size-2 rounded-full bg-primary" />
                </RadioGroup.Item>
                <span className="min-w-0 space-y-0.5">
                  <span id={`${id}-label`} className="block text-sm font-medium">
                    {m.label}
                  </span>
                  <span id={`${id}-about`} className="block text-xs text-muted-foreground">
                    {m.about}
                  </span>
                </span>
              </label>
            );
          })}
        </RadioGroup.Root>
      </div>
      <Field
        label="Minutes a request waits"
        htmlFor="settings-approval-ttl"
        hint={`${TTL_MIN} to ${TTL_MAX}. After that it expires and the run carries on without it.`}
      >
        <WaitInput
          id="settings-approval-ttl"
          value={ttl}
          readout={valid ? waitWords(minutes) : null}
          onChange={(v) => {
            setTtl(v);
            edited();
          }}
        />
      </Field>
      <p className="flex gap-2 text-xs text-muted-foreground">
        <Info aria-hidden className="mt-px size-3.5 shrink-0" />
        <span>
          Unless approvals are off, a run for a guest asks before any call that is not a read, and a
          run with nobody there to answer, such as a scheduled one, is refused a call it would have
          asked about.
        </span>
      </p>
    </Section>
  );
}

/** The wait in minutes, with what it comes to beside it. */
function WaitInput({
  id,
  value,
  readout,
  onChange,
  ...aria
}: {
  id: string;
  value: string;
  readout: string | null;
  onChange: (next: string) => void;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  return (
    <div className="flex items-center gap-2">
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        className="w-24 tabular-nums"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        {...aria}
      />
      <span className="text-sm text-muted-foreground">
        minutes{readout && readout !== `${Number(value)} minutes` ? ` · ${readout}` : ""}
      </span>
    </div>
  );
}

/** A rule's answer: its words, and the icon and colour it is shown in. */
const RULE_ANSWERS: {
  value: ApprovalRuleMode;
  label: string;
  short: string;
  icon: typeof Check;
  tone: string;
}[] = [
  {
    value: "allow",
    label: "Allow without asking",
    short: "Allow",
    icon: Check,
    tone: "text-succeeded",
  },
  { value: "ask", label: "Always ask", short: "Ask", icon: Clock, tone: "text-warning" },
  { value: "deny", label: "Never allow", short: "Never", icon: Ban, tone: "text-destructive" },
];
/**
 * A rule's answer as shown. One this build does not know is shown as asking,
 * since that is what the brain does with it (`verdictOf`).
 */
function answerOf(m: ApprovalRuleMode) {
  const ask = RULE_ANSWERS.find((a) => a.value === "ask");
  return RULE_ANSWERS.find((a) => a.value === m) ?? (ask as (typeof RULE_ANSWERS)[number]);
}

/** The brain's own tools a rule may name: those the approval gate stands before. */
const BUILT_INS: ChoiceGroup<string>[] = [
  {
    label: "Web",
    options: [
      { value: "web_search", label: "web_search" },
      { value: "web_fetch", label: "web_fetch" },
    ],
  },
  {
    label: "Browser",
    options: [
      {
        value: "browser_open",
        label: "browser_open",
        hint: "Asked when a page opens, and covers what is then done on that page's own site.",
      },
      { value: "browser_snapshot", label: "browser_snapshot" },
      { value: "browser_click", label: "browser_click" },
      { value: "browser_type", label: "browser_type" },
      { value: "browser_screenshot", label: "browser_screenshot" },
    ],
  },
  {
    label: "Workspace",
    options: [
      {
        value: "Bash",
        label: "Bash",
        hint: "Runs a command, which can reach any public host, so it asks wherever changes ask.",
      },
      { value: "Read", label: "Read" },
      { value: "Write", label: "Write" },
      { value: "Edit", label: "Edit" },
      { value: "Glob", label: "Glob" },
      { value: "Grep", label: "Grep" },
    ],
  },
];
const BUILT_IN_NAMES = BUILT_INS.flatMap((g) => g.options.map((o) => o.value));
// Not a UUID, so no server's id can be it.
const BUILT_IN: Picked = { id: ":built-in", name: "Built-in tools" };

/** The servers a rule may name, searched by name; a tool rule may also name a built-in. */
async function ruleServers(teamId: string, q: string, builtIns: boolean) {
  const page = await listMCPServers(teamId, null, { q, limit: PICK_PAGE });
  const items: Picked[] = page.items.map((s) => ({ id: s.id, name: s.name }));
  const wanted = q.trim().toLowerCase();
  const matches =
    wanted === "" ||
    "built-in tools".includes(wanted) ||
    BUILT_IN_NAMES.some((name) => name.includes(wanted.replaceAll(" ", "_")));
  if (builtIns && matches) items.unshift(BUILT_IN);
  return { items, more: Boolean(page.next_cursor) };
}

/**
 * The tool a rule names, chosen from what its server said it has: a rule's
 * target is <server>__<tool>, or a built-in's own name.
 */
function RuleTool({
  teamId,
  server,
  value,
  onChange,
}: {
  teamId: string;
  server: Picked | null;
  value: string;
  onChange: (target: string) => void;
}) {
  const builtIn = server?.id === BUILT_IN.id;
  const tools = useQuery({
    queryKey: ["mcp-tools", teamId, server?.id],
    queryFn: () => listMCPTools(teamId, server?.id as string),
    enabled: Boolean(server) && !builtIn,
  });
  // Each option carries its target and its label, so neither is cut from the other.
  const options: ChoiceGroup<string>[] = builtIn
    ? BUILT_INS
    : [
        {
          label: "",
          options: (tools.data?.items ?? []).map((t) => ({
            value: `${server?.name}__${t.name}`,
            label: t.name,
          })),
        },
      ];
  const empty = options.every((g) => g.options.length === 0);
  const hint = !server ? (
    "Choose a server first."
  ) : builtIn ? (
    (chosen(BUILT_INS, value)?.hint ??
    "Tools every agent can be given, such as reading a web page.")
  ) : tools.isPending ? (
    "Reading the server's tools…"
  ) : tools.isError ? (
    <>
      Couldn't read this server's tools.{" "}
      <button
        type="button"
        className="underline underline-offset-2 hover:text-foreground"
        onClick={() => void tools.refetch()}
      >
        Try again
      </button>
    </>
  ) : empty ? (
    "This server has said it has no tools."
  ) : undefined;
  return (
    <Field label="Tool" htmlFor="rule-target" hint={hint}>
      <Choice
        id="rule-target"
        value={value}
        options={options}
        placeholder="Choose a tool"
        disabled={empty}
        onChange={onChange}
      />
    </Field>
  );
}

/** A rule's target as a person reads it: the tool, and where it is from. */
function ruleSubject(kind: string, target: string): { name: string; from: string; mono: boolean } {
  if (kind === "server") return { name: target, from: "Every tool of this server", mono: false };
  const cut = target.indexOf("__");
  if (cut > 0) {
    return { name: target.slice(cut + 2), from: `on ${target.slice(0, cut)}`, mono: true };
  }
  return { name: target, from: "Built-in", mono: true };
}

const RULE_SCOPES = [
  { value: "tool", label: "One tool" },
  { value: "server", label: "Every tool of a server" },
] as const;

function ApprovalRulesSection({
  teamId,
  canWrite,
  mode: teamMode,
}: {
  teamId: string;
  canWrite: boolean;
  /** The team's mode, once read: with approvals off, no rule applies. */
  mode?: ApprovalMode;
}) {
  const qc = useQueryClient();
  const rules = useQuery({
    queryKey: ["approval-rules", teamId],
    queryFn: () => listApprovalRules(teamId),
  });
  const [kind, setKind] = useState<"tool" | "server">("tool");
  // The server a rule names, or whose tool it names: picked, not typed.
  const [server, setServer] = useState<Picked | null>(null);
  const [target, setTarget] = useState("");
  const [mode, setMode] = useState<ApprovalRuleMode>("ask");
  const [other, setOther] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);

  const refresh = () => qc.invalidateQueries({ queryKey: ["approval-rules", teamId] });
  const add = useMutation({
    mutationFn: () =>
      createApprovalRule(teamId, {
        target_kind: kind,
        target: target.trim(),
        mode,
        // Only an answer that asks has someone to answer it.
        require_other: mode === "ask" && other,
      }),
    onSuccess: () => {
      setServer(null);
      setTarget("");
      setOther(false);
      setProblem(null);
      void refresh();
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteApprovalRule(teamId, id),
    onSuccess: () => {
      setProblem(null);
      void refresh();
    },
    onError: (err: Error) => setProblem(err.message),
  });
  const items = rules.data?.items ?? [];

  return (
    <Section
      title="Tool approval rules"
      about="A standing answer for one tool, or every tool of a server, whatever the mode. A tool's own rule wins over its server's."
      canWrite={canWrite}
      dirty={false}
      saved={false}
      problem={problem}
      pending={false}
      busy={false}
      onSave={() => {
        if (target.trim() === "") {
          setProblem(
            kind === "tool"
              ? "Choose the tool the rule is for."
              : "Choose the server the rule is for.",
          );
          return;
        }
        add.mutate();
      }}
    >
      {teamMode === "off" ? (
        <p className="flex gap-2 text-sm">
          <TriangleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
          <span>
            Approvals are off, so no rule applies until a mode is chosen under Tool approvals.
          </span>
        </p>
      ) : null}
      {rules.isError ? (
        <ErrorState
          what="the approval rules"
          error={rules.error}
          onRetry={() => rules.refetch()}
          retrying={rules.isRefetching}
        />
      ) : rules.isPending ? (
        <Skeleton className="h-12 w-full" role="status" aria-label="Loading rules" />
      ) : items.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No rules. The mode above decides for every call.
        </p>
      ) : (
        <ul aria-label="Tool approval rules" className="divide-y divide-rule">
          {items.map((r) => {
            const subject = ruleSubject(r.target_kind, r.target);
            const answer = answerOf(r.mode);
            const Icon = answer.icon;
            return (
              <li
                key={r.id}
                data-rule={r.target}
                className="flex flex-wrap items-center gap-x-4 gap-y-1 py-2.5 first:pt-0"
              >
                <div className="min-w-0 flex-1 basis-48">
                  <p className={cn("truncate", subject.mono ? "font-mono text-xs" : "text-sm")}>
                    {subject.name}
                  </p>
                  <p className="text-xs text-muted-foreground">{subject.from}</p>
                </div>
                <div className="min-w-0 basis-40">
                  <p className="flex items-center gap-1.5 text-sm">
                    <Icon aria-hidden className={cn("size-3.5 shrink-0", answer.tone)} />
                    {answer.label}
                  </p>
                  {r.require_other ? (
                    <p className="text-xs text-muted-foreground">Someone else answers</p>
                  ) : null}
                </div>
                {canWrite ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="ml-auto"
                    disabled={remove.isPending}
                    aria-label={`Remove the rule for ${r.target}`}
                    onClick={() => remove.mutate(r.id)}
                  >
                    Remove
                  </Button>
                ) : null}
              </li>
            );
          })}
        </ul>
      )}
      {canWrite ? (
        <div className="space-y-4 rounded-md bg-surface p-4">
          <h3 className="text-sm font-medium">New rule</h3>
          <Segments
            label="Applies to"
            value={kind}
            options={RULE_SCOPES}
            onChange={(next) => {
              setKind(next);
              setServer(null);
              setTarget("");
            }}
          />
          <div className={cn("grid gap-3", kind === "tool" && "sm:grid-cols-2")}>
            <div className="min-w-0 space-y-1.5">
              <label
                id="rule-server-label"
                htmlFor="rule-server"
                className="block text-sm font-medium"
              >
                Server
              </label>
              <Picker
                label="Server"
                noun="server"
                max={1}
                field={{
                  id: "rule-server",
                  labelId: "rule-server-label",
                  placeholder: "Choose a server",
                }}
                value={server ? [server] : []}
                onChange={(next) => {
                  const picked = next[0] ?? null;
                  setServer(picked);
                  setTarget(kind === "server" && picked ? picked.name : "");
                }}
                search={(q) => ruleServers(teamId, q, kind === "tool")}
                searchKey={["rule-servers", teamId, kind]}
              />
            </div>
            {kind === "tool" ? (
              <RuleTool teamId={teamId} server={server} value={target} onChange={setTarget} />
            ) : null}
          </div>
          <div className="space-y-1.5">
            <p className="text-sm font-medium">Answer</p>
            <Segments
              label="Answer"
              value={mode}
              options={RULE_ANSWERS.map((a) => ({ ...a, label: a.short }))}
              onChange={(next) => {
                setMode(next);
                // Someone else's answer belongs to a rule that asks.
                if (next !== "ask") setOther(false);
              }}
            />
          </div>
          {mode === "ask" ? (
            <SwitchField
              label="Someone else must answer"
              description="The person the run works for may not approve their own request."
              checked={other}
              onCheckedChange={setOther}
            />
          ) : null}
          <Button type="submit" size="sm" loading={add.isPending}>
            Add rule
          </Button>
        </div>
      ) : null}
    </Section>
  );
}
