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

export type Role = "admin" | "approver" | "member" | "guest";

/** GET /teams, GET /teams/{id}. `role` is present only when listing your own. */
export type Team = {
  id: string;
  name: string;
  role?: string;
  /** Set only on a deleted team, which only a super admin ever sees. */
  deleted_at?: string;
  /** deleted_at plus the brain's TEAM_PURGE_AFTER: when it is erased for good. */
  purge_at?: string;
};

/** GET /teams/{id}/members. There is no nested user object and no join date. */
export type Member = {
  user_id: string;
  subject: string;
  email?: string;
  role: string;
  /** A channel user's name: they have no email. */
  name?: string;
  /** A sender let in as someone new, who never logs in. */
  channel_user?: boolean;
};

/** GET /users */
export type User = {
  id: string;
  issuer: string;
  subject: string;
  email?: string;
};

/**
 * GET /teams/{id}/channels. `provider` is the adapter's name, e.g. "custom".
 * `hook_url` is the full address to give the provider, present only when the
 * brain knows its public webhook address.
 */
export type Channel = {
  id: string;
  team_id: string;
  provider: string;
  name: string;
  hook_url?: string;
  /** Where the team's replies are posted; absent when none is set. */
  reply_url?: string;
  /**
   * Whether the channel's provider sends replies at all. Optional: a brain
   * from before the field sends none, and is read as one that does not.
   */
  can_reply?: boolean;
  /**
   * Whether replies go to an address the channel names (`reply_url`). False
   * for Telegram, which replies in the chat; the brain refuses a reply URL.
   */
  takes_reply_url: boolean;
  /**
   * Whether the provider tells a sender nobody has let in that they are
   * waiting, and that they are in. Telegram does. Optional: a brain from
   * before the field sends none, and is read as one that does not.
   */
  can_notify?: boolean;
  /** Whether this channel does. Always false where `can_notify` is. */
  notify_strangers?: boolean;
  /**
   * Whether the provider sends a run's steps as messages of their own.
   * Telegram and custom do; the dashboard channel does not, its stream being
   * every step already. Optional: a brain from before the field sends none.
   */
  can_show_events?: boolean;
  /**
   * What the conversation is sent while a run works: `final`, the answer
   * alone, or `events`, each step a person would follow and then the answer.
   * Always `final` where `can_show_events` is false.
   */
  reply_mode?: ReplyMode;
  /**
   * Whether the channel's number is linked by pairing, as a WhatsApp
   * channel's is. Optional: a brain from before the field sends none.
   */
  can_pair?: boolean;
  /** Where it answers. Optional: a brain from before the field sends none. */
  answers_in?: AnswersIn;
  /** Whether it can be called by a word, as a WhatsApp channel can. */
  can_trigger_word?: boolean;
  /** The word a message must hold to ask it, such as `@Fduv`; null is none. */
  trigger_word?: string | null;
};

/**
 * Where a channel answers: `everywhere` (direct chats, and groups where it is
 * mentioned), only `groups`, or only `direct` chats.
 */
export type AnswersIn = "everywhere" | "groups" | "direct";

export type ReplyMode = "final" | "events";

/** POST /teams/{id}/channels/{channelID}/pairing. `phone` is for `code` alone. */
export type PairChannelRequest = { method: "qr" | "code"; phone?: string };

/**
 * GET /teams/{id}/channels/{channelID}/pairing. `qr` is the text to draw as
 * a QR code, changing about every 20 seconds; `code` is typed on the phone;
 * `held` is whether a WhatsApp role holds the channel at all.
 */
export type ChannelPairing = {
  state: "unpaired" | "pairing" | "connected";
  method?: "qr" | "code";
  qr?: string;
  code?: string;
  phone?: string;
  error?: string;
  held: boolean;
  updated_at?: string;
  /** Once connected: the other channels linked to this number, in this team or another. */
  shared_with?: ChannelSharer[];
  /** The number is shared and this channel answers nothing until every channel on it has a word. */
  needs_trigger_word?: boolean;
};

/** Another channel on a channel's number. */
export type ChannelSharer = {
  channel_id: string;
  team_id: string;
  name: string;
  /** "" when it has none. */
  trigger_word: string;
  /** Whether the caller may set its word. */
  editable: boolean;
};

/**
 * PATCH /teams/{id}/channels/{channelID}. What is sent changes and the rest is
 * left: a null `reply_url` stops replies.
 */
export type UpdateChannelRequest = {
  reply_url?: string | null;
  notify_strangers?: boolean;
  reply_mode?: ReplyMode;
  answers_in?: AnswersIn;
  /** The word a message must hold to ask it; `""` takes it away. */
  trigger_word?: string;
};

/**
 * POST /teams/{id}/channels. Leave `signing_secret` out and the brain
 * generates one; send it only when the provider issued its own.
 */
export type CreateChannelRequest = {
  provider: string;
  name: string;
  signing_secret?: string;
  reply_url?: string;
  /** What the provider asks a person for: Telegram needs `bot_token`. */
  credentials?: Record<string, string>;
  /** Left out, it answers everywhere. */
  answers_in?: AnswersIn;
  /** Left out, on where the provider can tell; refused where it cannot. */
  notify_strangers?: boolean;
  /** WhatsApp only: the word a message must hold to ask it. */
  trigger_word?: string;
};

/**
 * The create response. `signing_secret` is present only when the brain
 * generated it, and no endpoint returns it again.
 */
