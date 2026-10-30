/**
 * A run's events, as the brain streams them, grouped into what a person reads:
 * the steps the agent took, each holding the tools it called with what they
 * returned, and the few things that happened to the run as a whole.
 *
 * The events are flat on the wire — `step`, `usage`, `tool_call`,
 * `tool_result`, one after another — and nothing in a result names its step.
 * A result belongs to the step that was current when it arrived, and to the
 * call with the same id; a pattern that sends no ids is paired by order.
 *
 * Pure, and run again over the whole list on every new event, so a trace read
 * live and the same trace read after the fact group the same way.
 */
import type { RunEvent } from "@/api/types";

export type Tokens = {
  input: number;
  output: number;
  cached: number;
  /** Input plus output: what the run was charged for. */
  total: number;
};

export type ToolResult = {
  output: string;
  /** Empty when the tool succeeded. */
  error: string;
  truncated: boolean;
  durationMs: number | null;
};

export type ToolCall = {
  /** The model's id for the call, or null when the pattern sends none. */
  id: string | null;
  name: string;
  /** The arguments as the model wrote them, usually JSON; null for a result nobody called. */
  arguments: string | null;
  truncated: boolean;
  /** Null until the result arrives. */
  result: ToolResult | null;
  /** The run a `delegate` call started, from the `delegated` event; absent until it arrives. */
  delegated?: { agent: string; runId: string };
};

export type Step = {
  /** The step's number as the agent counted it; null for events that came before any step. */
  number: number | null;
  calls: ToolCall[];
  usage: Tokens;
  /** The model each usage event named, in order, without repeats. */
  models: string[];
  /** Events of a kind this dashboard does not know, kept where they arrived. */
  other: RunEvent[];
};

export type Started = {
  agent: string;
  model: string;
  pattern: string;
  tools: string[];
  skills: string[];
};

export type PlanStatus = "pending" | "in_progress" | "done";

export type PlanItem = { text: string; status: PlanStatus };

/** What happened, in order. Steps and the run-level events between them. */
export type TraceItem =
  | { type: "step"; step: Step }
  | { type: "plan"; items: PlanItem[] }
  | { type: "resumed"; messages: number }
  | { type: "unavailable"; name: string; reason: string }
  | { type: "other"; event: RunEvent };

export type Trace = {
  started: Started | null;
  items: TraceItem[];
  /** The `finished` event: the run's own account of how it ended. */
  finished: { status: string; error: string } | null;
  /** The stream's closing `end` frame: nothing more will arrive. */
  ended: { status: string } | null;
  usage: Tokens;
  /** The hub's plan as it last kept it; null until it keeps one. */
  plan: PlanItem[] | null;
};

function emptyTokens(): Tokens {
  return { input: 0, output: 0, cached: 0, total: 0 };
}

function record(data: unknown): Record<string, unknown> {
  return data !== null && typeof data === "object" && !Array.isArray(data)
    ? (data as Record<string, unknown>)
    : {};
}

function text(v: unknown): string {
  return typeof v === "string" ? v : typeof v === "number" ? String(v) : "";
}

function count(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.map(String) : [];
}

