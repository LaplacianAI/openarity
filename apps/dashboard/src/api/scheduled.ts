/**
 * What the reader's schedules and watchers did that they have not seen: a
 * failed, skipped or missed fire, a page that could not be read, an answer
 * kept in a schedule's own conversation. Read by the Needs you badge and the
 * Schedules screen's News view from one query.
 */
import { useQuery } from "@tanstack/react-query";

import { getScheduledNews } from "./index";
import { usePermissions } from "./permissions";

export const READ_SCHEDULES = "schedule:read";

export function useScheduledNews(teamId: string | null, every = 60_000, background = false) {
  const perms = usePermissions(teamId);
  return useQuery({
    queryKey: ["scheduled-news", teamId],
    queryFn: ({ signal }) => getScheduledNews(teamId as string, signal),
    enabled: Boolean(teamId) && !perms.isPending && perms.can(READ_SCHEDULES),
    staleTime: Math.min(every, 30_000),
    refetchInterval: every,
    refetchIntervalInBackground: background,
  });
}

/** The badge: a count, "50+" past a page, or null for none. */
export function newsBadge(news: { count: number; more: boolean } | undefined): string | null {
  if (!news || news.count === 0) return null;
  return `${news.count}${news.more ? "+" : ""}`;
}
