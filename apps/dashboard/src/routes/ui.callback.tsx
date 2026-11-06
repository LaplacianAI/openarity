import { createFileRoute, useNavigate, useRouter } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";

import { completeLogin } from "@/api/oidc";
import { useAuthConfig } from "@/api/session";
import { setSession, setToken } from "@/api/token";
import { SignInFrame } from "@/components/sign-in";
import { Button } from "@/components/ui/button";

export const Route = createFileRoute("/ui/callback")({
  component: Callback,
});

/** Why a sign-in failed, in words, and what to do next. */
export type Refusal = { title: string; advice: string };

/**
 * What a failed sign-in means for the person who tried. The provider says
 * it refused in the query it sends back; everything else is this tab's own
 * check or the code's exchange, and the error's own words are kept beside
 * this for whoever has to look further.
 */
export function whatToTry(query: URLSearchParams, message: string): Refusal {
  const error = query.get("error");
  if (error === "access_denied") {
    return {
      title: "The identity provider refused the sign-in",
      advice:
        "You may have declined, or your account may not be allowed into this app. If you should have access, ask whoever runs the identity provider.",
    };
  }
  if (error) {
    return {
      title: "The identity provider could not sign you in",
      advice: "Start again. If it fails the same way, ask whoever runs this brain.",
    };
  }
  if (/not started in this tab|state this tab did not send/.test(message)) {
    return {
      title: "This sign-in was started somewhere else",
      advice:
        "It began in another tab, or this page was reloaded or reached from a bookmark. Start again from here.",
    };
  }
  return {
    title: "Sign-in did not complete",
    advice:
      "The answer from the identity provider could not be used, often because it was too old. Start again.",
  };
}

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
  const router = useRouter();
  const [error, setError] = useState<{ message: string; refusal: Refusal } | null>(null);
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

    const query = new URLSearchParams(window.location.search);
    completeLogin(oidc.issuer, oidc.client_id, query)
      .then(({ session, returnTo }) => {
        setSession(session);
        // Through the router's own history, so it moves with the address,
        // query and all: a page that is not the dashboard's is not one to
        // return to.
        router.history.replace(returnTo.startsWith("/ui") ? returnTo : "/ui");
      })
      .catch((err: unknown) => {
        const message = err instanceof Error ? err.message : String(err);
        setError({ message, refusal: whatToTry(query, message) });
      });
  }, [config.data, router]);

  return (
    <SignInFrame>
      {error ? (
        <div role="alert" className="space-y-3">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold">{error.refusal.title}</h2>
            <p className="text-sm text-muted-foreground">{error.refusal.advice}</p>
          </div>
          <Button
            className="w-full"
            onClick={() => {
              setToken(null);
              void navigate({ to: "/ui", replace: true });
            }}
          >
            Start again
          </Button>
          <p className="font-mono text-xs break-all text-muted-foreground">{error.message}</p>
        </div>
      ) : (
        <p role="status" className="text-sm text-muted-foreground">
          Completing sign-in…
        </p>
      )}
    </SignInFrame>
  );
}