export function groupTrace(events: readonly RunEvent[]): Trace {
  const trace: Trace = {
    started: null,
    items: [],
    finished: null,
    ended: null,
    usage: emptyTokens(),
    plan: null,
  };
  let current: Step | null = null;
  const calls: ToolCall[] = [];

  // Usage and tool calls that arrive before any step still need a home.
  const step = (): Step => {
    if (current) return current;
    current = { number: null, calls: [], usage: emptyTokens(), models: [], other: [] };
    trace.items.push({ type: "step", step: current });
    return current;
  };

  for (const e of events) {
    const d = record(e.data);
    switch (e.kind) {
      case "started":
        trace.started = {
          agent: text(d.agent),
          model: text(d.model),
          pattern: text(d.pattern),
          tools: strings(d.tools),
          skills: strings(d.skills),
        };
        break;
      case "resumed":
        current = null;
        trace.items.push({ type: "resumed", messages: count(d.messages) });
        break;
      case "tool_unavailable":
        trace.items.push({ type: "unavailable", name: text(d.name), reason: text(d.reason) });
        break;
      case "step": {
        const n = d.step;
        current = {
          number: typeof n === "number" ? n : null,
          calls: [],
          usage: emptyTokens(),
          models: [],
          other: [],
        };
        trace.items.push({ type: "step", step: current });
        break;
      }
      case "usage": {
        const s = step();
        const input = count(d.input_tokens);
        const output = count(d.output_tokens);
        const cached = count(d.cached_input_tokens);
        for (const t of [s.usage, trace.usage]) {
          t.input += input;
          t.output += output;
          t.cached += cached;
          t.total += input + output;
        }
        const model = text(d.model);
        if (model && !s.models.includes(model)) s.models.push(model);
        break;
      }
      case "tool_call": {
        const call: ToolCall = {
          id: text(d.id) || null,
          name: text(d.name),
          arguments: text(d.arguments),
          truncated: d.truncated === true,
          result: null,
        };
        step().calls.push(call);
        calls.push(call);
        break;
      }
      case "tool_result": {
        const result: ToolResult = {
          output: text(d.output),
          error: text(d.error),
          truncated: d.truncated === true,
          durationMs: typeof d.duration_ms === "number" ? d.duration_ms : null,
        };
        const id = text(d.id) || null;
        const name = text(d.name);
        const match = pairOf(calls, id, name);
        if (match) {
          match.result = result;
        } else {
          // A result with no call to answer is still something the tool did.
          const orphan: ToolCall = { id, name, arguments: null, truncated: false, result };
          step().calls.push(orphan);
          calls.push(orphan);
        }
        break;
      }
      case "delegated": {
        // Written while the call runs, so the child is reachable before its report.
        const call = calls.find((c) => c.id !== null && c.id === text(d.call_id));
        const runId = text(d.run_id);
        if (call && runId) {
          call.delegated = { agent: text(d.agent), runId };
        } else if (current) current.other.push(e);
        else trace.items.push({ type: "other", event: e });
        break;
      }
      case "plan": {
        // Each plan replaces the last; each stays where it was kept.
        const items = planItems(d.items);
        trace.plan = items;
        trace.items.push({ type: "plan", items });
        break;
      }
      case "finished":
        trace.finished = { status: text(d.status), error: text(d.error) };
        break;
      case "end":
        trace.ended = { status: text(d.status) };
        break;
      default:
        if (current) current.other.push(e);
        else trace.items.push({ type: "other", event: e });
    }
  }
  return trace;
}

const STATUSES: readonly PlanStatus[] = ["pending", "in_progress", "done"];

/** A plan's items, an item with no text dropped and an unknown status read as pending. */
function planItems(v: unknown): PlanItem[] {
  if (!Array.isArray(v)) return [];
  return v.flatMap((raw) => {
    const item = record(raw);
    const t = text(item.text);
    if (t === "") return [];
    const s = text(item.status) as PlanStatus;
    return [{ text: t, status: STATUSES.includes(s) ? s : "pending" }];
  });
}

/**
 * The call a result answers: the one with its id, or, when the pattern sends
 * no ids, the oldest call still waiting — by name if one matches, so two
 * different tools answered out of order still pair up.
 */
function pairOf(calls: ToolCall[], id: string | null, name: string): ToolCall | undefined {
  const waiting = calls.filter((c) => c.result === null);
  if (id !== null) return waiting.find((c) => c.id === id);
  const anonymous = waiting.filter((c) => c.id === null);
  return anonymous.find((c) => c.name === name) ?? anonymous[0];
}

export type DelegateReport = { agent: string; run: string; status: string; body: string };

