import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError, nextCursorOf, onSessionExpired, pageQuery, request } from "./client";
import { getToken, setSession, setToken } from "./token";

function respond(body: unknown, init: ResponseInit = { status: 200 }) {
  const mock = vi.fn<typeof fetch>(async () => new Response(JSON.stringify(body), init));
  vi.stubGlobal("fetch", mock);
  return mock;
}

function respondText(text: string, status: number) {
  const mock = vi.fn<typeof fetch>(async () => new Response(text, { status }));
  vi.stubGlobal("fetch", mock);
  return mock;
}

beforeEach(() => {
  window.sessionStorage.clear();
  setToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("request", () => {
  // The bundle is served by the brain, so the page and its API share an origin
  // whatever address the browser used. An absolute host would work only on the
  // machine it was written on.
  it("calls a relative path", async () => {
    const mock = respond({ items: [] });
    await request("/teams");
    expect(mock.mock.calls[0]?.[0]).toBe("/teams");
  });

  it("sends the token when there is one", async () => {
    setToken("t0ken");
    const mock = respond({ items: [] });

    await request("/teams");

    const headers = new Headers(mock.mock.calls[0]?.[1]?.headers);
    expect(headers.get("Authorization")).toBe("Bearer t0ken");
  });

  it("sends no Authorization header when there is no token", async () => {
    const mock = respond({ environment: "development", dev_token_accepted: true });

    await request("/auth/config", { anonymous: true });

    const headers = new Headers(mock.mock.calls[0]?.[1]?.headers);
    expect(headers.has("Authorization")).toBe(false);
  });

  // /auth/config is how the app learns to sign in, so it must never carry a
  // credential — including a stale one that would make it 401.
  it("omits the token on anonymous calls even when one is held", async () => {
    setToken("t0ken");
    const mock = respond({ environment: "development", dev_token_accepted: true });

    await request("/auth/config", { anonymous: true });

    const headers = new Headers(mock.mock.calls[0]?.[1]?.headers);
    expect(headers.has("Authorization")).toBe(false);
  });

  // Otherwise every later screen fails identically and the reason scrolls off.
  it("drops a token the brain refused", async () => {
    setToken("stale");
    respond({}, { status: 401 });

    await expect(request("/teams")).rejects.toBeInstanceOf(ApiError);
    expect(getToken()).toBeNull();
  });

  // The brain answers errors with http.Error, which writes a plain sentence.
  // Parsing it as JSON would discard the only useful part.
  it("carries the brain's own words on a failure", async () => {
    respondText("the spec names no pattern", 400);

    await expect(request("/teams")).rejects.toThrow("the spec names no pattern");
  });

  it("reports the status when the body is empty", async () => {
    respondText("", 500);

    await expect(request("/teams")).rejects.toThrow("500");
  });

  it("does not try to parse a 204 as JSON", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () => new Response(null, { status: 204 })),
    );

    await expect(request("/teams/x/members/y", { method: "DELETE" })).resolves.toBeUndefined();
  });

  it("sends a JSON body with the matching content type", async () => {
    const mock = respond({ id: "1", name: "Platform" });

    await request("/teams", { method: "POST", body: { name: "Platform" } });

    const init = mock.mock.calls[0]?.[1];
    const headers = new Headers(init?.headers);
    expect(init?.method).toBe("POST");
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(init?.body).toBe(JSON.stringify({ name: "Platform" }));
  });
});

describe("cursor pages", () => {
  it("sends nothing on the first page", () => {
    expect(pageQuery(null)).toBe("");
  });

  it("sends the cursor it was given back", () => {
    expect(pageQuery("eyJjIjoi")).toBe("?cursor=eyJjIjoi");
  });

  // next_cursor is omitempty in Go, so the last page has no key at all —
  // undefined, not null. Callers should see one shape.
  it("reads an absent next_cursor as the end of the list", () => {
    expect(nextCursorOf({ items: [] })).toBeNull();
    expect(nextCursorOf({ items: [], next_cursor: "more" })).toBe("more");
  });
});

describe("renewal on 401", () => {
  afterEach(() => {
    onSessionExpired(null);
  });

  it("retries once with the token the renewal produced", async () => {
    setToken("expired");
    const mock = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(new Response("", { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), { status: 200 }));
    vi.stubGlobal("fetch", mock);

    onSessionExpired(async () => {
      setSession({ access: "fresh", refresh: "rt" });
      return true;
    });

    await expect(request("/teams")).resolves.toEqual({ items: [] });

    expect(mock).toHaveBeenCalledTimes(2);
    expect(new Headers(mock.mock.calls[0]?.[1]?.headers).get("Authorization")).toBe(
      "Bearer expired",
    );
    expect(new Headers(mock.mock.calls[1]?.[1]?.headers).get("Authorization")).toBe("Bearer fresh");
  });

  // A development token has nothing behind it, so a 401 is the end of the
  // session rather than the start of a renewal.
  it("gives up when there is nothing to renew with", async () => {
    setToken("letmein");
    const mock = vi.fn<typeof fetch>(async () => new Response("", { status: 401 }));
    vi.stubGlobal("fetch", mock);

    await expect(request("/teams")).rejects.toBeInstanceOf(ApiError);
    expect(mock).toHaveBeenCalledTimes(1);
    expect(getToken()).toBeNull();
  });

  // A screen makes several calls at once, so an expired token fails all of
  // them together. Without single-flight each would spend the refresh token,
  // and only the first would succeed.
  it("renews once for several requests that fail together", async () => {
    setToken("expired");
    let renewals = 0;
    let renewed = false;

    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>(async () =>
        renewed
          ? new Response(JSON.stringify({ items: [] }), { status: 200 })
          : new Response("", { status: 401 }),
      ),
    );

    onSessionExpired(async () => {
      renewals += 1;
      await new Promise((r) => setTimeout(r, 5));
      renewed = true;
      setSession({ access: "fresh", refresh: "rt" });
      return true;
    });

    await Promise.all([request("/teams"), request("/users"), request("/whoami")]);

    expect(renewals).toBe(1);
  });

  // The retry calls send() rather than request(), so it cannot renew again.
  // Asserting the call count is what keeps that structural: a future edit that
  // routes the retry back through request() would loop, and this would hang.
  it("does not retry a second time", async () => {
    setToken("expired");
    const mock = vi.fn<typeof fetch>(async () => new Response("", { status: 401 }));
    vi.stubGlobal("fetch", mock);

    onSessionExpired(async () => {
      setSession({ access: "also-refused", refresh: "rt" });
      return true;
    });

    await expect(request("/teams")).rejects.toBeInstanceOf(ApiError);
    expect(mock).toHaveBeenCalledTimes(2);
  });
});
