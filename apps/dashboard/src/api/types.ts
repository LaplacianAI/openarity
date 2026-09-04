/**
 * Wire types for the Openarity API, transcribed from the brain's Go schemas.
 *
 * These are deliberately no richer than what the server actually returns. The
 * design this app was drawn from assumed member counts, channel addresses and
 * message previews; none of those exist, and inventing them client-side would
 * put a number on screen that nothing can justify.
 */

export type Page<T> = {
  items: T[];
  /**
   * Absent — not null — when there is no next page: the Go field is
   * `omitempty`. Optional rather than nullable so `page.next_cursor ?? null`
   * is the only place that has to care.
   */
  next_cursor?: string;
};

export type Role = "admin" | "member";

/** GET /teams, GET /teams/{id}. `role` is present only when listing your own. */
export type Team = {
  id: string;
  name: string;
  role?: string;
};

/** GET /teams/{id}/members. There is no nested user object and no join date. */
export type Member = {
  user_id: string;
  subject: string;
  email?: string;
  role: string;
};

/** GET /users */
export type User = {
  id: string;
  issuer: string;
  subject: string;
  email?: string;
};

/** GET /teams/{id}/channels. `provider` is the adapter's name, e.g. "slack". */
export type Channel = {
  id: string;
  team_id: string;
  provider: string;
  name: string;
};

/**
 * GET /teams/{id}/channels/{channelID}/senders/pending
 *
 * Identified by `sender_ref` rather than an id: an unapproved sender is not a
 * row the brain owns, it is an address that has been seen. Nothing here says
 * what they wrote — the brain does not return a preview.
 */
export type PendingSender = {
  sender_ref: string;
  sender_name: string;
  seen_count: number;
  first_seen: string;
  last_seen: string;
};

/** GET /teams/{id}/channels/{channelID}/senders */
export type ApprovedSender = {
  sender_ref: string;
  user_id: string;
  created_at: string;
};

/** GET /teams/{id}/sessions */
export type Session = {
  id: string;
  channel_id: string | null;
  provider_ref: string;
  kind: string;
  status: string;
  started_at: string;
  last_message_at: string;
};

/** GET /teams/{id}/sessions/{sessionID}/messages */
export type Message = {
  id: string;
  external_id: string;
  user_id: string;
  text: string;
  sent_at?: string;
  received_at: string;
};

/** GET /teams/{id}/sessions/{sessionID}/attachments */
export type Attachment = {
  id: string;
  message_id: string;
  filename: string;
  media_type: string;
  size_bytes: number;
  created_at: string;
};

/** GET /auth/config — public, and answered before anyone has a token. */
export type AuthConfig = {
  environment: string;
  dev_token_accepted: boolean;
  oidc?: { issuer: string; client_id: string };
};
