/**
 * In-memory mock database. Nothing here is imported by components — only
 * `src/mocks/api.ts` touches it. Delete this file when the real API is wired up.
 */
import type {
  Channel,
  Member,
  Message,
  ModelGateway,
  PendingSender,
  Session,
  SetupState,
  Team,
  User,
} from "./types";

export type Db = {
  setup: SetupState;
  users: User[];
  teams: Team[];
  members: Member[];
  channels: Channel[];
  pending: PendingSender[];
  sessions: Session[];
  messages: Message[];
  gateways: ModelGateway[];
};

const STORAGE_KEY = "openarity.mockdb.v1";

export function emptyDb(): Db {
  return {
    setup: {
      signed_in_user: null,
      team_id: null,
      gateway_id: null,
      channel_id: null,
      completed: false,
    },
    users: directory(),
    teams: [],
    members: [],
    channels: [],
    pending: [],
    sessions: [],
    messages: [],
    gateways: [],
  };
}

const COLORS = ["#3f7d6b", "#8a6a3c", "#5a6a9c", "#8c5a63", "#4c7a94", "#6b6b3f"];

function directory(): User[] {
  const people: Array<[string, string]> = [
    ["Ada Okonkwo", "ada@openarity.dev"],
    ["Bruno Salas", "bruno@openarity.dev"],
    ["Caro Lindqvist", "caro@openarity.dev"],
    ["Deniz Aydin", "deniz@openarity.dev"],
    ["Elif Roth", "elif@contractor.io"],
    ["Farid Nazari", "farid@openarity.dev"],
    ["Greta Moll", "greta@openarity.dev"],
    ["Hugo Petit", "hugo@partner.example"],
  ];
  return people.map(([display_name, email], i) => ({
    id: `usr_${i + 1}`,
    email,
    display_name,
    avatar_color: COLORS[i % COLORS.length] as string,
  }));
}

const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString();

const SENDER_SEEDS: Array<[string, string | null, string]> = [
  ["m.hartley@northwind.example", "Marcus Hartley", "Can the agent pull last quarter's invoices?"],
  ["ops@vendorlink.example", null, "Automated: nightly sync failed twice, please advise."],
  ["j.tanaka@keiretsu.example", "Junko Tanaka", "Following up on the migration checklist."],
  ["support@acme-tools.example", "Acme Tools Support", "Ticket #4412 has been escalated to you."],
  ["r.okafor@lagosworks.example", "Rita Okafor", "Do you support Portuguese transcripts?"],
  ["noreply@statuspage.example", null, "Incident resolved: degraded webhook delivery."],
  ["p.lindberg@nordkraft.example", "Petra Lindberg", "Requesting access for two more analysts."],
  ["billing@cloudhost.example", null, "Your usage exceeded the soft cap for October."],
  ["t.ibrahim@sahara.example", "Tariq Ibrahim", "Attached the redlined agreement."],
  ["k.novak@vltava.example", "Karel Novak", "The agent stopped replying mid-thread."],
  ["a.moreau@lumiere.example", "Anais Moreau", "Can we schedule a walkthrough this week?"],
  ["dev@integrations.example", null, "Webhook signature mismatch on retry."],
  ["s.raman@bengalore.example", "Shreya Raman", "Please confirm receipt of the audit export."],
  ["h.olsen@fjordline.example", "Henrik Olsen", "Is there a rate limit on attachments?"],
  ["m.silva@brasa.example", "Mateus Silva", "Duplicate session created after reconnect."],
  ["legal@grantham.example", null, "Retention policy questions before onboarding."],
  ["y.chen@harborlight.example", "Yuwen Chen", "Requesting a sandbox key for testing."],
  ["e.varga@dunabe.example", "Eszter Varga", "Agent replied in the wrong language."],
];

