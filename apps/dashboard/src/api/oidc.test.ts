import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  beginLogin,
  completeLogin,
  fetchDiscovery,
  pkceAvailable,
  redirectUri,
  refreshSession,
} from "./oidc";

const ISSUER = "http://127.0.0.1:5556";

function discovery(overrides: Record<string, string> = {}) {
  return {
    issuer: ISSUER,
    authorization_endpoint: `${ISSUER}/auth`,
    token_endpoint: `${ISSUER}/token`,
    ...overrides,
  };
}

function respondJson(...bodies: Array<{ body: unknown; status?: number }>) {
  const mock = vi.fn<typeof fetch>();
  for (const { body, status } of bodies) {
    mock.mockResolvedValueOnce(new Response(JSON.stringify(body), { status: status ?? 200 }));
  }
  vi.stubGlobal("fetch", mock);
  return mock;
}

beforeEach(() => {
  sessionStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

it("knows whether the browser can do PKCE at all", () => {
  expect(pkceAvailable()).toBe(true);
});

it("sends the provider back to this origin", () => {
  expect(redirectUri()).toBe(`${window.location.origin}/ui/callback`);
});

describe("discovery", () => {
  it("asks the issuer for its own document, with one slash", async () => {
    const mock = respondJson({ body: discovery() });

    await fetchDiscovery(`${ISSUER}/`);

    expect(mock.mock.calls[0]?.[0]).toBe(`${ISSUER}/.well-known/openid-configuration`);
  });

  // The issuer inside a token is what the brain compares against its own
  // configuration. A mismatch caught here is one sentence; uncaught it is a
  // 401 on every call with nothing to explain it.
  it("refuses a provider that calls itself something else", async () => {
    respondJson({ body: discovery({ issuer: "http://elsewhere.example" }) });

    await expect(fetchDiscovery(ISSUER)).rejects.toThrow(/calls itself/);
  });

  it("refuses a document with no endpoints", async () => {
    respondJson({ body: { issuer: ISSUER } });

    await expect(fetchDiscovery(ISSUER)).rejects.toThrow(/no authorization or token endpoint/);
  });
});

describe("beginLogin", () => {
  it("redirects with S256 and keeps the verifier for the way back", async () => {
    respondJson({ body: discovery() });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, origin: "http://127.0.0.1:21120", assign });

    await beginLogin(ISSUER, "openarity", "/ui/teams");

    const url = new URL(assign.mock.calls[0]?.[0] as string);
    expect(url.origin + url.pathname).toBe(`${ISSUER}/auth`);
    expect(url.searchParams.get("response_type")).toBe("code");
    expect(url.searchParams.get("client_id")).toBe("openarity");
    expect(url.searchParams.get("code_challenge_method")).toBe("S256");
    expect(url.searchParams.get("code_challenge")).toMatch(/^[\w-]{43}$/);
    expect(url.searchParams.get("scope")).toContain("offline_access");

    // The challenge is a hash of the verifier, so the verifier itself must
    // never appear in the URL the browser is about to leave in a history.
    const verifier = sessionStorage.getItem("openarity.pkce.verifier");
    expect(verifier).toBeTruthy();
    expect(assign.mock.calls[0]?.[0]).not.toContain(verifier as string);

    expect(sessionStorage.getItem("openarity.pkce.return")).toBe("/ui/teams");
  });
});

