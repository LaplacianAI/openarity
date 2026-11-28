/**
 * A schedule or watcher form, as typed, and the request it makes — or the
 * sentence that says why it makes none. The brain checks every field again;
 * these are so a mistake is said beside the field, before anything is sent.
 */
import type {
  CreateScheduleRequest,
  CreateWatcherRequest,
  Schedule,
  ScheduleTargetPick,
  ScheduleWhen,
  UpdateScheduleRequest,
  UpdateWatcherRequest,
  Watcher,
  WatcherSource,
} from "@/api/types";
import { wallToInstant } from "@/lib/when";

/** The brain's own caps. */
export const MAX_NAME = 80;
export const MAX_OBJECTIVE = 4000;
const MIN_EVERY = 60;
const MAX_EVERY = 31 * 86_400;
const MAX_COOLDOWN = 7 * 86_400;

export type Unit = "m" | "h" | "d" | "w";
const UNIT_SECONDS: Record<Unit, number> = { m: 60, h: 3_600, d: 86_400, w: 604_800 };

export type WhenMode = "once" | "repeat" | "cron";

/** Who runs it: the hub, or one agent. */
export type AgentPick = { id: string; name: string } | null;

export type ScheduleDraft = {
  name: string;
  objective: string;
  mode: WhenMode;
  /** Once: a wall date and time, read in `zone`. */
  date: string;
  time: string;
  /** Repeat: every `every` `unit`s. */
  every: string;
  unit: Unit;
  cron: string;
  zone: string;
  agent: AgentPick;
  /** Null is its own conversation, the brain's default. */
  target: ScheduleTargetPick | null;
  targetLabel: string;
};

export function emptyScheduleDraft(zone: string): ScheduleDraft {
  return {
    name: "",
    objective: "",
    mode: "cron",
    date: "",
    time: "09:00",
    every: "1",
    unit: "h",
    cron: "0 9 * * 1-5",
    zone,
    agent: null,
    target: null,
    targetLabel: "",
  };
}

/** Seconds for "every N unit", or why not. */
export function lengthOf(
  n: string,
  unit: Unit,
  what: string,
  min: number,
  max: number,
): number | string {
  const t = n.trim();
  if (!/^\d+$/.test(t)) return `Say ${what} as a whole number.`;
  const sec = Number(t) * UNIT_SECONDS[unit];
  if (sec < min)
    return min === 0 ? `Say ${what} as a whole number.` : `${cap(what)} is at least a minute.`;
  if (sec > max) return `${cap(what)} is at most ${max === MAX_EVERY ? "31 days" : "7 days"}.`;
  return sec;
}

