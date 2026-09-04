/**
 * Who is signed in, and which team is being looked at.
 *
 * Neither is something the brain stores for us. There is no "current team"
 * endpoint — a person can belong to several — so the choice lives here and is
 * remembered per tab. The signed-in principal comes from /whoami, which is
 * also the cheapest way to find out whether the token still works.
 */
import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useState } from "react";

import { onSessionExpired } from "./client";
import { fetchAuthConfig, listTeams } from "./index";
import { refreshSession } from "./oidc";
import { getSession, getToken, setSession, subscribeToken } from "./token";

const TEAM_KEY = "openarity.team";

/**
 * Teaches the client how to renew a session, once the brain has said which
 * provider to renew it against.
 *
 * Registered from a hook rather than at module load because the issuer is not
 * known until /auth/config has answered — and a development token has no
 * provider at all, in which case a 401 is final and the renewer says so by
 * returning false.
 */
export function useSessionRenewal() {
  const config = useAuthConfig();
  const oidc = config.data?.oidc;

  useEffect(() => {
    if (!oidc) {
      onSessionExpired(null);
      return;
    }

    onSessionExpired(async () => {
      const refresh = getSession()?.refresh;
      if (!refresh) return false;
      try {
        setSession(await refreshSession(oidc.issuer, oidc.client_id, refresh));
        return true;
      } catch {
        // A refresh token the provider will not honour is a session that is
        // over. Clearing it here puts the sign-in screen up once, rather than
        // leaving every screen to fail separately.
        setSession(null);
        return false;
      }
    });

    return () => onSessionExpired(null);
  }, [oidc]);
}

export function useAuthConfig() {
  return useQuery({
    queryKey: ["auth-config"],
    queryFn: ({ signal }) => fetchAuthConfig(signal),
    staleTime: Number.POSITIVE_INFINITY,
  });
}

/** Re-renders whichever screens care when a token arrives or is refused. */
export function useToken(): string | null {
  const [token, setLocal] = useState<string | null>(() => getToken());
  useEffect(() => subscribeToken(() => setLocal(getToken())), []);
  return token;
}

/**
 * The team every other call is scoped to.
 *
 * Defaults to the first the brain returns rather than making the operator
 * choose before anything can be shown — most installs have exactly one, and
 * being asked to pick from a list of one is a worse first screen than a list
 * of senders.
 */
export function useCurrentTeam() {
  const token = useToken();
  const teams = useQuery({
    queryKey: ["teams"],
    queryFn: () => listTeams(),
    enabled: Boolean(token),
  });

  const [chosen, setChosen] = useState<string | null>(() => {
    try {
      return window.sessionStorage.getItem(TEAM_KEY);
    } catch {
      return null;
    }
  });

  const items = teams.data?.items ?? [];
  const known = chosen && items.some((t) => t.id === chosen) ? chosen : (items[0]?.id ?? null);

  const choose = useCallback((teamId: string) => {
    setChosen(teamId);
    try {
      window.sessionStorage.setItem(TEAM_KEY, teamId);
    } catch {
      // Remembered for this page only; the default still applies on reload.
    }
  }, []);

  return { teamId: known, teams: items, isPending: teams.isPending, choose };
}
