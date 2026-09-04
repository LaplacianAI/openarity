/**
 * Every call the dashboard makes, in the shape the brain actually serves.
 *
 * One function per endpoint, named after what it does rather than after the
 * HTTP verb, and no function here that has no endpoint behind it. The mock
 * layer this replaces had five — setup state, model gateways, demo data and a
 * reset — and every one of them was a screen with nothing underneath.
 */
import { pageQuery, request } from "./client";
import type {
  ApprovedSender,
  Attachment,
  AuthConfig,
  Channel,
  Member,
  Message,
  Page,
  PendingSender,
  Session,
  Team,
  User,
} from "./types";

/* ------------------------------------------------------------------- auth */

/** Public: this is what tells the app which login flow applies. */
export function fetchAuthConfig(signal?: AbortSignal): Promise<AuthConfig> {
  return request<AuthConfig>("/auth/config", { anonymous: true, ...(signal ? { signal } : {}) });
}

/** The current principal — the cheapest call that proves a token works. */
export function fetchWhoami(signal?: AbortSignal): Promise<unknown> {
  return request<unknown>("/whoami", signal ? { signal } : {});
}

/* ------------------------------------------------------------------ teams */

export function listTeams(cursor?: string | null): Promise<Page<Team>> {
  return request<Page<Team>>(`/teams${pageQuery(cursor)}`);
}

export function getTeam(teamId: string): Promise<Team> {
  return request<Team>(`/teams/${teamId}`);
}

export function createTeam(name: string): Promise<Team> {
  return request<Team>("/teams", { method: "POST", body: { name } });
}

/* ---------------------------------------------------------------- members */

export function listMembers(teamId: string, cursor?: string | null): Promise<Page<Member>> {
  return request<Page<Member>>(`/teams/${teamId}/members${pageQuery(cursor)}`);
}

/**
 * A member is added by user id or by subject — the brain accepts either,
 * because the person inviting may know a colleague's login name before that
 * colleague has ever signed in and been given a row.
 */
export function addMember(
  teamId: string,
  who: { user_id: string } | { subject: string },
  role: string,
): Promise<Member> {
  return request<Member>(`/teams/${teamId}/members`, { method: "POST", body: { ...who, role } });
}

export function removeMember(teamId: string, userId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/members/${userId}`, { method: "DELETE" });
}

/* ------------------------------------------------------------------ users */

export function listUsers(cursor?: string | null): Promise<Page<User>> {
  return request<Page<User>>(`/users${pageQuery(cursor)}`);
}

/* --------------------------------------------------------------- channels */

export function listChannels(teamId: string, cursor?: string | null): Promise<Page<Channel>> {
  return request<Page<Channel>>(`/teams/${teamId}/channels${pageQuery(cursor)}`);
}

export function deleteChannel(teamId: string, channelId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/channels/${channelId}`, { method: "DELETE" });
}

/* ---------------------------------------------------------------- senders */

export function listPendingSenders(
  teamId: string,
  channelId: string,
  cursor?: string | null,
): Promise<Page<PendingSender>> {
  return request<Page<PendingSender>>(
    `/teams/${teamId}/channels/${channelId}/senders/pending${pageQuery(cursor)}`,
  );
}

export function listApprovedSenders(
  teamId: string,
  channelId: string,
  cursor?: string | null,
): Promise<Page<ApprovedSender>> {
  return request<Page<ApprovedSender>>(
    `/teams/${teamId}/channels/${channelId}/senders${pageQuery(cursor)}`,
  );
}

/**
 * Approving binds a sender to a user: the brain needs to know *whose* messages
 * these are, not merely that they are allowed. There is no reject endpoint —
 * a sender that is never approved never reaches an agent, so refusing is what
 * happens by not acting.
 */
export function approveSender(
  teamId: string,
  channelId: string,
  senderRef: string,
  userId: string,
): Promise<ApprovedSender> {
  return request<ApprovedSender>(`/teams/${teamId}/channels/${channelId}/senders`, {
    method: "POST",
    body: { sender_ref: senderRef, user_id: userId },
  });
}

export function removeSender(teamId: string, channelId: string, senderRef: string): Promise<void> {
  return request<void>(
    `/teams/${teamId}/channels/${channelId}/senders?ref=${encodeURIComponent(senderRef)}`,
    { method: "DELETE" },
  );
}

/* --------------------------------------------------------------- sessions */

export function listSessions(teamId: string, cursor?: string | null): Promise<Page<Session>> {
  return request<Page<Session>>(`/teams/${teamId}/sessions${pageQuery(cursor)}`);
}

export function listMessages(
  teamId: string,
  sessionId: string,
  cursor?: string | null,
): Promise<Page<Message>> {
  return request<Page<Message>>(
    `/teams/${teamId}/sessions/${sessionId}/messages${pageQuery(cursor)}`,
  );
}

export function listAttachments(
  teamId: string,
  sessionId: string,
  cursor?: string | null,
): Promise<Page<Attachment>> {
  return request<Page<Attachment>>(
    `/teams/${teamId}/sessions/${sessionId}/attachments${pageQuery(cursor)}`,
  );
}
