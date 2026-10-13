/**
 * What the signed-in person may do in the current team.
 *
 * The brain is the authority — every route checks for itself — so this only
 * decides what a screen offers. A button the brain would refuse is worse
 * than no button: it fails after the form was filled in.
 *
 * Permission names are data in the brain, granted to roles per team; nothing
 * here knows which role holds which. A screen asks for the permission its
 * route needs, by name.
 */
import { useQuery } from "@tanstack/react-query";

import { getPermissions } from "./index";

export type Permissions = {
  /** True when the brain says this person holds the permission in the team. */
  can: (permission: string) => boolean;
  /** Not yet known: a screen shows no write action rather than one that flickers. */
  isPending: boolean;
};

export function usePermissions(teamId: string | null): Permissions {
  const query = useQuery({
    queryKey: ["permissions", teamId],
    queryFn: ({ signal }) => getPermissions(teamId as string, signal),
    enabled: Boolean(teamId),
    staleTime: 60_000,
  });

  return {
    can: (permission) => {
      // An older brain without the route, or a failed read, offers every
      // action and leaves the refusal to the brain, which names it. Hiding
      // everything would make a working install look read-only.
      if (query.isError) return true;
      const data = query.data;
      return Boolean(data && (data.super_admin || data.actions.includes(permission)));
    },
    isPending: query.isPending && Boolean(teamId),
  };
}
