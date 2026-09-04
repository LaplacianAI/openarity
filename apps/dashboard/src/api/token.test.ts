import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getSession, getToken, setSession, setToken, subscribeToken } from "./token";

beforeEach(() => {
  window.sessionStorage.clear();
  setToken(null);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("the token store", () => {
  it("survives a reload by living in sessionStorage", () => {
    setToken("abc123");
    expect(JSON.parse(window.sessionStorage.getItem("openarity.session") as string)).toEqual({
      access: "abc123",
    });
    expect(getToken()).toBe("abc123");
  });

  it("clears the stored copy rather than storing the word null", () => {
    setToken("abc123");
    setToken(null);
    expect(window.sessionStorage.getItem("openarity.session")).toBeNull();
    expect(getToken()).toBeNull();
  });

  // A development token has no provider behind it, so nothing can renew it.
  // Its missing refresh token is what tells the client a 401 is final.
  it("gives a development token no refresh token", () => {
    setToken("letmein");
    expect(getSession()?.refresh).toBeUndefined();
  });

  it("keeps the refresh token and expiry a provider returned", () => {
    const expiresAt = Date.now() + 3_600_000;
    setSession({ access: "at", refresh: "rt", expiresAt });

    expect(getSession()).toEqual({ access: "at", refresh: "rt", expiresAt });
    expect(getToken()).toBe("at");
  });

  // A half-written or hand-edited entry must not be trusted into the app.
  it("ignores a stored session with no access token", () => {
    window.sessionStorage.setItem("openarity.session", JSON.stringify({ refresh: "rt" }));
    setSession(null);
    window.sessionStorage.setItem("openarity.session", JSON.stringify({ refresh: "rt" }));

    expect(getToken()).toBeNull();
  });

  it("tells subscribers, so a sign-out reaches the screens", () => {
    const seen: Array<string | null> = [];
    const stop = subscribeToken(() => seen.push(getToken()));

    setToken("one");
    setToken(null);
    stop();
    setToken("ignored");

    expect(seen).toEqual(["one", null]);
  });

  // Private browsing and sandboxed frames throw on write. Losing persistence
  // is acceptable; losing the session the operator just started is not.
  it("keeps the token in memory when storage refuses to hold it", () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });

    expect(() => setToken("held-in-memory")).not.toThrow();
    expect(getToken()).toBe("held-in-memory");
  });
});
