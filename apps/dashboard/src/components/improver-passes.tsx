/**
 * The improver's passes: one row for each time it read an agent's recurring
 * failures, with why it ran, what came of it, and, opened, what it reported.
 * Only an approver may read them; the brain refuses everyone else, and the
 * screen does not offer the tab to them.
 */
import { useInfiniteQuery } from "@tanstack/react-query";
import { ChevronRight, Loader2, XCircle } from "lucide-react";
import { Fragment, useId, useState } from "react";

import { listImproverPasses } from "@/api";
import { nextCursorOf } from "@/api/client";
import type { ImproverPass } from "@/api/types";
import { Time } from "@/components/facts";
import { ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState, LoadMore, Mono } from "@/components/ui-bits";
import {
  anyPassRunning,
  efficiencyReason,
  passOutcome,
  prunedWords,
  signatureWords,
} from "@/lib/improvements";
import { count } from "@/lib/runs";
import { cn } from "@/lib/utils";

/** "An efficiency pass, from 34 good runs: the same call made twice · crm__lookup". */
function EfficiencyWhy({ pass }: { pass: ImproverPass }) {
  const reasons = pass.signatures.map(efficiencyReason).filter((r) => r !== null);
  return (
    <>
      {pass.runs_seen != null
        ? `An efficiency pass, from ${count(pass.runs_seen, "good run")}`
        : "An efficiency pass"}
      {reasons.length > 0 ? ": " : null}
      {reasons.map((r, i) => (
        // A pass names each reason once, so the signature is the key.
        <Fragment key={pass.signatures[i]}>
          {i > 0 ? ", " : null}
          {r.words}
          {r.target ? (
            <>
              {" · "}
              <Mono>{r.target}</Mono>
            </>
          ) : null}
        </Fragment>
      ))}
    </>
  );
}

/** "A pattern: tool · crm__close_ticket · misused tool", or "The daily pass, after 5 runs", and how many prune candidates it looked at. */
function Why({ pass }: { pass: ImproverPass }) {
  const sigs = pass.signatures.map(signatureWords);
  return (
    <>
      {pass.trigger === "efficiency" ? (
        <EfficiencyWhy pass={pass} />
      ) : pass.trigger === "daily" ? (
        pass.runs_seen != null ? (
          `The daily pass, after ${count(pass.runs_seen, "judged run")}`
        ) : (
          "The daily pass"
        )
      ) : sigs.length === 0 ? (
        "A pattern"
      ) : (
        <>
          A pattern:{" "}
          {sigs.map((s, i) => (
            // A pass names each signature once, so the signature is the key.
            <Fragment key={pass.signatures[i]}>
              {i > 0 ? ", " : null}
              {s.part}
              {s.target ? (
                <>
                  {" · "}
                  <Mono>{s.target}</Mono>
                </>
              ) : null}
              {s.cause ? ` · ${s.cause}` : null}
            </Fragment>
          ))}
        </>
      )}
      {pass.pruned.length > 0
        ? `, ${count(pass.pruned.length, "prune candidate")} looked at`
        : null}
    </>
  );
}

/**
 * The prune candidates a pass looked at: unused in the agent's last 50 runs.
 * Each says whether the pass proposed removing it or kept it.
 */
