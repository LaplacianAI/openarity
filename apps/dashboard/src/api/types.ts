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

export type Role = "admin" | "approver" | "member";

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
};

/**
 * PATCH /teams/{id}/channels/{channelID}. What is sent changes and the rest is
 * left: a null `reply_url` stops replies.
 */
export type UpdateChannelRequest = { reply_url?: string | null; notify_strangers?: boolean };

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
};

/** GET /teams/{id}/sessions/{sessionID} */
export type Session = {
  id: string;
  channel_id: string | null;
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

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";

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
};

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
export type SessionEventPart = { type: string; text?: string };

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
};

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
};

/** GET /teams/{id}/mcp-servers. Credentials are references, never values. */
export type MCPServer = {
  id: string;
  team_id: string;
  name: string;
  url?: string;
  command?: string[];
  env: Record<string, string>;
  auth_secret_ref?: string;
  bare: boolean;
  discovered_at?: string | null;
  /** Why the last discovery failed, as the caller was told; null once one works. */
  last_discovery_error?: string | null;
  /** When discovery was last tried, whether or not it worked. */
  last_discovery_at?: string | null;
  created_at: string;
  updated_at: string;
};

export type MCPServerRequest = {
  name: string;
  url?: string;
  command?: string[];
  env?: Record<string, string>;
  auth_secret_ref?: string;
  bare?: boolean;
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
 * GET /teams/{id}/settings. A team that never saved reads the defaults, with
 * `updated_at` null. The search key is never returned; `key_set` says
 * whether there is one.
 */
export type TeamSettings = {
  model: { name: string; max_tokens: number; temperature?: number } | null;
  search: { provider: SearchProvider; url: string | null; key_set: boolean };
  learning: boolean;
  compaction_messages: number;
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
  learning: boolean;
  compaction_messages: number;
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
