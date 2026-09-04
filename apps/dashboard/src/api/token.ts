/**
 * Where the bearer token lives.
 *
 * One module, because a token that can be read from three places is a token
 * that gets logged from a fourth. Everything that talks to the brain goes
 * through `client.ts`, and `client.ts` reads it from here.
 *
 * sessionStorage rather than localStorage: it is scoped to the tab and cleared
 * when the tab closes, which is the behaviour somebody expects from a console
 * they opened on a shared machine. A refresh survives; a reboot does not.
 */

const STORAGE_KEY = "openarity.token";

let token: string | null = null;
const listeners = new Set<() => void>();

function read(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.sessionStorage.getItem(STORAGE_KEY);
  } catch {
    // Storage can throw in private modes and sandboxed frames. An unreadable
    // store means no token, not a broken app.
    return null;
  }
}

export function getToken(): string | null {
  if (token === null) token = read();
  return token;
}

export function setToken(next: string | null): void {
  token = next;
  try {
    if (next === null) window.sessionStorage.removeItem(STORAGE_KEY);
    else window.sessionStorage.setItem(STORAGE_KEY, next);
  } catch {
    // Held in memory for this page's lifetime even when it cannot be stored.
  }
  for (const listener of listeners) listener();
}

export function subscribeToken(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
