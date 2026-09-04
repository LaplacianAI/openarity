/**
 * The one place this app performs HTTP.
 *
 * Relative URLs throughout: the bundle is served by the brain itself, so the
 * page and its API share an origin whether that origin is 127.0.0.1, a LAN
 * address or a port forward. An absolute host here would work on the machine
 * it was written on and nowhere else.
 */
import { getToken, setSession } from "./token";
import type { Page } from "./types";

/**
 * How the client renews a session. Injected rather than imported so this
 * module keeps knowing nothing about OIDC — a development token has no
 * provider behind it, and the brain is reached the same way either way.
 */
type Renewer = () => Promise<boolean>;

let renew: Renewer | null = null;

export function onSessionExpired(renewer: Renewer | null): void {
  renew = renewer;
}

/**
 * One renewal at a time. Several requests failing together is the normal case
 * — a screen makes three calls and an expired token fails all of them — and
 * without this they would each spend the refresh token, of which only the
 * first would succeed.
 */
let inFlight: Promise<boolean> | null = null;

function renewOnce(): Promise<boolean> {
  if (!renew) return Promise.resolve(false);
  if (!inFlight) {
    inFlight = renew().finally(() => {
      inFlight = null;
    });
  }
  return inFlight;
}

/** Thrown for any non-2xx answer, carrying the status so callers can branch. */
export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }

  /** The one status with a specific remedy: the token is missing or stale. */
  get unauthenticated(): boolean {
    return this.status === 401;
  }
}

type RequestOptions = {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
  /** For /auth/config, which answers before anyone has a token. */
  anonymous?: boolean;
};

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  try {
    return await send<T>(path, options);
  } catch (err) {
    // One retry, and only for an expired session with something to renew it
    // with. There is no loop to guard against: the retry calls send() rather
    // than request(), so a brain that refuses even a freshly minted token
    // fails on the second attempt instead of renewing again.
    if (!(err instanceof ApiError) || !err.unauthenticated) throw err;
    if (!(await renewOnce())) {
      setSession(null);
      throw err;
    }
    return send<T>(path, options);
  }
}

async function send<T>(path: string, options: RequestOptions): Promise<T> {
  const headers = new Headers({ Accept: "application/json" });

  if (!options.anonymous) {
    const token = getToken();
    if (token) headers.set("Authorization", `Bearer ${token}`);
  }
  if (options.body !== undefined) headers.set("Content-Type", "application/json");

  const res = await fetch(path, {
    method: options.method ?? "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
    ...(options.signal ? { signal: options.signal } : {}),
  });

  if (res.status === 401) {
    // Not cleared here: the caller decides, because a session with a refresh
    // token has somewhere to go and one without does not. Clearing at this
    // depth would sign the operator out before the renewal was even tried.
    throw new ApiError(401, "the brain refused this token");
  }

  if (!res.ok) {
    throw new ApiError(res.status, await describe(res));
  }

  // 204, and any other body-less success.
  if (res.status === 204 || res.headers.get("Content-Length") === "0") {
    return undefined as T;
  }

  return (await res.json()) as T;
}

/**
 * The brain answers errors as text, not JSON — `http.Error` writes a plain
 * sentence. Reading it as JSON would swallow the only useful thing in it.
 */
async function describe(res: Response): Promise<string> {
  try {
    const text = (await res.text()).trim();
    if (text) return text;
  } catch {
    // Body already consumed or unreadable; the status still says something.
  }
  return `${res.status} ${res.statusText}`;
}

/**
 * A cursor page. `cursor` is opaque and comes from the previous page — the
 * only thing a caller may do with it is send it back.
 */
export function pageQuery(cursor?: string | null, limit?: number): string {
  const params = new URLSearchParams();
  if (cursor) params.set("cursor", cursor);
  if (limit) params.set("limit", String(limit));
  const query = params.toString();
  return query ? `?${query}` : "";
}

/** Normalises the absent-vs-null difference so callers see one shape. */
export function nextCursorOf<T>(page: Page<T>): string | null {
  return page.next_cursor ?? null;
}
