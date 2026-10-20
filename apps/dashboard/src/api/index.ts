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
  Agent,
  AgentRequest,
  AgentSummary,
  ApprovedSender,
  Attachment,
  AuthConfig,
  Channel,
  CreateChannelRequest,
  CreatedChannel,
  MCPServer,
  MCPServerRequest,
  MCPTool,
  Member,
  Message,
  Page,
  PendingSender,
  Session,
  Skill,
  SkillSummary,
  Team,
  TeamGraph,
  TeamPermissions,
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

export function createChannel(
  teamId: string,
  channel: CreateChannelRequest,
): Promise<CreatedChannel> {
  return request<CreatedChannel>(`/teams/${teamId}/channels`, { method: "POST", body: channel });
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

/* ------------------------------------------------------------ permissions */

/**
 * What the caller may do in a team, so a screen offers only what the brain
 * will allow. Every route still checks for itself.
 */
export function getPermissions(teamId: string, signal?: AbortSignal): Promise<TeamPermissions> {
  return request<TeamPermissions>(`/teams/${teamId}/permissions`, signal ? { signal } : {});
}

/* ----------------------------------------------------------------- skills */

export function listSkills(teamId: string, cursor?: string | null): Promise<Page<SkillSummary>> {
  return request<Page<SkillSummary>>(`/teams/${teamId}/skills${pageQuery(cursor)}`);
}

export function getSkill(teamId: string, skillId: string): Promise<Skill> {
  return request<Skill>(`/teams/${teamId}/skills/${skillId}`);
}

/**
 * A folder is sent as one `files` part per file, named by its path inside
 * the folder — what the brain reads — and a zip is sent as it is.
 */
export function uploadSkill(teamId: string, upload: SkillUpload): Promise<Skill> {
  return request<Skill>(`/teams/${teamId}/skills`, { method: "POST", upload: skillBody(upload) });
}

export function importSkill(teamId: string, source: string): Promise<Skill> {
  return request<Skill>(`/teams/${teamId}/skills/import`, { method: "POST", body: { source } });
}

export function syncSkill(teamId: string, skillId: string): Promise<Skill> {
  return request<Skill>(`/teams/${teamId}/skills/${skillId}/sync`, { method: "POST" });
}

export function deleteSkill(teamId: string, skillId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/skills/${skillId}`, { method: "DELETE" });
}

/** Either a zip, or the files of a folder each with the path they sit at. */
export type SkillUpload = { zip: File } | { files: { path: string; file: File }[] };

export function skillBody(upload: SkillUpload): FormData | File {
  if ("zip" in upload) return upload.zip;
  const form = new FormData();
  for (const { path, file } of upload.files) form.append("files", file, path);
  return form;
}

/* ----------------------------------------------------------------- agents */

export function listAgents(teamId: string, cursor?: string | null): Promise<Page<AgentSummary>> {
  return request<Page<AgentSummary>>(`/teams/${teamId}/agents${pageQuery(cursor)}`);
}

export function getAgent(teamId: string, agentId: string): Promise<Agent> {
  return request<Agent>(`/teams/${teamId}/agents/${agentId}`);
}

export function createAgent(teamId: string, agent: AgentRequest): Promise<Agent> {
  return request<Agent>(`/teams/${teamId}/agents`, { method: "POST", body: agent });
}

export function deleteAgent(teamId: string, agentId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/agents/${agentId}`, { method: "DELETE" });
}

/* ------------------------------------------------------------ mcp servers */

export function listMCPServers(teamId: string, cursor?: string | null): Promise<Page<MCPServer>> {
  return request<Page<MCPServer>>(`/teams/${teamId}/mcp-servers${pageQuery(cursor)}`);
}

export function createMCPServer(teamId: string, server: MCPServerRequest): Promise<MCPServer> {
  return request<MCPServer>(`/teams/${teamId}/mcp-servers`, { method: "POST", body: server });
}

export function deleteMCPServer(teamId: string, serverId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/mcp-servers/${serverId}`, { method: "DELETE" });
}

export function listMCPTools(teamId: string, serverId: string): Promise<{ items: MCPTool[] }> {
  return request<{ items: MCPTool[] }>(`/teams/${teamId}/mcp-servers/${serverId}/tools`);
}

export function discoverMCPServer(teamId: string, serverId: string): Promise<{ items: MCPTool[] }> {
  return request<{ items: MCPTool[] }>(`/teams/${teamId}/mcp-servers/${serverId}/discover`, {
    method: "POST",
  });
}

/* ------------------------------------------------------------------ graph */

export function getGraph(teamId: string, limit: number, signal?: AbortSignal): Promise<TeamGraph> {
  return request<TeamGraph>(`/teams/${teamId}/graph?limit=${limit}`, signal ? { signal } : {});
}
