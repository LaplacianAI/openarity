import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { completeLogin } from "@/api/oidc";
import { useAuthConfig } from "@/api/session";
import { setSession, setToken } from "@/api/token";

export const Route = createFileRoute("/ui/callback")({
  component: Callback,
});

/**
 * Where the identity provider sends the browser back.
 *
 * This route holds a one-time authorization code in its query string, so it
 * replaces itself in the history rather than pushing: pressing Back should
 * return to wherever the login started, not re-run an exchange that can only
 * succeed once.
 */
function Callback() {
  const config = useAuthConfig();
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const ran = useRef(false);

  useEffect(() => {
    // StrictMode runs effects twice in development. The second run would
    // redeem a code the first already spent, and the provider would refuse it
    // — so the failure would only appear in development, which is the worst
    // place for a bug to live.
    if (ran.current) return;
    const oidc = config.data?.oidc;
    if (!oidc) return;
    ran.current = true;

    completeLogin(oidc.issuer, oidc.client_id, new URLSearchParams(window.location.search))
      .then(({ session, returnTo }) => {
        setSession(session);
        window.history.replaceState(null, "", returnTo);
        void navigate({ to: "/ui", replace: true });
      })
      .catch((err: unknown) => {
        setError(err instanceof Error ? err.message : String(err));
      });
  }, [config.data, navigate]);

  return (
    <div className="mx-auto flex min-h-screen w-full max-w-md flex-col justify-center px-6">
      {error ? (
        <div role="alert" className="rounded-md border border-destructive/40 bg-surface px-4 py-3">
          <h1 className="text-sm font-semibold">Sign-in did not complete</h1>
          <p className="mt-2 font-mono text-xs break-all text-muted-foreground">{error}</p>
          <button
            type="button"
            className="mt-3 text-xs underline underline-offset-4"
            onClick={() => {
              setToken(null);
              void navigate({ to: "/ui", replace: true });
            }}
          >
            Start again
          </button>
        </div>
      ) : (
        <p className="text-sm text-muted-foreground">Completing sign-in…</p>
      )}
    </div>
  );
}
