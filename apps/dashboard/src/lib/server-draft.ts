import type { Egress, MCPServer, MCPServerRequest } from "@/api/types";

export type ServerDraft = {
  name: string;
  transport: "url" | "command";
  url: string;
  authRef: string;
  command: string;
  env: string;
  /** NAME=value, one per line: plain settings, shown as they are. */
  vars: string;
  /** host:port, one per line: where its sandbox room may connect. */
  egress: string;
  bare: boolean;
  /** A bearer token to write to the secret store; never read back, so never filled in. */
  token: string;
  /** A url server's way in: a static token or none, or a sign-in. */
  auth: "token" | "oauth";
  /** A sign-in's client: one openarity registers itself, or one registered by hand. */
  client: "auto" | "own";
  clientId: string;
  /** A confidential client's secret to write to the secret store; never read back. */
  clientSecret: string;
  /** A client secret already in the store, kept unless a new one is typed. */
  clientSecretRef: string;
  /** Space- or comma-separated, as a provider's documentation lists them. */
  scopes: string;
};

export const EMPTY_SERVER: ServerDraft = {
  name: "",
  transport: "url",
  url: "",
  authRef: "",
  command: "",
  env: "",
  vars: "",
  egress: "",
  bare: false,
  token: "",
  auth: "token",
  client: "auto",
  clientId: "",
  clientSecret: "",
  clientSecretRef: "",
  scopes: "",
};

/** The scopes as typed, one per word: spaces, commas and new lines all separate. */
export function scopesOf(typed: string): string[] {
  return typed.split(/[\s,]+/).filter(Boolean);
}

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
    if (d.auth === "oauth") {
      body.auth_kind = "oauth";
      // Registered automatically, the server names no client: the brain
      // registers one at the first Connect.
      if (d.client === "own") {
        if (!d.clientId.trim())
          return "Add the client ID you registered, or let openarity register one.";
        body.oauth_client_id = d.clientId.trim();
        if (d.clientSecretRef.trim()) body.oauth_client_secret_ref = d.clientSecretRef.trim();
      }
      const scopes = scopesOf(d.scopes);
      if (scopes.length > 0) body.oauth_scopes = scopes;
      return body;
    }
    if (d.authRef.trim()) body.auth_secret_ref = d.authRef.trim();
    return body;
  }

  const argv = d.command
    .split("\n")
    .map((a) => a.trim())
    .filter(Boolean);
  if (argv.length === 0) return "A command server needs its command, one argument per line.";
  // Run without a shell, a first line with a space names a program that
  // cannot exist; later arguments may hold spaces.
  const program = argv[0] ?? "";
  if (/\s/.test(program)) {
    return `Put each argument of "${program}" on its own line: the first line is only the program, such as npx.`;
  }
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

  const vars: Record<string, string> = {};
  for (const line of lines(d.vars)) {
    const at = line.indexOf("=");
    if (at <= 0) return `"${line}" is not NAME=value.`;
    const k = line.slice(0, at).trim();
    if (k in env) return `${k} is in both the environment and the settings; keep it in one.`;
    vars[k] = line.slice(at + 1).trim();
  }
  if (Object.keys(vars).length > 0) body.vars = vars;

  const egress: Egress[] = [];
  for (const line of lines(d.egress)) {
    const place = egressOf(line);
    if (!place) return `"${line}" is not host:port.`;
    egress.push(place);
  }
  if (egress.length > 0) body.egress = egress;
  return body;
}

function lines(typed: string): string[] {
  return typed
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
}

/** host:port, a port from 1 to 65535. Which hosts are allowed is the brain's to say. */
export function egressOf(line: string): Egress | null {
  const at = line.lastIndexOf(":");
  if (at <= 0) return null;
  let host = line.slice(0, at).trim();
  if (host.startsWith("[") && host.endsWith("]")) host = host.slice(1, -1);
  const raw = line.slice(at + 1).trim();
  if (!host || !/^\d{1,5}$/.test(raw)) return null;
  const port = Number(raw);
  return port >= 1 && port <= 65535 ? { host, port } : null;
}

/** The registry npx installs from; a room that cannot reach it never starts. */
export const NPM_REGISTRY = "registry.npmjs.org:443";

/** Whether a command run through npx has no way to its registry. */
export function missesRegistry(d: ServerDraft): boolean {
  const first = lines(d.command)[0];
  if (first !== "npx" && first !== "npm") return false;
  return !lines(d.egress).some((l) => {
    const e = egressOf(l);
    return e?.host.toLowerCase() === "registry.npmjs.org" && e.port === 443;
  });
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
    vars: Object.entries(s.vars ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    egress: (s.egress ?? [])
      .map((e) => `${e.host.includes(":") ? `[${e.host}]` : e.host}:${e.port}`)
      .join("\n"),
    bare: s.bare,
    token: "",
    auth: s.auth_kind === "oauth" ? "oauth" : "token",
    client: s.oauth_client_id ? "own" : "auto",
    clientId: s.oauth_client_id ?? "",
    clientSecret: "",
    clientSecretRef: s.oauth_client_secret_ref ?? "",
    scopes: (s.oauth_scopes ?? []).join(" "),
  };
}

/** Where a token typed into a server's form is written, and what the server then names. */
export const TOKEN_KEY = "token";

/** A secret's name: letters, digits, _ and -, as the brain takes one. */
export const SECRET_NAME = /^[A-Za-z0-9_-]{1,128}$/;

export function tokenRef(teamId: string, serverName: string): string {
  return `teams/${teamId}/mcp/${serverName}#${TOKEN_KEY}`;
}

/** Where a client secret typed into a server's form is written: a secret of its own, beside a token's. */
export const CLIENT_SECRET_KEY = "client_secret";

export function clientSecretName(serverName: string): string {
  return `${serverName}-oauth`;
}

export function clientSecretRef(teamId: string, serverName: string): string {
  return `teams/${teamId}/mcp/${clientSecretName(serverName)}#${CLIENT_SECRET_KEY}`;
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
    // How it is reached is the form's: a server that signs in carries its
    // client, and one that does not carries none.
    if (form.auth_kind === "oauth") {
      delete body.auth_secret_ref;
      body.auth_kind = "oauth";
      if (form.oauth_client_id) body.oauth_client_id = form.oauth_client_id;
      if (form.oauth_client_secret_ref) body.oauth_client_secret_ref = form.oauth_client_secret_ref;
      if (form.oauth_scopes) body.oauth_scopes = form.oauth_scopes;
    }
  } else {
    delete body.url;
    body.command = form.command;
    body.env = form.env;
    // The form shows both, so what it leaves empty is cleared.
    if (form.vars) body.vars = form.vars;
    if (form.egress) body.egress = form.egress;
  }
  return body;
}

/** Where a server's discovery stands: what the row's status says. */
export type Discovery = "answered" | "failed" | "not discovered";

/**
 * A server whose last discovery failed says so until one works; one that has
 * answered once and not failed since is answered; one never asked is neither.
 */
/** Where a server's sign-in stands; null for one that does not sign in. */
export type SignIn = "connected" | "needs sign-in" | "not connected";

export function signInOf(s: MCPServer): SignIn | null {
  if (s.auth_kind !== "oauth") return null;
  if (!s.connection) return "not connected";
  return s.connection.status === "needs_sign_in" ? "needs sign-in" : "connected";
}

export function discoveryOf(s: MCPServer): Discovery {
  if (s.last_discovery_error) return "failed";
  if (s.discovered_at) return "answered";
  return "not discovered";
}
