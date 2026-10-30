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
};

/**
 * POST /teams/{id}/channels. Leave `signing_secret` out and the brain
 * generates one; send it only when the provider issued its own.
 */
export type CreateChannelRequest = {
  provider: string;
  name: string;
  signing_secret?: string;
};

/**
 * The create response. `signing_secret` is present only when the brain
 * generated it, and no endpoint returns it again.
 */
export type CreatedChannel = Channel & { signing_secret?: string };

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

export type RunStatus = "queued" | "running" | "succeeded" | "failed" | "cancelled";

/** GET /teams/{id}/sessions/{sessionID}/runs, newest first. */
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
