/**
 * Every call the dashboard makes, in the shape the brain actually serves.
 *
 * One function per endpoint, named after what it does rather than after the
 * HTTP verb, and no function here that has no endpoint behind it. The mock
 * layer this replaces had five — setup state, model gateways, demo data and a
 * reset — and every one of them was a screen with nothing underneath.
 */
import { ApiError, pageQuery, request } from "./client";
import { readFrames } from "./sse";
import type {
  Agent,
  AgentRequest,
  ApprovedSender,
  Attachment,
  AuthConfig,
  Blueprint,
  BlueprintChange,
  BlueprintPlan,
  Channel,
  CreateChannelRequest,
  CreatedChannel,
  ListedAgent,
  ListedChannel,
  ListedSession,
  ListedSkill,
  MCPServer,
  MCPServerRequest,
  MCPTool,
  Member,
  Message,
  Page,
  PendingSender,
  Run,
  RunEvent,
  RunRequest,
  RunStatus,
  Session,
  SessionStatus,
  Skill,
  Team,
  TeamGraph,
  TeamPendingSender,
  TeamPermissions,
  TeamSettings,
  TeamSettingsRequest,
  TeamSummary,
  UpdateChannelRequest,
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

/** Super admins only. The brain ignores the flag for anyone else and lists their live teams. */
export function listDeletedTeams(cursor?: string | null): Promise<Page<Team>> {
  const query = pageQuery(cursor);
  return request<Page<Team>>(`/teams${query ? `${query}&` : "?"}deleted=true`);
}

/** Marks the team deleted; it can be restored until the brain purges it. */
export function deleteTeam(teamId: string): Promise<void> {
  return request<void>(`/teams/${teamId}`, { method: "DELETE" });
}

export function restoreTeam(teamId: string): Promise<Team> {
  return request<Team>(`/teams/${teamId}/restore`, { method: "POST" });
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

export function listChannels(teamId: string, cursor?: string | null): Promise<Page<ListedChannel>> {
  return request<Page<ListedChannel>>(`/teams/${teamId}/channels${pageQuery(cursor)}`);
}

export function createChannel(
  teamId: string,
  channel: CreateChannelRequest,
): Promise<CreatedChannel> {
  return request<CreatedChannel>(`/teams/${teamId}/channels`, { method: "POST", body: channel });
}

/** Sets where a channel's replies go, or with null stops them. */
export function updateChannel(
  teamId: string,
  channelId: string,
  body: UpdateChannelRequest,
): Promise<Channel> {
  return request<Channel>(`/teams/${teamId}/channels/${channelId}`, { method: "PATCH", body });
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
/** Every channel's waiting senders as one queue, the longest waiting first. */
export function listTeamPendingSenders(
  teamId: string,
  cursor?: string | null,
  limit?: number,
): Promise<Page<TeamPendingSender>> {
  return request<Page<TeamPendingSender>>(
    `/teams/${teamId}/senders/pending${pageQuery(cursor, limit)}`,
  );
}

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

function queryOf(q: URLSearchParams): string {
  const text = q.toString();
  return text ? `?${text}` : "";
}

/* --------------------------------------------------------------- sessions */

/** One session, as the lists return it. Somebody else's direct one is a 404. */
export function getSession(teamId: string, sessionId: string): Promise<Session> {
  return request(`/teams/${teamId}/sessions/${sessionId}`);
}

/** What a list of sessions may be narrowed to. Each array is "any of these". */
export type SessionFilter = {
  channelId?: string | null;
  status?: SessionStatus[];
  /** The status of each session's newest run; a session with no run matches none. */
  runStatus?: RunStatus[];
};

export function listSessions(
  teamId: string,
  cursor?: string | null,
  filter: SessionFilter = {},
): Promise<Page<ListedSession>> {
  const q = new URLSearchParams(pageQuery(cursor));
  if (filter.channelId) q.set("channel_id", filter.channelId);
  for (const s of filter.status ?? []) q.append("status", s);
  for (const s of filter.runStatus ?? []) q.append("run_status", s);
  return request<Page<ListedSession>>(`/teams/${teamId}/sessions${queryOf(q)}`);
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

/** An attachment's bytes. Saved by the caller, never opened in the page. */
export function downloadAttachment(
  teamId: string,
  sessionId: string,
  attachmentId: string,
): Promise<Blob> {
  return request<Blob>(`/teams/${teamId}/sessions/${sessionId}/attachments/${attachmentId}`, {
    blob: true,
  });
}

/* ------------------------------------------------------------------- runs */

export function listSessionRuns(
  teamId: string,
  sessionId: string,
  cursor?: string | null,
): Promise<Page<Run>> {
  return request<Page<Run>>(`/teams/${teamId}/sessions/${sessionId}/runs${pageQuery(cursor)}`);
}

/** What the team's runs may be narrowed to. */
export type RunFilter = {
  status?: RunStatus[];
  /** Only runs created at or after this instant. */
  since?: Date | null;
  /** true for the hub's runs only, false for a named agent's only. */
  hub?: boolean | null;
  limit?: number;
};

/** The team's top-level runs, newest first, wherever each was started. */
export function listTeamRuns(
  teamId: string,
  cursor?: string | null,
  filter: RunFilter = {},
): Promise<Page<Run>> {
  const q = new URLSearchParams(pageQuery(cursor, filter.limit));
  for (const s of filter.status ?? []) q.append("status", s);
  if (filter.since) q.set("since", filter.since.toISOString());
  if (filter.hub != null) q.set("hub", String(filter.hub));
  return request<Page<Run>>(`/teams/${teamId}/runs${queryOf(q)}`);
}

/**
 * Queues a run and answers with it as it now is, before it has done
 * anything; its events say what it does. With no agent, the team's hub
 * answers.
 */
export function startRun(teamId: string, run: RunRequest): Promise<Run> {
  return request<Run>(`/teams/${teamId}/runs`, { method: "POST", body: run });
}

/** One run, wherever it was started — what a link to a run's trace reads. */
export function getRun(teamId: string, runId: string): Promise<Run> {
  return request<Run>(`/teams/${teamId}/runs/${runId}`);
}

export function cancelRun(teamId: string, runId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/runs/${runId}/cancel`, { method: "POST" });
}

/** How long to wait before following a stream again that closed early. */
export const RESUME_AFTER_MS = 1000;

/** The longest wait between two tries at a stream that keeps dropping. */
const RESUME_AT_MOST_MS = 15_000;

/**
 * How many tries in a row may fail, with nothing read between them, before
 * following gives up and says so.
 */
export const FOLLOW_ATTEMPTS = 5;

/**
 * Follows a run's events until the brain sends `end` or `signal` aborts,
 * calling `onEvent` for each, `end` included. A stream that closes without
 * `end` was cut, not finished, so it is followed again from the last event
 * seen — the brain replays nothing twice.
 *
 * A connection that drops, or a brain that answers 5xx, is tried again the
 * same way, waiting twice as long each time nothing was read in between, and
 * given up on after FOLLOW_ATTEMPTS. A refusal (4xx) ends it at once: trying
 * again cannot change it.
 */
export async function followRunEvents(
  teamId: string,
  runId: string,
  onEvent: (event: RunEvent) => void,
  signal: AbortSignal,
  from: number | null = null,
): Promise<void> {
  let after: number | null = from;
  let failures = 0;
  while (!signal.aborted) {
    const query: string = after === null ? "" : `?after=${after}`;
    try {
      const body: ReadableStream<Uint8Array> = await request(
        `/teams/${teamId}/runs/${runId}/events${query}`,
        { stream: true, signal },
      );
      for await (const frame of readFrames(body, signal)) {
        failures = 0;
        const seq: number | null = frame.id === null ? null : Number(frame.id);
        if (seq !== null && Number.isInteger(seq)) after = seq;
        onEvent({ seq, kind: frame.event, data: parseData(frame.data) });
        if (frame.event === "end") return;
      }
    } catch (err) {
      if (signal.aborted) return;
      if (err instanceof ApiError && err.status < 500) throw err;
      failures += 1;
      if (failures >= FOLLOW_ATTEMPTS) throw err;
    }
    const wait = RESUME_AFTER_MS * 2 ** Math.max(failures - 1, 0);
    await pause(Math.min(wait, RESUME_AT_MOST_MS), signal);
  }
}

function parseData(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

function pause(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener(
      "abort",
      () => {
        clearTimeout(timer);
        resolve();
      },
      { once: true },
    );
  });
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

/** search narrows the list to names containing q, a page of limit at a time. */
export function listSkills(
  teamId: string,
  cursor?: string | null,
  search?: Search,
): Promise<Page<ListedSkill>> {
  return request<Page<ListedSkill>>(
    `/teams/${teamId}/skills${pageQuery(cursor, search?.limit, search?.q)}`,
  );
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

/** A new version of a skill, uploaded as a whole; it keeps its id and grants. */
export function replaceSkill(teamId: string, skillId: string, upload: SkillUpload): Promise<Skill> {
  return request<Skill>(`/teams/${teamId}/skills/${skillId}`, {
    method: "PUT",
    upload: skillBody(upload),
  });
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

/** search narrows the list to names containing q, a page of limit at a time. */
export function listAgents(
  teamId: string,
  cursor?: string | null,
  search?: Search,
): Promise<Page<ListedAgent>> {
  return request<Page<ListedAgent>>(
    `/teams/${teamId}/agents${pageQuery(cursor, search?.limit, search?.q)}`,
  );
}

export function getAgent(teamId: string, agentId: string): Promise<Agent> {
  return request<Agent>(`/teams/${teamId}/agents/${agentId}`);
}

export function createAgent(teamId: string, agent: AgentRequest): Promise<Agent> {
  return request<Agent>(`/teams/${teamId}/agents`, { method: "POST", body: agent });
}

/** Whole, like the CLI's replace: a grant the request does not name is revoked. */
export function replaceAgent(teamId: string, agentId: string, agent: AgentRequest): Promise<Agent> {
  return request<Agent>(`/teams/${teamId}/agents/${agentId}`, { method: "PUT", body: agent });
}

export function deleteAgent(teamId: string, agentId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/agents/${agentId}`, { method: "DELETE" });
}

/* ------------------------------------------------------------ mcp servers */

/** search narrows the list to names containing q, a page of limit at a time. */
export function listMCPServers(
  teamId: string,
  cursor?: string | null,
  search?: Search,
): Promise<Page<MCPServer>> {
  return request<Page<MCPServer>>(
    `/teams/${teamId}/mcp-servers${pageQuery(cursor, search?.limit, search?.q)}`,
  );
}

export function createMCPServer(teamId: string, server: MCPServerRequest): Promise<MCPServer> {
  return request<MCPServer>(`/teams/${teamId}/mcp-servers`, { method: "POST", body: server });
}

export function replaceMCPServer(
  teamId: string,
  serverId: string,
  server: MCPServerRequest,
): Promise<MCPServer> {
  return request<MCPServer>(`/teams/${teamId}/mcp-servers/${serverId}`, {
    method: "PUT",
    body: server,
  });
}

export function deleteMCPServer(teamId: string, serverId: string): Promise<void> {
  return request<void>(`/teams/${teamId}/mcp-servers/${serverId}`, { method: "DELETE" });
}

export function getMCPServer(teamId: string, serverId: string): Promise<MCPServer> {
  return request<MCPServer>(`/teams/${teamId}/mcp-servers/${serverId}`);
}

/** The loops an agent's pattern may name: short, sorted, and always whole. */
export function listLoops(teamId: string): Promise<{ items: { name: string }[] }> {
  return request<{ items: { name: string }[] }>(`/teams/${teamId}/loops`);
}

export type Search = { q?: string; limit?: number };

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

export function getTeamSummary(teamId: string): Promise<TeamSummary> {
  return request<TeamSummary>(`/teams/${teamId}/summary`);
}

export function getTeamSettings(teamId: string): Promise<TeamSettings> {
  return request<TeamSettings>(`/teams/${teamId}/settings`);
}

export function saveTeamSettings(
  teamId: string,
  settings: TeamSettingsRequest,
): Promise<TeamSettings> {
  return request<TeamSettings>(`/teams/${teamId}/settings`, { method: "PUT", body: settings });
}

/* ------------------------------------------------------------- blueprints */

/** A read: what applying the blueprint would change. */
export function planBlueprint(teamId: string, blueprint: Blueprint): Promise<BlueprintPlan> {
  return request<BlueprintPlan>(`/teams/${teamId}/blueprints/plan`, {
    method: "POST",
    body: blueprint,
  });
}

/**
 * Applies the plan with that digest. The brain answers a plan, not a
 * sentence, when it will not: 422 when the plan has refusals and 409 when the
 * team changed since it was shown. Both come back as the new plan to show; a
 * plain-text 409 (a name taken while applying) stays an error.
 */
export type Applied =
  | { kind: "applied"; changes: BlueprintChange[] }
  | { kind: "refused" | "changed"; plan: BlueprintPlan };

export async function applyBlueprint(
  teamId: string,
  blueprint: Blueprint,
  digest: string,
): Promise<Applied> {
  try {
    const out = await request<{ changes: BlueprintChange[] }>(`/teams/${teamId}/blueprints/apply`, {
      method: "POST",
      body: { blueprint, plan_digest: digest },
    });
    return { kind: "applied", changes: out.changes };
  } catch (err) {
    const plan = err instanceof ApiError ? planIn(err) : null;
    if (!plan) throw err;
    return { kind: (err as ApiError).status === 422 ? "refused" : "changed", plan };
  }
}

function planIn(err: ApiError): BlueprintPlan | null {
  if (err.status !== 409 && err.status !== 422) return null;
  try {
    const plan = JSON.parse(err.message) as BlueprintPlan;
    return Array.isArray(plan.changes) && typeof plan.digest === "string" ? plan : null;
  } catch {
    return null;
  }
}
