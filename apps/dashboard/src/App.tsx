import { useEffect, useState } from "react";
import { type AuthConfig, fetchAuthConfig } from "./authConfig";

type State =
  | { status: "loading" }
  | { status: "ready"; config: AuthConfig }
  | { status: "failed"; error: string };

// This slice exists to prove the shell, not to be the dashboard: the bundle is
// built, embedded in the brain, served under /ui, and can reach the API on the
// same origin. Every screen after this one replaces what is below.
export function App() {
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    const abort = new AbortController();

    fetchAuthConfig(abort.signal)
      .then((config) => setState({ status: "ready", config }))
      .catch((err: unknown) => {
        if (abort.signal.aborted) return;
        setState({ status: "failed", error: err instanceof Error ? err.message : String(err) });
      });

    return () => abort.abort();
  }, []);

  return (
    <main>
      <h1>Openarity</h1>
      {state.status === "loading" && <p>Asking the brain how to log in…</p>}
      {state.status === "failed" && <p role="alert">Could not reach the brain: {state.error}</p>}
      {state.status === "ready" && <AuthSummary config={state.config} />}
    </main>
  );
}

function AuthSummary({ config }: { config: AuthConfig }) {
  return (
    <dl>
      <dt>Environment</dt>
      <dd>{config.environment}</dd>
      <dt>Sign in with</dt>
      <dd>
        {config.oidc
          ? `${config.oidc.issuer} (client ${config.oidc.client_id})`
          : config.dev_token_accepted
            ? "a development token"
            : "nothing — this brain accepts no logins"}
      </dd>
    </dl>
  );
}
