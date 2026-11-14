/**
 * The states gallery: every shared component in every state, rendered from
 * fixtures, so states are reviewed without a brain having to fail on cue.
 * Dev builds only — the route renders it behind import.meta.env.DEV, which a
 * production build folds away together with this module.
 */
import { type ReactNode, useId, useState } from "react";

import type { Run, RunEvent, SessionEnvelope, SessionEvent } from "@/api/types";
import { CodeBlock, JsonView } from "@/components/code";
import { Composer } from "@/components/composer";
import { ConversationView } from "@/components/conversation-view";
import { CopyId, KeyValue, KeyValues, Time } from "@/components/facts";
import { AttachedChip } from "@/components/file-chip";
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
import { empty, fold } from "@/lib/session-stream";
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

/** A conversation as its stream would build it, in every state a turn can be in. */
function conversationOf(...events: SessionEvent[]) {
  return events.map((e, i) => ({ ...e, seq: i + 1 }) as SessionEnvelope).reduce(fold, empty);
}

const AT = (s: number) => new Date(Date.UTC(2026, 10, 13, 10, 0, s)).toISOString();
const ME = "user-me";

const finishedTalk = conversationOf(
  {
    kind: "message",
    data: {
      at: AT(0),
      id: "m1",
      user_id: ME,
      parts: [
        { type: "text", text: "Why did last night's deploy fail?" },
        {
          type: "file",
          file: {
            id: "f1",
            filename: "deploy-2026-11-12.log",
            media_type: "text/plain",
            size: 48_210,
          },
        },
        {
          type: "file",
          file: {
            id: "f2",
            filename: "grafana-error-rate-during-the-rollout-window.png",
            media_type: "image/png",
            size: 312_004,
          },
        },
      ],
      state: "pending",
    },
  },
  { kind: "run_started", data: { at: AT(1), run_id: "r1", agent: "ops-lead" } },
  { kind: "message_consumed", data: { at: AT(1), run_id: "r1", message_ids: ["m1"] } },
  { kind: "step_started", data: { at: AT(2), run_id: "r1", step: 1 } },
  {
    kind: "reasoning_message_chunk",
    data: {
      at: AT(2),
      run_id: "r1",
      message_id: "r1:1:reasoning",
      delta: "The deploy log will say which stage failed; read it before guessing.",
    },
  },
  {
    kind: "tool_call_chunk",
    data: {
      at: AT(3),
      run_id: "r1",
      tool_call_id: "c1",
      name: "read_logs",
      arguments: '{"service":"deploy","since":"24h"}',
    },
  },
  {
    kind: "tool_call_result",
    data: {
      at: AT(4),
      run_id: "r1",
      tool_call_id: "c1",
      name: "read_logs",
      content: "migration 0042 timed out",
      duration_ms: 412,
    },
  },
  {
    kind: "run_started",
    data: { at: AT(5), run_id: "r2", agent: "db-reviewer", parent_run_id: "r1" },
  },
  {
    kind: "run_finished",
    data: {
      at: AT(9),
      run_id: "r2",
      status: "succeeded",
      output: "The migration locks the invoices table.",
    },
  },
  { kind: "step_started", data: { at: AT(10), run_id: "r1", step: 2 } },
  {
    kind: "run_finished",
    data: {
      at: AT(18),
      run_id: "r1",
      status: "succeeded",
      output:
        "The deploy failed because **migration 0042 timed out** after 30 seconds: it takes a lock on `invoices` that the nightly export already held.\n\nRun the migration outside the export's window, or split the backfill into batches.",
    },
  },
  {
    kind: "message",
    data: {
      at: AT(30),
      id: "m2",
      user_id: "user-ann",
      parts: [{ type: "text", text: "Can we move the export to 03:00?" }],
    },
  },
  { kind: "run_started", data: { at: AT(31), run_id: "r3", agent: "ops-lead" } },
  {
    kind: "run_finished",
    data: {
      at: AT(33),
      run_id: "r3",
      status: "failed",
      error: "the run exceeded its token budget of 20000",
    },
  },
);