function PruneLooks({ pass }: { pass: ImproverPass }) {
  const proposed = new Set(pass.prune_proposed ?? []);
  return (
    <div className="space-y-1 text-xs text-muted-foreground">
      <p>Prune candidates it looked at, unused in the agent's last 50 runs:</p>
      <ul aria-label="Prune candidates" className="flex flex-wrap gap-x-3 gap-y-1">
        {pass.pruned.map((c) => (
          <li key={c} data-prune={proposed.has(c) ? "proposed" : "kept"}>
            {prunedWords(c)}{" "}
            <span className={proposed.has(c) ? "text-foreground" : undefined}>
              ({proposed.has(c) ? "proposed removing" : "kept"})
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}

function Outcome({ pass }: { pass: ImproverPass }) {
  const text = passOutcome(pass);
  if (pass.status === "running") {
    return (
      <p className="flex items-start gap-1.5 text-sm">
        <Loader2 aria-hidden className="mt-0.5 size-3.5 shrink-0 animate-spin text-running" />
        <span>{text}</span>
      </p>
    );
  }
  if (pass.status === "failed") {
    return (
      <p className="flex items-start gap-1.5 text-sm text-failed">
        <XCircle aria-hidden className="mt-0.5 size-3.5 shrink-0" />
        <span className="min-w-0 break-words">{text}</span>
      </p>
    );
  }
  return <p className="text-sm">{text}</p>;
}

export function ImproverPassRow({ pass }: { pass: ImproverPass }) {
  const [open, setOpen] = useState(false);
  const reportId = useId();
  const tokens = pass.input_tokens + pass.output_tokens;
  return (
    <div
      data-pass={pass.id}
      data-status={pass.status}
      className="max-w-[75ch] space-y-2 rounded-lg bg-card px-4 py-3"
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={open ? reportId : undefined}
        onClick={() => setOpen(!open)}
        className="-mx-1 flex w-[calc(100%+0.5rem)] min-w-0 items-start gap-1.5 rounded px-1 text-left hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
      >
        <ChevronRight
          aria-hidden
          className={cn(
            "mt-0.5 size-4 shrink-0 text-muted-foreground transition-transform duration-(--dur-press) ease-(--ease-out) motion-reduce:transition-none",
            open && "rotate-90",
          )}
        />
        <span className="min-w-0 text-sm break-words">
          <span className="font-medium">{pass.agent.name}</span>
          <span className="text-muted-foreground">
            {" · "}
            <Why pass={pass} />
          </span>
        </span>
      </button>
      <div className="space-y-1 pl-[1.375rem]">
        <Outcome pass={pass} />
        <p className="flex flex-wrap items-baseline gap-x-2 text-xs text-muted-foreground">
          <Time iso={pass.started_at} />
          {/* A pass counts its tokens as each try ends: none yet is not a count. */}
          {tokens > 0 ? (
            <>
              <span aria-hidden>·</span>
              <span className="tabular-nums">{tokens.toLocaleString("en-US")} tokens</span>
            </>
          ) : null}
        </p>
      </div>
      {open ? (
        <div id={reportId} className="space-y-2 pl-[1.375rem]">
          {pass.pruned.length > 0 ? <PruneLooks pass={pass} /> : null}
          {pass.report_withheld ? (
            <p className="text-sm text-muted-foreground">
              The report draws on a conversation you can't read, so it isn't shown. Someone who can
              read every conversation sees it.
            </p>
          ) : pass.report.trim() === "" ? (
            <p className="text-sm text-muted-foreground">
              {pass.status === "running" ? "No report yet." : "It left no report."}
            </p>
          ) : (
            <p className="max-w-[75ch] text-sm break-words whitespace-pre-wrap">{pass.report}</p>
          )}
          {tokens > 0 ? (
            <p className="text-xs text-muted-foreground tabular-nums">
              {pass.model} · {pass.input_tokens.toLocaleString("en-US")} in ·{" "}
              {pass.output_tokens.toLocaleString("en-US")} out
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** Rows being read: the shape of one, so the page does not jump when they come. */
function Loading() {
  return (
    <div className="space-y-2" role="status" aria-label="Loading passes">
      {[0, 1, 2].map((i) => (
        <div key={i} className="max-w-[75ch] space-y-2 rounded-lg bg-card px-4 py-3">
          <Skeleton className="h-4 w-64 max-w-full" />
          <Skeleton className="h-4 w-40" />
        </div>
      ))}
    </div>
  );
}

/** How often passes are read again while one is running. */
const PASS_REFRESH_MS = 15_000;

export function ImproverPasses({ teamId, agent }: { teamId: string; agent: string | undefined }) {
  const list = useInfiniteQuery({
    queryKey: ["improver-passes", teamId, agent ?? null],
    queryFn: ({ pageParam, signal }) =>
      listImproverPasses(teamId, agent ? { agent } : {}, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    // A pass under way ends within minutes: its row is read again until it has.
    refetchInterval: (query) => (anyPassRunning(query.state.data?.pages) ? PASS_REFRESH_MS : false),
  });
  const rows = list.data?.pages.flatMap((p) => p.items) ?? [];

  if (list.isPending) return <Loading />;
  if (list.isError) {
    return (
      <ErrorState
        what="the passes"
        error={list.error}
        onRetry={() => list.refetch()}
        retrying={list.isRefetching}
      />
    );
  }
  if (rows.length === 0) {
    return agent ? (
      <EmptyState title="Nothing matches" body="The improver has made no pass for this agent." />
    ) : (
      <EmptyState
        title="No passes yet"
        body="The improver runs when a pattern recurs in three runs, and once a day."
      />
    );
  }
  return (
    <div>
      <ul aria-label="Improver passes" className="space-y-2">
        {rows.map((p) => (
          <li key={p.id}>
            <ImproverPassRow pass={p} />
          </li>
        ))}
      </ul>
      <LoadMore
        hasMore={Boolean(list.hasNextPage)}
        loading={list.isFetchingNextPage}
        onClick={() => list.fetchNextPage()}
        loadedLabel={`${count(rows.length, "pass", "passes")} loaded`}
      />
    </div>
  );
}
