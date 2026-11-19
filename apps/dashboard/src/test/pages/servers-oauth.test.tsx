import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { MCPServer } from "@/api/types";
import { scopesOf, serverRequest, signInOf } from "@/lib/server-draft";
import { brain, STAMP, showAt, TEAM, team } from "@/test/brain";

const toasts = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast: toasts, Toaster: () => null }));

const { Route, outcomeMessage } = await import("@/routes/ui.servers.index");
/** The screen under a router, since it reads what a sign-in came back with from its address. */
function showServers(at = "/ui/servers/") {
  return showAt(at, [["/ui/servers/", Route]]);
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  window.sessionStorage.clear();
  setToken(null);
});

function gmail(connection: MCPServer["connection"]): MCPServer {
  return {
    id: "m2",
    team_id: TEAM,
    name: "gmail",
    url: "https://gmailmcp.example/mcp/v1",
    env: {},
    auth_kind: "oauth",
    oauth_client_id: "client-1",
    oauth_scopes: ["gmail.readonly"],
    connection,
    bare: false,
    trust_hints: false,
    discovered_at: null,
    last_discovery_at: null,
    last_discovery_error: null,
    created_at: STAMP,
    updated_at: STAMP,
  };
}

const ended = {
  status: "needs_sign_in" as const,
  reason: "the authorization server refused the refresh",
  connected_at: STAMP,
  scopes_granted: ["gmail.readonly"],
};
const live = {
  status: "connected" as const,
  connected_at: STAMP,
  scopes_granted: ["gmail.readonly"],
};

const summary = {
  runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
  sessions: 0,
  waiting_senders: 0,
  servers_failing: 0,
  agents: 0,
};

function serving(...items: MCPServer[]) {
  return {
    "GET /teams/{id}/mcp-servers": { body: { items } },
    "GET /teams/{id}/summary": { body: summary },
    "GET /teams/{id}/mcp-servers/{sid}/tools": { body: { items: [] } },
  };
}

const draft = {
  name: "gmail",
  transport: "url" as const,
  url: "https://gmailmcp.example/mcp/v1",
  authRef: "teams/t/mcp/x#token",
  command: "",
  env: "",
  vars: "",
  egress: "",
  bare: false,
  token: "",
  auth: "oauth" as const,
  client: "own" as const,
  clientId: " client-1 ",
  clientSecret: "",
  clientSecretRef: "",
  scopes: "gmail.readonly, gmail.send\nopenid",
};

function rowOf(name: string): HTMLElement {
  return screen.getByRole("button", { name }).closest("tr") as HTMLElement;
}

describe("a server that signs in, as the form sends it", () => {
  it("sends its client and scopes, and no static token", () => {
    expect(serverRequest(draft)).toEqual({
      name: "gmail",
      url: "https://gmailmcp.example/mcp/v1",
      auth_kind: "oauth",
      oauth_client_id: "client-1",
      oauth_scopes: ["gmail.readonly", "gmail.send", "openid"],
    });
  });

  it("refuses a client registered by hand with no ID before anything is sent", () => {
    expect(serverRequest({ ...draft, clientId: "  " })).toMatch(/client ID/);
  });

  // Registered automatically, the server names no client and no secret: the
  // brain registers one when it is first connected.
  it("sends no client when openarity is to register one", () => {
    expect(
      serverRequest({
        ...draft,
        client: "auto",
        clientSecretRef: "teams/t/mcp/gmail-oauth#client_secret",
      }),
    ).toEqual({
      name: "gmail",
      url: "https://gmailmcp.example/mcp/v1",
      auth_kind: "oauth",
      oauth_scopes: ["gmail.readonly", "gmail.send", "openid"],
    });
  });

  it("reads scopes split by spaces, commas and lines", () => {
    expect(scopesOf(" a,b\n c  ")).toEqual(["a", "b", "c"]);
    expect(scopesOf("")).toEqual([]);
  });

  it("says where a sign-in stands, and nothing for a server that does not sign in", () => {
    expect(signInOf(gmail(null))).toBe("not connected");
    expect(signInOf(gmail(ended))).toBe("needs sign-in");
    expect(signInOf(gmail(live))).toBe("connected");
    expect(signInOf({ ...gmail(null), auth_kind: "static" })).toBeNull();
  });
});

