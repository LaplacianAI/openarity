import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Avatar, EmptyState, LoadMore, Mono, PageHeader } from "@/components/ui-bits";
import { relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { decideSender, listChannels, listPendingSenders, listTeams } from "@/mocks/api";
import type { PendingSender } from "@/mocks/types";

export const Route = createFileRoute("/ui/approvals")({
  component: Approvals,
});

function Approvals() {
  const qc = useQueryClient();
  const [teamId, setTeamId] = useState<string>("all");
  const [channelId, setChannelId] = useState<string>("all");
  const [cursorIndex, setCursorIndex] = useState(0);
  const rowRefs = useRef<Array<HTMLTableRowElement | null>>([]);

  const teams = useQuery({ queryKey: ["teams", null], queryFn: () => listTeams() });
  const channels = useQuery({
    queryKey: ["channels", teamId],
    queryFn: () => listChannels(teamId === "all" ? null : teamId),
  });

  const filter = {
    team_id: teamId === "all" ? null : teamId,
    channel_id: channelId === "all" ? null : channelId,
  };

  const queue = useInfiniteQuery({
    queryKey: ["pending", filter.team_id, filter.channel_id],
    queryFn: ({ pageParam }) => listPendingSenders(filter, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: (last) => last.next_cursor,
  });

  const rows: PendingSender[] = queue.data?.pages.flatMap((p) => p.items) ?? [];

  const decide = useMutation({
    mutationFn: ({ id, decision }: { id: string; decision: "approve" | "reject" }) =>
      decideSender(id, decision),
    onSuccess: (_d, vars) => {
      qc.invalidateQueries({ queryKey: ["pending"] });
      qc.invalidateQueries({ queryKey: ["channels"] });
      qc.invalidateQueries({ queryKey: ["sessions"] });
      toast.success(vars.decision === "approve" ? "Sender approved" : "Sender rejected");
    },
  });

  const focusRow = useCallback((index: number) => {
    rowRefs.current[index]?.scrollIntoView({ block: "nearest" });
  }, []);

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      if (target && ["INPUT", "TEXTAREA", "SELECT"].includes(target.tagName)) return;
      if (rows.length === 0) return;
      const key = e.key.toLowerCase();
      if (key === "j" || e.key === "ArrowDown") {
        e.preventDefault();
        setCursorIndex((i) => {
          const next = Math.min(i + 1, rows.length - 1);
          focusRow(next);
          return next;
        });
      } else if (key === "k" || e.key === "ArrowUp") {
        e.preventDefault();
        setCursorIndex((i) => {
          const next = Math.max(i - 1, 0);
          focusRow(next);
          return next;
        });
      } else if (key === "a" || key === "r") {
        const row = rows[cursorIndex];
        if (!row) return;
        e.preventDefault();
        decide.mutate({ id: row.id, decision: key === "a" ? "approve" : "reject" });
        setCursorIndex((i) => Math.min(i, Math.max(0, rows.length - 2)));
      }
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, [rows, cursorIndex, decide, focusRow]);

  const teamOptions = teams.data?.items ?? [];
  const channelOptions = channels.data?.items ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Approval queue"
        description="Unknown senders who messaged an agent. Nothing reaches the agent until you approve the sender."
        actions={
          <div className="flex items-center gap-2">
            <Select
              value={teamId}
              onValueChange={(v) => {
                setTeamId(v);
                setChannelId("all");
              }}
            >
              <SelectTrigger className="h-8 w-44 text-xs" aria-label="Filter by team">
                <SelectValue placeholder="All teams" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All teams</SelectItem>
                {teamOptions.map((t) => (
                  <SelectItem key={t.id} value={t.id}>
                    {t.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Select value={channelId} onValueChange={setChannelId}>
              <SelectTrigger className="h-8 w-44 text-xs" aria-label="Filter by channel">
                <SelectValue placeholder="All channels" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="all">All channels</SelectItem>
                {channelOptions.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        }
      />

      <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground">
        <span>
          Keyboard: <Kbd>J</Kbd>/<Kbd>K</Kbd> move · <Kbd>A</Kbd> approve · <Kbd>R</Kbd> reject
        </span>
      </div>

      {queue.isPending ? (
        <SkeletonRows />
      ) : rows.length === 0 ? (
        <EmptyState
          title="Nothing waiting for a decision"
          body="When someone the platform doesn't recognise messages an agent, they appear here first. An empty queue means every sender on your channels has already been approved or rejected."
          action={
            <Button asChild size="sm" variant="outline">
              <Link to="/ui/channels">Review channels</Link>
            </Button>
          }
        />
      ) : (
        <>
          {/* A real table, not a list wearing ARIA: these rows have columns and
              per-row controls, and a <tr> already carries the semantics that
              role="row" would only describe. Selection is visual plus
              aria-selected; the window-level handler below moves it. */}
          <table className="w-full overflow-hidden rounded-md border border-border bg-card text-left">
            <caption className="sr-only">
              Senders waiting for a decision. Press J and K to move, A to approve, R to reject.
            </caption>
            <tbody className="divide-y divide-border">
              {rows.map((row, i) => (
                <tr
                  key={row.id}
                  ref={(el) => {
                    rowRefs.current[i] = el;
                  }}
                  aria-selected={i === cursorIndex}
                  className={cn(
                    "transition-colors",
                    i === cursorIndex
                      ? "bg-accent/60 shadow-[inset_3px_0_0_0_var(--color-primary)]"
                      : "hover:bg-secondary/60",
                  )}
                >
                  <td className="w-9 py-2 pl-3">
                    <Avatar name={row.sender_label ?? row.sender_address} />
                  </td>
                  <td className="min-w-0 py-2">
                    <div className="flex items-baseline gap-2">
                      <span className="truncate text-sm font-medium">
                        {row.sender_label ?? row.sender_address}
                      </span>
                      {row.sender_label ? <Mono>{row.sender_address}</Mono> : null}
                    </div>
                    <p className="truncate text-xs text-muted-foreground">
                      {row.last_message_preview}
                    </p>
                  </td>
                  <td className="hidden w-40 py-2 text-right text-xs text-muted-foreground lg:table-cell">
                    <div className="truncate">{row.channel_name}</div>
                    <div className="font-mono">
                      {row.message_count} msg · {relativeTime(row.first_seen_at)}
                    </div>
                  </td>
                  <td className="py-2 pr-3 pl-3">
                    <div className="flex items-center justify-end gap-1.5">
                      <Button
                        size="sm"
                        className="h-7 px-2.5 text-xs"
                        disabled={decide.isPending}
                        onClick={() => decide.mutate({ id: row.id, decision: "approve" })}
                      >
                        Approve
                      </Button>
                      <Button
                        size="sm"
                        variant="outline"
                        className="h-7 px-2.5 text-xs"
                        disabled={decide.isPending}
                        onClick={() => decide.mutate({ id: row.id, decision: "reject" })}
                      >
                        Reject
                      </Button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <LoadMore
            hasMore={Boolean(queue.hasNextPage)}
            loading={queue.isFetchingNextPage}
            onClick={() => queue.fetchNextPage()}
            loadedLabel={`${rows.length} senders loaded`}
          />
        </>
      )}
    </div>
  );
}

function Kbd({ children }: { children: React.ReactNode }) {
  return (
    <kbd className="rounded-sm border border-border bg-surface px-1 py-0.5 font-mono text-[10px] text-foreground">
      {children}
    </kbd>
  );
}

const SKELETON_ROWS = ["a", "b", "c", "d", "e", "f"];

function SkeletonRows() {
  return (
    <table className="w-full overflow-hidden rounded-md border border-border bg-card">
      <caption className="sr-only">Loading the approval queue</caption>
      <tbody className="divide-y divide-border">
        {SKELETON_ROWS.map((key) => (
          <tr key={key}>
            <td className="w-9 py-3 pl-3">
              <span className="block size-7 animate-pulse rounded-full bg-muted" />
            </td>
            <td className="py-3 pr-3">
              <span className="block h-3 animate-pulse rounded bg-muted" />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
