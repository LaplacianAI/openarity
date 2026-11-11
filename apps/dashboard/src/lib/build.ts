/**
 * The Build chat: a conversation with the team's builder, read from the
 * session's builder runs and their proposals. Nothing here decides anything;
 * the brain plans and refuses.
 */
import type { Blueprint, BlueprintChange, BlueprintPlan, Proposal, Run } from "@/api/types";
import { type AskRefusal, askRefusal } from "@/lib/ask";

/** One exchange: what the person asked, the builder's run, and what it proposed. */
export type Turn = { run: Run; proposal: Proposal | null };

/** A run is live until it ends; the chat polls while one is. */
export function isLive(run: Run): boolean {
  return run.status === "queued" || run.status === "running";
}

/**
 * The session's builder runs, oldest first, each with the newest proposal it
 * made. A run proposes up to five times and only its last one stays open,
 * so the newest is the one that matters.
 */
export function turnsOf(runs: Run[], proposals: Proposal[]): Turn[] {
  const byRun = new Map<string, Proposal>();
  for (const p of proposals) {
    const seen = byRun.get(p.run_id);
    if (!seen || p.created_at > seen.created_at) byRun.set(p.run_id, p);
  }
  return runs
    .filter((r) => r.builder)
    .slice()
    .sort((a, b) => (a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0))
    .map((run) => ({ run, proposal: byRun.get(run.id) ?? null }));
}

export const NO_BUILDER_MODEL = "The builder can't run until the team has a model.";

/** Why the builder could not be asked, in the chat's words. */
export function buildRefusal(err: unknown): AskRefusal {
  const r = askRefusal(err);
  return r.settings ? { text: NO_BUILDER_MODEL, settings: true } : r;
}

/**
 * What a proposal asks of the person besides Apply: each secret its servers
 * name, which they write to the secret store themselves, and each address it
 * would newly send the team's agents to. Only servers the plan creates or
 * changes count; one left alone asks nothing new.
 */
export type Needs = { secrets: string[]; addresses: { server: string; host: string }[] };

export function needsOf(blueprint: Blueprint | undefined, plan: BlueprintPlan): Needs {
  const touched = new Map<string, BlueprintChange>();
  for (const c of plan.changes) {
    if (c.kind === "mcp_server" && c.action !== "unchanged") touched.set(c.name, c);
  }
  const secrets = new Set<string>();
  const addresses: Needs["addresses"] = [];
  for (const s of blueprint?.mcp_servers ?? []) {
    const name = typeof s.name === "string" ? s.name : "";
    const change = touched.get(name);
    if (!change) continue;
    if (typeof s.auth_secret === "string") secrets.add(s.auth_secret);
    if (s.env && typeof s.env === "object") {
      for (const ref of Object.values(s.env as Record<string, unknown>)) {
        if (typeof ref === "string") secrets.add(ref);
      }
    }
    const moved = change.action === "create" || change.fields?.includes("url");
    const host = typeof s.url === "string" ? hostOf(s.url) : null;
    if (moved && host) addresses.push({ server: name, host });
  }
  return { secrets: [...secrets].sort(), addresses };
}

function hostOf(url: string): string | null {
  try {
    return new URL(url).host || null;
  } catch {
    return null;
  }
}
