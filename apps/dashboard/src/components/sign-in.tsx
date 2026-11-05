import { type ReactNode, useState } from "react";

import { beginLogin, pkceAvailable } from "@/api/oidc";
import { useAuthConfig } from "@/api/session";
import { setToken } from "@/api/token";
import { Mark } from "@/components/mark";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

/**
 * The login screen, which the brain describes rather than this app deciding.
 *
 * /auth/config is public and answers before anybody has a token, so the page
 * can ask "how do I sign in here" and get a different answer on a laptop and
 * in a cluster without a second build.
 */
export function SignIn() {
  const config = useAuthConfig();

  return (
    <SignInFrame>
      {config.isPending ? (
        <p className="text-sm text-muted-foreground">Asking the brain how to sign in…</p>
      ) : config.isError ? (
        <Unreachable message={config.error.message} />
      ) : config.data.oidc ? (
        <Oidc issuer={config.data.oidc.issuer} clientId={config.data.oidc.client_id} />
      ) : config.data.dev_token_accepted ? (
        <DevToken />
      ) : (
        <NoWayIn />
      )}
    </SignInFrame>
  );
}

/**
 * The product's name, what it is in one sentence, and room for the one thing
 * to do. Shared with the callback, so a failed sign-in looks like where it
 * started.
 */
export function SignInFrame({ children }: { children: ReactNode }) {
  return (
    <main className="mx-auto flex min-h-dvh w-full max-w-sm flex-col justify-center gap-6 px-4 py-10">
      <div className="space-y-4">
        <div className="flex items-center gap-2.5">
          <Mark className="size-8" />
          <span className="text-lg font-semibold tracking-tight">openarity</span>
        </div>
        <div className="space-y-1">
          <h1 className="text-2xl font-semibold tracking-tight">Sign in</h1>
          <p className="text-sm text-muted-foreground">
            Your team's agents, what they are doing, and how they are set up.
          </p>
        </div>
      </div>
      {children}
    </main>
  );
}

function Unreachable({ message }: { message: string }) {
  return (
    <div role="alert" className="rounded-md border border-destructive/40 bg-surface px-4 py-3">
      <h2 className="text-sm font-semibold">The brain did not answer</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        This page is served by the brain, so if it loaded but the API did not respond, the server is
        starting or something in front of it is not passing requests through.
      </p>
      <p className="mt-2 font-mono text-xs text-muted-foreground">{message}</p>
    </div>
  );
}

function DevToken() {
  const [value, setValue] = useState("");

  return (
    <form
      className="space-y-3"
      onSubmit={(e) => {
        e.preventDefault();
        const trimmed = value.trim();
        if (trimmed) setToken(trimmed);
      }}
    >
      <div className="space-y-1.5">
        <Label htmlFor="dev-token">Development token</Label>
        <Input
          id="dev-token"
          type="password"
          autoComplete="off"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          placeholder="Paste the development token"
        />
      </div>
      <Button type="submit" className="w-full" disabled={!value.trim()}>
        Sign in
      </Button>
      <p className="text-xs text-muted-foreground">
        This brain is running in development and accepts a shared token. It is refused outside
        development, where an identity provider takes its place.
      </p>
    </form>
  );
}

function Oidc({ issuer, clientId }: { issuer: string; clientId: string }) {
  const [error, setError] = useState<string | null>(null);
  const [going, setGoing] = useState(false);

  // Not a warning: without SubtleCrypto there is no S256 challenge, and a
  // "plain" one would make the verifier decorative. Said before the button is
  // pressed rather than after the redirect fails.
  if (!pkceAvailable()) {
    return (
      <div role="alert" className="rounded-md border border-border-strong bg-surface px-4 py-4">
        <h2 className="text-sm font-semibold">This page cannot sign you in</h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Signing in needs Web Crypto, which browsers only provide in a secure context. This page
          was loaded over plain HTTP from <span className="font-mono">{window.location.host}</span>,
          which is not one.
        </p>
        <p className="mt-2 text-xs text-muted-foreground">
          Reach it at <span className="font-mono">127.0.0.1</span>, which counts as secure, or serve
          it over HTTPS. <code className="font-mono">oa login</code> works either way.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-3">
      <Button
        className="w-full"
        disabled={going}
        onClick={() => {
          setGoing(true);
          setError(null);
          // The path being left, so the provider returns to the screen that
          // was wanted rather than always to the overview.
          beginLogin(issuer, clientId, window.location.pathname).catch((err: unknown) => {
            setGoing(false);
            setError(err instanceof Error ? err.message : String(err));
          });
        }}
      >
        {going ? "Redirecting…" : "Sign in"}
      </Button>
      <p className="text-xs text-muted-foreground">
        Through this brain's identity provider,{" "}
        <span className="font-mono break-all">{issuer}</span>
      </p>
      {error ? (
        <p role="alert" className="font-mono text-xs break-all text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function NoWayIn() {
  return (
    <div role="alert" className="rounded-md border border-border-strong bg-surface px-4 py-4">
      <h2 className="text-sm font-semibold">This brain accepts no logins</h2>
      <p className="mt-1 text-sm text-muted-foreground">
        It has no identity provider connected and no development sign-in, so there is no way to sign
        in. Ask whoever runs it to connect an identity provider.
      </p>
    </div>
  );
}
