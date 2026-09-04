/**
 * Authorization code with PKCE, run entirely in the browser.
 *
 * The dashboard is a public client: it ships to every user, so it holds no
 * secret and proves possession with a code verifier instead. Nothing here is
 * specific to dex — the endpoints all come from the provider's own discovery
 * document, which is the whole reason the brain only needs an issuer.
 */
import { ApiError } from "./client";

const VERIFIER_KEY = "openarity.pkce.verifier";
const STATE_KEY = "openarity.pkce.state";
const RETURN_KEY = "openarity.pkce.return";

/** The scopes the CLI asks for, so a token from either behaves the same. */
const SCOPE = "openid profile email offline_access";

type Discovery = {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
};

/**
 * SubtleCrypto exists only in a secure context. 127.0.0.1 counts as one;
 * http://192.168.1.6 does not, and there the whole flow is impossible rather
 * than merely degraded — S256 is the only challenge method worth having, and
 * "plain" would make the verifier pointless.
 */
export function pkceAvailable(): boolean {
  return typeof crypto !== "undefined" && typeof crypto.subtle?.digest === "function";
}

export async function fetchDiscovery(issuer: string, signal?: AbortSignal): Promise<Discovery> {
  // Exactly one slash, whether or not the configured issuer ends with one.
  const url = `${issuer.replace(/\/+$/, "")}/.well-known/openid-configuration`;
  const res = await fetch(url, signal ? { signal } : {});
  if (!res.ok) {
    throw new ApiError(res.status, `the identity provider answered ${res.status} at ${url}`);
  }

  const doc = (await res.json()) as Discovery;
  if (!doc.authorization_endpoint || !doc.token_endpoint) {
    throw new Error(`${url} describes no authorization or token endpoint`);
  }

  // The issuer a provider claims is the one that ends up in the token, and the
  // brain rejects a token whose issuer is not the value it was configured
  // with. Catching the mismatch here names it; letting it through produces a
  // 401 on every call afterwards with nothing to explain it.
  if (doc.issuer.replace(/\/+$/, "") !== issuer.replace(/\/+$/, "")) {
    throw new Error(
      `the provider calls itself ${doc.issuer}, but the brain was configured with ${issuer}`,
    );
  }
  return doc;
}

function randomText(bytes = 32): string {
  return base64url(crypto.getRandomValues(new Uint8Array(bytes)));
}

function base64url(bytes: Uint8Array | ArrayBuffer): string {
  const view = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
  let binary = "";
  for (const byte of view) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

async function challengeFor(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return base64url(digest);
}

/** Where the provider sends the browser back. Same origin as this page. */
export function redirectUri(): string {
  return `${window.location.origin}/ui/callback`;
}

/**
 * Sends the browser to the provider. The verifier and the state are kept in
 * sessionStorage because the page is about to be unloaded — they have to
 * survive a navigation this app does not control, and they must not survive
 * the tab.
 */
export async function beginLogin(issuer: string, clientId: string, returnTo: string) {
  const doc = await fetchDiscovery(issuer);

  const verifier = randomText();
  const state = randomText(16);
  sessionStorage.setItem(VERIFIER_KEY, verifier);
  sessionStorage.setItem(STATE_KEY, state);
  sessionStorage.setItem(RETURN_KEY, returnTo);

  const params = new URLSearchParams({
    response_type: "code",
    client_id: clientId,
    redirect_uri: redirectUri(),
    scope: SCOPE,
    state,
    code_challenge: await challengeFor(verifier),
    code_challenge_method: "S256",
  });

  window.location.assign(`${doc.authorization_endpoint}?${params}`);
}

export type LoginResult = { accessToken: string; returnTo: string };

/**
 * Completes the flow from the query string the provider redirected back with.
 *
 * The verifier is removed before the exchange rather than after: a code can be
 * redeemed once, so a retry with the same pair is a request that cannot
 * succeed, and leaving them behind invites one.
 */
export async function completeLogin(
  issuer: string,
  clientId: string,
  query: URLSearchParams,
): Promise<LoginResult> {
  const error = query.get("error");
  if (error) {
    throw new Error(query.get("error_description") ?? `the provider refused the login: ${error}`);
  }

  const code = query.get("code");
  const returnedState = query.get("state");
  const verifier = sessionStorage.getItem(VERIFIER_KEY);
  const expectedState = sessionStorage.getItem(STATE_KEY);
  const returnTo = sessionStorage.getItem(RETURN_KEY) ?? "/ui";

  sessionStorage.removeItem(VERIFIER_KEY);
  sessionStorage.removeItem(STATE_KEY);
  sessionStorage.removeItem(RETURN_KEY);

  if (!code) throw new Error("the provider returned no authorization code");
  if (!verifier || !expectedState) {
    throw new Error("this login was not started in this tab, so it cannot be completed here");
  }
  // The one check that makes state worth sending: a code delivered with
  // somebody else's state is a code this tab did not ask for.
  if (returnedState !== expectedState) {
    throw new Error("the provider returned a state this tab did not send");
  }

  const doc = await fetchDiscovery(issuer);
  const res = await fetch(doc.token_endpoint, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "application/json" },
    body: new URLSearchParams({
      grant_type: "authorization_code",
      code,
      client_id: clientId,
      redirect_uri: redirectUri(),
      code_verifier: verifier,
    }),
  });

  const body = (await res.json().catch(() => ({}))) as {
    access_token?: string;
    error?: string;
    error_description?: string;
  };

  if (!res.ok || body.error) {
    throw new Error(
      body.error_description ?? body.error ?? `the token endpoint answered ${res.status}`,
    );
  }
  if (!body.access_token) {
    throw new Error("the token endpoint returned no access token");
  }

  // The access token, not the id token — this is what `oa` sends, and the pair
  // has to agree or a session that works in one fails in the other.
  return { accessToken: body.access_token, returnTo };
}
