import { useInfiniteQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";

import { downloadAttachment, listAttachments, listMessages, listSessions } from "@/api";
import { nextCursorOf } from "@/api/client";
import { useCurrentTeam } from "@/api/session";
import type { Attachment, Session } from "@/api/types";
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
        </div>
      </DialogContent>
    </Dialog>
  );
}
