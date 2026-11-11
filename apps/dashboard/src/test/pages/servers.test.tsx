import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { MCPServer } from "@/api/types";
import { discoveryOf, serverRequest } from "@/lib/server-draft";
import { brain, pageOf, type Seen, STAMP, show, TEAM, team } from "@/test/brain";

const { Route, serversFinding } = await import("@/routes/ui.servers.index");
const Servers = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const github = {
  id: "m1",
  team_id: TEAM,
  name: "github",
  url: "https://mcp.example.com",
  auth_secret_ref: `teams/${TEAM}/mcp/github#token`,
  env: {},
  bare: false,
  discovered_at: STAMP,
  last_discovery_at: STAMP,
  last_discovery_error: null,
  created_at: STAMP,
  updated_at: STAMP,
};

const summary = {
  runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
  sessions: 0,
  waiting_senders: 0,
  servers_failing: 0,
  agents: 0,
};

const servers = {
  "GET /teams/{id}/mcp-servers": { body: { items: [github] } },
  "GET /teams/{id}/summary": { body: summary },
};

const base = {
  name: "github",
  transport: "url" as const,
  url: "",
  authRef: "",
  command: "",
  env: "",
  bare: false,
  token: "",
};

/** The row a server's name sits in. */
function rowOf(name: string): HTMLElement {
  return screen.getByRole("button", { name }).closest("tr") as HTMLElement;
}

/** Opens a server's sheet from its row. */
async function opened(name: string): Promise<HTMLElement> {
  await userEvent.click(await screen.findByRole("button", { name }));
  return screen.findByRole("dialog", { name: new RegExp(name) });
}

describe("the register form", () => {
  it("sends a url server's URL and reference, and nothing else", () => {
    expect(
      serverRequest({ ...base, url: " https://a.b ", authRef: "teams/t/mcp/g#k", command: "npx" }),
    ).toEqual({
      name: "github",
      url: "https://a.b",
      auth_secret_ref: "teams/t/mcp/g#k",
    });
  });

  // A line per argument: splitting on spaces would break any argument that
  // holds one, such as a path.
  it("sends a command one argument per line, and its environment", () => {
    expect(
      serverRequest({
        ...base,
        transport: "command",
        command: "npx\n-y\n\n/srv/My Files\n",
        env: "TOKEN=teams/t/mcp/f#k\n\nOTHER = r#k",
        bare: true,
      }),
    ).toEqual({
      name: "github",
      bare: true,
      command: ["npx", "-y", "/srv/My Files"],
      env: { TOKEN: "teams/t/mcp/f#k", OTHER: "r#k" },
    });
  });

  it.each([
    ["no name", { ...base, name: "  ", url: "https://a.b" }, /name/],
    ["no URL", { ...base }, /URL/],
    ["no command", { ...base, transport: "command" as const }, /command/],
    [
      "env without =",
      { ...base, transport: "command" as const, command: "npx", env: "TOKEN" },
      /NAME=reference/,
    ],
    [
      "env with no name",
      { ...base, transport: "command" as const, command: "npx", env: "=r#k" },
      /NAME=reference/,
    ],
  ])("refuses %s before anything is sent", (_, draft, said) => {
    const got = serverRequest(draft);
    expect(typeof got).toBe("string");
    expect(got).toMatch(said);
  });
});

// A failure stays until a discovery works; a server never asked is neither.
describe("where a server's discovery stands", () => {
  it.each([
    [{}, "answered"],
    [{ last_discovery_error: "timed out" }, "failed"],
    [{ last_discovery_error: "timed out", discovered_at: null }, "failed"],
    [{ discovered_at: null, last_discovery_at: null }, "not discovered"],
  ])("%o is %s", (patch, said) => {
    expect(discoveryOf({ ...github, ...patch } as MCPServer)).toBe(said);
  });
});

