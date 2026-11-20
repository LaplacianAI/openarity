/** The longest a request may wait, in minutes: a day. */
const DAY = 1440;

/** A wait as a person says it: "45 minutes", "1 hour 30 minutes", "1 day". */
export function waitWords(minutes: number): string {
  const unit = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;
  if (minutes >= DAY && minutes % DAY === 0) return unit(minutes / DAY, "day");
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  if (h === 0) return unit(m, "minute");
  return m === 0 ? unit(h, "hour") : `${unit(h, "hour")} ${unit(m, "minute")}`;
}
