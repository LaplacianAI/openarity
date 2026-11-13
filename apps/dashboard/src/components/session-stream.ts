/**
 * Follows a session's stream for as long as the conversation is on screen.
 *
 * What was read is kept in the query cache under the session, so coming back
 * to it resumes from the last frame seen rather than replaying the whole
 * conversation; leaving aborts the stream, which otherwise never ends.
 */
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";

import { followSessionEvents } from "@/api";
import { type Conversation, empty, fold } from "@/lib/session-stream";

export type Followed = {
  conversation: Conversation;
  /** The stream has not answered yet, and nothing was read before. */
  loading: boolean;
  /** The connection dropped, and it is being followed again. */
  reconnecting: boolean;
  /** Why following stopped: a refusal, or too many drops in a row. */
  failed: unknown;
  /** Follow it again after a failure. */
  retry: () => void;
};

export const sessionStreamKey = (teamId: string, sessionId: string) => [
  "session-stream",
  teamId,
  sessionId,
];

export function useSessionStream(teamId: string, sessionId: string): Followed {
  const qc = useQueryClient();
  const key = useMemo(() => sessionStreamKey(teamId, sessionId), [teamId, sessionId]);
  const [conversation, setConversation] = useState<Conversation>(
    () => qc.getQueryData<Conversation>(key) ?? empty,
  );
  const [reconnecting, setReconnecting] = useState(false);
  // A conversation with nothing in it sends no frame, so loading ends when
  // the stream answers, not when the first frame does.
  const [connected, setConnected] = useState(false);
  const [failed, setFailed] = useState<unknown>(null);
  const [attempt, setAttempt] = useState(0);

  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt restarts the stream on retry
  useEffect(() => {
    let read = qc.getQueryData<Conversation>(key) ?? empty;
    setConversation(read);
    setFailed(null);
    setReconnecting(false);
    setConnected(false);

    const abort = new AbortController();
    followSessionEvents(
      teamId,
      sessionId,
      (env) => {
        const next = fold(read, env);
        if (next === read) return;
        read = next;
        qc.setQueryData<Conversation>(key, read);
        setConversation(read);
      },
      abort.signal,
      read.seen,
      (waiting) => {
        setReconnecting(waiting);
        if (!waiting) setConnected(true);
      },
    ).catch((err: unknown) => {
      // Aborted by this effect's own cleanup, which is not a failure.
      if (abort.signal.aborted) return;
      setReconnecting(false);
      setFailed(err);
    });
    return () => abort.abort();
  }, [qc, key, teamId, sessionId, attempt]);

  return {
    conversation,
    loading: conversation.seen === null && !connected && failed === null,
    reconnecting,
    failed,
    retry: () => setAttempt((a) => a + 1),
  };
}
