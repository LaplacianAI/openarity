/**
 * The states gallery: every shared component in every state, rendered from
 * fixtures, so states are reviewed without a brain having to fail on cue.
 * Dev builds only — the route renders it behind import.meta.env.DEV, which a
 * production build folds away together with this module.
 */
import { type ReactNode, useId, useState } from "react";

import type { Run, RunEvent } from "@/api/types";
import { CodeBlock, JsonView } from "@/components/code";
import { CopyId, KeyValue, KeyValues, Time } from "@/components/facts";
import { Markdown } from "@/components/markdown";
import { Field, ReadOnlyNote, Tag, TextArea } from "@/components/registry-bits";
import { RunView } from "@/components/run-detail";
import { ErrorState, ForbiddenState } from "@/components/states";
import { Count, LiveBadge, StatusPill } from "@/components/status";
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
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
  SheetTrigger,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Checkbox, SwitchField } from "@/components/ui/switch";
import {
  RowActions,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Avatar, EmptyState, LoadMore, Mono, PageHeader, StatusDot } from "@/components/ui-bits";
import { useTheme } from "@/lib/theme";

const VARIANTS = ["default", "outline", "ghost", "destructive"] as const;
const SIZES = ["sm", "default"] as const;

const RUN_STATES = ["queued", "running", "succeeded", "failed", "cancelled"];

const MINUTES_AGO = new Date(Date.now() - 4 * 60_000).toISOString();

const JSON_SAMPLE = JSON.stringify({
  agent: "ops-lead",
  input: "Nightly export job has failed three nights running.",
  grants: { tools: ["web_search", "web_fetch", "graph"], skills: ["incident-postmortem"] },
  budget: { tokens: 1_000_000, steps: 5 },
  delegates: ["log-reader", "runbook-writer"],
});

const LONG =
  "A description long enough to wrap twice at a phone's width, because the states that break a layout are the long ones: a team named by a committee, an agent whose instructions were pasted whole, an error the brain explained at length.";

const MARKDOWN = `## A run's answer

The hub delegated to **triage**, which found three failing checks:

1. \`lint\` — an unused import in \`api/teams.go\`
2. \`test\` — a timeout under load
3. \`vuln\` — a toolchain advisory

\`\`\`sh
make check db=openarity_test
\`\`\`

> Restoring the team brings everything back as it was.`;

/**
 * A run still going: one call that answered (folded to its line), one that
 * failed (open on its error), and a second step waiting on a call.
 */
function runningRun(): { run: Run; events: RunEvent[] } {
  const started = new Date(Date.now() - 41_000).toISOString();
  const run: Run = {
    id: "gallery-run",
    session_id: "gallery-session",
    agent: "ops-lead",
    status: "running",
    input:
      "Exports to the EU bucket have timed out three nights running. Find out why and say what to change.",
    output: null,
    error: null,
    input_tokens: 2_310,
    output_tokens: 412,
    created_at: started,
    started_at: started,
    finished_at: null,
    parent_run_id: null,
    depth: 0,
    hub: true,
  };
  let seq = 0;
  const e = (kind: string, data: unknown): RunEvent => ({ seq: ++seq, kind, data });
  const events = [
    e("started", {
      agent: "ops-lead",
      model: "gemini-3.8-flash",
      pattern: "react",
      tools: ["web_search", "web_fetch", "graph"],
      skills: ["incident-postmortem"],
    }),
    e("step", { step: 1 }),
    e("usage", {
      model: "gemini-3.8-flash",
      input_tokens: 1_480,
      output_tokens: 190,
      cached_input_tokens: 0,
      spent: 1_670,
    }),
    e("tool_call", {
      id: "c1",
      name: "web_search",
      arguments: JSON.stringify({ query: "s3 PutObject RequestTimeout cross-region" }),
    }),
    e("tool_result", {
      id: "c1",
      name: "web_search",
      output:
        "Three results: the SDK's default socket timeout, cross-region transfer acceleration, and multipart upload sizing.",
      duration_ms: 812,
    }),
    e("tool_call", {
      id: "c2",
      name: "web_fetch",
      arguments: JSON.stringify({ url: "https://status.example.invalid/exports" }),
    }),
    e("tool_result", {
      id: "c2",
      name: "web_fetch",
      error: "the host refused the connection",
      duration_ms: 46,
    }),
    e("step", { step: 2 }),
    e("usage", {
      model: "gemini-3.8-flash",
      input_tokens: 830,
      output_tokens: 222,
      cached_input_tokens: 0,
      spent: 1_052,
    }),
    e("tool_call", {
      id: "c3",
      name: "graph",
      arguments: JSON.stringify({ query: "who owns the export job" }),
    }),
  ];
  return { run, events };
}

