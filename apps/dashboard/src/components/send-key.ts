/**
 * The key a send to the dashboard channel carries. The brain treats two sends
 * with one key as one message, so a retry of the same words reuses its key,
 * and anything else gets a new one: an edited retry under the old key would
 * be answered with the message that was stored before the edit.
 */
import { useRef } from "react";

export function useSendKeys() {
  const last = useRef<{ text: string; key: string } | null>(null);
  return {
    /** The key for sending this text: the last one's again only for the same words. */
    keyFor(text: string): string {
      if (last.current?.text !== text) last.current = { text, key: crypto.randomUUID() };
      return last.current.key;
    },
    /** The send went through: the next one is new, even with the same words. */
    done(): void {
      last.current = null;
    },
  };
}
