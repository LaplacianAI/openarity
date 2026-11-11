/**
 * A builder's proposal in the Build chat. The plan shown is the brain's,
 * made now as the reader, so a permission they lack is a refusal here and
 * Apply is not offered. Apply sends that plan's digest: if the team moved,
 * the new plan is shown and nothing is written.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, History } from "lucide-react";
import { useState } from "react";

import { type Applied, applyProposal, getProposal } from "@/api";
import { ApiError } from "@/api/client";
import type { BlueprintChange, BlueprintPlan, Proposal } from "@/api/types";
import { AppliedView, PlanView } from "@/components/blueprint-plan";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { tally } from "@/lib/blueprint";

export function ProposalCard({
  teamId,
  proposal,
  onChange,
}: {
  teamId: string;
  proposal: Proposal;
  /** Focus the message box: the next message is the change. */
  onChange: () => void;
}) {
  if (proposal.state === "superseded") {
    return (
      <Frame
        label="Proposal · superseded"
        tone="muted"
        icon={<History aria-hidden className="size-4" />}
      >
        <p className="text-sm text-muted-foreground">{proposal.summary}</p>
        <p className="text-xs text-muted-foreground">
          The builder proposed again after this, below.
        </p>
      </Frame>
    );
  }
  if (proposal.state === "applied") return <AppliedProposal teamId={teamId} proposal={proposal} />;
  return <OpenProposal teamId={teamId} proposal={proposal} onChange={onChange} />;
}

/** What this tab applied, kept by proposal: the list re-read after an apply says only that it was. */
const appliedKey = (teamId: string, id: string) => ["applied", teamId, id];

function AppliedProposal({ teamId, proposal }: { teamId: string; proposal: Proposal }) {
  const qc = useQueryClient();
  const changes = qc.getQueryData<BlueprintChange[]>(appliedKey(teamId, proposal.id));
  return (
    <Frame
      label="Proposal · applied"
      tone="ok"
      icon={<CircleCheck aria-hidden className="size-4" />}
    >
      <p className="text-sm">{proposal.summary}</p>
      {changes ? <AppliedView changes={changes} /> : null}
    </Frame>
  );
}

function OpenProposal({
  teamId,
  proposal,
  onChange,
}: {
  teamId: string;
  proposal: Proposal;
  onChange: () => void;
}) {
  const qc = useQueryClient();
  const read = useQuery({
    queryKey: ["proposal", teamId, proposal.id],
    queryFn: () => getProposal(teamId, proposal.id),
  });
  // A plan the apply came back with, when the team had moved: shown in place
  // of the one read, and the one the next Apply sends.
  const [newer, setNewer] = useState<{ plan: BlueprintPlan; why: string } | null>(null);
  const [done, setDone] = useState<Applied | null>(null);

  const apply = useMutation({
    mutationFn: (digest: string) => applyProposal(teamId, proposal.id, digest),
    onSuccess: (out) => {
      if (out.kind === "applied") {
        qc.setQueryData(appliedKey(teamId, proposal.id), out.changes);
        setDone(out);
        for (const key of ["agents", "mcp-servers", "skills", "summary", "graph"]) {
          void qc.invalidateQueries({ queryKey: [key, teamId] });
        }
        void qc.invalidateQueries({ queryKey: ["proposals", teamId] });
        return;
      }
      setNewer({
        plan: out.plan,
        why:
          out.kind === "changed"
            ? "The team changed since this plan, so nothing was applied. This is what applying would do now."
            : "This can't be applied as it is: see what was refused.",
      });
    },
  });

  if (done?.kind === "applied") return <AppliedProposal teamId={teamId} proposal={proposal} />;

  const plan = newer?.plan ?? read.data?.plan;
  const counts = plan ? tally(plan.changes) : null;
  const applicable = plan
    ? plan.refusals.length === 0 && (counts?.create ?? 0) + (counts?.update ?? 0) > 0
    : false;
  const gone = apply.error instanceof ApiError && apply.error.status === 409;

  return (
    <Frame label="Proposal · open" tone="open">
      <p className="text-sm font-medium">{proposal.summary}</p>
      {read.isPending ? (
        <Skeleton className="h-16 w-full" />
      ) : read.isError ? (
        <p role="alert" className="text-sm text-destructive">
          Couldn't read this proposal's plan.{" "}
          {read.error instanceof ApiError ? read.error.message : ""}
        </p>
      ) : plan ? (
        <PlanView plan={plan} note={newer?.why} />
      ) : null}
      {plan && counts ? (
        <p className="text-xs text-muted-foreground">
          {counts.create} to create, {counts.update} to change
          {plan.refusals.length ? `, ${plan.refusals.length} refused` : ""}.
        </p>
      ) : null}
      {apply.isError ? (
        <p role="alert" className="text-sm text-destructive">
          {gone
            ? "This proposal was applied or replaced meanwhile, so nothing was applied."
            : "Couldn't apply it. Nothing was written. Try again."}
        </p>
      ) : null}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" size="sm" onClick={onChange}>
          Change something
        </Button>
        {applicable && plan ? (
          <Button
            type="button"
            size="sm"
            loading={apply.isPending}
            onClick={() => apply.mutate(plan.digest)}
          >
            Apply
          </Button>
        ) : null}
      </div>
    </Frame>
  );
}

function Frame({
  label,
  tone,
  icon,
  children,
}: {
  label: string;
  tone: "open" | "ok" | "muted";
  icon?: React.ReactNode;
  children: React.ReactNode;
}) {
  const border = tone === "open" ? "border-primary/40" : "border-border";
  const head = tone === "ok" ? "text-success" : "text-muted-foreground";
  return (
    <section
      aria-label={label}
      className={`space-y-3 rounded-lg border ${border} bg-card p-3 sm:p-4`}
    >
      <p
        className={`flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide ${head}`}
      >
        {icon}
        {label}
      </p>
      {children}
    </section>
  );
}
