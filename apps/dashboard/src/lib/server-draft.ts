import type { MCPServer, MCPServerRequest } from "@/api/types";

export type ServerDraft = {
  name: string;
  transport: "url" | "command";
  url: string;
  authRef: string;
  command: string;
  env: string;
  bare: boolean;
  /** A bearer token to write to the secret store; never read back, so never filled in. */
  token: string;
};

export const EMPTY_SERVER: ServerDraft = {
  name: "",
  transport: "url",
  url: "",
  authRef: "",
  command: "",
  env: "",
  bare: false,
  token: "",
};

/**
 * The form as the API takes it. A command is one argument per line, because
 * splitting a line on spaces breaks every argument that has one; env is one
 * NAME=reference per line. What the brain refuses, it explains.
 */
export function serverRequest(d: ServerDraft): MCPServerRequest | string {
  const name = d.name.trim();
  if (!name) return "A server needs a name.";
  const body: MCPServerRequest = { name };
  if (d.bare) body.bare = true;

  if (d.transport === "url") {
    if (!d.url.trim()) return "A url server needs its URL.";
    body.url = d.url.trim();
    if (d.authRef.trim()) body.auth_secret_ref = d.authRef.trim();
    return body;
  }

  const argv = d.command
    .split("\n")
    .map((a) => a.trim())
    .filter(Boolean);
  if (argv.length === 0) return "A command server needs its command, one argument per line.";
  body.command = argv;

  const env: Record<string, string> = {};
  for (const line of d.env
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)) {
    const at = line.indexOf("=");
    if (at <= 0) return `"${line}" is not NAME=reference.`;
    env[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  body.env = env;
  return body;
}

/** A server as the form shows it. */
export function draftOf(s: MCPServer): ServerDraft {
  return {
    name: s.name,
    transport: s.command ? "command" : "url",
    url: s.url ?? "",
    authRef: s.auth_secret_ref ?? "",
    command: (s.command ?? []).join("\n"),
    env: Object.entries(s.env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    bare: s.bare,
    token: "",
  };
}

/** Where a token typed into a server's form is written, and what the server then names. */
export const TOKEN_KEY = "token";

/** A secret's name: letters, digits, _ and -, as the brain takes one. */
export const SECRET_NAME = /^[A-Za-z0-9_-]{1,128}$/;

export function tokenRef(teamId: string, serverName: string): string {
  return `teams/${teamId}/mcp/${serverName}#${TOKEN_KEY}`;
}

/**
 * The whole definition a PUT replaces the server with: the server as it
 * stands, with the form laid over it. The form owns the name, bare and its
 * transport's fields; a field it does not show (env on a url server, a token
 * on a command one) is kept, unless the transport itself changed, when the
 * old transport's fields go with it.
 */
export function editRequest(current: MCPServer, d: ServerDraft): MCPServerRequest | string {
  const form = serverRequest(d);
  if (typeof form === "string") return form;

  const body: MCPServerRequest = { name: form.name, bare: Boolean(form.bare) };
  const sameTransport = (current.command ? "command" : "url") === d.transport;
  if (sameTransport) {
    if (current.url !== undefined) body.url = current.url;
    if (current.auth_secret_ref !== undefined) body.auth_secret_ref = current.auth_secret_ref;
    if (current.command !== undefined) body.command = current.command;
    body.env = current.env;
  }

  if (d.transport === "url") {
    delete body.command;
    body.url = form.url;
    if (form.auth_secret_ref) body.auth_secret_ref = form.auth_secret_ref;
    else delete body.auth_secret_ref;
  } else {
    delete body.url;
    body.command = form.command;
    body.env = form.env;
  }
  return body;
}

/** Where a server's discovery stands: what the row's status says. */
export type Discovery = "answered" | "failed" | "not discovered";

/**
 * A server whose last discovery failed says so until one works; one that has
 * answered once and not failed since is answered; one never asked is neither.
 */
export function discoveryOf(s: MCPServer): Discovery {
  if (s.last_discovery_error) return "failed";
  if (s.discovered_at) return "answered";
  return "not discovered";
}
