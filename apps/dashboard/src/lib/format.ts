export function relativeTime(iso: string): string {
  const diff = Date.now() - Date.parse(iso);
  const mins = Math.round(diff / 60_000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.round(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(iso).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

export function absoluteTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function fileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

/**
 * A session's status in one of the three tones a status dot knows. The
 * brain's vocabulary is wider; anything unrecognised is drawn as closed
 * rather than guessed at.
 */
export function sessionTone(status: string): "active" | "idle" | "closed" {
  if (status === "active" || status === "open") return "active";
  if (status === "idle" || status === "waiting") return "idle";
  return "closed";
}

/**
 * How long since `iso`, as a span a person says: "4 minutes", "3 hours",
 * "2 days". For a wait, where "3h ago" would read as when, not how long.
 */
export function waitedFor(iso: string, now: number = Date.now()): string {
  const mins = Math.floor((now - Date.parse(iso)) / 60_000);
  const span = (n: number, unit: string) => `${n} ${unit}${n === 1 ? "" : "s"}`;
  if (mins < 1) return "under a minute";
  if (mins < 60) return span(mins, "minute");
  const hours = Math.floor(mins / 60);
  if (hours < 48) return span(hours, "hour");
  return span(Math.floor(hours / 24), "day");
}
