/**
 * The only module components talk to for data.
 *
 * Every function here is async and returns wire types from `./types`. To go
 * live, replace each body with a `fetch()` against the Go API — the signatures
 * and the cursor-page contract are identical. No component changes required.
 */
import { commit, demoDb, emptyDb, getDb, id, replaceDb } from "./db";
import type {
  Channel,
  Member,
  Message,
  ModelGateway,
  Page,
  PendingSender,
  Role,
  Session,
  SetupState,
  Team,
  User,
} from "./types";

export const PAGE_SIZE = 20;

const latency = () => new Promise((r) => setTimeout(r, 140));

function paginate<T extends { id: string }>(
  rows: T[],
  cursor?: string | null,
  size = PAGE_SIZE,
): Page<T> {
  const start = cursor
    ? Math.max(
        0,
        rows.findIndex((r) => r.id === cursor),
      )
    : 0;
  const items = rows.slice(start, start + size);
  const next = rows[start + size];
  return { items, next_cursor: next ? next.id : null };
}

/* ------------------------------------------------------------------ setup */

export async function getSetupState(): Promise<SetupState> {
  await latency();
  return getDb().setup;
}

export async function signIn(email: string, displayName: string): Promise<User> {
  await latency();
  const db = getDb();
  const user: User = {
    id: id("usr"),
    email,
    display_name: displayName || email.split("@")[0] || "Operator",
    avatar_color: "#3f7d6b",
  };
  db.users = [user, ...db.users.filter((u) => u.email !== email)];
  db.setup.signed_in_user = user;
  commit();
  return user;
}

export async function completeSetup(): Promise<SetupState> {
  await latency();
  const db = getDb();
  db.setup.completed = true;
  commit();
  return db.setup;
}

export async function resetInstall(): Promise<void> {
  await latency();
  replaceDb(emptyDb());
}

export async function loadDemoInstall(): Promise<void> {
  await latency();
  replaceDb(demoDb());
}

/* --------------------------------------------------------------- gateways */

export async function listGateways(): Promise<Page<ModelGateway>> {
  await latency();
  return paginate(getDb().gateways);
}

export async function createGateway(input: {
  provider: ModelGateway["provider"];
  label: string;
  api_key: string;
}): Promise<ModelGateway> {
  await latency();
  const db = getDb();
  const gateway: ModelGateway = {
    id: id("gw"),
    provider: input.provider,
    label: input.label || "Model gateway",
    key_last4: input.api_key.slice(-4) || "····",
    created_at: new Date().toISOString(),
  };
  db.gateways.push(gateway);
  db.setup.gateway_id = gateway.id;
  commit();
  return gateway;
}

/* ------------------------------------------------------------------ teams */

export async function listTeams(cursor?: string | null): Promise<Page<Team>> {
  await latency();
  return paginate(getDb().teams, cursor);
}

export async function getTeam(teamId: string): Promise<Team | null> {
  await latency();
  return getDb().teams.find((t) => t.id === teamId) ?? null;
}

export async function createTeam(name: string): Promise<Team> {
  await latency();
  const db = getDb();
  const team: Team = {
    id: id("team"),
    name,
    slug: name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, ""),
    created_at: new Date().toISOString(),
    member_count: db.setup.signed_in_user ? 1 : 0,
    channel_count: 0,
  };
  db.teams.push(team);
  if (db.setup.signed_in_user) {
    db.members.push({
      id: id("mem"),
      team_id: team.id,
      user: db.setup.signed_in_user,
      role: "admin",
      added_at: team.created_at,
    });
  }
  if (!db.setup.team_id) db.setup.team_id = team.id;
  commit();
  return team;
}

/* ---------------------------------------------------------------- members */

export async function listMembers(teamId: string, cursor?: string | null): Promise<Page<Member>> {
  await latency();
  return paginate(
    getDb().members.filter((m) => m.team_id === teamId),
    cursor,
  );
}

export async function listDirectory(): Promise<Page<User>> {
  await latency();
  return paginate(getDb().users, null, 50);
}

