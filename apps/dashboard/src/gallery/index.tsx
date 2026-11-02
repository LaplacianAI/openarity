/**
 * The states gallery: every shared component in every state, rendered from
 * fixtures, so states are reviewed without a brain having to fail on cue.
 * Dev builds only — the route renders it behind import.meta.env.DEV, which a
 * production build folds away together with this module.
 */
import { type ReactNode, useId, useState } from "react";

import { Markdown } from "@/components/markdown";
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
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { Avatar, EmptyState, LoadMore, Mono, PageHeader, StatusDot } from "@/components/ui-bits";
import { useTheme } from "@/lib/theme";

const VARIANTS = ["default", "destructive", "outline", "secondary", "ghost", "link"] as const;
const SIZES = ["xs", "sm", "default", "lg"] as const;

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
      <p className="text-[11px] text-muted-foreground">{label}</p>
      {children}
    </div>
  );
}

function Grid({ children }: { children: ReactNode }) {
  return <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">{children}</div>;
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
            <Field label="Name" htmlFor="g-invalid" hint="A team with that name already exists.">
              <Input id="g-invalid" defaultValue="platform" aria-invalid />
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

      <Section title="Empty state">
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
          <Avatar name="Platform On-call" color="var(--color-chart-2)" />
        </div>
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
          <State label="progress, a third">
            <Progress value={33} />
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
    </div>
  );
}
