import { CircleAlert, CircleCheck, Minus, PenLine, Plus } from "lucide-react";

import type { BlueprintChange, BlueprintDiscovered, BlueprintPlan } from "@/api/types";
import { KIND_WORDS } from "@/lib/blueprint";

/**
 * A plan as a list: a row per item, by what applying does to it, then a row
 * per refusal. The same list shows a builder's proposal in B4.
 */
export function PlanView({ plan, note }: { plan: BlueprintPlan; note?: string }) {
  const { changes, refusals } = plan;
  return (
    <section aria-labelledby="plan-heading" className="space-y-2">
      <h2 id="plan-heading" className="text-sm font-medium">
        Plan
      </h2>
      {note ? (
        <p role="status" className="text-sm text-muted-foreground">
          {note}
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

/** What an apply did, and what each MCP server it wrote answered when asked for its tools. */
export function AppliedView({
  changes,
  discovered,
}: {
  changes: BlueprintChange[];
  discovered: BlueprintDiscovered[];
}) {
  const made = changes.filter((c) => c.action !== "unchanged");
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
      {discovered.length > 0 ? (
        <ul aria-label="Tools discovered" className="space-y-1 text-sm">
          {discovered.map((d) => (
            <DiscoveredRow key={d.server} d={d} />
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function DiscoveredRow({ d }: { d: BlueprintDiscovered }) {
  if (!d.error) {
    return (
      <li className="flex items-baseline gap-1.5">
        <CircleCheck aria-hidden className="size-4 self-center text-success" />
        <span className="font-medium">{d.server}</span>
        <span className="text-muted-foreground">
          {d.tools.length === 1 ? "1 tool" : `${d.tools.length} tools`} discovered
        </span>
      </li>
    );
  }
  return (
    <li className="flex flex-wrap items-baseline gap-x-1.5">
      <CircleAlert aria-hidden className="size-4 self-center text-warning" />
      <span className="font-medium">{d.server}</span>
      <span className="text-muted-foreground">has no tools yet: {d.error}</span>
      <span className="basis-full pl-5.5 text-xs text-muted-foreground">
        Once that is fixed, open it on MCP servers and discover it.
      </span>
    </li>
  );
}
