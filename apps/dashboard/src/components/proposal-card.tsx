/**
 * A builder's proposal in the Build chat. The plan shown is the brain's,
 * made now as the reader, so a permission they lack is a refusal here and
 * Apply is not offered. Apply sends that plan's digest: if the team moved,
 * the new plan is shown and nothing is written.
 */
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CircleCheck, Globe, History, KeyRound } from "lucide-react";
import { useId, useState } from "react";

import { type Applied, applyProposal, getProposal, putMCPSecret } from "@/api";
import { ApiError } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import type { BlueprintPlan, Proposal } from "@/api/types";
import { AppliedView, PlanView } from "@/components/blueprint-plan";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { tally } from "@/lib/blueprint";
import { type Needs, needsOf, type SecretGroup, secretGroups } from "@/lib/build";

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
  const out = qc.getQueryData<Extract<Applied, { kind: "applied" }>>(
    appliedKey(teamId, proposal.id),
  );
  return (
    <Frame
      label="Proposal · applied"
      tone="ok"
      icon={<CircleCheck aria-hidden className="size-4" />}
    >
      <p className="text-sm">{proposal.summary}</p>
      {out ? <AppliedView changes={out.changes} discovered={out.discovered} /> : null}
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
        qc.setQueryData(appliedKey(teamId, proposal.id), out);
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
  const needs = plan ? needsOf(read.data?.blueprint, plan) : null;

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
      {needs ? <NeedsView teamId={teamId} needs={needs} /> : null}
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

/**
 * What applying asks of the person besides the click: the secrets the
 * servers name, and the hosts the team's agents would newly be sent to. A
 * builder that read a page could have been steered to a host, so each new
 * one is named for the person to recognise.
 */
function NeedsView({ teamId, needs }: { teamId: string; needs: Needs }) {
  if (needs.secrets.length === 0 && needs.addresses.length === 0) return null;
  const groups = secretGroups(needs.secrets, teamId);
  return (
    <section aria-labelledby="needs-heading" className="space-y-2 text-sm">
      <h3 id="needs-heading" className="font-medium">
        Needs from you
      </h3>
      {groups.length > 0 ? (
        <div className="space-y-2">
          <p className="flex gap-2">
            <KeyRound aria-hidden className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
            The servers that name these secrets get no tools until each is set. What you type goes
            to the secret store and is never shown again.
          </p>
          <ul className="space-y-2 pl-6">
            {groups.map((g) => (
              <SecretRow key={g.name} teamId={teamId} group={g} />
            ))}
          </ul>
        </div>
      ) : null}
      {needs.addresses.length > 0 ? (
        <div className="flex gap-2">
          <Globe aria-hidden className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          <div>
            <p>Agents will be sent to these addresses. Apply only if you recognise them:</p>
            <ul className="mt-1 space-y-0.5">
              {needs.addresses.map((a) => (
                <li key={a.server}>
                  <span className="font-medium">{a.server}</span>{" "}
                  <code className="font-mono text-xs">{a.host}</code>
                </li>
              ))}
            </ul>
          </div>
        </div>
      ) : null}
    </section>
  );
}

/** One secret a proposal names: a field per key, written together. */
function SecretRow({ teamId, group }: { teamId: string; group: SecretGroup }) {
  const permissions = usePermissions(teamId);
  const id = useId();
  const [values, setValues] = useState<Record<string, string>>({});
  const write = useMutation({
    mutationFn: () => putMCPSecret(teamId, group.name, values),
    onSuccess: () => setValues({}),
  });
  const filled = group.keys.length > 0 && group.keys.every((k) => (values[k] ?? "").trim() !== "");
  const refs = group.keys.map((k) => `${group.name}#${k}`).join(", ");

  if (!group.settable || group.keys.length === 0) {
    return (
      <li>
        <code className="font-mono text-xs">{refs || group.name}</code>{" "}
        <span className="text-muted-foreground">
          — write it to the secret store yourself; it is not one this page can set.
        </span>
      </li>
    );
  }
  return (
    <li className="space-y-1.5">
      <form
        aria-label={`Secret ${group.name}`}
        className="flex flex-wrap items-end gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (filled) write.mutate();
        }}
      >
        {group.keys.map((k) => (
          <div key={k} className="flex min-w-48 flex-1 flex-col gap-1">
            <label htmlFor={`${id}-${k}`} className="font-mono text-xs">
              {group.name}#{k}
            </label>
            <Input
              id={`${id}-${k}`}
              type="password"
              autoComplete="off"
              spellCheck={false}
              value={values[k] ?? ""}
              onChange={(e) => {
                write.reset();
                setValues((v) => ({ ...v, [k]: e.target.value }));
              }}
            />
          </div>
        ))}
        {permissions.can(TOOL_WRITE) ? (
          <Button
            type="submit"
            size="sm"
            variant="outline"
            disabled={!filled}
            loading={write.isPending}
          >
            Set
          </Button>
        ) : null}
      </form>
      {write.isSuccess ? (
        <p role="status" className="flex items-center gap-1.5 text-success">
          <CircleCheck aria-hidden className="size-4" /> Set. Apply, and its server is asked for its
          tools.
        </p>
      ) : write.isError ? (
        <p role="alert" className="text-destructive">
          Not set: {write.error instanceof ApiError ? write.error.message : String(write.error)}
        </p>
      ) : permissions.isPending || permissions.can(TOOL_WRITE) ? null : (
        <p className="text-muted-foreground">Setting it needs {TOOL_WRITE} in this team.</p>
      )}
    </li>
  );
}

const TOOL_WRITE = "tool:write";

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
