/**
 * The team's runs as a table: who answered, how it went, what was asked,
 * what it cost and when. A row opens the run's trace in its session, which
 * is where a run is read.
 */
import { Link, useNavigate } from "@tanstack/react-router";

import type { Run } from "@/api/types";
import { Time } from "@/components/facts";
import { StatusPill } from "@/components/status";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { handedTo } from "@/lib/runs";
import { cn } from "@/lib/utils";

/** Where a run is read: its session's page, on the runs, with this run chosen. */
export function runLink(run: Run) {
  return {
    to: "/ui/sessions/$sessionId",
    params: { sessionId: run.session_id },
    search: { tab: "runs", run: run.id },
  } as const;
}

/** The pill a hub run carries beside its agent, as the trace draws it. */
export function HubTag() {
  return (
    <span
      data-hub
      title="Started for the team: its hub answered"
      className="rounded-full bg-card px-2 py-0.5 text-xs font-medium"
    >
      hub
    </span>
  );
}

/** Whom a run handed work to, under its agent's name; nothing when it handed none. */
export function HandedTo({ run, className }: { run: Run; className?: string }) {
  const line = handedTo(run);
  if (!line) return null;
  return (
    <span
      data-delegates
      title={`Handed work to ${(run.delegates ?? []).join(", ")}`}
      className={cn("block truncate text-xs text-muted-foreground", className)}
    >
      {line}
    </span>
  );
}

export function RunTable({ runs, label }: { runs: Run[]; label: string }) {
  const navigate = useNavigate();
  return (
    <Table aria-label={label}>
      <TableHeader>
        <tr>
          <TableHead>Agent</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Asked</TableHead>
          <TableHead numeric>Tokens</TableHead>
          <TableHead>Started</TableHead>
        </tr>
      </TableHeader>
      <TableBody>
        {runs.map((run) => (
          <TableRow key={run.id} data-run={run.id} onOpen={() => void navigate(runLink(run))}>
            <TableCell>
              <span className="flex min-w-0 items-center gap-2">
                {/* The row is the keyboard's stop; the link is for the pointer and the reader. */}
                <Link
                  {...runLink(run)}
                  tabIndex={-1}
                  className="truncate font-medium hover:underline"
                >
                  {run.agent}
                </Link>
                {run.hub ? <HubTag /> : null}
              </span>
              <HandedTo run={run} className="mt-0.5" />
            </TableCell>
            <TableCell label="Status">
              <StatusPill status={run.status} />
            </TableCell>
            <TableCell
              label="Asked"
              // Takes what the other columns leave, and cuts to fit it: a table
              // cell never shrinks below its text otherwise.
              className="text-muted-foreground sm:w-full sm:max-w-0"
            >
              {/* What a person typed: shown as text, cut to one line. */}
              <span className="block min-w-0 truncate">{run.input}</span>
            </TableCell>
            <TableCell label="Tokens" numeric>
              {(run.input_tokens + run.output_tokens).toLocaleString("en-US")}
            </TableCell>
            <TableCell label="Started" className="whitespace-nowrap text-muted-foreground">
              <Time iso={run.created_at} />
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
