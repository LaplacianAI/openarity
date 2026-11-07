/**
 * Asking the team: what may be sent, and what a refusal means to the person
 * who asked. The brain decides; this only says it in the dashboard's words
 * and catches, before sending, the one rule a person can see coming.
 */
import { ApiError } from "@/api/client";

/** Said wherever the hub can't run for want of a model: here, and on Team settings. */
export const NO_MODEL = "The hub can't run until a model is set.";

/** The brain's limit on what a run is asked, in bytes of UTF-8. */
export const ASK_LIMIT_BYTES = 100 * 1024;

const encoder = new TextEncoder();

/**
 * What is wrong with a question before it is sent, or null. Blank is not
 * an error to show: the send is just not offered.
 */
export function askProblem(input: string): string | null {
  if (encoder.encode(input).length > ASK_LIMIT_BYTES) {
    return "That is more than 100 KB. Shorten it, or attach the rest to a channel message instead.";
  }
  if (input.includes("\u0000")) {
    return "It holds a character that can't be sent. Paste it again as plain text.";
  }
  return null;
}

/** Why asking failed, and whether the remedy is on Team settings. */
export type AskRefusal = { text: string; settings: boolean };

export function askRefusal(err: unknown): AskRefusal {
  if (err instanceof ApiError) {
    if (err.status === 409) return { text: NO_MODEL, settings: true };
    if (err.status === 403) {
      return { text: "You can't start runs in this team. An admin can grant it.", settings: false };
    }
    if (err.status === 400 && err.message) {
      // The brain names the rule in a sentence of its own.
      return { text: `The brain refused it: ${err.message}.`, settings: false };
    }
    if (err.status < 500) return { text: "The brain refused it.", settings: false };
  }
  return {
    text: "Couldn't reach the brain, so nothing was asked. Try again.",
    settings: false,
  };
}
