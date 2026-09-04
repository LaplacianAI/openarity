/**
 * The one place this app performs HTTP.
 *
 * Relative URLs throughout: the bundle is served by the brain itself, so the
 * page and its API share an origin whether that origin is 127.0.0.1, a LAN
 * address or a port forward. An absolute host here would work on the machine
 * it was written on and nowhere else.
 */
import { getToken, setToken } from "./token";
import type { Page } from "./types";

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
    // Drop it rather than keep retrying with a credential the brain has
    // already refused — otherwise every subsequent screen fails the same way
    // and the reason scrolls off.
    setToken(null);
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
