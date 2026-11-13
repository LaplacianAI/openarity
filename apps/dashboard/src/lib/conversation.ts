/**
 * What the conversation says about a run beside its answer: what stands in
 * for an answer it has not given, and whether that answer reached the person
 * on the channel.
 */
import type { Reply, Run } from "@/api/types";
import { explainRunError } from "@/lib/run-error";

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

/**
 * What became of a run's answer to its channel, as one line under it: sent,
 * on its way, or given up. `when` is the moment worth showing with it, and
 * the words that lead into it. Null for a run that answers no channel.
 */
export type ReplyLine = {
  tone: "sent" | "waiting" | "failed";
  text: string;
  when: { lead: string; iso: string } | null;
};

export function replyLine(reply: Reply | null | undefined): ReplyLine | null {
  if (!reply) return null;
  const what = reply.kind === "apology" ? "The apology" : "The answer";
  switch (reply.status) {
    case "sent":
      return {
        tone: "sent",
        text: `${what} was sent to the channel`,
        when: reply.sent_at ? { lead: " at ", iso: reply.sent_at } : null,
      };
    case "pending":
      // Claimed and on its way, or not tried yet: either way there is no
      // failure to report.
      if (!reply.last_error) {
        return {
          tone: "waiting",
          text: `Sending ${what.toLowerCase()} to the channel…`,
          when: null,
        };
      }
      return {
        tone: "waiting",
        text: `${what} has not reached the channel yet: ${reply.last_error}`,
        when: reply.next_attempt_at
          ? { lead: ". Trying again at ", iso: reply.next_attempt_at }
          : null,
      };
    case "failed":
      return {
        tone: "failed",
        text: `${what} was not sent: ${reply.last_error ?? "it was given up"}`,
        when: null,
      };
  }
}

/** Whether any of these runs still has a reply on its way, so the page keeps reading. */
export function awaitsReply(runs: Run[]): boolean {
  return runs.some((r) => r.reply?.status === "pending");
}
