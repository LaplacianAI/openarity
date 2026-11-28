/**
 * Time, as schedules and watchers need it said: a rule in words, an instant
 * in a zone, and a wall time in a zone turned into an instant.
 *
 * A schedule keeps its own zone (Asia/Kolkata), and the reader has theirs.
 * Every time is shown in the reader's, with the schedule's beside it when the
 * two differ, so "09:00" is never read in the wrong place.
 */
import type { ScheduleWhen } from "@/api/types";

/** The zone this browser reads time in. */
export function readerZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

/** Every IANA zone this browser knows, with `keep` in it even if it does not. */
export function zones(keep?: string): string[] {
  let all: string[] = [];
  try {
    all =
      (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.(
        "timeZone",
      ) ?? [];
  } catch {
    all = [];
  }
  if (!all.includes("UTC")) all = ["UTC", ...all];
  if (keep && !all.includes(keep)) all = [keep, ...all];
  return all;
}

/** Whether this browser can read time in zone. */
export function knownZone(zone: string): boolean {
  try {
    new Intl.DateTimeFormat("en-GB", { timeZone: zone });
    return true;
  } catch {
    return false;
  }
}

const SHOWN: Intl.DateTimeFormatOptions = {
  weekday: "short",
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  hourCycle: "h23",
};

/** An instant as "Mon 1 Dec 09:00" in zone; in UTC when the zone is unknown. */
export function timeIn(iso: string, zone: string): string {
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  const timeZone = knownZone(zone) ? zone : "UTC";
  const parts = new Intl.DateTimeFormat("en-GB", { ...SHOWN, timeZone }).formatToParts(at);
  const part = (t: string) => parts.find((p) => p.type === t)?.value ?? "";
  return `${part("weekday")} ${part("day")} ${part("month")} ${part("hour")}:${part("minute")}`;
}

/**
 * An instant for the reader: in their zone, and, when the schedule's own
 * zone reads it differently, as it reads there too.
 */
export function timeBoth(
  iso: string,
  zone: string,
  reader: string = readerZone(),
): { here: string; there: string | null } {
  const here = timeIn(iso, reader);
  const there = timeIn(iso, zone);
  return { here, there: there === here || zone === reader ? null : `${there} in ${zone}` };
}

/** A length in seconds as a person says it: 30 min, 2 h, 1 day, 2 weeks. */
export function lengthSays(seconds: number): string {
  const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
  if (seconds === 0) return "none";
  if (seconds % 604_800 === 0) return plural(seconds / 604_800, "week", "weeks");
  if (seconds % 86_400 === 0) return plural(seconds / 86_400, "day", "days");
  if (seconds % 3_600 === 0) return `${seconds / 3_600} h`;
  if (seconds % 60 === 0) return `${seconds / 60} min`;
  return `${seconds} s`;
}

/** One of a unit is said as the unit: every hour, not every 1 h. */
const EVERY_ONE: Record<number, string> = {
  3600: "every hour",
  86400: "every day",
  604800: "every week",
};

const DAYS = ["Sundays", "Mondays", "Tuesdays", "Wednesdays", "Thursdays", "Fridays", "Saturdays"];

function hhmm(hour: string, minute: string): string | null {
  const h = Number(hour);
  const m = Number(minute);
  if (!/^\d{1,2}$/.test(hour) || !/^\d{1,2}$/.test(minute) || h > 23 || m > 59) return null;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
}

/** A cron rule in words, for the rules people write most; null for any other. */
export function cronSays(rule: string): string | null {
  const r = rule.trim().replace(/\s+/g, " ");
  const named: Record<string, string> = {
    "@hourly": "every hour",
    "@daily": "every day at 00:00",
    "@midnight": "every day at 00:00",
    "@weekly": "Sundays at 00:00",
    "@monthly": "on the 1st of each month at 00:00",
  };
  if (named[r]) return named[r];
  const f = r.split(" ");
  if (f.length !== 5) return null;
  const [minute, hour, dom, month, dow] = f as [string, string, string, string, string];
  if (dom !== "*" || month !== "*") return null;
  if (hour === "*" && dow === "*") {
    if (minute === "0") return "every hour";
    if (/^\d{1,2}$/.test(minute) && Number(minute) < 60)
      return `every hour at :${minute.padStart(2, "0")}`;
    return null;
  }
  const at = hhmm(hour, minute);
  if (!at) return null;
  if (dow === "*") return `every day at ${at}`;
  if (dow === "1-5") return `weekdays at ${at}`;
  if (dow === "0,6" || dow === "6,0") return `weekends at ${at}`;
  if (/^[0-7]$/.test(dow)) return `${DAYS[Number(dow) % 7]} at ${at}`;
  return null;
}

/** When a schedule fires, in words: "weekdays at 09:00", "every 2 h", "once, Mon 1 Dec 09:00". */
export function whenSays(w: ScheduleWhen, zone: string, reader: string = readerZone()): string {
  switch (w.kind) {
    case "every":
      if (!w.every_seconds) return "repeatedly";
      return EVERY_ONE[w.every_seconds] ?? `every ${lengthSays(w.every_seconds)}`;
    case "cron":
      return w.cron ? (cronSays(w.cron) ?? w.cron) : "on a rule";
    case "at":
      return w.at ? `once, ${timeBoth(w.at, zone, reader).here}` : "once";
  }
}

/** How far zone's clock is from UTC at instant t, in milliseconds. */
function offsetAt(t: number, zone: string): number {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: zone,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).formatToParts(new Date(t));
  const n = (type: string) => Number(parts.find((p) => p.type === type)?.value);
  const asUTC = Date.UTC(n("year"), n("month") - 1, n("day"), n("hour"), n("minute"), n("second"));
  return asUTC - t;
}

/**
 * A wall time ("2026-12-01", "09:00") read in zone, as an instant. Null when
 * it is not a time, or the zone's clocks skip it, so it never happens there.
 */
export function wallToInstant(date: string, time: string, zone: string): string | null {
  const d = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  const t = /^(\d{1,2}):(\d{2})$/.exec(time);
  if (!d || !t || !knownZone(zone)) return null;
  const [y, mo, da, h, mi] = [Number(d[1]), Number(d[2]), Number(d[3]), Number(t[1]), Number(t[2])];
  if (h > 23 || mi > 59) return null;
  const wall = Date.UTC(y, mo - 1, da, h, mi);
  const check = new Date(wall);
  if (check.getUTCMonth() !== mo - 1 || check.getUTCDate() !== da) return null;
  // Two passes settle the offset either side of a change of clocks.
  let at = wall - offsetAt(wall, zone);
  at = wall - offsetAt(at, zone);
  if (at + offsetAt(at, zone) !== wall) return null;
  return new Date(at).toISOString();
}