const liveTalk = conversationOf(
  {
    kind: "message",
    data: {
      at: AT(0),
      id: "m1",
      user_id: ME,
      parts: [{ type: "text", text: "Is staging healthy?" }],
      state: "pending",
    },
  },
  { kind: "run_started", data: { at: AT(1), run_id: "r1", agent: "ops-lead" } },
  { kind: "message_consumed", data: { at: AT(1), run_id: "r1", message_ids: ["m1"] } },
  { kind: "step_started", data: { at: AT(2), run_id: "r1", step: 1 } },
  {
    kind: "reasoning_message_chunk",
    data: {
      at: AT(2),
      run_id: "r1",
      message_id: "r1:1:reasoning",
      delta: "Check the health endpoint, then the last deploy.",
    },
  },
  {
    kind: "tool_call_chunk",
    data: {
      at: AT(3),
      run_id: "r1",
      tool_call_id: "c1",
      name: "http_get",
      arguments: '{"url":"https://staging.example/health"}',
    },
  },
  {
    kind: "message",
    data: {
      at: AT(4),
      id: "m2",
      user_id: ME,
      parts: [{ type: "text", text: "Also check the queue depth." }],
      state: "pending",
    },
  },
  { kind: "step_started", data: { at: AT(5), run_id: "r1", step: 2 } },
  {
    kind: "text_message_chunk",
    data: {
      at: AT(6),
      run_id: "r1",
      message_id: "r1:2:text",
      delta: "Staging answers its health check in 80 ms, and the last deploy",
    },
  },
);

/** A file of a claimed size, for chips that never send it. */
function demoFile(name: string, type: string, size: number): File {
  const f = new File(["x"], name, { type });
  Object.defineProperty(f, "size", { value: size });
  return f;
}
const picture = new File(
  [
    Uint8Array.from(
      atob(
        "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",
      ),
      (c) => c.charCodeAt(0),
    ),
  ],
  "error-rate.png",
  { type: "image/png" },
);

const people = {
  nameOf: (id: string) => (id === "user-ann" ? "ann@team.test" : null),
  me: ME,
};

function ConversationDemo() {
  return (
    <div className="grid gap-8 lg:grid-cols-2">
      <State label="Finished: folded working, the answer, a handed run, a failure">
        <ConversationView
          conversation={finishedTalk}
          sessionId="s-demo"
          rows={[]}
          people={people}
          files={{ open: () => {}, load: async () => picture }}
        />
      </State>
      <State label="Live: thinking open, a tool running, text as it is written, a message waiting">
        <ConversationView conversation={liveTalk} sessionId="s-demo" rows={[]} people={people} />
        <div className="mt-4 border-t border-rule pt-3">
          <Composer
            teamId="t-demo"
            writes={{ kind: "reply", sessionId: "s-demo" }}
            label="Reply"
            placeholder="Write a reply…"
          />
        </div>
      </State>
      <State label="Files in the composer: a picture, a long name cut before its extension, refused, and going up">
        <ul className="flex flex-wrap gap-1.5">
          <AttachedChip file={picture} refused={false} sending={false} onRemove={() => {}} />
          <AttachedChip
            file={demoFile(
              "quarterly-revenue-by-region-final-v3-approved.xlsx.zip",
              "application/zip",
              4_404_019,
            )}
            refused={false}
            sending={false}
            onRemove={() => {}}
          />
          <AttachedChip
            file={demoFile("screen-recording.mov", "video/quicktime", 88_000_000)}
            refused
            sending={false}
            onRemove={() => {}}
          />
          <AttachedChip
            file={demoFile("report.pdf", "application/pdf", 1_258_291)}
            refused={false}
            sending
            progress={0.42}
            onRemove={() => {}}
          />
        </ul>
      </State>
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

      <Section title="Conversation">
        <ConversationDemo />
      </Section>
    </div>
  );
}