describe("the servers list", () => {
  it("says how each server last answered, and when it was asked", async () => {
    brain({
      ...team([]),
      ...servers,
      "GET /teams/{id}/mcp-servers": {
        body: {
          items: [
            github,
            {
              ...github,
              id: "m2",
              name: "linear",
              last_discovery_error: "the server did not answer in time",
            },
            {
              ...github,
              id: "m3",
              name: "files",
              discovered_at: null,
              last_discovery_at: null,
            },
          ],
        },
      },
    });
    show(Servers);

    await screen.findByRole("button", { name: "github" });
    expect(within(rowOf("github")).getByText("answered")).toBeInTheDocument();
    expect(within(rowOf("linear")).getByText("failed")).toBeInTheDocument();
    expect(within(rowOf("files")).getByText("not discovered")).toBeInTheDocument();
    expect(within(rowOf("files")).getByText("Never")).toBeInTheDocument();
  });

  it("says under its title how many failed their last discovery", async () => {
    brain({
      ...team([]),
      ...servers,
      "GET /teams/{id}/summary": { body: { ...summary, servers_failing: 2 } },
    });
    show(Servers);
    expect(await screen.findByText("2 servers failed their last discovery.")).toBeInTheDocument();
  });

  it("says why the servers could not be listed", async () => {
    brain({
      ...team([]),
      ...servers,
      "GET /teams/{id}/mcp-servers": { status: 500, text: "the store is down" },
    });
    show(Servers);
    expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
  });
});

describe("what may be done", () => {
  it("offers no write action without tool:write, and says what it takes", async () => {
    brain({ ...team(["skill:write", "agent:write"]), ...servers });
    show(Servers);

    await screen.findByRole("button", { name: "github" });
    expect(await screen.findByText("tool:write")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /register server/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /^discover$/i })).toBeNull();

    const sheet = await opened("github");
    for (const name of [/^discover$/i, /^edit$/i, /^delete$/i]) {
      expect(within(sheet).queryByRole("button", { name })).toBeNull();
    }
  });

  it("offers register and discover on the list, and edit and delete on the server", async () => {
    brain({ ...team(["tool:write"]), ...servers });
    show(Servers);

    expect(await screen.findByRole("button", { name: /register server/i })).toBeInTheDocument();
    expect(
      within(rowOf("github")).getByRole("button", { name: /^discover$/i }),
    ).toBeInTheDocument();
    const sheet = await opened("github");
    for (const name of [/^discover$/i, /^edit$/i, /^delete$/i]) {
      expect(within(sheet).getByRole("button", { name })).toBeInTheDocument();
    }
  });

  // Discovery reaches a URL; the brain does not start a command to ask it.
  it("offers discovery only for a url server", async () => {
    brain({
      ...team(["tool:write"]),
      ...servers,
      "GET /teams/{id}/mcp-servers": {
        body: {
          items: [{ ...github, id: "m2", name: "files", url: undefined, command: ["npx", "srv"] }],
        },
      },
    });
    show(Servers);

    await screen.findByRole("button", { name: "files" });
    expect(screen.queryByRole("button", { name: /^discover$/i })).toBeNull();
    // The reason is said where the button would be, not left as a gap.
    expect(within(rowOf("files")).getByText("Command servers can't be used yet")).toBeVisible();
  });

  it("says under its title which servers nobody has asked, when none failed", async () => {
    const never = { ...github, discovered_at: null, last_discovery_at: null };
    brain({
      ...team([]),
      ...servers,
      "GET /teams/{id}/mcp-servers": {
        body: {
          items: [
            github,
            { ...never, id: "m2", name: "statuspage" },
            { ...never, id: "m3", name: "files", url: undefined, command: ["npx", "srv"] },
          ],
        },
      },
    });
    show(Servers);
    expect(await screen.findByText("1 server has never been asked for its tools.")).toBeVisible();
  });

  it("puts a failed discovery before servers nobody has asked", () => {
    const never = { ...github, discovered_at: null, last_discovery_at: null } as MCPServer;
    expect(serversFinding(1, [never])).toBe("1 server failed its last discovery.");
    expect(serversFinding(0, [never, never])).toBe(
      "2 servers have never been asked for their tools.",
    );
    expect(serversFinding(0, [github as MCPServer])).toBeNull();
  });
});

