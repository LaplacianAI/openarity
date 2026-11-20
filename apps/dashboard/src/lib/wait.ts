const MINUTES_PER_DAY = 1440;

/**
 * A wait as a person says it: "45 minutes", "1 hour 30 minutes", "1 day";
 * empty for what is not a wait.
 */
export function waitWords(minutes: number): string {
  // Only a whole number of minutes is a wait; anything else says nothing.
  if (!Number.isInteger(minutes) || minutes < 1) return "";
  const unit = (n: number, one: string) => `${n} ${one}${n === 1 ? "" : "s"}`;
  if (minutes >= MINUTES_PER_DAY && minutes % MINUTES_PER_DAY === 0)
    return unit(minutes / MINUTES_PER_DAY, "day");
  const h = Math.floor(minutes / 60);
  const m = minutes % 60;
  if (h === 0) return unit(m, "minute");
  return m === 0 ? unit(h, "hour") : `${unit(h, "hour")} ${unit(m, "minute")}`;
}
