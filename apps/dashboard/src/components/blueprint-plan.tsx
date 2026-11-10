import { CircleAlert, Minus, PenLine, Plus } from "lucide-react";

import type { BlueprintChange, BlueprintPlan } from "@/api/types";
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

/** What an apply did, with what a new MCP server still needs. */
export function AppliedView({ changes }: { changes: BlueprintChange[] }) {
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