function RunDemo() {
  const [{ run, events }] = useState(runningRun);
  return (
    <div className="max-w-3xl">
      <RunView
        teamId="gallery-team"
        run={run}
        canCancel
        onChanged={() => {}}
        events={events}
        failed={null}
      />
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section aria-labelledby={id} className="space-y-3 border-t border-border pt-6">
      <h2 id={id} className="text-sm font-semibold">
        {title}
      </h2>
      {children}
    </section>
  );
}

/** One labelled state, so a screenshot says which is which. */
function State({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="space-y-1.5">
      <p className="text-xs text-muted-foreground">{label}</p>
      {children}
    </div>
  );
}

function Grid({ children }: { children: ReactNode }) {
  return <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">{children}</div>;
}

const AGENTS = [
  { name: "ops-lead", pattern: "react", runs: 1_204, tokens: 3_733_120 },
  { name: "log-reader", pattern: "react", runs: 86, tokens: 412_880 },
  { name: "runbook-writer", pattern: "plan", runs: 9, tokens: 61_002 },
];

function TableDemo() {
  const [opened, setOpened] = useState<string | null>(null);
  return (
    <div className="space-y-2">
      <Table>
        <TableHeader>
          <tr>
            <TableHead>Agent</TableHead>
            <TableHead>Pattern</TableHead>
            <TableHead numeric>Runs</TableHead>
            <TableHead numeric>Tokens</TableHead>
            <TableHead>
              <span className="sr-only">Actions</span>
            </TableHead>
          </tr>
        </TableHeader>
        <TableBody>
          {AGENTS.map((a) => (
            <TableRow key={a.name} onOpen={() => setOpened(a.name)}>
              <TableCell className="font-medium">{a.name}</TableCell>
              <TableCell label="Pattern">
                <Tag>{a.pattern}</Tag>
              </TableCell>
              <TableCell label="Runs" numeric>
                {a.runs.toLocaleString("en-US")}
              </TableCell>
              <TableCell label="Tokens" numeric>
                {a.tokens.toLocaleString("en-US")}
              </TableCell>
              <TableCell>
                <RowActions>
                  <Button variant="ghost" size="sm">
                    Edit
                  </Button>
                </RowActions>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <p className="text-xs text-muted-foreground" role="status">
        {opened
          ? `Opened ${opened}`
          : "Click a row, or Tab to the table and use the arrows and Enter."}
      </p>
    </div>
  );
}

function SheetDemo() {
  return (
    <Sheet>
      <SheetTrigger asChild>
        <Button variant="outline" size="sm">
          Open the sheet
        </Button>
      </SheetTrigger>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>Edit log-reader</SheetTitle>
          <SheetDescription>Changes apply to the next run.</SheetDescription>
        </SheetHeader>
        <Field label="Model" htmlFor="g-sheet-model" hint="As the model gateway names it.">
          <Input id="g-sheet-model" defaultValue="gemini-3.8-flash" />
        </Field>
        <SwitchField label="Web" description="Search the web and fetch pages." defaultChecked />
        <SheetFooter>
          <Button>Save</Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  );
}

function DialogDemo() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button variant="outline" size="sm" onClick={() => setOpen(true)}>
        Open the dialog
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Delete platform?</DialogTitle>
            <DialogDescription>{LONG}</DialogDescription>
          </DialogHeader>
          <Field label="Type the team's name to confirm" htmlFor="gallery-confirm">
            <Input id="gallery-confirm" placeholder="platform" />
          </Field>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button variant="destructive" disabled>
              Delete platform
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

export default function Gallery() {
  const { theme, toggle } = useTheme();

  return (
    <div className="mx-auto max-w-6xl space-y-6 px-4 py-6 sm:px-6">
      <PageHeader
        title="States gallery"
        description="Every shared component in every state, from fixtures. Dev builds only."
        actions={
          <Button variant="outline" size="sm" onClick={toggle}>
            {theme === "dark" ? "Light" : "Dark"}
          </Button>
        }
      />

      <Section title="Buttons">
        <div className="space-y-3">
          {VARIANTS.map((variant) => (
            <div key={variant} className="flex flex-wrap items-center gap-2">
              <Button variant={variant}>{variant}</Button>
              <Button variant={variant} disabled>
                {variant}
              </Button>
              <Button variant={variant} loading>
                {variant}
              </Button>
            </div>
          ))}
          <div className="flex flex-wrap items-center gap-2">
            {SIZES.map((size) => (
              <Button key={size} size={size} variant="outline">
                size {size}
              </Button>
            ))}
          </div>
        </div>
      </Section>

      <Section title="Fields">
        <Grid>
          <State label="empty">
            <Field label="Name" htmlFor="g-empty" hint="Lowercase, as people will type it.">
              <Input id="g-empty" placeholder="platform" />
            </Field>
          </State>
          <State label="filled">
            <Field label="Name" htmlFor="g-filled">
              <Input id="g-filled" defaultValue="platform-observability-oncall" />
            </Field>
          </State>
          <State label="invalid">
            <Field
              label="Name"
              htmlFor="g-invalid"
              hint="Lowercase, as people will type it."
              error="A team with that name already exists. Pick another name."
            >
              <Input id="g-invalid" defaultValue="platform" />
            </Field>
          </State>
          <State label="with a limit">
            <Field label="Description" htmlFor="g-limit" limit="58 of 280">
              <Input
                id="g-limit"
                defaultValue="Reads stack traces and log excerpts, names the cause."
              />
            </Field>
          </State>
          <State label="disabled">
            <Field label="Name" htmlFor="g-disabled">
              <Input id="g-disabled" defaultValue="platform" disabled />
            </Field>
          </State>
          <State label="text area, long">
            <Field label="Instructions" htmlFor="g-area">
              <TextArea id="g-area" defaultValue={LONG} />
            </Field>
          </State>
        </Grid>
      </Section>

      <Section title="Page header">
        <State label="title only">
          <PageHeader title="Agents" />
        </State>
        <State label="description and actions">
          <PageHeader
            title="Agents"
            description={LONG}
            actions={<Button size="sm">Define agent</Button>}
          />
        </State>
      </Section>

      <Section title="Block states">
        <Grid>
          <State label="body only">
            <EmptyState
              title="No sessions yet"
              body="A conversation appears here once a channel delivers a message."
            />
          </State>
          <State label="with action and hint">
            <EmptyState
              title="No agents"
              body={LONG}
              action={<Button size="sm">Define agent</Button>}
              hint="Only members with agent:write can define one."
            />
          </State>
          <State label="failed to load">
            <ErrorState
              what="the agents"
              error={new Error("The brain took too long to answer.")}
              onRetry={() => {}}
            />
          </State>
          <State label="failed, retrying">
            <ErrorState what="the agents" error={null} onRetry={() => {}} retrying />
          </State>
          <State label="forbidden">
            <ForbiddenState what="this team's sessions" permission="session:read" />
          </State>
        </Grid>
      </Section>

      <Section title="Load more">
        <State label="more to load">
          <LoadMore hasMore loading={false} onClick={() => {}} loadedLabel="50 loaded" />
        </State>
        <State label="loading">
          <LoadMore hasMore loading onClick={() => {}} loadedLabel="50 loaded" />
        </State>
        <State label="end">
          <LoadMore hasMore={false} loading={false} onClick={() => {}} loadedLabel="128 loaded" />
        </State>
      </Section>

      <Section title="Marks">
        <div className="flex flex-wrap items-center gap-4 text-sm">
          {(["active", "idle", "closed", "paused"] as const).map((tone) => (
            <span key={tone} className="inline-flex items-center gap-1.5">
              <StatusDot tone={tone} /> {tone}
            </span>
          ))}
          <Tag>github</Tag>
          <Tag>streamable-http</Tag>
          <Mono>6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d</Mono>
          <Avatar name="alice@example.com" />
          <Avatar name="Platform On-call" />
        </div>
      </Section>

      <Section title="Status">
        <div className="space-y-3">
          <div className="flex flex-wrap items-center gap-4">
            {RUN_STATES.map((s) => (
              <StatusPill key={s} status={s} />
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {RUN_STATES.map((s) => (
              <StatusPill key={s} status={s} pill />
            ))}
            <LiveBadge />
          </div>
          <div className="flex flex-wrap items-center gap-4">
            {["open", "idle", "closed", "pending", "archived"].map((s) => (
              <StatusPill key={s} status={s} />
            ))}
            <span className="inline-flex items-center gap-2 text-sm">
              Approvals <Count n="5" />
            </span>
            <span className="inline-flex items-center gap-2 text-sm">
              Approvals <Count n="50+" />
            </span>
          </div>
        </div>
      </Section>

      <Section title="Facts">
        <Grid>
          <State label="a run's facts">
            <KeyValues>
              <KeyValue label="Model">gemini-3.8-flash</KeyValue>
              <KeyValue label="Tokens">2,722</KeyValue>
              <KeyValue label="Run">
                <CopyId value="6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d" label="run id" />
              </KeyValue>
              <KeyValue label="Started">
                <Time iso={MINUTES_AGO} />
              </KeyValue>
            </KeyValues>
          </State>
          <State label="a long value">
            <KeyValues>
              <KeyValue label="Description">{LONG}</KeyValue>
            </KeyValues>
          </State>
        </Grid>
      </Section>

      <Section title="Code">
        <Grid>
          <State label="short">
            <CodeBlock code="make check db=openarity_test" label="the command" />
          </State>
          <State label="JSON, folded past a dozen lines">
            <JsonView value={JSON_SAMPLE} label="the request" />
          </State>
          <State label="does not parse">
            <JsonView value="{ truncated" label="output" />
          </State>
        </Grid>
      </Section>

      <Section title="Switches">
        <Grid>
          <State label="on">
            <SwitchField label="Web" description="Search the web and fetch pages." defaultChecked />
          </State>
          <State label="off">
            <SwitchField label="Graph" description="Read and write the team's graph." />
          </State>
          <State label="disabled">
            <SwitchField label="Delegate" description="Hand work to other agents." disabled />
          </State>
          <State label="checkboxes">
            <div className="flex items-center gap-4 text-sm">
              <span className="flex items-center gap-2">
                <Checkbox id="g-cb-web_search" defaultChecked />
                <label htmlFor="g-cb-web_search">web_search</label>
              </span>
              <span className="flex items-center gap-2">
                <Checkbox id="g-cb-web_fetch" />
                <label htmlFor="g-cb-web_fetch">web_fetch</label>
              </span>
              <span className="flex items-center gap-2 opacity-50">
                <Checkbox id="g-cb-graph" disabled />
                <label htmlFor="g-cb-graph">graph</label>
              </span>
            </div>
          </State>
        </Grid>
      </Section>

      <Section title="Table">
        <TableDemo />
      </Section>

      <Section title="Sheet">
        <SheetDemo />
      </Section>

      <Section title="Read-only note">
        <ReadOnlyNote permission="agent:write" what="agents" />
      </Section>

      <Section title="Loading">
        <Grid>
          <State label="skeleton rows">
            <div className="space-y-2">
              <Skeleton className="h-4 w-3/4" />
              <Skeleton className="h-4 w-1/2" />
              <Skeleton className="h-4 w-2/3" />
            </div>
          </State>
        </Grid>
      </Section>

      <Section title="Markdown">
        <div className="max-w-2xl rounded-md border border-border bg-card p-4">
          <Markdown>{MARKDOWN}</Markdown>
        </div>
      </Section>

      <Section title="Dialog">
        <DialogDemo />
      </Section>

      <Section title="Run">
        <RunDemo />
      </Section>
    </div>
  );
}