describe("completeLogin", () => {
  async function start() {
    respondJson({ body: discovery() });
    vi.stubGlobal("location", {
      ...window.location,
      origin: "http://127.0.0.1:21120",
      assign: vi.fn(),
    });
    await beginLogin(ISSUER, "openarity", "/ui");
    return sessionStorage.getItem("openarity.pkce.state") as string;
  }

  it("exchanges the code and returns the access token", async () => {
    const state = await start();
    const mock = respondJson(
      { body: discovery() },
      { body: { access_token: "at_1", refresh_token: "rt_1", expires_in: 3600 } },
    );

    const result = await completeLogin(
      ISSUER,
      "openarity",
      new URLSearchParams({ code: "c0de", state }),
    );

    expect(result.session.access).toBe("at_1");
    expect(result.session.refresh).toBe("rt_1");
    expect(result.session.expiresAt).toBeGreaterThan(Date.now());
    expect(result.returnTo).toBe("/ui");

    const body = new URLSearchParams(mock.mock.calls[1]?.[1]?.body as string);
    expect(body.get("grant_type")).toBe("authorization_code");
    expect(body.get("code")).toBe("c0de");
    expect(body.get("code_verifier")).toBeTruthy();
  });

  // A code arriving with a state this tab never sent is a code this tab never
  // asked for. Without the check, a link could hand the app somebody else's.
  it("refuses a state it did not send", async () => {
    await start();
    respondJson({ body: discovery() });

    await expect(
      completeLogin(ISSUER, "openarity", new URLSearchParams({ code: "c0de", state: "forged" })),
    ).rejects.toThrow(/state this tab did not send/);
  });

  it("refuses to complete a login another tab started", async () => {
    await expect(
      completeLogin(ISSUER, "openarity", new URLSearchParams({ code: "c0de", state: "x" })),
    ).rejects.toThrow(/not started in this tab/);
  });

  it("reports the provider's own refusal", async () => {
    await start();

    await expect(
      completeLogin(
        ISSUER,
        "openarity",
        new URLSearchParams({ error: "access_denied", error_description: "user cancelled" }),
      ),
    ).rejects.toThrow("user cancelled");
  });

  it("reports a token endpoint that answers with an error", async () => {
    const state = await start();
    respondJson(
      { body: discovery() },
      { body: { error: "invalid_grant", error_description: "code already used" }, status: 400 },
    );

    await expect(
      completeLogin(ISSUER, "openarity", new URLSearchParams({ code: "c0de", state })),
    ).rejects.toThrow("code already used");
  });

  // A code can be redeemed once. Leaving the pair behind invites a retry that
  // cannot succeed and looks like a different bug.
  it("clears the verifier even when the exchange fails", async () => {
    const state = await start();
    respondJson({ body: discovery() }, { body: { error: "invalid_grant" }, status: 400 });

    await expect(
      completeLogin(ISSUER, "openarity", new URLSearchParams({ code: "c0de", state })),
    ).rejects.toThrow();

    expect(sessionStorage.getItem("openarity.pkce.verifier")).toBeNull();
    expect(sessionStorage.getItem("openarity.pkce.state")).toBeNull();
  });
});

describe("refreshSession", () => {
  it("exchanges the refresh token and keeps the new one", async () => {
    const mock = respondJson(
      { body: discovery() },
      { body: { access_token: "at_2", refresh_token: "rt_2", expires_in: 3600 } },
    );

    const session = await refreshSession(ISSUER, "openarity", "rt_1");

    expect(session.access).toBe("at_2");
    expect(session.refresh).toBe("rt_2");

    const body = new URLSearchParams(mock.mock.calls[1]?.[1]?.body as string);
    expect(body.get("grant_type")).toBe("refresh_token");
    expect(body.get("refresh_token")).toBe("rt_1");
  });

  // Providers are not required to rotate. Dropping the old token when none
  // comes back would make the first refresh the last one.
  it("carries the old refresh token forward when none is returned", async () => {
    respondJson({ body: discovery() }, { body: { access_token: "at_2", expires_in: 60 } });

    const session = await refreshSession(ISSUER, "openarity", "rt_1");

    expect(session.refresh).toBe("rt_1");
  });

  it("refuses a refresh the provider rejected", async () => {
    respondJson(
      { body: discovery() },
      { body: { error: "invalid_grant", error_description: "token expired" }, status: 400 },
    );

    await expect(refreshSession(ISSUER, "openarity", "rt_1")).rejects.toThrow("token expired");
  });
});