describe("signing in from the servers screen", () => {
  it("offers Connect for a server never signed in, and sends the browser where the brain says", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, origin: "https://dash.example" });
    const seen = brain({
      ...team(["tool:write"]),
      ...serving(gmail(null)),
      "POST /teams/{id}/mcp-servers/{sid}/oauth/start": {
        body: { authorize_url: "https://accounts.example/auth?state=s" },
      },
    });
    showServers();

    await screen.findByRole("button", { name: "gmail" });
    expect(within(rowOf("gmail")).getByText("not connected")).toBeInTheDocument();
    await userEvent.click(within(rowOf("gmail")).getByRole("button", { name: /^connect$/i }));

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith("https://accounts.example/auth?state=s"),
    );
    expect(seen.find((s) => s.method === "POST")?.path).toBe(
      `/teams/${TEAM}/mcp-servers/m2/oauth/start`,
    );
  });

  it("offers Reconnect for one that needs it, and says why in its sheet", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(ended)) });
    showServers();

    await screen.findByRole("button", { name: "gmail" });
    expect(
      within(rowOf("gmail")).getByRole("button", { name: /^reconnect$/i }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "gmail" }));
    const sheet = await screen.findByRole("dialog", { name: /gmail/ });
    expect(within(sheet).getByText(/refused the refresh/)).toBeInTheDocument();
    expect(within(sheet).getByText("needs sign-in")).toBeInTheDocument();
  });

  it("offers Discover only once a server that signs in is connected", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(null)) });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "gmail" }));
    const sheet = await screen.findByRole("dialog", { name: /gmail/ });
    expect(within(sheet).queryByRole("button", { name: /^discover$/i })).toBeNull();
    expect(within(sheet).getByRole("button", { name: /^connect$/i })).toBeInTheDocument();
  });

  it("offers Discover on a server that signs in once it is connected", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(live)) });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "gmail" }));
    const sheet = await screen.findByRole("dialog", { name: /gmail/ });
    expect(within(sheet).getByRole("button", { name: /^discover$/i })).toBeInTheDocument();
  });

  it("offers no sign-in action without tool:write", async () => {
    brain({ ...team([]), ...serving(gmail(null)) });
    showServers();

    await screen.findByRole("button", { name: "gmail" });
    expect(screen.queryByRole("button", { name: /connect/i })).toBeNull();
  });

  it("disconnects once it is confirmed, then reads the list again", async () => {
    let lists = 0;
    const seen = brain({
      ...team(["tool:write"]),
      ...serving(),
      "GET /teams/{id}/mcp-servers": () => {
        lists++;
        return { body: { items: [gmail(live)] } };
      },
      "DELETE /teams/{id}/mcp-servers/{sid}/oauth": { status: 204 },
    });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "gmail" }));
    const sheet = await screen.findByRole("dialog", { name: /gmail/ });
    await userEvent.click(within(sheet).getByRole("button", { name: /^disconnect$/i }));
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);

    const confirm = await screen.findByRole("dialog", { name: /disconnect gmail/i });
    const before = lists;
    await userEvent.click(within(confirm).getByRole("button", { name: /^disconnect$/i }));
    await waitFor(() =>
      expect(seen.find((s) => s.method === "DELETE")?.path).toBe(
        `/teams/${TEAM}/mcp-servers/m2/oauth`,
      ),
    );
    await waitFor(() => expect(lists).toBeGreaterThan(before));
  });

  it("writes a typed client secret to the store under its own name, and asks no tools of it yet", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...serving(),
      "PUT /teams/{id}/mcp-secrets/{name}": { status: 204 },
      "POST /teams/{id}/mcp-servers": { status: 201, body: gmail(null) },
    });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "gmail");
    await userEvent.type(
      screen.getByRole("textbox", { name: /^url$/i }),
      "https://gmailmcp.example/mcp/v1",
    );
    await userEvent.click(screen.getByRole("radio", { name: /oauth/i }));
    await userEvent.click(screen.getByRole("radio", { name: /use a client you registered/i }));
    await userEvent.type(screen.getByLabelText(/^client id$/i), "client-1");
    await userEvent.type(screen.getByLabelText(/^client secret$/i), "the-client-secret");
    await userEvent.type(screen.getByLabelText(/^scopes$/i), "gmail.readonly");
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    const writes = seen.filter((s) => s.method !== "GET");
    expect(writes.map((s) => `${s.method} ${s.path}`)).toEqual([
      `PUT /teams/${TEAM}/mcp-secrets/gmail-oauth`,
      `POST /teams/${TEAM}/mcp-servers`,
    ]);
    expect(JSON.parse(String(writes[0]?.body))).toEqual({
      values: { client_secret: "the-client-secret" },
    });
    const body = String(writes[1]?.body);
    expect(JSON.parse(body)).toEqual({
      name: "gmail",
      url: "https://gmailmcp.example/mcp/v1",
      auth_kind: "oauth",
      oauth_client_id: "client-1",
      oauth_client_secret_ref: `teams/${TEAM}/mcp/gmail-oauth#client_secret`,
      oauth_scopes: ["gmail.readonly"],
    });
    expect(body).not.toContain("the-client-secret");
  });
});

