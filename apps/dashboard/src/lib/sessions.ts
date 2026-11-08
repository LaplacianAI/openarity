import type { Session } from "@/api/types";

/** What a session is called wherever it is listed or titled. */
export const UNNAMED = "Direct session";

/**
 * A session's name: the provider's reference when it has one ("ticket-42"),
 * else what was first asked in it, on one line, else "Direct session". A
 * session started from the dashboard or the API has no reference, so its
 * first question is the only thing that tells it from the others.
 */
export function sessionName(s: Pick<Session, "provider_ref" | "first_asked">): string {
  const ref = s.provider_ref.trim();
  if (ref) return ref;
  const asked = s.first_asked?.replace(/\s+/g, " ").trim();
  return asked || UNNAMED;
}
