import { useState } from "react";

import { beginLogin, pkceAvailable } from "@/api/oidc";
import { useAuthConfig } from "@/api/session";
import { setToken } from "@/api/token";
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
    <div className="mx-auto flex min-h-screen w-full max-w-md flex-col justify-center px-6">
      <div className="flex items-center gap-2 pb-6">
        <span className="size-2 rounded-[2px] bg-primary" aria-hidden />
        <span className="text-sm font-semibold tracking-tight">Openarity</span>
      </div>

      {config.isPending ? (
        <p className="text-sm text-muted-foreground">Asking the brain how to sign in…</p>
      ) : config.isError ? (
        <Unreachable message={config.error.message} />
      ) : config.data.oidc ? (
        <Oidc issuer={config.data.oidc.issuer} clientId={config.data.oidc.client_id} />
      ) : config.data.dev_token_accepted ? (
        <DevToken />
      ) : (
        <NoWayIn environment={config.data.environment} />
      )}
    </div>
  );
}

function Unreachable({ message }: { message: string }) {
  return (
    <div role="alert" className="rounded-md border border-destructive/40 bg-surface px-4 py-3">
      <h1 className="text-sm font-semibold">The brain did not answer</h1>
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
          placeholder="OPENARITY_DEV_TOKEN"
        />
      </div>
      <Button type="submit" size="sm" disabled={!value.trim()}>
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
        <h1 className="text-sm font-semibold">This page cannot sign you in</h1>
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
      <p className="text-sm text-muted-foreground">
        This brain authenticates against an identity provider.
      </p>
      <p className="font-mono text-xs break-all text-muted-foreground">{issuer}</p>
      <Button
        size="sm"
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
      {error ? (
        <p role="alert" className="font-mono text-xs break-all text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function NoWayIn({ environment }: { environment: string }) {
  return (
    <div role="alert" className="rounded-md border border-border-strong bg-surface px-4 py-4">
      <h1 className="text-sm font-semibold">This brain accepts no logins</h1>
      <p className="mt-1 text-sm text-muted-foreground">
        It is running in <span className="font-mono">{environment}</span> with no identity provider
        configured and no development token, so there is no way to authenticate. Set{" "}
        <span className="font-mono">OPENARITY_OIDC_ENABLED</span> and an issuer, or run it in
        development.
      </p>
    </div>
  );
}