export function demoDb(): Db {
  const db = emptyDb();
  const me = db.users[0] as User;
  db.setup = {
    signed_in_user: me,
    team_id: "team_1",
    gateway_id: "gw_1",
    channel_id: "chn_1",
    completed: true,
  };
  db.gateways = [
    {
      id: "gw_1",
      provider: "anthropic",
      label: "Primary gateway",
      key_last4: "7Q4a",
      created_at: hoursAgo(720),
    },
  ];
  db.teams = [
    {
      id: "team_1",
      name: "Customer Operations",
      slug: "customer-ops",
      created_at: hoursAgo(720),
      member_count: 5,
      channel_count: 3,
    },
    {
      id: "team_2",
      name: "Platform Reliability",
      slug: "platform-reliability",
      created_at: hoursAgo(400),
      member_count: 3,
      channel_count: 2,
    },
  ];
  db.members = db.users.slice(0, 5).map((user, i) => ({
    id: `mem_1_${i}`,
    team_id: "team_1",
    user,
    role: i === 0 ? "admin" : i === 2 ? "admin" : "member",
    added_at: hoursAgo(700 - i * 20),
  }));
  db.members.push(
    ...db.users.slice(2, 5).map((user, i) => ({
      id: `mem_2_${i}`,
      team_id: "team_2",
      user,
      role: (i === 0 ? "admin" : "member") as Member["role"],
      added_at: hoursAgo(390 - i * 15),
    })),
  );
  db.channels = [
    {
      id: "chn_1",
      team_id: "team_1",
      kind: "email",
      name: "Support inbox",
      address: "support@openarity.dev",
      status: "active",
      created_at: hoursAgo(700),
      pending_sender_count: 0,
    },
    {
      id: "chn_2",
      team_id: "team_1",
      kind: "slack",
      name: "#customer-escalations",
      address: "T04KJ/C08AB",
      status: "active",
      created_at: hoursAgo(500),
      pending_sender_count: 0,
    },
    {
      id: "chn_3",
      team_id: "team_1",
      kind: "webhook",
      name: "Zendesk bridge",
      address: "hooks/zendesk-inbound",
      status: "paused",
      created_at: hoursAgo(300),
      pending_sender_count: 0,
    },
    {
      id: "chn_4",
      team_id: "team_2",
      kind: "slack",
      name: "#oncall-agent",
      address: "T04KJ/C09ZX",
      status: "active",
      created_at: hoursAgo(380),
      pending_sender_count: 0,
    },
    {
      id: "chn_5",
      team_id: "team_2",
      kind: "webhook",
      name: "Alertmanager",
      address: "hooks/alertmanager",
      status: "active",
      created_at: hoursAgo(200),
      pending_sender_count: 0,
    },
  ];

  db.pending = Array.from({ length: 47 }, (_, i) => {
    const seed = SENDER_SEEDS[i % SENDER_SEEDS.length] as [string, string | null, string];
    const channel = db.channels[i % 5] as Channel;
    const suffix = i >= SENDER_SEEDS.length ? `+${Math.floor(i / SENDER_SEEDS.length)}` : "";
    const [address, label, preview] = seed;
    const [local, domain] = address.split("@") as [string, string];
    return {
      id: `snd_${i + 1}`,
      team_id: channel.team_id,
      channel_id: channel.id,
      channel_name: channel.name,
      channel_kind: channel.kind,
      sender_address: `${local}${suffix}@${domain}`,
      sender_label: label,
      first_seen_at: hoursAgo(1 + i * 1.7),
      message_count: 1 + ((i * 3) % 6),
      last_message_preview: preview,
    };
  });
  for (const channel of db.channels) {
    channel.pending_sender_count = db.pending.filter((p) => p.channel_id === channel.id).length;
  }

  db.sessions = Array.from({ length: 24 }, (_, i) => {
    const channel = db.channels[i % 5] as Channel;
    const seed = SENDER_SEEDS[i % SENDER_SEEDS.length] as [string, string | null, string];
    return {
      id: `ses_${i + 1}`,
      team_id: channel.team_id,
      channel_id: channel.id,
      channel_name: channel.name,
      agent_name: i % 3 === 0 ? "triage-agent" : i % 3 === 1 ? "billing-agent" : "oncall-agent",
      participant: seed[1] ?? seed[0],
      status: i < 4 ? "active" : i < 12 ? "idle" : "closed",
      started_at: hoursAgo(2 + i * 5),
      last_activity_at: hoursAgo(i * 4),
      message_count: 4 + ((i * 5) % 11),
    };
  });

  db.messages = db.sessions.flatMap((session, si) => transcript(session, si));
  return db;
}

function transcript(session: Session, si: number): Message[] {
  const base = Date.parse(session.started_at);
  const lines: Array<[Message["author_kind"], string]> = [
    ["system", `Session opened on ${session.channel_name} — sender approved by Ada Okonkwo.`],
    ["user", "Hi — we're seeing duplicate invoices for October. Can you check what happened?"],
    [
      "agent",
      "I pulled the October billing export for your account. Two invoices were issued 14 minutes apart; the second is a retry that was never voided. I can void it and re-issue a credit note.",
    ],
    ["user", "Please do. I've attached the PDF we received so you can confirm the totals."],
    [
      "agent",
      "Confirmed — the attached PDF matches invoice INV-4412-R. Voiding the retry now and issuing a credit note for the duplicate amount.",
    ],
    ["user", "Thanks. How long until it shows on our side?"],
    [
      "agent",
      "Credit notes propagate within one billing cycle, usually under an hour. I'll keep this session open until it appears.",
    ],
  ];
  return lines.slice(0, Math.max(3, session.message_count % lines.length || lines.length)).map(
    (line, i): Message => ({
      id: `msg_${session.id}_${i}`,
      session_id: session.id,
      author_kind: line[0],
      author_name:
        line[0] === "agent"
          ? session.agent_name
          : line[0] === "user"
            ? session.participant
            : "system",
      body: line[1],
      created_at: new Date(base + i * 6 * 60_000).toISOString(),
      attachments:
        i === 3
          ? [
              {
                id: `att_${session.id}`,
                filename: `invoice-oct-${si + 1}.pdf`,
                content_type: "application/pdf",
                size_bytes: 182_400 + si * 913,
              },
              {
                id: `att_${session.id}_b`,
                filename: "billing-export.csv",
                content_type: "text/csv",
                size_bytes: 24_118,
              },
            ]
          : [],
    }),
  );
}

let db: Db | null = null;
const listeners = new Set<() => void>();

function load(): Db {
  if (typeof window === "undefined") return emptyDb();
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (raw) return JSON.parse(raw) as Db;
  } catch {
    /* ignore corrupt snapshots */
  }
  return emptyDb();
}

export function getDb(): Db {
  if (!db) db = load();
  return db;
}

export function commit(): void {
  if (typeof window !== "undefined" && db) {
    try {
      window.localStorage.setItem(STORAGE_KEY, JSON.stringify(db));
    } catch {
      /* quota — ignore */
    }
  }
  for (const l of listeners) l();
}

export function replaceDb(next: Db): void {
  db = next;
  commit();
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function id(prefix: string): string {
  return `${prefix}_${Math.random().toString(36).slice(2, 10)}`;
}