export async function addMember(teamId: string, userId: string, role: Role): Promise<Member> {
  await latency();
  const db = getDb();
  const user = db.users.find((u) => u.id === userId);
  if (!user) throw new Error("Unknown user");
  const existing = db.members.find((m) => m.team_id === teamId && m.user.id === userId);
  if (existing) throw new Error("Already a member of this team");
  const member: Member = {
    id: id("mem"),
    team_id: teamId,
    user,
    role,
    added_at: new Date().toISOString(),
  };
  db.members.push(member);
  const team = db.teams.find((t) => t.id === teamId);
  if (team) team.member_count += 1;
  commit();
  return member;
}

export async function updateMemberRole(memberId: string, role: Role): Promise<Member> {
  await latency();
  const db = getDb();
  const member = db.members.find((m) => m.id === memberId);
  if (!member) throw new Error("Unknown member");
  member.role = role;
  commit();
  return member;
}

export async function removeMember(memberId: string): Promise<void> {
  await latency();
  const db = getDb();
  const member = db.members.find((m) => m.id === memberId);
  db.members = db.members.filter((m) => m.id !== memberId);
  const team = db.teams.find((t) => t.id === member?.team_id);
  if (team) team.member_count = Math.max(0, team.member_count - 1);
  commit();
}

/* --------------------------------------------------------------- channels */

export async function listChannels(
  teamId: string | null,
  cursor?: string | null,
): Promise<Page<Channel>> {
  await latency();
  const rows = getDb().channels.filter((c) => !teamId || c.team_id === teamId);
  return paginate(rows, cursor);
}

export async function createChannel(input: {
  team_id: string;
  kind: Channel["kind"];
  name: string;
  address: string;
}): Promise<Channel> {
  await latency();
  const db = getDb();
  const channel: Channel = {
    id: id("chn"),
    team_id: input.team_id,
    kind: input.kind,
    name: input.name,
    address: input.address,
    status: "active",
    created_at: new Date().toISOString(),
    pending_sender_count: 0,
  };
  db.channels.push(channel);
  const team = db.teams.find((t) => t.id === input.team_id);
  if (team) team.channel_count += 1;
  if (!db.setup.channel_id) db.setup.channel_id = channel.id;
  commit();
  return channel;
}

/* -------------------------------------------------------- pending senders */

export async function listPendingSenders(
  filter: { team_id?: string | null; channel_id?: string | null },
  cursor?: string | null,
): Promise<Page<PendingSender>> {
  await latency();
  const rows = getDb().pending.filter(
    (p) =>
      (!filter.team_id || p.team_id === filter.team_id) &&
      (!filter.channel_id || p.channel_id === filter.channel_id),
  );
  return paginate(rows, cursor, 25);
}

export async function decideSender(
  senderId: string,
  decision: "approve" | "reject",
): Promise<void> {
  await latency();
  const db = getDb();
  const sender = db.pending.find((p) => p.id === senderId);
  db.pending = db.pending.filter((p) => p.id !== senderId);
  const channel = db.channels.find((c) => c.id === sender?.channel_id);
  if (channel) channel.pending_sender_count = Math.max(0, channel.pending_sender_count - 1);
  if (sender && decision === "approve") {
    const session: Session = {
      id: id("ses"),
      team_id: sender.team_id,
      channel_id: sender.channel_id,
      channel_name: sender.channel_name,
      agent_name: "triage-agent",
      participant: sender.sender_label ?? sender.sender_address,
      status: "active",
      started_at: new Date().toISOString(),
      last_activity_at: new Date().toISOString(),
      message_count: 1,
    };
    db.sessions.unshift(session);
    db.messages.push({
      id: id("msg"),
      session_id: session.id,
      author_kind: "system",
      author_name: "system",
      body: `Sender ${sender.sender_address} approved. Agent will respond on ${sender.channel_name}.`,
      created_at: session.started_at,
      attachments: [],
    });
  }
  commit();
}

/* --------------------------------------------------------------- sessions */

export async function listSessions(
  teamId: string | null,
  cursor?: string | null,
): Promise<Page<Session>> {
  await latency();
  const rows = getDb().sessions.filter((s) => !teamId || s.team_id === teamId);
  return paginate(rows, cursor);
}

export async function getSession(sessionId: string): Promise<Session | null> {
  await latency();
  return getDb().sessions.find((s) => s.id === sessionId) ?? null;
}

export async function listMessages(
  sessionId: string,
  cursor?: string | null,
): Promise<Page<Message>> {
  await latency();
  return paginate(
    getDb().messages.filter((m) => m.session_id === sessionId),
    cursor,
    50,
  );
}
