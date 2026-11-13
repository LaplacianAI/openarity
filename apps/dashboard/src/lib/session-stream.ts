/**
 * A session's stream folded into the conversation it shows: what people said,
 * and what each run thought, did and answered, in the brain's own order.
 *
 * Pure, and idempotent on a frame already seen, so a stream followed again
 * after a drop never shows anything twice.
 */
import type { SessionEnvelope } from "@/api/types";

export type RunState = "running" | "succeeded" | "failed" | "cancelled";

/** Something a person said. */
export type Said = {
  id: string;
  userId: string;
  text: string;
  at: string;
  /** Waiting for a run to take it. */
  pending: boolean;
  /** The run that took it. */
  readBy: string | null;
};

/** One tool call, and what it returned once it has. */
export type ToolLine = {
  id: string;
  name: string;
  args: string;
  done: boolean;
  result?: string;
  error?: string;
  ms?: number;
};

/** One model call of a run: what it thought, said and called. */
export type Step = {
  step: number;
  reasoning: string;
  text: string;
  tools: ToolLine[];
  /** Messages a person sent while the run worked, which it read before this step. */
  read: string[];
};

export type RunTurn = {
  id: string;
  agent: string;
  parent: string | null;
  status: RunState;
  startedAt: string;
  endedAt: string | null;
  steps: Step[];
  /** Runs this one handed work to. */
  children: string[];
  output: string;
  error: string;
};

export type Entry = { kind: "said"; id: string } | { kind: "run"; id: string };

export type Conversation = {
  /** The last frame folded in: where a stream followed again resumes. */
  seen: number | null;
  said: Record<string, Said>;
  runs: Record<string, RunTurn>;
  /** Oldest first. A run handed work by another is in its parent, not here. */
  order: Entry[];
};

export const empty: Conversation = { seen: null, said: {}, runs: {}, order: [] };

export function fold(c: Conversation, env: SessionEnvelope): Conversation {
  if (c.seen !== null && env.seq <= c.seen) return c;
  const next = { ...c, seen: env.seq };
  switch (env.kind) {
    case "message": {
      const d = env.data;
      const text = d.parts
        .filter((p) => p.type === "text")
        .map((p) => p.text ?? "")
        .join("");
      const known = c.said[d.id];
      return {
        ...next,
        said: {
          ...c.said,
          [d.id]: {
            id: d.id,
            userId: d.user_id,
            text,
            at: d.at,
            pending: d.state === "pending",
            readBy: known?.readBy ?? null,
          },
        },
        order: known ? c.order : [...c.order, { kind: "said", id: d.id }],
      };
    }
    case "message_consumed": {
      const d = env.data;
      const said = { ...c.said };
      for (const id of d.message_ids) {
        const m = said[id];
        if (m) said[id] = { ...m, pending: false, readBy: d.run_id };
      }
      const run = c.runs[d.run_id];
      // The message that started a run is taken before its first step, and is
      // already the turn above it; only one sent while it worked is news.
      const runs =
        run && run.steps.length > 0
          ? {
              ...c.runs,
              [run.id]: onLast(run, (s) => ({ ...s, read: [...s.read, ...d.message_ids] })),
            }
          : c.runs;
      return { ...next, said, runs };
    }
    case "run_started": {
      const d = env.data;
      if (c.runs[d.run_id]) return next;
      const parent = d.parent_run_id ? c.runs[d.parent_run_id] : undefined;
      const run: RunTurn = {
        id: d.run_id,
        agent: d.agent,
        parent: d.parent_run_id ?? null,
        status: "running",
        startedAt: d.at,
        endedAt: null,
        steps: [],
        children: [],
        output: "",
        error: "",
      };
      const runs = { ...c.runs, [run.id]: run };
      if (parent) {
        runs[parent.id] = { ...parent, children: [...parent.children, run.id] };
        return { ...next, runs };
      }
      // A parent the stream never showed: the child stands on its own.
      return { ...next, runs, order: [...c.order, { kind: "run", id: run.id }] };
    }
    case "step_started": {
      const d = env.data;
      return withRun(next, d.run_id, (r) =>
        r.steps.some((s) => s.step === d.step) ? r : { ...r, steps: [...r.steps, newStep(d.step)] },
      );
    }
    case "text_message_chunk":
    case "reasoning_message_chunk": {
      const d = env.data;
      const part = env.kind === "text_message_chunk" ? "text" : "reasoning";
      return withRun(next, d.run_id, (r) =>
        onStep(r, stepOf(d.message_id), (s) => ({ ...s, [part]: s[part] + d.delta })),
      );
    }
    case "tool_call_chunk": {
      const d = env.data;
      return withRun(next, d.run_id, (r) =>
        onLast(r, (s) => ({
          ...s,
          tools: [...s.tools, { id: d.tool_call_id, name: d.name, args: d.arguments, done: false }],
        })),
      );
    }
    case "tool_call_result": {
      const d = env.data;
      return withRun(next, d.run_id, (r) => ({
        ...r,
        steps: r.steps.map((s) => ({
          ...s,
          tools: s.tools.map((t) =>
            t.id === d.tool_call_id
              ? {
                  ...t,
                  done: true,
                  result: d.content,
                  ms: d.duration_ms,
                  ...(d.error ? { error: d.error } : {}),
                }
              : t,
          ),
        })),
      }));
    }
    case "run_finished": {
      const d = env.data;
      return withRun(next, d.run_id, (r) => ({
        ...r,
        status: d.status,
        endedAt: d.at,
        output: d.output ?? "",
        error: d.error ?? "",
      }));
    }
    case "other":
      return next;
  }
}

