import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchAuthConfig } from "./authConfig";

function respondWith(body: unknown, init: ResponseInit = { status: 200 }) {
  const fetchMock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify(body), init));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("fetchAuthConfig", () => {
  // Relative, so the page works behind a port forward, at a LAN address, or
  // through a reverse proxy. An absolute host here would be a bug nobody sees
  // until the app is reached by a name other than the one it was written for.
  it("asks the origin it was served from", async () => {
    const fetchMock = respondWith({ environment: "development", dev_token_accepted: true });

    await fetchAuthConfig();

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/auth/config");
  });

  it("returns what an OIDC brain reports", async () => {
    respondWith({
      environment: "staging",
      dev_token_accepted: false,
      oidc: { issuer: "http://127.0.0.1:5556", client_id: "openarity" },
    });

    const config = await fetchAuthConfig();

    expect(config.environment).toBe("staging");
    expect(config.dev_token_accepted).toBe(false);
    expect(config.oidc?.issuer).toBe("http://127.0.0.1:5556");
  });

  // A brain with no identity provider omits the key entirely rather than
  // sending null, so the optional field has to survive being absent.
  it("accepts a brain with no identity provider", async () => {
    respondWith({ environment: "development", dev_token_accepted: true });

    const config = await fetchAuthConfig();

    expect(config.oidc).toBeUndefined();
    expect(config.dev_token_accepted).toBe(true);
  });

  // The failure that matters: a brain that is up but answering an error must
  // not be reported as "no OIDC configured", which is what silently returning
  // an empty object would do.
  it("refuses a non-2xx answer rather than inventing one", async () => {
    respondWith({ error: "nope" }, { status: 503 });

    await expect(fetchAuthConfig()).rejects.toThrow("503");
  });

  it("passes the abort signal through", async () => {
    const fetchMock = respondWith({ environment: "development", dev_token_accepted: true });
    const abort = new AbortController();

    await fetchAuthConfig(abort.signal);

    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ signal: abort.signal });
  });
});