const REPORT_HEAD = /^<report agent="([^"]*)" run="([^"]*)" status="([^"]*)" end="([^"]*)">\n/;

const ESCAPES: Record<string, string> = {
  "&#34;": '"',
  "&#39;": "'",
  "&amp;": "&",
  "&lt;": "<",
  "&gt;": ">",
};

/**
 * A delegate call's output read as the envelope the brain wraps a child's
 * answer in, or null when it is not one. The body ends only at the closing
 * tag that carries the envelope's own end, which the child never saw; a
 * closing tag the child wrote is part of what it said.
 */
export function delegateReport(output: string): DelegateReport | null {
  const head = REPORT_HEAD.exec(output);
  if (!head) return null;
  const [opening, agent = "", run = "", status = "", end = ""] = head;
  const closing = `\n</report end="${end}">`;
  if (!output.endsWith(closing) || output.length < opening.length + closing.length) return null;
  return {
    agent: agent.replace(/&#34;|&#39;|&amp;|&lt;|&gt;/g, (m) => ESCAPES[m] ?? m),
    run,
    status,
    body: output.slice(opening.length, output.length - closing.length),
  };
}

export type SearchHit = { title: string; url: string; snippet: string };

export type SearchResults = { query: string; hits: SearchHit[]; cut: boolean };

const SEARCH_HEAD = /^<search_results query="([^"]*)" end="([^"]*)">\n/;
const SEARCH_CUT = "[the results were cut at 16 KiB]";

function unescaped(s: string): string {
  return s.replace(/&#34;|&#39;|&amp;|&lt;|&gt;/g, (m) => ESCAPES[m] ?? m);
}

// A hit's field has only the < of an envelope tag escaped; the rest is the page's.
function untagged(s: string): string {
  return s.replace(/&lt;(\/?search_results)/gi, "<$1");
}

/**
 * A web_search call's output read as the envelope the brain wraps results in,
 * or null when it is not one. As with a report, the list ends only at the
 * closing tag that carries the envelope's own end; every hit's fields are the
 * page's, one line each, with the tags the brain escaped read back as text
 * and every other entity left as the page wrote it.
 */
export function searchResults(output: string): SearchResults | null {
  const head = SEARCH_HEAD.exec(output);
  if (!head) return null;
  const [opening, query = "", end = ""] = head;
  const closing = `\n</search_results end="${end}">`;
  if (!output.endsWith(closing) || output.length < opening.length + closing.length) return null;

  const body = output.slice(opening.length, output.length - closing.length);
  const results: SearchResults = { query: unescaped(query), hits: [], cut: false };
  let hit: SearchHit | null = null;
  let field = 0;
  for (const line of body.split("\n")) {
    const numbered = /^\d+\. (.*)$/.exec(line);
    if (numbered) {
      hit = { title: untagged(numbered[1] ?? ""), url: "", snippet: "" };
      results.hits.push(hit);
      field = 0;
    } else if (line === SEARCH_CUT) {
      results.cut = true;
    } else if (hit && line.startsWith("   ")) {
      const value = untagged(line.slice(3));
      if (field === 0) hit.url = value;
      else hit.snippet = value;
      field += 1;
    }
  }
  return results;
}

export type FetchedPage = { url: string; title: string; markdown: string; cut: boolean };

const PAGE_HEAD = /^<page url="([^"]*)" title="([^"]*)" end="([^"]*)">\n/;
const PAGE_CUT = "\n[the page was cut at 32 KiB]";

/**
 * A web_fetch call's output read as the envelope the brain wraps a page in,
 * or null when it is not one. The page ends only at the closing tag that
 * carries the envelope's own end. Both attributes are escaped in full by the
 * brain and read back in full; the body has only the tags' < escaped, so
 * every other entity is the page's own text.
 */
export function fetchedPage(output: string): FetchedPage | null {
  const head = PAGE_HEAD.exec(output);
  if (!head) return null;
  const [opening, url = "", title = "", end = ""] = head;
  const closing = `\n</page end="${end}">`;
  if (!output.endsWith(closing) || output.length < opening.length + closing.length) return null;

  let body = output.slice(opening.length, output.length - closing.length);
  const cut = body.endsWith(PAGE_CUT);
  if (cut) body = body.slice(0, body.length - PAGE_CUT.length);
  return {
    url: unescaped(url),
    title: unescaped(title),
    markdown: body.replace(/&lt;(\/?page)/gi, "<$1"),
    cut,
  };
}

/** The steps alone, for counting. */
export function stepsOf(trace: Trace): Step[] {
  return trace.items.flatMap((i) => (i.type === "step" ? [i.step] : []));
}

export type GraphHit = {
  kind: string;
  name: string;
  description: string;
  score: number;
  /** The walk from the match to this node, the node itself excluded. */
  via: { kind: string; name: string }[];
};

/**
 * The graph tool's output read as ranked hits, or null when it is not the
 * shape the brain writes — a cut or foreign output is shown as text instead.
 */
export function graphHits(output: string): GraphHit[] | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(output);
  } catch {
    return null;
  }
  const results = record(parsed).results;
  if (!Array.isArray(results)) return null;
  return results.map((r) => {
    const h = record(r);
    const path = Array.isArray(h.path) ? h.path.map(record) : [];
    return {
      kind: text(h.kind),
      name: text(h.name),
      description: text(h.description),
      score: count(h.score),
      via: path.slice(1).map((p) => ({ kind: text(p.kind), name: text(p.name) })),
    };
  });
}

/**
 * A tool call's arguments as key and value pairs to show at a glance, or null
 * when they are not a JSON object — then they are shown as they were written.
 */
export function argumentPairs(args: string): [string, string][] | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(args);
  } catch {
    return null;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return null;
  return Object.entries(parsed as Record<string, unknown>).map(([k, v]) => [
    k,
    typeof v === "string" ? v : JSON.stringify(v),
  ]);
}

/** Pretty JSON when it parses, the text as it was when it does not. */
export function pretty(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}
