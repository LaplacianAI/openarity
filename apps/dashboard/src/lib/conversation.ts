/**
 * A session's conversation: what people said and what its agents were asked
 * and answered, in one order.
 *
 * The brain keeps the two apart. A message is what arrived on a channel; an
 * agent's answer is a run's output, and is never written back as a message.
 * Both are paged newest first, so a page of one may reach further back than
 * the other has been read. A turn older than what both lists have loaded
 * could have something unread before it, so it is held back until the list
 * that stops short is read further.
 */
import type { Message, Run } from "@/api/types";
import { explainRunError } from "@/lib/run-error";

export type Turn =
  | { kind: "message"; at: number; message: Message }
  | { kind: "run"; at: number; run: Run };

/** One paged list as read so far: its items, newest first, and whether more are older. */
export type Read<T> = { items: T[]; more: boolean };

export type Conversation = {
  /** Oldest first, as a conversation reads. */
  turns: Turn[];
  /** The lists to read further before anything held back can be shown. */
  readFurther: { messages: boolean; runs: boolean };
};

/**
 * The earliest instant the conversation can be shown from: every list with
 * more to read is complete only back to its oldest loaded item, so the later
 * of those is where the merge can start. -Infinity when both are read whole.
 */
function horizon(oldest: { more: boolean; at: number | null }[]): number {
  let h = Number.NEGATIVE_INFINITY;
  for (const l of oldest) {
    if (!l.more) continue;
    // More to read and nothing read yet: nothing can be placed.
    h = Math.max(h, l.at ?? Number.POSITIVE_INFINITY);
  }
  return h;
}

const oldestOf = (ats: number[]): number | null => (ats.length ? Math.min(...ats) : null);

export function conversation(messages: Read<Message>, runs: Read<Run>): Conversation {
  const said: Turn[] = messages.items.map((m) => ({
    kind: "message",
    at: Date.parse(m.received_at),
    message: m,
  }));
  const ran: Turn[] = runs.items.map((r) => ({
    kind: "run",
    at: Date.parse(r.created_at),
    run: r,
  }));

  const oldestSaid = oldestOf(said.map((t) => t.at));
  const oldestRan = oldestOf(ran.map((t) => t.at));
  const from = horizon([
    { more: messages.more, at: oldestSaid },
    { more: runs.more, at: oldestRan },
  ]);

  const turns = [...said, ...ran]
    .filter((t) => t.at >= from)
    // At the same instant a message comes first: it is what a run answers.
    .sort((a, b) => a.at - b.at || (a.kind === b.kind ? 0 : a.kind === "message" ? -1 : 1));

  // Only the list that stops at the horizon holds anything back: reading the
  // other further would bring back turns older still, and show none of them.
  return {
    turns,
    readFurther: {
      messages: messages.more && (oldestSaid ?? Number.POSITIVE_INFINITY) >= from,
      runs: runs.more && (oldestRan ?? Number.POSITIVE_INFINITY) >= from,
    },
  };
}

/** What stands in for an answer a run has not given. */
export function noAnswer(run: Run): string {
  switch (run.status) {
    case "queued":
      return "Waiting to start.";
    case "running":
      return "Working on it…";
    case "failed":
      return run.error ? explainRunError(run.error).said : "It failed before answering.";
    case "cancelled":
      return "Cancelled before it answered.";
    case "succeeded":
      return "It finished without an answer.";
  }
}
