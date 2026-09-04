// The brain answers this before anybody has logged in, which is what lets one
// build serve a personal install and an enterprise deployment: the page asks
// how to log in here rather than being compiled for one of them.
export type AuthConfig = {
  environment: string;
  dev_token_accepted: boolean;
  oidc?: { issuer: string; client_id: string };
};

// Relative, not absolute. The SPA is served from the same origin as the API it
// calls, so this works behind a port forward, a LAN address or a reverse proxy
// without anything knowing what address the browser used.
export async function fetchAuthConfig(signal?: AbortSignal): Promise<AuthConfig> {
  const res = await fetch("/auth/config", { signal });
  if (!res.ok) {
    throw new Error(`the brain answered ${res.status} at /auth/config`);
  }
  return (await res.json()) as AuthConfig;
}