function newStep(step: number): Step {
  return { step, reasoning: "", text: "", tools: [], read: [] };
}

function withRun(c: Conversation, id: string, f: (r: RunTurn) => RunTurn): Conversation {
  const r = c.runs[id];
  return r ? { ...c, runs: { ...c.runs, [id]: f(r) } } : c;
}

/** The step a message id names: `<run_id>:<step>:<part>`, or null for one with none. */
export function stepOf(messageId: string | undefined): number | null {
  const n = Number(messageId?.split(":").at(-2));
  return messageId && Number.isInteger(n) && n > 0 ? n : null;
}

/** Applies f to the named step, made if it is new, or to the last one for none. */
function onStep(r: RunTurn, step: number | null, f: (s: Step) => Step): RunTurn {
  if (step === null) return onLast(r, f);
  const at = r.steps.findIndex((s) => s.step === step);
  if (at === -1) return { ...r, steps: [...r.steps, f(newStep(step))] };
  return { ...r, steps: r.steps.map((s, i) => (i === at ? f(s) : s)) };
}

/** Applies f to the run's last step, making a first one if it has none. */
function onLast(r: RunTurn, f: (s: Step) => Step): RunTurn {
  const last = r.steps.at(-1);
  if (!last) return { ...r, steps: [f(newStep(1))] };
  return { ...r, steps: [...r.steps.slice(0, -1), f(last)] };
}

/** What a run spent its working on, for the line its working folds to. */
export type Worked = {
  steps: number;
  tools: number;
  handed: string[];
  read: number;
  ms: number | null;
};

export function worked(c: Conversation, r: RunTurn, now: number): Worked {
  const end = r.endedAt ? Date.parse(r.endedAt) : now;
  const ms = end - Date.parse(r.startedAt);
  return {
    steps: r.steps.length,
    tools: r.steps.reduce((n, s) => n + s.tools.length, 0),
    handed: r.children.map((id) => c.runs[id]?.agent ?? "").filter((a) => a !== ""),
    read: r.steps.reduce((n, s) => n + s.read.length, 0),
    ms: Number.isFinite(ms) && ms >= 0 ? ms : null,
  };
}

/** What a run has said so far: the last step's text, the one being written. */
export function saying(r: RunTurn): string {
  for (let i = r.steps.length - 1; i >= 0; i--) {
    const text = r.steps[i]?.text ?? "";
    if (text !== "") return text;
  }
  return "";
}