describe("discovering a server from its row", () => {
  function lists(seen: Seen[]) {
    return seen.filter((s) => s.method === "GET" && s.path.endsWith("/mcp-servers")).length;
  }

  it("asks that server, then reads the list again", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "POST /teams/{id}/mcp-servers/{sid}/discover": {
        body: { items: [{ name: "search_code", description: "", input_schema: {} }] },
      },
    });
    show(Servers);

    await screen.findByRole("button", { name: "github" });
    const before = lists(seen);
    await userEvent.click(within(rowOf("github")).getByRole("button", { name: /^discover$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(seen.find((s) => s.method === "POST")?.path).toBe(
      `/teams/${TEAM}/mcp-servers/m1/discover`,
    );
    await waitFor(() => expect(lists(seen)).toBeGreaterThan(before));
  });

  // The brain records a failed discovery on the server, so the row must be
  // read again to say it failed.
  it("reads the list again when the server does not answer", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "POST /teams/{id}/mcp-servers/{sid}/discover": {
        status: 502,
        text: "the server did not answer in time",
      },
    });
    show(Servers);

    await screen.findByRole("button", { name: "github" });
    const before = lists(seen);
    await userEvent.click(within(rowOf("github")).getByRole("button", { name: /^discover$/i }));
    await waitFor(() => expect(lists(seen)).toBeGreaterThan(before));
  });
});

describe("a server's sheet", () => {
  // A tool's name and description are the server's words, and so is its
  // refusal. They are shown as text, so markup in them is inert.
  it("shows the server's tools, references and last error as text", async () => {
    brain({
      ...team([]),
      ...servers,
      "GET /teams/{id}/mcp-servers": {
        body: { items: [{ ...github, last_discovery_error: "<b>refused</b>" }] },
      },
      "GET /teams/{id}/mcp-servers/{serverID}/tools": {
        body: {
          items: [
            {
              name: "search_code",
              description: '<img src=x onerror="alert(1)">',
              input_schema: {},
            },
          ],
        },
      },
    });
    show(Servers);

    const sheet = await opened("github");
    expect(await within(sheet).findByText("search_code")).toBeInTheDocument();
    expect(within(sheet).getByText('<img src=x onerror="alert(1)">')).toBeInTheDocument();
    expect(within(sheet).getByText("<b>refused</b>")).toBeInTheDocument();
    expect(sheet.querySelector("img, b")).toBeNull();
    expect(within(sheet).getByText(`teams/${TEAM}/mcp/github#token`)).toBeInTheDocument();
  });

  it("opens from the keyboard", async () => {
    brain({ ...team([]), ...servers });
    show(Servers);

    await screen.findByRole("button", { name: "github" });
    rowOf("github").focus();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: /github/ })).toBeInTheDocument();
  });

  it("deletes the server once it is confirmed, and shows the brain's refusal", async () => {
    let held = true;
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "DELETE /teams/{id}/mcp-servers/{serverID}": () =>
        held ? { status: 409, text: "the MCP server is granted to an agent" } : { status: 204 },
    });
    show(Servers);

    const sheet = await opened("github");
    await userEvent.click(within(sheet).getByRole("button", { name: /^delete$/i }));
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);

    const confirm = await screen.findByRole("dialog", { name: "Delete github?" });
    await userEvent.click(within(confirm).getByRole("button", { name: /^delete$/i }));
    expect(await within(confirm).findByRole("alert")).toHaveTextContent("granted to an agent");

    held = false;
    await userEvent.click(within(confirm).getByRole("button", { name: /^delete$/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(seen.filter((s) => s.method === "DELETE").map((s) => s.path)).toEqual([
      `/teams/${TEAM}/mcp-servers/m1`,
      `/teams/${TEAM}/mcp-servers/m1`,
    ]);
  });
});

