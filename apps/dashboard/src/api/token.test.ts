import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getToken, setToken, subscribeToken } from "./token";

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
    expect(window.sessionStorage.getItem("openarity.token")).toBe("abc123");
    expect(getToken()).toBe("abc123");
  });

  it("clears the stored copy rather than storing the word null", () => {
    setToken("abc123");
    setToken(null);
    expect(window.sessionStorage.getItem("openarity.token")).toBeNull();
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