function cap(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

/** The rule the draft's When says, or why it says none. */
export function whenOf(d: ScheduleDraft): ScheduleWhen | string {
  switch (d.mode) {
    case "cron": {
      const rule = d.cron.trim();
      if (!rule) return "Write a cron rule, or pick one.";
      return { kind: "cron", cron: rule };
    }
    case "repeat": {
      const sec = lengthOf(d.every, d.unit, "how often", MIN_EVERY, MAX_EVERY);
      return typeof sec === "string" ? sec : { kind: "every", every_seconds: sec };
    }
    case "once": {
      if (!d.date || !d.time) return "Say the day and the time it runs.";
      const at = wallToInstant(d.date, d.time, d.zone);
      if (!at) return `${d.date} ${d.time} does not happen in ${d.zone}: the clocks skip it.`;
      return { kind: "at", at };
    }
  }
}

function nameAndObjective(
  name: string,
  objective: string,
): { name: string; objective: string } | string {
  const n = name.trim();
  const o = objective.trim();
  if (!n) return "Name it.";
  if ([...n].length > MAX_NAME) return `A name is ${MAX_NAME} characters at most.`;
  if (!o) return "Say what each run is to do.";
  if ([...o].length > MAX_OBJECTIVE)
    return `The objective is ${[...o].length} characters; ${MAX_OBJECTIVE} at most.`;
  return { name: n, objective: o };
}

export function scheduleRequest(d: ScheduleDraft): CreateScheduleRequest | string {
  const base = nameAndObjective(d.name, d.objective);
  if (typeof base === "string") return base;
  const when = whenOf(d);
  if (typeof when === "string") return when;
  return {
    ...base,
    when,
    timezone: d.zone,
    ...(d.agent ? { agent_id: d.agent.id } : {}),
    ...(d.target ? { target: d.target } : {}),
  };
}

/** A schedule's draft, to edit it. Where it answers cannot change, so it is not drafted. */
export function scheduleDraftOf(s: Schedule, agentName?: string): ScheduleDraft {
  const d = emptyScheduleDraft(s.timezone);
  d.name = s.name;
  d.objective = s.objective;
  if (s.when.kind === "cron") {
    d.mode = "cron";
    d.cron = s.when.cron ?? "";
  } else if (s.when.kind === "every") {
    d.mode = "repeat";
    const sec = s.when.every_seconds ?? 3600;
    const unit = (["w", "d", "h", "m"] as Unit[]).find((u) => sec % UNIT_SECONDS[u] === 0) ?? "m";
    d.unit = unit;
    d.every = String(sec / UNIT_SECONDS[unit]);
  } else {
    d.mode = "once";
    d.cron = "";
  }
  d.agent = s.hub || !s.agent_id ? null : { id: s.agent_id, name: agentName ?? s.agent_id };
  return d;
}

/** Only what changed, as the brain's PATCH takes it; a string says why not. */
export function scheduleUpdate(s: Schedule, d: ScheduleDraft): UpdateScheduleRequest | string {
  const base = nameAndObjective(d.name, d.objective);
  if (typeof base === "string") return base;
  const out: UpdateScheduleRequest = {};
  if (base.name !== s.name) out.name = base.name;
  if (base.objective !== s.objective) out.objective = base.objective;
  if (d.zone !== s.timezone) out.timezone = d.zone;
  // A once is changed only when a new day or time was typed.
  if (!(d.mode === "once" && !d.date)) {
    const when = whenOf(d);
    if (typeof when === "string") return when;
    if (JSON.stringify(when) !== JSON.stringify(s.when)) out.when = when;
  }
  const wasHub = s.hub || !s.agent_id;
  if (d.agent === null && !wasHub) out.hub = true;
  if (d.agent && d.agent.id !== s.agent_id) out.agent_id = d.agent.id;
  return out;
}

export type WatcherDraft = {
  name: string;
  objective: string;
  source: WatcherSource;
  url: string;
  selector: string;
  /** Empty is the team's shortest. */
  every: string;
  everyUnit: Unit;
  /** Empty is the brain's hour; 0 holds nothing. */
  cooldown: string;
  cooldownUnit: Unit;
  agent: AgentPick;
  target: ScheduleTargetPick | null;
  targetLabel: string;
};

export function emptyWatcherDraft(): WatcherDraft {
  return {
    name: "",
    objective: "",
    source: "web_page",
    url: "",
    selector: "",
    every: "",
    everyUnit: "h",
    cooldown: "",
    cooldownUnit: "h",
    agent: null,
    target: null,
    targetLabel: "",
  };
}

function urlOf(v: string): string | null {
  const t = v.trim();
  try {
    const u = new URL(t);
    return u.protocol === "https:" || u.protocol === "http:" ? t : null;
  } catch {
    return null;
  }
}

function lengths(d: WatcherDraft): { every_seconds?: number; cooldown_seconds?: number } | string {
  const out: { every_seconds?: number; cooldown_seconds?: number } = {};
  if (d.source !== "webhook" && d.every.trim()) {
    const sec = lengthOf(d.every, d.everyUnit, "how often it looks", MIN_EVERY, MAX_EVERY);
    if (typeof sec === "string") return sec;
    out.every_seconds = sec;
  }
  if (d.cooldown.trim()) {
    const sec = lengthOf(d.cooldown, d.cooldownUnit, "the cooldown", 0, MAX_COOLDOWN);
    if (typeof sec === "string") return sec;
    out.cooldown_seconds = sec;
  }
  return out;
}

export function watcherRequest(d: WatcherDraft): CreateWatcherRequest | string {
  const base = nameAndObjective(d.name, d.objective);
  if (typeof base === "string") return base;
  const out: CreateWatcherRequest = { ...base, source: d.source };
  if (d.source !== "webhook") {
    const url = urlOf(d.url);
    if (!url)
      return `Give the address of the ${d.source === "feed" ? "feed" : "page"}, starting https://.`;
    out.url = url;
  }
  if (d.source === "web_page" && d.selector.trim()) out.selector = d.selector.trim();
  const l = lengths(d);
  if (typeof l === "string") return l;
  Object.assign(out, l);
  if (d.agent) out.agent_id = d.agent.id;
  if (d.target) out.target = d.target;
  return out;
}

export function watcherDraftOf(w: Watcher, agentName?: string): WatcherDraft {
  const d = emptyWatcherDraft();
  d.name = w.name;
  d.objective = w.objective;
  d.source = w.source;
  d.url = w.url ?? "";
  d.selector = w.selector ?? "";
  const split = (sec: number): [string, Unit] => {
    const unit = (["w", "d", "h", "m"] as Unit[]).find((u) => sec % UNIT_SECONDS[u] === 0) ?? "m";
    return [String(sec / UNIT_SECONDS[unit]), unit];
  };
  if (w.every_seconds) [d.every, d.everyUnit] = split(w.every_seconds);
  if (w.cooldown_seconds === 0) [d.cooldown, d.cooldownUnit] = ["0", "h"];
  else [d.cooldown, d.cooldownUnit] = split(w.cooldown_seconds);
  d.agent = w.hub || !w.agent_id ? null : { id: w.agent_id, name: agentName ?? w.agent_id };
  return d;
}

/** Only what changed. What it watches and where it answers cannot change. */
export function watcherUpdate(w: Watcher, d: WatcherDraft): UpdateWatcherRequest | string {
  const base = nameAndObjective(d.name, d.objective);
  if (typeof base === "string") return base;
  const l = lengths(d);
  if (typeof l === "string") return l;
  const out: UpdateWatcherRequest = {};
  if (base.name !== w.name) out.name = base.name;
  if (base.objective !== w.objective) out.objective = base.objective;
  if (w.source === "web_page" && d.selector.trim() !== (w.selector ?? ""))
    out.selector = d.selector.trim();
  if (l.every_seconds !== undefined && l.every_seconds !== w.every_seconds)
    out.every_seconds = l.every_seconds;
  if (l.cooldown_seconds !== undefined && l.cooldown_seconds !== w.cooldown_seconds)
    out.cooldown_seconds = l.cooldown_seconds;
  const wasHub = w.hub || !w.agent_id;
  if (d.agent === null && !wasHub) out.hub = true;
  if (d.agent && d.agent.id !== w.agent_id) out.agent_id = d.agent.id;
  return out;
}
