/**
 * A team's limits on schedules and watchers, as typed, and the settings they
 * save — or the sentence that says why not. The ranges are the brain's; the
 * settings are saved whole, so what was not changed is sent as it was read.
 */
import type { ScheduleSettings } from "@/api/types";
import { knownZone } from "@/lib/when";

export type ScheduleSettingsDraft = {
  agents: boolean;
  zone: string;
  /** The shortest repeat, in minutes. */
  interval: string;
  perMember: string;
  perTeam: string;
  firesPerHour: string;
};

export function settingsDraftOf(s: ScheduleSettings): ScheduleSettingsDraft {
  return {
    agents: s.agents_may_schedule,
    zone: s.timezone,
    interval: String(s.schedule_min_interval_minutes),
    perMember: String(s.schedules_per_member),
    perTeam: String(s.schedules_per_team),
    firesPerHour: String(s.fires_per_hour),
  };
}

function whole(v: string, what: string, min: number, max: number): number | string {
  const t = v.trim();
  if (!/^\d+$/.test(t)) return `${what} is a whole number.`;
  const n = Number(t);
  if (n < min || n > max)
    return `${what} is between ${min.toLocaleString("en-US")} and ${max.toLocaleString("en-US")}.`;
  return n;
}

export function scheduleSettingsRequest(d: ScheduleSettingsDraft): ScheduleSettings | string {
  const zone = d.zone.trim();
  if (!knownZone(zone)) return "Pick the team's time zone from the list, such as Europe/London.";
  const interval = whole(d.interval, "The shortest repeat", 5, 7 * 24 * 60);
  if (typeof interval === "string")
    return "The shortest repeat is a whole number of minutes, from 5 minutes to 7 days (10,080).";
  const perMember = whole(d.perMember, "One person's", 1, 100);
  if (typeof perMember === "string") return perMember;
  const perTeam = whole(d.perTeam, "The team's", 1, 1000);
  if (typeof perTeam === "string") return perTeam;
  if (perTeam < perMember)
    return `The team may have ${perTeam}, fewer than the ${perMember} one person may.`;
  const fires = whole(d.firesPerHour, "Runs an hour", 1, 1000);
  if (typeof fires === "string") return fires;
  return {
    agents_may_schedule: d.agents,
    timezone: zone,
    schedule_min_interval_minutes: interval,
    schedules_per_member: perMember,
    schedules_per_team: perTeam,
    fires_per_hour: fires,
  };
}
