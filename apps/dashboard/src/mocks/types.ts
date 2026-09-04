/**
 * Wire types for the Openarity API.
 *
 * These mirror the JSON the Go API server returns (snake_case). Components must
 * only ever touch these types, never the mock store itself, so the transport in
 * `src/mocks/api.ts` can be swapped for real `fetch()` calls later.
 */

export type Page<T> = {
  items: T[];
  /** Opaque cursor. `null` means there is nothing more to load. */
  next_cursor: string | null;
};

export type Role = "admin" | "member";

export type User = {
  id: string;
  email: string;
  display_name: string;
  avatar_color: string;
};

export type Team = {
  id: string;
  name: string;
  slug: string;
  created_at: string;
  member_count: number;
  channel_count: number;
};

export type Member = {
  id: string;
  team_id: string;
  user: User;
  role: Role;
  added_at: string;
};

export type ChannelKind = "slack" | "email" | "webhook" | "discord";

export type Channel = {
  id: string;
  team_id: string;
  kind: ChannelKind;
  name: string;
  address: string;
  status: "active" | "paused";
  created_at: string;
  pending_sender_count: number;
};

export type PendingSender = {
  id: string;
  team_id: string;
  channel_id: string;
  channel_name: string;
  channel_kind: ChannelKind;
  sender_address: string;
  sender_label: string | null;
  first_seen_at: string;
  message_count: number;
  last_message_preview: string;
};

export type SessionStatus = "active" | "idle" | "closed";

export type Session = {
  id: string;
  team_id: string;
  channel_id: string;
  channel_name: string;
  agent_name: string;
  participant: string;
  status: SessionStatus;
  started_at: string;
  last_activity_at: string;
  message_count: number;
};

export type Attachment = {
  id: string;
  filename: string;
  content_type: string;
  size_bytes: number;
};

export type Message = {
  id: string;
  session_id: string;
  author_kind: "user" | "agent" | "system";
  author_name: string;
  body: string;
  created_at: string;
  attachments: Attachment[];
};

export type ModelGateway = {
  id: string;
  provider: "openai" | "anthropic" | "openrouter" | "custom";
  label: string;
  key_last4: string;
  created_at: string;
};

export type SetupState = {
  signed_in_user: User | null;
  team_id: string | null;
  gateway_id: string | null;
  channel_id: string | null;
  completed: boolean;
};