describe("registering a server", () => {
  it("sends what the form describes, and shows the brain's refusal", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "POST /teams/{id}/mcp-servers": {
        status: 400,
        text: "a url server's address may not carry a password",
      },
    });
    show(Servers);

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "linear");
    await userEvent.type(screen.getByRole("textbox", { name: /^url$/i }), "https://mcp.linear.app");
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("may not carry a password");
    const post = seen.find((s) => s.method === "POST");
    expect(JSON.parse(String(post?.body))).toEqual({
      name: "linear",
      url: "https://mcp.linear.app",
    });
  });

  it("keeps a typed token in the secret store, names it, and asks the server for its tools", async () => {
    const linear = { ...github, id: "m9", name: "linear", url: "https://mcp.linear.app" };
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "PUT /teams/{id}/mcp-secrets/{name}": { status: 204 },
      "POST /teams/{id}/mcp-servers": { status: 201, body: linear },
      "POST /teams/{id}/mcp-servers/{sid}/discover": { body: { items: [] } },
    });
    show(Servers);

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "linear");
    await userEvent.type(screen.getByRole("textbox", { name: /^url$/i }), "https://mcp.linear.app");
    await userEvent.type(screen.getByLabelText(/^token$/i), "lin-secret-value");
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    await waitFor(() => expect(seen.some((s) => s.path.endsWith("/discover"))).toBe(true));
    const writes = seen.filter((s) => s.method !== "GET");
    expect(writes.map((s) => `${s.method} ${s.path}`)).toEqual([
      `PUT /teams/${TEAM}/mcp-secrets/linear`,
      `POST /teams/${TEAM}/mcp-servers`,
      `POST /teams/${TEAM}/mcp-servers/m9/discover`,
    ]);
    expect(JSON.parse(String(writes[0]?.body))).toEqual({ values: { token: "lin-secret-value" } });
    const server = String(writes[1]?.body);
    expect(JSON.parse(server)).toEqual({
      name: "linear",
      url: "https://mcp.linear.app",
      auth_secret_ref: `teams/${TEAM}/mcp/linear#token`,
    });
    expect(server).not.toContain("lin-secret-value");
  });

  it("registers nothing when the secret store does not take the token", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "PUT /teams/{id}/mcp-secrets/{name}": {
        status: 503,
        text: "the secret store is unavailable",
      },
    });
    show(Servers);

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "linear");
    await userEvent.type(screen.getByRole("textbox", { name: /^url$/i }), "https://mcp.linear.app");
    await userEvent.type(screen.getByLabelText(/^token$/i), "x");
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent("secret store is unavailable");
    expect(seen.some((s) => s.method === "POST")).toBe(false);
  });

  it("refuses a token for a server whose name cannot name a secret", async () => {
    const seen = brain({ ...team(["tool:write"]), ...servers });
    show(Servers);

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "linear.prod");
    await userEvent.type(screen.getByRole("textbox", { name: /^url$/i }), "https://mcp.linear.app");
    await userEvent.type(screen.getByLabelText(/^token$/i), "x");
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/letters, digits/);
    expect(seen.some((s) => s.method !== "GET")).toBe(false);
  });

  it("refuses an incomplete form without a request", async () => {
    const seen = brain({ ...team(["tool:write"]), ...servers });
    show(Servers);

    await userEvent.click(await screen.findByRole("button", { name: /register server/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "linear");
    await userEvent.click(screen.getByRole("button", { name: /^register$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/needs its URL/);
    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(false));
  });
});

describe("editing a server", () => {
  // What a PUT carries: the server as it stands, without what the brain owns.
  const {
    id,
    team_id,
    discovered_at,
    last_discovery_at,
    last_discovery_error,
    created_at,
    updated_at,
    ...definition
  } = github;

  const files = {
    ...github,
    id: "m2",
    name: "files",
    url: undefined,
    auth_secret_ref: undefined,
    command: ["npx", "-y", "/srv/My Files"],
    env: { TOKEN: `teams/${TEAM}/mcp/files#token` },
    bare: true,
  };

  async function editing(name: string) {
    const sheet = await opened(name);
    await userEvent.click(within(sheet).getByRole("button", { name: /^edit$/i }));
    return screen.findByRole("dialog", { name: /edit server/i });
  }

  it("opens a url server's form filled with what it has", async () => {
    brain({ ...team(["tool:write"]), ...servers });
    show(Servers);

    const dialog = await editing("github");
    expect(within(dialog).getByLabelText(/^name$/i)).toHaveValue("github");
    expect(within(dialog).getByRole("radio", { name: /url/i })).toBeChecked();
    expect(within(dialog).getByRole("textbox", { name: /^url$/i })).toHaveValue(
      "https://mcp.example.com",
    );
    expect(within(dialog).getByLabelText(/bearer token reference/i)).toHaveValue(
      `teams/${TEAM}/mcp/github#token`,
    );
    expect(within(dialog).getByRole("switch", { name: /bare/i })).not.toBeChecked();
    expect(within(dialog).getByRole("button", { name: /^save$/i })).toBeInTheDocument();
  });

  it("says a token is set without showing it, and keeps it unless a new one is typed", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "PUT /teams/{id}/mcp-servers/{sid}": { body: github },
    });
    show(Servers);

    const dialog = await editing("github");
    const token = within(dialog).getByLabelText(/^token$/i);
    expect(token).toHaveValue("");
    expect(within(dialog).getByText(/A token is set/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    expect(seen.some((s) => s.path.includes("/mcp-secrets/"))).toBe(false);
    const put = seen.find((s) => s.method === "PUT");
    expect(JSON.parse(String(put?.body)).auth_secret_ref).toBe(`teams/${TEAM}/mcp/github#token`);
  });

  it("opens a command server's form filled with its command and references", async () => {
    brain({
      ...team(["tool:write"]),
      ...servers,
      "GET /teams/{id}/mcp-servers": { body: { items: [files] } },
    });
    show(Servers);

    const dialog = await editing("files");
    expect(within(dialog).getByRole("radio", { name: /command/i })).toBeChecked();
    expect(within(dialog).getByRole("textbox", { name: /^command$/i })).toHaveValue(
      "npx\n-y\n/srv/My Files",
    );
    expect(within(dialog).getByLabelText(/^environment$/i)).toHaveValue(
      `TOKEN=teams/${TEAM}/mcp/files#token`,
    );
    expect(within(dialog).getByRole("switch", { name: /bare/i })).toBeChecked();
  });

  it("saves an untouched server as exactly what it was", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "PUT /teams/{id}/mcp-servers/{serverID}": { body: github },
    });
    show(Servers);

    const dialog = await editing("github");
    await userEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    const put = seen.find((s) => s.method === "PUT");
    expect(put?.path).toBe(`/teams/${TEAM}/mcp-servers/m1`);
    expect(JSON.parse(String(put?.body))).toEqual(definition);
  });

  it("saves an untouched command server as exactly what it was", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "GET /teams/{id}/mcp-servers": { body: { items: [files] } },
      "PUT /teams/{id}/mcp-servers/{serverID}": { body: files },
    });
    show(Servers);

    const dialog = await editing("files");
    await userEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    expect(JSON.parse(String(seen.find((s) => s.method === "PUT")?.body))).toEqual({
      name: "files",
      command: ["npx", "-y", "/srv/My Files"],
      env: { TOKEN: `teams/${TEAM}/mcp/files#token` },
      bare: true,
    });
  });

  it("sends a new URL with the rest unchanged, then reads the list again", async () => {
    const seen = brain({
      ...team(["tool:write"]),
      ...servers,
      "PUT /teams/{id}/mcp-servers/{serverID}": {
        body: { ...github, url: "https://mcp2.example.com" },
      },
    });
    show(Servers);

    const dialog = await editing("github");
    const url = within(dialog).getByRole("textbox", { name: /^url$/i });
    await userEvent.clear(url);
    await userEvent.type(url, "https://mcp2.example.com");
    const listsBefore = seen.filter(
      (s) => s.method === "GET" && s.path.endsWith("/mcp-servers"),
    ).length;
    await userEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: /edit server/i })).not.toBeInTheDocument(),
    );
    expect(JSON.parse(String(seen.find((s) => s.method === "PUT")?.body))).toEqual({
      ...definition,
      url: "https://mcp2.example.com",
    });
    await waitFor(() =>
      expect(
        seen.filter((s) => s.method === "GET" && s.path.endsWith("/mcp-servers")).length,
      ).toBeGreaterThan(listsBefore),
    );
  });

  it("shows the brain's refusal in the form", async () => {
    brain({
      ...team(["tool:write"]),
      ...servers,
      "PUT /teams/{id}/mcp-servers/{serverID}": {
        status: 400,
        text: "a url server's address may not carry a password",
      },
    });
    show(Servers);

    const dialog = await editing("github");
    await userEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("may not carry a password");
  });
});
