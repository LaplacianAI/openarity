import type { Agent, AgentRequest, Delegation, ListedAgent, MCPServerGrant } from "@/api/types";
import { count } from "@/lib/runs";

/** An agent as the editor holds it: numbers as typed, lists as chosen. */
export type Draft = {
  name: string;
  description: string;
  instructions: string;
  model: string;
  maxTokens: string;
  pattern: string;
  maxSteps: string;
  tokenBudget: string;
  servers: MCPServerGrant[];
  skills: string[];
  delegation: Delegation;
  /** Kept while another choice is shown, and sent only with listed. */
  delegates: string[];
  web: boolean;
};

/** The brain refuses more; this only says so before the round trip. */
const MAX_TOKEN_BUDGET = 100_000_000;

/** A new agent: alone and off the web, as the brain's own defaults are. */
export const EMPTY: Draft = {
  name: "",
  description: "",
  instructions: "",
  model: "",
  maxTokens: "1024",
  pattern: "react",
  maxSteps: "5",
  tokenBudget: "",
  servers: [],
  skills: [],
  delegation: "none",
  delegates: [],
  web: false,
};

/**
 * The draft as the API takes it. Only what cannot be sent is refused here;
 * which patterns exist and what a model name means are the brain's to say.
 */
export function agentRequest(d: Draft): AgentRequest | string {
  if (!d.name.trim()) return "An agent needs a name.";
  if (!d.model.trim()) return "An agent needs a model, as the gateway names it.";
  const maxTokens = Number(d.maxTokens);
  const maxSteps = Number(d.maxSteps);
  if (!Number.isInteger(maxTokens) || maxTokens < 1)
    return "Max tokens is a whole number above zero.";
  if (!Number.isInteger(maxSteps) || maxSteps < 1) return "Max steps is a whole number above zero.";
  const budget = d.tokenBudget.trim() === "" ? undefined : Number(d.tokenBudget);
  if (
    budget !== undefined &&
    (!Number.isInteger(budget) || budget < 1 || budget > MAX_TOKEN_BUDGET)
  )
    return "Token budget is a whole number from 1 to 100,000,000, or empty for the default.";

  return {
    name: d.name.trim(),
    description: d.description.trim(),
    instructions: d.instructions,
    model: { name: d.model.trim(), max_tokens: maxTokens },
    pattern: d.pattern.trim(),
    max_steps: maxSteps,
    mcp_servers: d.servers.map((g) => (g.allow ? { id: g.id, allow: g.allow } : { id: g.id })),
    skills: d.skills,
    ...(budget === undefined ? {} : { token_budget: budget }),
    delegation: d.delegation,
    ...(d.delegation === "listed" ? { delegates: d.delegates } : {}),
    web: d.web,
  };
}

/** The agent as the editor shows it. */
export function draftOf(a: Agent): Draft {
  return {
    name: a.name,
    description: a.description,
    instructions: a.instructions,
    model: a.model.name,
    maxTokens: String(a.model.max_tokens),
    pattern: a.pattern,
    maxSteps: String(a.max_steps),
    tokenBudget: a.token_budget === undefined ? "" : String(a.token_budget),
    servers: a.mcp_servers,
    skills: a.skills,
    delegation: a.delegation,
    delegates: a.delegates,
    web: a.web === true,
  };
}

/** Whether two drafts would send the same agent: what "unsaved changes" means. */
export function sameDraft(a: Draft, b: Draft): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** What the brain holds beyond the editor; the PUT is whole, so they go back as they came. */
const UNSHOWN = ["steer_continuations", "output_schema", "parser", "parser_model"] as const;

/**
 * The PUT for an edit: the agent as it is, with the draft laid over it. The
 * draft carries each server's tool limit, so a grant limited to some tools
 * stays limited unless the editor changed it.
 */
export function replaceRequest(a: Agent, d: Draft): AgentRequest | string {
  const form = agentRequest(d);
  if (typeof form === "string") return form;
  const kept: Partial<AgentRequest> = {};
  for (const k of UNSHOWN) {
    if (a[k] !== undefined && a[k] !== null) (kept as Record<string, unknown>)[k] = a[k];
  }
  return { ...form, ...kept, model: { ...a.model, ...form.model } };
}

/** "2 servers, 3 skills", leaving out what it has none of; "None" for an agent with nothing. */
export function grantWords(a: ListedAgent): string {
  const parts = [
    a.mcp_server_count > 0 ? count(a.mcp_server_count, "server") : null,
    a.skill_count > 0 ? count(a.skill_count, "skill") : null,
  ].filter(Boolean);
  return parts.length === 0 ? "None" : parts.join(", ");
}

/** Whom it may hand work to, as a person would say it. */
export function delegationWords(a: ListedAgent): string {
  switch (a.delegation) {
    case "team":
      return "Any agent";
    case "listed":
      return a.delegate_count === 0 ? "No one listed" : count(a.delegate_count, "agent");
    default:
      return "No one";
  }
}
