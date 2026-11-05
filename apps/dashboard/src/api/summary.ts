import { useQuery } from "@tanstack/react-query";

import { getTeamSummary } from "@/api";

/**
 * The team at a glance, read once a minute. The shell's waiting count and
 * Home share it, so a page that shows both asks once.
 */
export function useTeamSummary(teamId: string | null) {
  return useQuery({
    queryKey: ["summary", teamId],
    queryFn: () => getTeamSummary(teamId as string),
    enabled: Boolean(teamId),
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
}