/**
 * POST /teams/{id}/channels. connected is whether the brain pointed the
 * provider at the channel itself (Telegram's setWebhook); then there is no
 * step left for the person.
 */
export type CreatedChannel = Channel & {
  signing_secret?: string;
  connected?: boolean;
  /**
   * What the person should know about how the provider's account is set up,
   * such as a Telegram bot whose privacy mode hides group messages. Absent
   * when there is nothing to say; the channel is made either way.
   */
  warnings?: string[];
};

/**
 * A row of GET /teams/{id}/channels. Both facts count only the sessions the
 * caller may open: their own direct ones, and everyone's with session:read_all.
 */
export type ListedChannel = Channel & {
  /** The last message of its newest session; null for a channel never used. */
  last_message_at: string | null;
  /** Its sessions with a message in the last 24 hours, on the brain's clock. */
  sessions_24h: number;
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
/** GET /teams/{id}/senders/pending: a channel's pending sender, with the channel it waits on. */
export type TeamPendingSender = PendingSender & {
  channel_id: string;
  channel_name: string;
  channel_provider: string;
};

export type ApprovedSender = {
  sender_ref: string;
  user_id: string;
  created_at: string;
  /** The user's name, or their email; empty when they have neither. */
  name?: string;
  /** The user's role in the team; empty once they have left it. */
  role?: string;
  /** Whether the sender has a user of their own, let in as someone new. */
  channel_user?: boolean;
};

/** Who an approved sender speaks as: a member, or someone new with a role. */
export type Approval = { user_id: string } | { new: { role: string; name?: string } };

/** PATCH /teams/{id}/members/{userID} */
export type MemberChange = { role?: string; name?: string };

/** GET /teams/{id}/sessions/{sessionID} */
export type Session = {
  id: string;
  channel_id: string | null;
  /** A dashboard conversation the team's builder answers, rather than its hub. */
  builder?: boolean;
  provider_ref: string;
  kind: string;
  status: SessionStatus;
  started_at: string;
  last_message_at: string;
  /**
   * The oldest top-level run's input, cut by the brain to 140 characters;
   * null when no run was started in it. Absent from a brain before the field.
   * Read through sessionName, never on its own.
   */
  first_asked?: string | null;
  /** Who a direct session is with; absent for anything else. */
  user_id?: string;
};

export type SessionStatus = "open" | "closed";

/** GET /teams/{id}/sessions, and a channel's: a session with what a list row needs. */
export type ListedSession = Session & {
  /** Null with no channel. */
  channel_name: string | null;
  /** An email, else the provider's subject: who a direct session is with, or who spoke last. */
  participant: string | null;
  /** The newest message, cut by the brain to 140 characters. A stranger's text: render it as text. */
  last_message: string | null;
  /** The newest top-level run's status; null with no run. */
  latest_run_status: RunStatus | null;
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

export type RunStatus = "queued" | "running" | "waiting" | "succeeded" | "failed" | "cancelled";

/** GET /teams/{id}/runs and GET /teams/{id}/sessions/{sessionID}/runs, newest first. */
export type Run = {
  id: string;
  session_id: string;
  /** The agent's name when the run started, kept if it is later renamed. */
  agent: string;
  status: RunStatus;
  input: string;
  output: string | null;
  error: string | null;
  input_tokens: number;
  output_tokens: number;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
  /** The run that delegated this one; null for a run a person started. */
  parent_run_id: string | null;
  /** 0 for a run a person started, one more for each delegation beneath it. */
  depth: number;
  /** Started for the team rather than for an agent: its agent is the team's hub. */
  hub: boolean;
  /** The team's builder, which proposes blueprints and writes nothing. Optional: older brains send none. */
  builder?: boolean;
  /** The message the run was started to answer; its files are the session's attachments with this message_id. */
  message_id?: string | null;
  /**
   * The agents a top-level run handed work to directly, each once, in the
   * order it first did. Optional: a brain from before the field sends none.
   */
  delegates?: string[];
  /**
   * What became of the run's answer to its channel; null for a run that
   * answers none. Optional: a brain from before the field sends none.
   */
  reply?: Reply | null;
  /** How the run was judged, on a single run's read only; absent before, and for a team that never had improvement on. */
  outcome?: RunOutcome;
  /** The caller's 👍/👎 on a single run's read; absent when none. */
  my_feedback?: RunFeedback;
};

export type RunVerdict = "done" | "partly" | "not_done" | "wrong" | "cant_tell";

/** The judge's verdict on a run, and what it comes to once members' feedback is weighed. */
export type RunOutcome = {
  verdict: RunVerdict;
  /** What was asked and not delivered; empty when nothing was. */
  missed: string;
  why: string;
  implicit: "none" | "correction" | "repeat" | "retry" | "thanks";
  /** A member's 👎 or 👍 if there is one, else the verdict. */
  effective: "good" | "bad" | "neither";
  judged_at: string;
  /** How many members gave each, never who. */
  feedback: { up: number; down: number };
};

export type FeedbackValue = "up" | "down";

/** PUT /teams/{id}/runs/{runID}/feedback */
export type RunFeedback = { value: FeedbackValue; note: string; updated_at: string };

export type RunFeedbackRequest = { value: FeedbackValue; note?: string; via?: "dashboard" | "cli" };

/** GET /teams/{id}/sessions/{sessionID}/feedback: the caller's own only. */
export type SessionFeedback = { items: (RunFeedback & { run_id: string })[] };

/** A run's reply to the channel conversation it came from, as the outbox holds it. */
export type Reply = {
  /** `apology` when the run failed or answered nothing; it never carries the error. */
  kind: "answer" | "apology";
  /** `failed` is given up: the channel has no reply URL, or every attempt failed. */
  status: "pending" | "sent" | "failed";
  /** How many times it was tried; 0 before the first. */
  attempts: number;
  last_error: string | null;
  sent_at: string | null;
  /** When a pending reply is tried next; null once it is sent or given up. */
  next_attempt_at: string | null;
};

/** POST /teams/{id}/runs. No `agent` asks the team's hub; no `session_id` gives the run a session of its own. */
/** POST /teams/{id}/sessions and POST …/sessions/{sessionID}/messages: what a person typed on the dashboard. */
export type SayRequest = {
  text: string;
  /** The same for a retry of the same send, so it is one message. */
  key: string;
  /** On a new conversation only: the team's builder answers it, not the hub. */
  builder?: boolean;
};

/** What became of a message typed on the dashboard. */
export type Said = {
  session_id: string;
  message_id: string;
  /** The run answering it: a new one, or the one already working when steered. */
  run_id?: string;
  /** Sent to a run already working, which reads it before its next step. */
  steered?: boolean;
  /** Why no run took it, when none did. */
  unanswered?: string;
};

/** One part of a message on the session stream. */
/** A part of a message: its text, or a file sent with it. */
export type SessionEventPart = { type: string; text?: string; file?: SessionEventFile };

/** A file a person sent: its attachment id, which downloads it. */
export type SessionEventFile = { id: string; filename: string; media_type: string; size: number };

/**
 * A frame of a session's stream, GET /teams/{id}/sessions/{sessionID}/events:
 * sdk/stream's catalog. `seq` is the frame's id, its place in the conversation.
 * Every kind carries `at`, and a run's carry `run_id`.
 */
export type SessionEnvelope = { seq: number } & SessionEvent;

export type SessionEvent =
  | {
      kind: "message";
      data: {
        at: string;
        id: string;
        user_id: string;
        parts: SessionEventPart[];
        state?: "pending";
      };
    }
  | { kind: "message_consumed"; data: { at: string; run_id: string; message_ids: string[] } }
  | {
      kind: "run_started";
      data: { at: string; run_id: string; agent: string; parent_run_id?: string };
    }
  | { kind: "step_started"; data: { at: string; run_id: string; step: number } }
  | {
      kind: "text_message_chunk" | "reasoning_message_chunk";
      data: { at: string; run_id: string; message_id?: string; delta: string; truncated?: boolean };
    }
  | {
      kind: "tool_call_chunk";
      data: {
        at: string;
        run_id: string;
        tool_call_id: string;
        name: string;
        arguments: string;
        truncated?: boolean;
      };
    }
  | {
      kind: "tool_call_result";
      data: {
        at: string;
        run_id: string;
        tool_call_id: string;
        name: string;
        content: string;
        error?: string;
        truncated?: boolean;
        duration_ms: number;
      };
    }
  | {
      kind: "run_finished";
      data: {
        at: string;
        run_id: string;
        status: "succeeded" | "failed" | "cancelled";
        output?: string;
        error?: string;
      };
    }
  | {
      kind: "approval_requested";
      data: {
        at: string;
        run_id: string;
        approval_id: string;
        tool_call_id: string;
        tool: string;
        class: ApprovalClass;
        summary: string;
        code: string;
        expires_at: string;
      };
    }
  | {
      kind: "approval_decided";
      data: {
        at: string;
        run_id: string;
        approval_id: string;
        decision: "approved" | "denied" | "expired" | "cancelled";
        by?: string;
        via?: string;
        scope?: ApprovalScope;
        superseded?: boolean;
      };
    }
  | { kind: "run_waiting"; data: { at: string; run_id: string; reason: string; said?: string } }
  | {
      kind: "file_sent";
      data: {
        at: string;
        run_id: string;
        tool_call_id: string;
        file: SessionEventFile;
        caption?: string;
      };
    }
  | { kind: "other"; name: string; data: unknown };

export type RunRequest = {
  input: string;
  agent?: string;
  session_id?: string;
  /** Start the team's builder instead of an agent or the hub; never with `agent`. */
  builder?: boolean;
};

/**
 * One frame of GET /teams/{id}/runs/{runID}/events. `data` is the parsed
 * JSON as the run recorded it — tool arguments and outputs are whatever a
 * model or a tool produced, so nothing in it is trusted. `seq` is null only
 * on the closing `end` frame.
 */
export type RunEvent = {
  seq: number | null;
  kind: string;
  data: unknown;
};

/** GET /auth/config — public, and answered before anyone has a token. */
export type AuthConfig = {
  environment: string;
  dev_token_accepted: boolean;
  oidc?: { issuer: string; client_id: string };
};

/* --------------------------------------------------------------- registry */

/** GET /teams/{id}/permissions — advice for what to offer, never authority. */
export type TeamPermissions = {
  actions: string[];
  super_admin: boolean;
};

/** GET /teams/{id}/skills */
export type SkillSummary = {
  id: string;
  team_id: string;
  name: string;
  description: string;
  source: string;
  created_at: string;
  updated_at: string;
};

/** A row of GET /teams/{id}/skills: the summary, and how many agents hold it. */
export type ListedSkill = SkillSummary & { agent_count: number };

export type SkillFile = {
  path: string;
  size: number;
  sha256: string;
  media_type: string;
};

/** GET /teams/{id}/skills/{skillID} */
export type Skill = SkillSummary & {
  body: string;
  files: SkillFile[];
  metadata: Record<string, string>;
  license?: string;
  compatibility?: string;
  allowed_tools?: string;
  source_ref?: string;
  source_sha?: string;
};

export type ModelRef = { name: string; max_tokens: number; temperature?: number };

export type MCPServerGrant = { id: string; allow?: string[] };

/** GET /teams/{id}/agents */
export type AgentSummary = {
  id: string;
  team_id: string;
  name: string;
  description: string;
  kind: string;
  model: ModelRef;
  pattern: string;
  created_at: string;
  updated_at: string;
};

/** A row of GET /teams/{id}/agents: the summary, and what it reaches, as counts. */
export type ListedAgent = AgentSummary & {
  delegation: Delegation;
  /** web_search and web_fetch by its own grant. */
  web: boolean;
  /** The team's browser, by its own grant. */
  browser: boolean;
  /** A shell and files in its session's sandbox, by its own grant. Absent from a brain that predates it. */
  workspace?: boolean;
  /** Proposes schedules and watchers from a chat, by its own grant. Absent from a brain that predates it. */
  schedule?: boolean;
  mcp_server_count: number;
  skill_count: number;
  /** Read only when delegation is listed. */
  delegate_count: number;
};

/** GET /teams/{id}/agents/{agentID} */
export type Agent = AgentSummary & {
  instructions: string;
  max_steps: number;
  steer_continuations: number;
  parser: boolean;
  /** A JSON Schema the answer must fit. The dashboard carries it, never edits it. */
  output_schema?: unknown;
  /** The model that fits the answer to output_schema, when parser is on. */
  parser_model?: unknown;
  mcp_servers: MCPServerGrant[];
  skills: string[];
  /** Tokens one run may spend. Absent when the deployment's default applies. */
  token_budget?: number;
  delegation: Delegation;
  /** Whom it may delegate to, by id, when delegation is listed. */
  delegates: string[];
  /** web_search and web_fetch, granted together. A hub has both whatever this says. */
  web: boolean;
  /** The team's browser, to drive pages with. A hub has it whatever this says. */
  browser: boolean;
  /**
   * Bash, Read, Write, Edit, Glob and Grep in its session's sandbox room, a
   * chosen hub's as any agent's. Absent from a brain that predates it.
   */
  workspace?: boolean;
  /**
   * The schedule and watch tools in a chat: it proposes schedules and
   * watchers, each confirmed by the person. Absent from a brain that predates it.
   */
  schedule?: boolean;
  /**
   * Usage notes on tools, left by approved improvements. Read only: a PUT
   * never sends them, and the brain refuses a body that does.
   */
  tool_notes?: ToolNote[];
};

/** A note on one tool of one server, for one agent. */
export type ToolNote = { server: string; tool: string; note: string };

/** Whom an agent may hand work to: any other agent in its team, only its delegates, or no one. */
export type Delegation = "team" | "listed" | "none";

/**
 * POST and PUT /teams/{id}/agents. A PUT is whole, so an edit sends back what
 * the form does not show as it came.
 */
export type AgentRequest = {
  name: string;
  description?: string;
  instructions?: string;
  model: ModelRef;
  pattern: string;
  max_steps: number;
  steer_continuations?: number;
  output_schema?: unknown;
  parser?: boolean;
  parser_model?: unknown;
  mcp_servers?: MCPServerGrant[];
  skills?: string[];
  token_budget?: number;
  /** Left out, the brain reads none. */
  delegation?: Delegation;
  /** Refused unless delegation is listed. */
  delegates?: string[];
  /** Left out, the brain reads false. */
  web?: boolean;
  /** Left out, the brain reads false. */
  browser?: boolean;
  /** Left out, the brain reads false, and a PUT that leaves it out takes it away. */
  workspace?: boolean;
  /** Left out, the brain reads false, and a PUT that leaves it out takes it away. */
  schedule?: boolean;
};

/** GET /teams/{id}/mcp-servers. Credentials are references, never values. */
export type MCPServer = {
  id: string;
  team_id: string;
  name: string;
  url?: string;
  command?: string[];
  env: Record<string, string>;
  /** A command server's plain settings, shown as they are. Absent when there are none. */
  vars?: Record<string, string>;
  /** Where a command server's sandbox room may connect. Absent when nowhere. */
  egress?: Egress[];
  auth_secret_ref?: string;
  /** static: a token or none; oauth: signed in to on the dashboard. */
  auth_kind: "static" | "oauth";
  oauth_client_id?: string;
  oauth_client_secret_ref?: string;
  oauth_scopes?: string[];
  /** Its sign-in, null until one is made. Never its tokens. */
  connection?: MCPServerConnection | null;
  /** The client openarity registered, for a server with no client of its own. Never its secret. */
  oauth_registration?: MCPServerOAuthRegistration | null;
  /** Whether the server's own read-only and destructive hints are believed when a call is classed for approval. */
  trust_hints: boolean;
  bare: boolean;
  discovered_at?: string | null;
  /** Why the last discovery failed, as the caller was told; null once one works. */
  last_discovery_error?: string | null;
  /** When discovery was last tried, whether or not it worked. */
  last_discovery_at?: string | null;
  created_at: string;
  updated_at: string;
};

/** A server's sign-in, as GET /teams/{id}/mcp-servers carries it. */
export type MCPServerConnection = {
  status: "connected" | "needs_sign_in";
  /** Why it needs signing in again. */
  reason?: string | null;
  connected_by?: string | null;
  connected_at: string;
  scopes_granted: string[];
  access_expires_at?: string | null;
};

/** A client openarity registered with a server's sign-in. */
export type MCPServerOAuthRegistration = {
  client_id: string;
  registered_at: string;
};

/** One place a command server's room may connect to. */
export type Egress = { host: string; port: number };

export type MCPServerRequest = {
  name: string;
  url?: string;
  command?: string[];
  env?: Record<string, string>;
  vars?: Record<string, string>;
  egress?: Egress[];
  auth_secret_ref?: string;
  bare?: boolean;
  auth_kind?: "static" | "oauth";
  oauth_client_id?: string;
  oauth_client_secret_ref?: string;
  oauth_scopes?: string[];
};

/** GET /teams/{id}/mcp-servers/{serverID}/tools. The server's own words. */
export type MCPTool = {
  name: string;
  description: string;
  input_schema: Record<string, unknown>;
};

export type GraphLabel = "Team" | "Agent" | "Toolkit" | "Tool" | "Skill";
export type GraphEdgeType = "OWNS" | "GRANTED" | "CONTAINS" | "HAS_SKILL";

/**
 * GET /teams/{id}/summary — the team at a glance. Each count obeys its list's
 * rule, and all four are read at once, so they agree with each other.
 */
export type TeamSummary = {
  /** Top-level runs of the last 24 hours, by status; every status present. */
  runs_24h: Record<RunStatus, number>;
  /** The sessions the caller can see. */
  sessions: number;
  /** Senders waiting to be let in; null without channel:read. */
  waiting_senders: number | null;
  /** MCP servers whose last discovery failed. */
  servers_failing: number;
  /** The team's agents, as the agents list lists them. */
  agents: number;
};

/** GET /teams/{id}/graph — the index as it is, which may lag Postgres. */
export type GraphNode = {
  id: string;
  label: GraphLabel;
  name?: string;
  description?: string;
  kind?: string;
  model?: string;
};

export type GraphEdge = {
  type: GraphEdgeType;
  from: string;
  to: string;
  only?: string[];
};

export type TeamGraph = {
  nodes: GraphNode[];
  edges: GraphEdge[];
  truncated: boolean;
};

/** The providers a team may search with. The first two are self-hosted and take a URL. */
export type SearchProvider =
  | "none"
  | "searxng"
  | "brave"
  | "tavily"
  | "firecrawl-cloud"
  | "firecrawl-oss";

/**
 * Where a team's command MCP servers run. E2B is hosted; Daytona is hosted or
 * self-hosted at a URL; sandboxd is self-hosted and takes a URL.
 */
export type SandboxProvider = "none" | "e2b" | "daytona" | "sandboxd";

/** A workspace room's memory, CPUs and disk, as the brain takes them. */
export type WorkspaceSize = { memory_mb: number; cpus: number; disk_mb: number };

/**
 * What a team's runs drive pages in. Browserless, Steel and Lightpanda are
 * hosted, or self-hosted at a URL; cdp is any CDP browser at a URL; the rest
 * are hosted only.
 */
export type BrowserProvider =
  | "none"
  | "browserless"
  | "steel"
  | "lightpanda"
  | "cdp"
  | "browserbase"
  | "hyperbrowser"
  | "anchor"
  | "kernel"
  | "brightdata";

/**
 * GET /teams/{id}/settings. A team that never saved reads the defaults, with
 * `updated_at` null. The search key is never returned; `key_set` says
 * whether there is one.
 */
/** The room template the brain builds in an E2B team's account. */
export type SandboxTemplateBuild = {
  name: string;
  status: "building" | "ready" | "failed";
  /** E2B's reason; null unless failed. */
  error: string | null;
  updated_at: string;
};

export type TeamSettings = {
  model: { name: string; max_tokens: number; temperature?: number } | null;
  search: { provider: SearchProvider; url: string | null; key_set: boolean };
  /** The key, like the search key, is never returned. */
  sandbox: {
    provider: SandboxProvider;
    url: string | null;
    /** E2B only: a template of the team's own; null for the one the brain builds. */
    template?: string | null;
    key_set: boolean;
    /** Hours a stopped workspace keeps its files. Absent from a brain that predates stopping. */
    keep_hours?: number;
    /** Minutes a running workspace may sit unused; null is the deployment's. */
    idle_minutes?: number | null;
    /** The team's workspace size; null is the provider's own. */
    workspace_size?: WorkspaceSize | null;
    /**
     * E2B with no template of the team's own: the brain's build of its room
     * image in the team's account. Null before one is asked for, and absent
     * from a brain that predates it.
     */
    template_build?: SandboxTemplateBuild | null;
  };
  /** The key is never returned. Absent from a brain that predates browsers. */
  browser?: { provider: BrowserProvider; url: string | null; key_set: boolean };
  improvement: boolean;
  /** The model that judges and digests runs; null means the team's model. */
  judge_model: ModelRef | null;
  /** The percentage of runs judged done that are digested anyway, for contrast. */
  improvement_sample_share: number;
  /** The most passes the improver makes for the team in a day; 1 to 200. */
  improvement_daily_passes: number;
  /** The tokens a pass may spend across its tries, its screen's included; 100,000 to 2,000,000. */
  improvement_pass_tokens: number;
  /** The minutes one try at a pass may run; 1 to 25. */
  improvement_pass_minutes: number;
  /** The judged runs an agent needs since its last pass before the daily pass looks at it. */
  improvement_daily_runs: number;
  /** Whether the weekly efficiency pass runs; only while improvement is on. A brain from before it sends none. */
  improvement_efficiency?: boolean;
  /** The good runs an agent needs in the window; 10 to 500. */
  improvement_efficiency_runs?: number;
  /** The days of runs it counts; 7 to 60. */
  improvement_efficiency_days?: number;
  /** How far the costliest good runs sit above the usual, in percent of it (200 is twice); 125 to 1000. */
  improvement_efficiency_spread?: number;
  /** The good runs a waste mark must appear in; 2 to 50, at most the runs. */
  improvement_efficiency_mark_runs?: number;
  /** Bad runs fewer than the usual bar that flag an efficiency change; 0 to 3. */
  improvement_efficiency_margin?: number;
  /** Whether each proposal is tried on a few past runs before it is decided. */
  improvement_replay?: boolean;
  /** Past runs a proposal is tried on; 1 to 10. */
  improvement_replay_runs?: number;
  /** Tokens one try may spend; 20,000 to 1,000,000. */
  improvement_replay_tokens?: number;
  /** Minutes one try may take; 1 to 30. */
  improvement_replay_minutes?: number;
  /** Tries the team runs a day (UTC); 1 to 500. */
  improvement_replay_daily?: number;
  /** When improvement was last turned on; runs that finished before it are never judged. Read only. */
  improvement_since: string | null;
  compaction_messages: number;
  /** A run compacts at a tenth of its budget, held between these two. */
  compaction_tokens_min: number;
  compaction_tokens_max: number;
  /** The agent every message reaches first; null for the built-in orchestrator. */
  hub: { id: string; name: string } | null;
  updated_at: string | null;
};

/**
 * PUT /teams/{id}/settings, the whole set. Leave `api_key` out to keep the
 * stored key; for firecrawl-oss an empty one removes it.
 */
export type TeamSettingsRequest = {
  model: { name: string; max_tokens: number; temperature?: number } | null;
  search: { provider: SearchProvider; url?: string; api_key?: string };
  /**
   * Left out, the stored sandbox stays. `api_key` left out keeps the stored
   * key while the provider and its URL are unchanged.
   */
  sandbox?: {
    provider: SandboxProvider;
    url?: string;
    template?: string;
    api_key?: string;
    /** 1 to 720. Left out, the stored value stays. */
    keep_hours?: number;
    /** 1 to 1440. Left out, the stored value stays; null is the deployment's. */
    idle_minutes?: number | null;
    /** Left out, the stored size stays; null is the provider's. Not for E2B yet. */
    workspace_size?: WorkspaceSize | null;
  };
  /**
   * Left out, the stored browser stays. `api_key` left out keeps the stored
   * key while the provider and its URL are unchanged; for a self-hosted
   * browser an empty one removes it.
   */
  browser?: { provider: BrowserProvider; url?: string; api_key?: string };
  improvement: boolean;
  /** Left out, the stored judge model stays; null clears it, so runs are judged on the team's model. */
  judge_model?: ModelRef | null;
  /** 0 to 100. Left out, the stored share stays. */
  improvement_sample_share?: number;
  /** 1 to 200. Left out, the stored limit stays. */
  improvement_daily_passes?: number;
  /** 100,000 to 2,000,000. Left out, the stored value stays. */
  improvement_pass_tokens?: number;
  /** 1 to 25. Left out, the stored value stays. */
  improvement_pass_minutes?: number;
  improvement_daily_runs?: number;
  /** Each left out keeps the stored value. */
  improvement_efficiency?: boolean;
  improvement_efficiency_runs?: number;
  improvement_efficiency_days?: number;
  improvement_efficiency_spread?: number;
  improvement_efficiency_mark_runs?: number;
  improvement_efficiency_margin?: number;
  improvement_replay?: boolean;
  improvement_replay_runs?: number;
  improvement_replay_tokens?: number;
  improvement_replay_minutes?: number;
  improvement_replay_daily?: number;
  compaction_messages: number;
  /** Left out, the stored value stays. */
  compaction_tokens_min?: number;
  compaction_tokens_max?: number;
  /** An agent's name in the team; null for the built-in orchestrator. */
  hub: string | null;
};

/**
 * POST /teams/{id}/blueprints/plan. The dashboard does not build a blueprint's
 * servers and agents; a person writes them, and the brain checks every field,
 * so they pass through as written. Skills are folders the page reads.
 */
export type Blueprint = {
  mcp_servers?: Record<string, unknown>[];
  skills?: BlueprintSkill[];
  agents?: Record<string, unknown>[];
};

/** A skill's files, each one's bytes as base64. */
export type BlueprintSkill = { files: { path: string; data: string }[] };

export type BlueprintKind = "mcp_server" | "skill" | "agent";

export type BlueprintChange = {
  kind: BlueprintKind;
  name: string;
  action: "create" | "update" | "unchanged";
  /** What an update changes, by field name, never the values. */
  fields?: string[];
  id?: string;
};

/**
 * An MCP server an apply wrote, asked for its tools once the write
 * committed: its tools by name, or why it has none yet.
 */
export type BlueprintDiscovered = { server: string; tools: string[]; error?: string };

export type BlueprintRefusal = { kind: BlueprintKind; name: string; message: string };

export type BlueprintPlan = {
  changes: BlueprintChange[];
  refusals: BlueprintRefusal[];
  /** Names this plan; apply sends it back. */
  digest: string;
};

/**
 * What the team's builder proposed: a blueprint and its plan. Read one by
 * id and an open proposal's plan is made now, as the reader: what applying it
 * would do, and a refusal for any write they may not make.
 */
export type Proposal = {
  id: string;
  session_id: string;
  run_id: string;
  summary: string;
  /** `superseded` once the builder proposes again in the same conversation. */
  state: "open" | "applied" | "superseded";
  created_at: string;
  applied_at?: string | null;
  /** Present when one proposal is read. */
  blueprint?: Blueprint;
  plan?: BlueprintPlan;
};

/* -------------------------------------------------------- tool approvals */

export type ApprovalStatus = "pending" | "approved" | "denied" | "expired" | "cancelled";
export type ApprovalClass = "read" | "write" | "destructive";
export type ApprovalScope = "once" | "run";

/**
 * GET /teams/{id}/approvals. One call an agent asked a person about before
 * making it. Not the sender `Approval` above, which is a request to let
 * someone in.
 */
export type ToolApproval = {
  id: string;
  /** The run that asked, which may be a delegate's. */
  run_id: string;
  /** The top-level run its tree waits under. */
  root_run_id: string;
  call_id: string;
  /** `<server>__<tool>`, or a built-in's own name. */
  tool: string;
  class: ApprovalClass;
  /** Exactly as the call will be made if approved. */
  arguments: Record<string, unknown>;
  /** A few "field: value" lines. */
  summary: string;
  /** Four characters a person can type into a chat to name this request. */
  code: string;
  status: ApprovalStatus;
  requested_for: string | null;
  require_other: boolean;
  decided_by: string | null;
  decided_via: string | null;
  scope: ApprovalScope | null;
  reason: string | null;
  expires_at: string;
  decided_at: string | null;
  used_at: string | null;
  created_at: string;
};

/** POST /teams/{id}/approvals/{approvalID}/decision */
export type ApprovalDecision = {
  approve: boolean;
  /** Only a yes may be "run". */
  scope?: ApprovalScope;
  reason?: string;
  via?: "dashboard" | "cli";
};

export type ApprovalMode = "off" | "writes" | "destructive" | "all";

/** GET and PUT /teams/{id}/approval-settings */
export type ApprovalSettings = { mode: ApprovalMode; ttl_minutes: number };

export type ApprovalRuleMode = "allow" | "ask" | "deny";

/** GET /teams/{id}/approval-rules */
export type ApprovalRule = {
  id: string;
  target_kind: "tool" | "server";
  target: string;
  mode: ApprovalRuleMode;
  require_other: boolean;
  created_at: string;
};

/** POST /teams/{id}/approval-rules */
export type ApprovalRuleRequest = {
  target_kind: "tool" | "server";
  target: string;
  mode: ApprovalRuleMode;
  require_other?: boolean;
};

/* ------------------------------------------------------------ improvements */

export type ImprovementStatus = "waiting" | "applied" | "rejected" | "reverted" | "withdrawn";

/** A proposal's state to list by, or flagged: the applied changes whose watch flagged them. */
export type ImprovementFilter = ImprovementStatus | "flagged";

export type ImprovementKind =
  | "instructions"
  | "description"
  | "skill_grant"
  | "skill_drop"
  | "skill_new"
  | "skill_edit"
  | "server_grant"
  | "server_drop"
  | "tools_narrow"
  | "builtin_tool"
  | "tool_note"
  | "limits"
  | "delegation"
  | "pattern";

export type ImprovementMark =
  | "widens_access"
  | "widens_reach"
  | "raises_cost"
  | "changes_code"
  | "changes_discovery"
  | "changes_behaviour";

/** A thing by id and name; the name is the id when it no longer exists. */
export type NamedRef = { id: string; name: string };

/** One change: text is whole before and after, list is names, value is one value. */
export type ImprovementChange = {
  field: string;
} & (
  | { type: "text" | "value"; before: string | null; after: string | null }
  | { type: "list"; before: string[] | null; after: string[] | null }
);

/** GET /teams/{id}/improvements/{proposalID} */
export type Improvement = {
  id: string;
  kind: ImprovementKind;
  status: ImprovementStatus;
  agent: NamedRef;
  /** The skill a skill_edit changes; null for every other kind. */
  skill?: NamedRef | null;
  why: string;
  hypothesis: string;
  /** A general weakness of agents (invariant), or this team's domain. */
  scope: "invariant" | "domain";
  signature?: string | null;
  /** Quality, for runs that went wrong, or efficiency, for good runs made cheaper or quicker. A brain from before sends none. */
  goal?: "quality" | "efficiency";
  /** What an efficiency change aims to save; empty for a quality change. */
  saves?: ("cost" | "time")[];
  marks: ImprovementMark[];
  /** For a skill_edit, every agent that holds the skill now. */
  reaches: NamedRef[];
  changes: ImprovementChange[];
  /** The runs it cites; a readable one says the conversation it is read in. */
  runs: { id: string; readable: boolean; session_id?: string }[];
  /** A channel sender who is not a member spoke in a session it came from. */
  outsider: boolean;
  /** The improver pass that wrote it; null for one that did not come from a pass. */
  pass_id?: string | null;
  created_at: string;
  decided_by?: string | null;
  decided_at?: string | null;
  reverted_by?: string | null;
  reverted_at?: string | null;
  /** How the change went once applied; null until it is. */
  watch?: ImprovementWatch | null;
  /** How it fared on past runs before it was decided; null in a decision's answer. */
  replay?: ImprovementReplay | null;
};

/**
 * How a change fared on a few of its agent's past runs, tried before anyone
 * decided on it: off when never tried, trying while a try is still to run,
 * done once one came to an outcome, none when none could be tried.
 */
export type ImprovementReplay = {
  status: "off" | "trying" | "done" | "none";
  runs: ImprovementReplayRun[];
};

/** What a run spent: tokens (a cached one as a tenth), steps, time working. */
export type ImprovementReplayCost = { tokens: number; steps: number; work_ms: number };

/** What a done try made of its run: went wrong and now goes well, and so on. */
export type ImprovementReplayOutcome = "fixed" | "kept" | "broke" | "still_bad" | "unclear";

/** One past run a change was tried on, beside what the run did itself. */
export type ImprovementReplayRun = {
  run: { id: string; readable: boolean; session_id?: string };
  chosen: "cited" | "good";
  status: "queued" | "trying" | "done" | "failed" | "skipped";
  skip_why?: string | null;
  error?: string | null;
  original: "good" | "bad";
  replayed?: "good" | "bad" | "cant_tell" | null;
  /** The run's outcome beside the try's, once it is done; the brain decides it. */
  outcome?: ImprovementReplayOutcome | null;
  verdict?: string | null;
  why?: string | null;
  /** What the run was asked; null, with withheld, for a conversation the reader cannot read. */
  request?: string | null;
  withheld: boolean;
  /** The try came to a call the run never made that was not a read, and it was not made. */
  diverged: boolean;
  recorded: number;
  live: number;
  not_made: number;
  before: ImprovementReplayCost;
  after?: ImprovementReplayCost | null;
  started_at?: string | null;
  finished_at?: string | null;
};

/** One try whole: each call as it was served, and the answer it came to. */
export type ImprovementReplayTrace = {
  run: { id: string; readable: boolean; session_id?: string };
  answer?: string | null;
  calls: {
    name: string;
    how: "recorded" | "live" | "not_made";
    arguments: string;
    output?: string;
    error?: string;
  }[];
};

/** What one window of judged runs came to. */
export type ImprovementWatchSide = {
  runs: number;
  bad: number;
  /** Runs a member gave 👍, and 👎. */
  up: number;
  down: number;
  /** The median tokens a run's tree spent, a cached token as a tenth. */
  tokens?: number | null;
  steps?: number | null;
  /** The median time its good runs worked, in ms, leaving out waits for a person. For an efficiency change, tokens are its good runs' too. */
  work_ms?: number | null;
  /** Runs carrying the signature the change targeted; null for a removal. */
  signature?: number | null;
};

/**
 * The agent's judged runs after a change against the ones before, measured
 * against how much its runs usually vary. A flag asks a person to look.
 */
export type ImprovementWatch = {
  status: "watching" | "clear" | "flagged" | "cleared" | "ended";
  flag?: "worse" | "costlier" | "both" | "no_saving" | null;
  ended_why?: "reverted" | "too_few_runs" | null;
  /** The judged runs after the change that decide it. */
  needed: number;
  baseline_runs: number;
  baseline_bad: number;
  /** The bad runs out of `needed` that flag it; null with too little history. */
  bar?: number | null;
  baseline_p90?: number | null;
  before: ImprovementWatchSide;
  after: ImprovementWatchSide;
  /** Other changes applied to the same agents within the windows. */
  overlapping: string[];
  started_at: string;
  finished_at?: string | null;
  cleared_by?: string | null;
  cleared_at?: string | null;
};

/** What set an improver pass going: a weakness that recurred, the day's turn, or the week's look at good runs. */
export type ImproverTrigger = "pattern" | "daily" | "efficiency";

export type ImproverPassStatus = "running" | "done" | "failed";

/** GET /teams/{id}/improvements/passes: one background read of an agent's failure patterns. */
export type ImproverPass = {
  id: string;
  agent: NamedRef;
  trigger: ImproverTrigger;
  /** "part:target:cause", as "tool:crm__close_ticket:misused_tool"; the target may be empty. */
  signatures: string[];
  /** The prune candidates it was given to look at: "skill:<name>" or "tool:<server>/<tool>". */
  pruned: string[];
  /** The candidates it proposed removing; it kept the rest of pruned. */
  prune_proposed: string[];
  /** The judged runs a daily pass had since the agent's last pass, or the good runs an efficiency pass drew from; null for one a pattern started. */
  runs_seen?: number | null;
  status: ImproverPassStatus;
  /** Done before it finished: out of steps, tokens or time, improvement turned off, or its agent gone. */
  cut_short: boolean;
  /** Plain text, possibly several paragraphs. */
  report: string;
  /** The report draws on a conversation the caller may not read, so it is not sent. */
  report_withheld: boolean;
  proposed: number;
  screened_out: number;
  model: string;
  input_tokens: number;
  output_tokens: number;
  error: string | null;
  started_at: string;
  finished_at: string | null;
};

/** POST /teams/{id}/improvements/{proposalID}/decision */
export type ImprovementDecision = { action: "approve" | "reject" | "revert" | "clear" };