/** A server that signs in with no client of its own: openarity registers one. */
function cloud(connection: MCPServer["connection"], registeredAs?: string): MCPServer {
  const { oauth_client_id: _own, ...rest } = gmail(connection);
  return {
    ...rest,
    id: "m3",
    name: "cloud",
    url: "https://cloud.example/mcp",
    oauth_registration: registeredAs ? { client_id: registeredAs, registered_at: STAMP } : null,
  };
}

describe("a client openarity registers itself", () => {
  it("is the default, and asks for nothing to copy", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...serving(),
      "POST /teams/{id}/mcp-servers": { status: 201, body: cloud(null) },
    });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "cloud");
    await userEvent.type(
      screen.getByRole("textbox", { name: /^url$/i }),
      "https://cloud.example/mcp",
    );
    await userEvent.click(screen.getByRole("radio", { name: /oauth/i }));

    expect(screen.getByRole("radio", { name: /register automatically/i })).toBeChecked();
    expect(screen.queryByLabelText(/^client id$/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/redirect uri/i)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    const writes = seen.filter((s) => s.method !== "GET");
    expect(writes.map((s) => `${s.method} ${s.path}`)).toEqual([`POST /teams/${TEAM}/mcp-servers`]);
    expect(JSON.parse(String(writes[0]?.body))).toEqual({
      name: "cloud",
      url: "https://cloud.example/mcp",
      auth_kind: "oauth",
    });
  });

  // A secret typed for a client registered by hand, then the choice changed
  // back: the brain refuses a secret with no client to go with it.
  it("drops a typed client secret once the choice goes back to registering automatically", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...serving(),
      "PUT /teams/{id}/mcp-secrets/{name}": { status: 204 },
      "POST /teams/{id}/mcp-servers": { status: 201, body: cloud(null) },
    });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "cloud");
    await userEvent.type(
      screen.getByRole("textbox", { name: /^url$/i }),
      "https://cloud.example/mcp",
    );
    await userEvent.click(screen.getByRole("radio", { name: /oauth/i }));
    await userEvent.click(screen.getByRole("radio", { name: /use a client you registered/i }));
    await userEvent.type(screen.getByLabelText(/^client secret$/i), "typed-then-dropped");
    await userEvent.click(screen.getByRole("radio", { name: /register automatically/i }));
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    const writes = seen.filter((s) => s.method !== "GET");
    expect(writes.map((s) => `${s.method} ${s.path}`)).toEqual([`POST /teams/${TEAM}/mcp-servers`]);
    expect(String(writes[0]?.body)).not.toContain("client_secret");
  });

  it("shows the redirect URI only for a client registered by hand", async () => {
    vi.stubGlobal("location", { ...window.location, origin: "https://dash.example" });
    brain({ ...team(["tool:write"]), ...serving() });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.click(screen.getByRole("radio", { name: /oauth/i }));
    await userEvent.click(screen.getByRole("radio", { name: /use a client you registered/i }));

    expect(screen.getByLabelText(/^client id$/i)).toBeInTheDocument();
    expect(screen.getByText("https://dash.example/oauth/mcp/callback")).toBeInTheDocument();
  });

  it("opens an edit on the client it already uses", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(null), cloud(null, "reg-1")) });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "gmail" }));
    let sheet = await screen.findByRole("dialog", { name: /gmail/ });
    await userEvent.click(within(sheet).getByRole("button", { name: /^edit$/i }));
    expect(
      await screen.findByRole("radio", { name: /use a client you registered/i }),
    ).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: /cancel/i }));

    await userEvent.click(await screen.findByRole("button", { name: "cloud" }));
    sheet = await screen.findByRole("dialog", { name: /cloud/ });
    await userEvent.click(within(sheet).getByRole("button", { name: /^edit$/i }));
    expect(await screen.findByRole("radio", { name: /register automatically/i })).toBeChecked();
  });

  it("says in its sheet which client was registered, and when", async () => {
    brain({ ...team(["tool:write"]), ...serving(cloud(live, "reg-1")) });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "cloud" }));
    const sheet = await screen.findByRole("dialog", { name: /cloud/ });
    expect(within(sheet).getByText(/registered automatically/i)).toBeInTheDocument();
    expect(within(sheet).getByText("reg-1")).toBeInTheDocument();
  });

  it("registers again once confirmed, then sends the browser to sign in", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, origin: "https://dash.example" });
    const seen = brain({
      ...team(["tool:write"]),
      ...serving(cloud(live, "reg-1")),
      "DELETE /teams/{id}/mcp-servers/{sid}/oauth/registration": { status: 204 },
      "POST /teams/{id}/mcp-servers/{sid}/oauth/start": {
        body: { authorize_url: "https://accounts.example/auth?state=s" },
      },
    });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "cloud" }));
    const sheet = await screen.findByRole("dialog", { name: /cloud/ });
    await userEvent.click(within(sheet).getByRole("button", { name: /^register again$/i }));
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);

    const confirm = await screen.findByRole("dialog", { name: /register cloud again/i });
    await userEvent.click(within(confirm).getByRole("button", { name: /^register again$/i }));

    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith("https://accounts.example/auth?state=s"),
    );
    expect(seen.filter((s) => s.method !== "GET").map((s) => `${s.method} ${s.path}`)).toEqual([
      `DELETE /teams/${TEAM}/mcp-servers/m3/oauth/registration`,
      `POST /teams/${TEAM}/mcp-servers/m3/oauth/start`,
    ]);
  });

  it("offers no Register again for a client registered by hand", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(live)) });
    showServers();

    await userEvent.click(await screen.findByRole("button", { name: "gmail" }));
    const sheet = await screen.findByRole("dialog", { name: /gmail/ });
    expect(
      within(sheet).queryByRole("button", { name: /register again/i }),
    ).not.toBeInTheDocument();
  });

  it("offers a client of its own when the server's sign-in registers nobody", async () => {
    brain({
      ...team(["tool:write"]),
      ...serving(cloud(null)),
      "POST /teams/{id}/mcp-servers/{sid}/oauth/start": {
        status: 409,
        text: "the server's sign-in does not let apps register themselves: register a client with it and set its client id on the server",
      },
    });
    showServers();

    await screen.findByRole("button", { name: "cloud" });
    await userEvent.click(within(rowOf("cloud")).getByRole("button", { name: /^connect$/i }));

    await waitFor(() => expect(toasts.error).toHaveBeenCalled());
    const [text, options] = toasts.error.mock.calls[0] ?? [];
    expect(text).toMatch(/can't register automatically/);
    expect(options?.action?.label).toBe("Edit server");

    options.action.onClick();
    expect(
      await screen.findByRole("radio", { name: /use a client you registered/i }),
    ).toBeChecked();
  });

  it("says why any other refused Connect was refused", async () => {
    brain({
      ...team(["tool:write"]),
      ...serving(cloud(null)),
      "POST /teams/{id}/mcp-servers/{sid}/oauth/start": {
        status: 502,
        text: "the server's sign-in did not accept openarity's registration: redirect not allowed",
      },
    });
    showServers();

    await screen.findByRole("button", { name: "cloud" });
    await userEvent.click(within(rowOf("cloud")).getByRole("button", { name: /^connect$/i }));

    await waitFor(() => expect(toasts.error).toHaveBeenCalled());
    const [text, options] = toasts.error.mock.calls[0] ?? [];
    expect(text).toMatch(/redirect not allowed/);
    expect(options).toBeUndefined();
  });
});

describe("back from a sign-in", () => {
  it.each([
    ["connected", true, /gmail is connected/],
    ["connected-no-tools", true, /could not be listed/],
    ["denied", false, /declined/],
    ["expired", false, /ten minutes/],
    ["refused", false, /refused/],
    ["failed", false, /could not be finished/],
    ["unregistered", false, /no longer accepts openarity's registration/],
  ] as const)("tells %s in its own words", (outcome, ok, said) => {
    const m = outcomeMessage(outcome, "gmail");
    expect(m.ok).toBe(ok);
    expect(m.text).toMatch(said);
  });

  it("says how it went once, then takes it out of the address", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(live)) });
    const router = showServers("/ui/servers/?oauth=connected&server=m2");

    await waitFor(() => expect(toasts.success).toHaveBeenCalledWith("gmail is connected."));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(toasts.success).toHaveBeenCalledTimes(1);
  });

  it("ignores an outcome it does not know", async () => {
    brain({ ...team(["tool:write"]), ...serving(gmail(live)) });
    showServers("/ui/servers/?oauth=pwned&server=m2");

    await screen.findByRole("button", { name: "gmail" });
    expect(toasts.success).not.toHaveBeenCalled();
    expect(toasts.error).not.toHaveBeenCalled();
  });
});
