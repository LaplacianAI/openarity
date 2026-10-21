import { useInfiniteQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { type ReactNode, useEffect, useRef, useState } from "react";

import {
  cancelRun,
  downloadAttachment,
  followRunEvents,
  listAttachments,
  listMessages,
  listSessionRuns,
  listSessions,
} from "@/api";
import { nextCursorOf } from "@/api/client";
import { usePermissions } from "@/api/permissions";
import { useCurrentTeam } from "@/api/session";
import type { Attachment, Run, RunEvent, Session } from "@/api/types";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { EmptyState, LoadMore, Mono, PageHeader, StatusDot } from "@/components/ui-bits";
import { absoluteTime, fileSize, relativeTime } from "@/lib/format";

export const Route = createFileRoute("/ui/sessions/")({
  component: Sessions,
});

// The brain's status vocabulary, mapped to the three tones the dot knows.
// Anything unrecognised is drawn as closed rather than guessed at.
function toneOf(status: string): "active" | "idle" | "closed" {
  if (status === "active" || status === "open") return "active";
  if (status === "idle" || status === "waiting") return "idle";
  return "closed";
}

function Sessions() {
  const { teamId } = useCurrentTeam();
  const [opened, setOpened] = useState<Session | null>(null);

  const sessions = useInfiniteQuery({
    queryKey: ["sessions", teamId, "paged"],
    queryFn: ({ pageParam }) => listSessions(teamId as string, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: Boolean(teamId),
  });

  const rows = sessions.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Sessions"
        description="A conversation between someone on a channel and an agent."
      />

      {!teamId ? (
        <EmptyState title="No team" body="Join or create a team to see its sessions." />
      ) : sessions.isPending ? (
        <p className="text-sm text-muted-foreground">Loading sessions…</p>
      ) : rows.length === 0 ? (
        <EmptyState
          title="No sessions yet"
          body="A session begins the first time an approved sender writes in on a channel. An empty list means nothing has been received, or nobody has been approved to send."
        />
      ) : (
        <>
          <table className="w-full overflow-hidden rounded-md border border-border bg-card text-left">
            <caption className="sr-only">Sessions in this team</caption>
            <tbody className="divide-y divide-border">
              {rows.map((s) => (
                <tr
                  key={s.id}
                  className="cursor-pointer hover:bg-secondary/60"
                  onClick={() => setOpened(s)}
                >
                  <td className="w-6 py-2 pl-3">
                    <StatusDot tone={toneOf(s.status)} />
                  </td>
                  <td className="min-w-0 py-2">
                    {/* The row is the mouse target; this is the keyboard one. */}
                    <button
                      type="button"
                      className="block max-w-full truncate text-left text-sm font-medium hover:underline focus-visible:underline focus-visible:outline-none"
                      onClick={(e) => {
                        e.stopPropagation();
                        setOpened(s);
                      }}
                    >
                      {s.provider_ref}
                    </button>
                    <p className="text-xs text-muted-foreground">
                      {s.kind} · started {relativeTime(s.started_at)} · last message{" "}
                      {relativeTime(s.last_message_at)}
                    </p>
                  </td>
                  <td className="py-2 pr-3 text-right">
                    <Mono>{s.status}</Mono>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          <LoadMore
            hasMore={Boolean(sessions.hasNextPage)}
            loading={sessions.isFetchingNextPage}
            onClick={() => sessions.fetchNextPage()}
            loadedLabel={`${rows.length} session${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}

      {teamId && opened ? (
        <SessionDialog teamId={teamId} session={opened} onClose={() => setOpened(null)} />
      ) : null}
    </div>
  );
}

/**
 * One conversation, read in place. The brain pages messages newest first, so
 * every page it hands over is older than the last: the whole list is reversed
 * to read top to bottom like a chat, and "Load more" sits above it because
 * what it brings back is earlier.
 */
/**
 * Saves what a stranger may have sent. Retyped as plain bytes and handed over
 * as a download, so the browser never renders it on this origin, whatever
 * type it claims.
 */
function save(bytes: Blob, filename: string): void {
  const url = URL.createObjectURL(bytes.slice(0, bytes.size, "application/octet-stream"));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.rel = "noopener";
  a.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function SessionDialog({
  teamId,
  session,
  onClose,
}: {
  teamId: string;
  session: Session;
  onClose: () => void;
}) {
  const messages = useInfiniteQuery({
    queryKey: ["sessions", teamId, session.id, "messages"],
    queryFn: ({ pageParam }) => listMessages(teamId, session.id, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const attachments = useInfiniteQuery({
    queryKey: ["sessions", teamId, session.id, "attachments"],
    queryFn: ({ pageParam }) => listAttachments(teamId, session.id, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });

  const [failed, setFailed] = useState<string | null>(null);
  const download = async (f: Attachment) => {
    setFailed(null);
    try {
      save(await downloadAttachment(teamId, session.id, f.id), f.filename);
    } catch (err) {
      setFailed(`${f.filename}: ${err instanceof Error ? err.message : String(err)}`);
    }
  };

  const said = (messages.data?.pages.flatMap((p) => p.items) ?? []).slice().reverse();
  const files = attachments.data?.pages.flatMap((p) => p.items) ?? [];

  return (
    <Dialog open onOpenChange={(o) => (o ? null : onClose())}>
      <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="truncate pr-6">{session.provider_ref}</DialogTitle>
          <DialogDescription>
            {session.kind} · {session.status} · started {relativeTime(session.started_at)}
          </DialogDescription>
        </DialogHeader>

        <div className="min-h-0 flex-1 space-y-4 overflow-y-auto">
          {messages.isPending ? (
            <p className="text-sm text-muted-foreground">Loading messages…</p>
          ) : messages.isError ? (
            <p role="alert" className="text-sm text-destructive">
              {messages.error.message}
            </p>
          ) : said.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing has been said here.</p>
          ) : (
            <>
              {messages.hasNextPage ? (
                <LoadMore
                  hasMore
                  loading={messages.isFetchingNextPage}
                  onClick={() => messages.fetchNextPage()}
                  loadedLabel={`${said.length} message${said.length === 1 ? "" : "s"} loaded`}
                />
              ) : null}
              <ol className="space-y-3">
                {said.map((m) => (
                  <li
                    key={m.id}
                    data-message
                    className="rounded-md border border-border bg-card px-3 py-2"
                  >
                    <div className="flex items-baseline justify-between gap-3 text-xs text-muted-foreground">
                      <Mono>{m.user_id}</Mono>
                      <time dateTime={m.received_at}>{absoluteTime(m.received_at)}</time>
                    </div>
                    {/* A person typed this: it is shown as text, never parsed. */}
                    <p data-text className="mt-1 whitespace-pre-wrap break-words text-sm">
                      {m.text}
                    </p>
                  </li>
                ))}
              </ol>
            </>
          )}

          {messages.isSuccess ? (
            attachments.isError ? (
              <p role="alert" className="text-xs text-destructive">
                {attachments.error.message}
              </p>
            ) : files.length > 0 ? (
              <section className="space-y-2 border-t border-border pt-3">
                <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  Attachments
                </h3>
                <ul className="space-y-1 text-sm">
                  {files.map((f) => (
                    <li key={f.id} className="flex items-baseline justify-between gap-3">
                      <button
                        type="button"
                        className="truncate text-left underline-offset-2 hover:underline"
                        aria-label={`Download ${f.filename}`}
                        onClick={() => void download(f)}
                      >
                        {f.filename}
                      </button>
                      <span className="shrink-0 text-xs text-muted-foreground">
                        {f.media_type} · {fileSize(f.size_bytes)}
                      </span>
                    </li>
                  ))}
                </ul>
                {failed ? (
                  <p role="alert" className="text-xs text-destructive">
                    {failed}
                  </p>
                ) : null}
                {attachments.hasNextPage ? (
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={attachments.isFetchingNextPage}
                    onClick={() => attachments.fetchNextPage()}
                  >
                    {attachments.isFetchingNextPage ? "Loading…" : "More attachments"}
                  </Button>
                ) : null}
              </section>
            ) : null
          ) : null}

          <RunsSection teamId={teamId} sessionId={session.id} />
        </div>
      </DialogContent>
    </Dialog>
  );
}

const RUN_WRITE = "run:write";

function RunsSection({ teamId, sessionId }: { teamId: string; sessionId: string }) {
  const qc = useQueryClient();
  const canCancel = usePermissions(teamId).can(RUN_WRITE);
  const [open, setOpen] = useState<string | null>(null);
  const key = ["sessions", teamId, sessionId, "runs"];

  const runs = useInfiniteQuery({
    queryKey: key,
    queryFn: ({ pageParam }) => listSessionRuns(teamId, sessionId, pageParam),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
  });
  const rows = runs.data?.pages.flatMap((p) => p.items) ?? [];
  const changed = () => qc.invalidateQueries({ queryKey: key });

  return (
    <section aria-label="Runs" className="space-y-2 border-t border-border pt-3">
      <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Runs</h3>
      {runs.isPending ? (
        <p className="text-sm text-muted-foreground">Loading runs…</p>
      ) : runs.isError ? (
        <p role="alert" className="text-xs text-destructive">
          {runs.error.message}
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">No agent has run in this session.</p>
      ) : (
        <>
          <ul className="divide-y divide-border rounded-md border border-border">
            {rows.map((r) => (
              <RunRow
                key={r.id}
                teamId={teamId}
                run={r}
                open={open === r.id}
                canCancel={canCancel}
                onToggle={() => setOpen(open === r.id ? null : r.id)}
                onChanged={changed}
              />
            ))}
          </ul>
          <LoadMore
            hasMore={Boolean(runs.hasNextPage)}
            loading={runs.isFetchingNextPage}
            onClick={() => runs.fetchNextPage()}
            loadedLabel={`${rows.length} run${rows.length === 1 ? "" : "s"} loaded`}
          />
        </>
      )}
    </section>
  );
}

function runTone(status: string): "active" | "idle" | "closed" {
  if (status === "running") return "active";
  if (status === "queued") return "idle";
  return "closed";
}

function RunRow({
  teamId,
  run,
  open,
  canCancel,
  onToggle,
  onChanged,
}: {
  teamId: string;
  run: Run;
  open: boolean;
  canCancel: boolean;
  onToggle: () => void;
  onChanged: () => void;
}) {
  const live = run.status === "queued" || run.status === "running";
  const cancel = useMutation({
    mutationFn: () => cancelRun(teamId, run.id),
    onSuccess: onChanged,
  });

  return (
    <li data-run={run.id}>
      <div className="flex items-center gap-3 px-3 py-2">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          className="min-w-0 flex-1 text-left"
        >
          <div className="flex items-center gap-2">
            <StatusDot tone={runTone(run.status)} />
            <span className="truncate text-sm font-medium">{run.agent}</span>
            <Mono>{run.status}</Mono>
          </div>
          <p className="text-xs text-muted-foreground">
            started {relativeTime(run.created_at)} · {run.input_tokens} in / {run.output_tokens} out
            tokens
          </p>
        </button>
        {live && canCancel ? (
          <Button
            variant="outline"
            size="sm"
            disabled={cancel.isPending}
            onClick={() => cancel.mutate()}
          >
            Cancel
          </Button>
        ) : null}
      </div>
      {cancel.isError ? (
        <p role="alert" className="px-3 pb-2 text-xs text-destructive">
          {cancel.error.message}
        </p>
      ) : null}
      {open ? (
        <div className="space-y-2 border-t border-border bg-surface px-3 py-2 text-sm">
          <Labelled label="Input">{run.input}</Labelled>
          {run.output !== null ? <Labelled label="Output">{run.output}</Labelled> : null}
          {run.error !== null ? <Labelled label="Error">{run.error}</Labelled> : null}
          <RunTrace teamId={teamId} runId={run.id} onEnded={live ? onChanged : undefined} />
        </div>
      ) : null}
    </li>
  );
}

function Labelled({ label, children }: { label: string; children: string }) {
  return (
    <div>
      <p className="text-xs font-semibold text-muted-foreground">{label}</p>
      <p className="whitespace-pre-wrap break-words">{children}</p>
    </div>
  );
}

/**
 * What a run did, followed for as long as this is mounted. Collapsing the run
 * or closing the dialog unmounts it, which is what stops the stream: a
 * running run's stream otherwise stays open for as long as the run does.
 */
function RunTrace({
  teamId,
  runId,
  onEnded,
}: {
  teamId: string;
  runId: string;
  onEnded?: (() => void) | undefined;
}) {
  const [events, setEvents] = useState<RunEvent[]>([]);
  const [ended, setEnded] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null);

  // A ref, because onEnded is a fresh closure every render and restarting the
  // stream for it would replay every event.
  const onEndedRef = useRef(onEnded);
  onEndedRef.current = onEnded;

  useEffect(() => {
    const abort = new AbortController();
    setEvents([]);
    setEnded(null);
    setFailed(null);
    followRunEvents(
      teamId,
      runId,
      (e) => {
        if (e.kind === "end") {
          setEnded(textOf(e.data, "status") || "ended");
          onEndedRef.current?.();
        } else {
          setEvents((prev) => [...prev, e]);
        }
      },
      abort.signal,
    ).catch((err: unknown) => {
      if (abort.signal.aborted) return;
      setFailed(err instanceof Error ? err.message : String(err));
    });
    return () => abort.abort();
  }, [teamId, runId]);

  return (
    <div>
      <p className="text-xs font-semibold text-muted-foreground">Trace</p>
      {events.length === 0 && !ended && !failed ? (
        <p className="text-xs text-muted-foreground">Waiting for events…</p>
      ) : null}
      {/* Everything here came from a model or a tool: text only, never markup. */}
      <ol className="space-y-1">
        {events.map((e, i) => (
          <li key={e.seq ?? `i${i}`} data-event={e.kind} className="text-xs">
            <EventLine event={e} />
          </li>
        ))}
      </ol>
      {failed ? (
        <p role="alert" className="text-xs text-destructive">
          {failed}
        </p>
      ) : ended ? (
        <p className="text-xs text-muted-foreground">Ended: {ended}</p>
      ) : events.length > 0 ? (
        <p className="text-xs text-muted-foreground">Live…</p>
      ) : null}
    </div>
  );
}

function field(data: unknown, key: string): unknown {
  return data !== null && typeof data === "object"
    ? (data as Record<string, unknown>)[key]
    : undefined;
}

function textOf(data: unknown, key: string): string {
  const v = field(data, key);
  return typeof v === "string" ? v : typeof v === "number" ? String(v) : "";
}

function listOf(data: unknown, key: string): string {
  const v = field(data, key);
  return Array.isArray(v) && v.length > 0 ? v.map(String).join(", ") : "none";
}

const SHORT = 300;

function short(text: string): string {
  return text.length > SHORT ? `${text.slice(0, SHORT)}…` : text;
}

function EventLine({ event }: { event: RunEvent }) {
  const d = event.data;
  const cut = field(d, "truncated") === true ? " (truncated)" : "";
  switch (event.kind) {
    case "started":
      return (
        <Line kind="started">
          {textOf(d, "agent")} on {textOf(d, "model")} ({textOf(d, "pattern")}) · tools:{" "}
          {listOf(d, "tools")} · skills: {listOf(d, "skills")}
        </Line>
      );
    case "resumed":
      return <Line kind="resumed">with {textOf(d, "messages")} messages</Line>;
    case "step":
      return <Line kind="step">{textOf(d, "step")}</Line>;
    case "tool_call":
      return (
        <Line kind="tool call">
          <span className="font-medium">{textOf(d, "name")}</span>
          <Block>{textOf(d, "arguments") + cut}</Block>
        </Line>
      );
    case "tool_result": {
      const error = textOf(d, "error");
      return (
        <Line kind="tool result">
          <span className="font-medium">{textOf(d, "name")}</span> in {textOf(d, "duration_ms")} ms
          {error ? (
            <Block className="text-destructive">failed: {error}</Block>
          ) : (
            <Block>{short(textOf(d, "output")) + cut}</Block>
          )}
        </Line>
      );
    }
    case "tool_unavailable":
      return (
        <Line kind="tool unavailable">
          {textOf(d, "name")}: {textOf(d, "reason")}
        </Line>
      );
    case "usage":
      return (
        <Line kind="usage">
          {textOf(d, "model")} · {textOf(d, "input_tokens")} in / {textOf(d, "output_tokens")} out (
          {textOf(d, "cached_input_tokens")} cached) · {textOf(d, "spent")} spent
        </Line>
      );
    case "finished": {
      const error = textOf(d, "error");
      return (
        <Line kind="finished">
          {textOf(d, "status")}
          {error ? <Block className="text-destructive">{error}</Block> : null}
        </Line>
      );
    }
    default:
      return (
        <Line kind={event.kind}>
          <Block>{typeof d === "string" ? d : JSON.stringify(d)}</Block>
        </Line>
      );
  }
}

function Line({ kind, children }: { kind: string; children: ReactNode }) {
  return (
    <div>
      <Mono className="mr-2">{kind}</Mono>
      {children}
    </div>
  );
}

function Block({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={`block whitespace-pre-wrap break-words font-mono ${className ?? ""}`}>
      {children}
    </span>
  );
}
