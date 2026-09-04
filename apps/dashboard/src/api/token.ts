/**
 * Where the session lives.
 *
 * One module, because a token that can be read from three places is a token
 * that gets logged from a fourth. Everything that talks to the brain goes
 * through `client.ts`, and `client.ts` reads it from here.
 *
 * sessionStorage rather than localStorage: it is scoped to the tab and cleared
 * when the tab closes, which is what somebody expects from a console opened on
 * a shared machine. A refresh survives; a reboot does not.
 */

const STORAGE_KEY = "openarity.session";

export type Session = {
  access: string;
  /**
   * Absent for a development token, which no provider issued and nothing can
   * renew. Its absence is the signal that a 401 is final.
   */
  refresh?: string;
  /** Epoch milliseconds. Absent when the provider did not say. */
  expiresAt?: number;
};

let session: Session | null = null;
let loaded = false;
const listeners = new Set<() => void>();

function read(): Session | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.sessionStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Session;
    return typeof parsed?.access === "string" ? parsed : null;
  } catch {
    // Unreadable or corrupt storage means no session, not a broken app.
    return null;
  }
}

export function getSession(): Session | null {
  if (!loaded) {
    session = read();
    loaded = true;
  }
  return session;
}

export function getToken(): string | null {
  return getSession()?.access ?? null;
}

export function setSession(next: Session | null): void {
  session = next;
  loaded = true;
  try {
    if (next === null) window.sessionStorage.removeItem(STORAGE_KEY);
    else window.sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    // Held in memory for this page's lifetime even when it cannot be stored.
  }
  for (const listener of listeners) listener();
}

/** The development-token path: an opaque string with nothing behind it. */
export function setToken(next: string | null): void {
  setSession(next === null ? null : { access: next });
}

export function subscribeToken(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}
