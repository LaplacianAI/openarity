/**
 * A request an agent made of a person, as the card shows it. It comes from two
 * places: the session stream, which says it was made and how it was answered,
 * and the approvals API, which also has the arguments and who it was for.
 */
import type { ApprovalClass, ApprovalScope, ApprovalStatus, ToolApproval } from "@/api/types";
import type { Request } from "@/lib/session-stream";

export type RequestView = {
  id: string;
  runId: string;
  tool: string;
  class: ApprovalClass;
  summary: string;
  code: string;
  expiresAt: string;
  status: ApprovalStatus;
  by: string | null;
  via: string | null;
  scope: ApprovalScope | null;
  reason: string | null;
  superseded: boolean;
  /** Unknown (null) until the API has been read. */
  requestedFor: string | null;
  requireOther: boolean;
  arguments: Record<string, unknown> | null;
};

export function fromApproval(a: ToolApproval): RequestView {
  return {
    id: a.id,
    runId: a.run_id,
    tool: a.tool,
    class: a.class,
    summary: a.summary,
    code: a.code,
    expiresAt: a.expires_at,
    status: a.status,
    by: a.decided_by,
    via: a.decided_via,
    scope: a.scope,
    reason: a.reason,
    // A request ended with its run is decided by nobody; one its person
    // gave up by writing again is decided by them.
    superseded: a.status === "cancelled" && a.decided_by != null,
    requestedFor: a.requested_for,
    requireOther: a.require_other,
    arguments: a.arguments,
  };
}

export function fromStream(r: Request): RequestView {
  return {
    id: r.id,
    runId: r.runId,
    tool: r.tool,
    class: r.class,
    summary: r.summary,
    code: r.code,
    expiresAt: r.expiresAt,
    status: r.status,
    by: r.by,
    via: r.via,
    scope: r.scope,
    reason: null,
    superseded: r.superseded,
    requestedFor: null,
    requireOther: false,
    arguments: null,
  };
}

/**
 * The stream's view with what the API added. The stream is the newer word on
 * where a request stands once it says it was answered; the API has the rest.
 */
export function merged(stream: RequestView, api: ToolApproval | undefined): RequestView {
  if (!api) return stream;
  const full = fromApproval(api);
  return {
    ...full,
    status: stream.status !== "pending" ? stream.status : full.status,
    by: stream.by ?? full.by,
    via: stream.via ?? full.via,
    scope: stream.scope ?? full.scope,
    superseded: stream.superseded,
  };
}

/** "<server>__<tool>" as the two words a person reads; a built-in has no server. */
export function toolParts(tool: string): { server: string | null; name: string } {
  const at = tool.indexOf("__");
  if (at <= 0) return { server: null, name: tool };
  return { server: tool.slice(0, at), name: tool.slice(at + 2) };
}

/** "4 min left", "under a minute left", or null once it has passed. */
export function timeLeft(expiresAt: string, now: number): string | null {
  const ms = Date.parse(expiresAt) - now;
  if (!Number.isFinite(ms) || ms <= 0) return null;
  const mins = Math.ceil(ms / 60_000);
  if (mins < 2) return "under a minute left";
  if (mins < 60) return `${mins} min left`;
  const hours = Math.floor(mins / 60);
  const rest = mins % 60;
  return rest === 0 ? `${hours} h left` : `${hours} h ${rest} min left`;
}

/** Who may answer, as the brain decides it; it still checks. */
export function mayAnswer(
  r: Pick<RequestView, "requestedFor" | "requireOther">,
  can: (permission: string) => boolean,
  me: string | null,
): boolean {
  if (!can("approval:self")) return false;
  const mine = me !== null && r.requestedFor === me;
  if (r.requireOther && mine) return false;
  return can("approval:decide") || mine;
}

/** What it takes when the caller may not answer, as a sentence. */
export function whyNot(
  r: Pick<RequestView, "requestedFor" | "requireOther">,
  can: (permission: string) => boolean,
  me: string | null,
): string {
  if (!can("approval:self")) return "Answering takes approval:self.";
  if (r.requireOther && me !== null && r.requestedFor === me) {
    return "A rule wants someone other than the person it was asked for to answer.";
  }
  return "Only the person it was asked for, or someone who may decide for the team, can answer.";
}

const VIA: Record<string, string> = {
  dashboard: "on the dashboard",
  channel: "in chat",
  cli: "with oa",
};

/** The outcome as a sentence, without who. */
export function outcome(r: RequestView): string {
  switch (r.status) {
    case "pending":
      return "Waiting for an answer";
    case "approved":
      return r.scope === "run" ? "Approved for this run" : "Approved";
    case "denied":
      return "Declined";
    case "expired":
      return "Expired: nobody answered in time";
    case "cancelled":
      return r.superseded ? "Set aside: you wrote again" : "Cancelled";
  }
}

/** "by ann on the dashboard", "by the brain", or "" when nothing is known. */
export function answeredBy(r: RequestView, nameOf: (id: string) => string | null): string {
  const parts: string[] = [];
  if (r.via === "sweep") parts.push("by the brain");
  else if (r.by) parts.push(`by ${nameOf(r.by) ?? "a team member"}`);
  if (r.via && r.via !== "sweep") parts.push(VIA[r.via] ?? r.via);
  return parts.join(" ");
}
