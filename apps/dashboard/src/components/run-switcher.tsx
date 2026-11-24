import { Link } from "@tanstack/react-router";
import { ChevronDown, ChevronsUpDown, ChevronUp } from "lucide-react";
import { useState } from "react";
import type { Run } from "@/api/types";
import { Time } from "@/components/facts";
import { HandedTo } from "@/components/run-table";
import { StatusPill } from "@/components/status";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { cn } from "@/lib/utils";

/** What a run spent, as its row in the list says it. */
function tokensOf(run: Run): string {
  return `${(run.input_tokens + run.output_tokens).toLocaleString("en-US")} tokens`;
}

/**
 * The session's runs as one line over the run that is shown: which run it is,
 * newer and older beside it, and the whole list from the line itself. It
 * takes one line where a list of every run pushed the trace down the page.
 * Choosing a run writes it into the URL, so the trace can be linked to, and
 * leaves where the page was scrolled alone.
 */
export function RunSwitcher({
  sessionId,
  rows,
  chosen,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  sessionId: string;
  /** The runs read so far, newest first. */
  rows: readonly Run[];
  /** The run shown; it may be one linked to that is not among the rows read. */
  chosen: Run | undefined;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  const [open, setOpen] = useState(false);
  const at = chosen ? rows.findIndex((r) => r.id === chosen.id) : -1;
  const newer = at > 0 ? rows[at - 1] : undefined;
  const older = at >= 0 && at < rows.length - 1 ? rows[at + 1] : undefined;
  const count = `${rows.length}${hasMore ? "+" : ""}`;

  const step = (run: Run | undefined, label: string, Icon: typeof ChevronUp) =>
    run ? (
      <Button variant="ghost" size="icon" className="size-8" asChild>
        <Link
          to="/ui/sessions/$sessionId"
          params={{ sessionId }}
          search={(prev) => ({ ...prev, tab: "runs", run: run.id })}
          replace
          resetScroll={false}
          aria-label={label}
          title={label}
        >
          <Icon aria-hidden className="size-4" />
        </Link>
      </Button>
    ) : (
      <Button variant="ghost" size="icon" className="size-8" disabled aria-label={label}>
        <Icon aria-hidden className="size-4" />
      </Button>
    );

  return (
    <div data-run-switcher className="flex min-w-0 items-center gap-1 rounded-lg bg-card p-1">
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={
              chosen
                ? `Run by ${chosen.agent}, ${at >= 0 ? `${at + 1} of ${count}, newest first` : "not among the runs listed"}. Choose another run`
                : "Choose a run"
            }
            className="flex min-h-9 min-w-0 flex-1 items-center gap-x-3 rounded-md px-2.5 text-left transition-colors hover:bg-sheet/60 focus-visible:outline-2 focus-visible:outline-offset-[-2px] data-[state=open]:bg-sheet"
          >
            {chosen ? (
              <>
                <span className="min-w-0 flex-1 truncate text-sm font-medium">{chosen.agent}</span>
                <StatusPill status={chosen.status} />
                <span className="hidden text-xs text-muted-foreground tabular-nums sm:inline">
                  <Time iso={chosen.created_at} />
                </span>
                {at >= 0 ? (
                  <span className="text-xs text-muted-foreground tabular-nums">
                    {at + 1} of {count}
                  </span>
                ) : null}
              </>
            ) : (
              <span className="flex-1 text-sm text-muted-foreground">Choose a run</span>
            )}
            <ChevronsUpDown aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          className="w-(--radix-dropdown-menu-trigger-width) min-w-72 max-h-[min(24rem,var(--radix-dropdown-menu-content-available-height))]"
        >
          <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">
            The session's runs, newest first
          </DropdownMenuLabel>
          {rows.map((r) => (
            <DropdownMenuItem key={r.id} asChild>
              <Link
                to="/ui/sessions/$sessionId"
                params={{ sessionId }}
                search={(prev) => ({ ...prev, tab: "runs", run: r.id })}
                replace
                resetScroll={false}
                data-run={r.id}
                aria-current={r.id === chosen?.id ? "page" : undefined}
                className={cn(
                  "flex flex-wrap items-center gap-x-3 gap-y-0.5 py-2",
                  r.id === chosen?.id && "bg-sheet shadow-[inset_0_0_0_1px_var(--color-primary)]",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span data-agent className="block truncate font-medium">
                    {r.agent}
                  </span>
                  <HandedTo run={r} />
                </span>
                <StatusPill status={r.status} />
                <span className="flex basis-full gap-3 text-xs text-muted-foreground tabular-nums">
                  <span>{tokensOf(r)}</span>
                  <Time iso={r.created_at} />
                </span>
              </Link>
            </DropdownMenuItem>
          ))}
          {hasMore ? (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                disabled={loadingMore}
                onSelect={(e) => {
                  // Stays open, so the runs it brings can be chosen.
                  e.preventDefault();
                  onLoadMore();
                }}
                className="justify-center text-muted-foreground"
              >
                {loadingMore ? "Loading…" : "Load older runs"}
              </DropdownMenuItem>
            </>
          ) : null}
        </DropdownMenuContent>
      </DropdownMenu>
      {step(newer, "Newer run", ChevronUp)}
      {hasMore && at === rows.length - 1 ? (
        <Button
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label="Older run"
          title="Older run"
          disabled={loadingMore}
          onClick={onLoadMore}
        >
          <ChevronDown aria-hidden className="size-4" />
        </Button>
      ) : (
        step(older, "Older run", ChevronDown)
      )}
    </div>
  );
}
