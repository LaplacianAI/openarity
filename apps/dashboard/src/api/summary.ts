import { useQuery } from "@tanstack/react-query";

import { getTeamSummary } from "@/api";

/**
 * The team at a glance, read once a minute, or as often as `every` asks. The
 * shell's waiting count and Home share it, so a page that shows both asks once.
 */
export function useTeamSummary(teamId: string | null, every = 60_000, background = false) {
  return useQuery({
    queryKey: ["summary", teamId],
    queryFn: () => getTeamSummary(teamId as string),
    enabled: Boolean(teamId),
    staleTime: Math.min(every, 60_000),
    refetchInterval: every,
    // A hidden tab is skipped unless asked, as notifications must.
    refetchIntervalInBackground: background,
  });
}
