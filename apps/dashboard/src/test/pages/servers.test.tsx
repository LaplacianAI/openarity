import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, show, TEAM, team } from "@/test/brain";

const { Route, serverRequest } = await import("@/routes/ui.servers.index");
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
  created_at: STAMP,
  updated_at: STAMP,
};

const servers = { "GET /teams/{id}/mcp-servers": { body: { items: [github] } } };

const base = {
  name: "github",
  transport: "url" as const,
  url: "",
  authRef: "",
  command: "",
  env: "",
  bare: false,
};

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

it("offers no write action without tool:write", async () => {
  brain({ ...team(["skill:write", "agent:write"]), ...servers });
  show(Servers);

  await screen.findByText("github");
  expect(await screen.findByText("tool:write")).toBeInTheDocument();
  for (const name of [/register server/i, /^discover$/i, /^edit$/i, /^delete$/i]) {
    expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  }
});

it("offers register, discover, edit and delete with tool:write", async () => {
  brain({ ...team(["tool:write"]), ...servers });
  show(Servers);

  for (const name of [/register server/i, /^discover$/i, /^edit$/i, /^delete$/i]) {
    expect(await screen.findByRole("button", { name })).toBeInTheDocument();
  }
});

// Discovery reaches a URL; the brain does not start a command to ask it.
it("offers discovery only for a url server", async () => {
  brain({
    ...team(["tool:write"]),
    "GET /teams/{id}/mcp-servers": {
      body: {
        items: [{ ...github, id: "m2", name: "files", url: undefined, command: ["npx", "srv"] }],
      },
    },
  });
  show(Servers);

  await screen.findByText("files");
  expect(screen.queryByRole("button", { name: /^discover$/i })).not.toBeInTheDocument();
});

// A tool's name and description are the server's words. They are shown as
// text, so markup in them is inert.
it("shows a server's tools as text", async () => {
  brain({
    ...team([]),
    ...servers,
    "GET /teams/{id}/mcp-servers/{serverID}/tools": {
      body: {
        items: [
          { name: "search_code", description: '<img src=x onerror="alert(1)">', input_schema: {} },
        ],
      },
    },
  });
  const { container } = show(Servers);

  await userEvent.click(await screen.findByRole("button", { name: /github/ }));
  expect(await screen.findByText("search_code")).toBeInTheDocument();
  expect(screen.getByText('<img src=x onerror="alert(1)">')).toBeInTheDocument();
  expect(container.querySelector("img")).toBeNull();
  expect(screen.getByText(`teams/${TEAM}/mcp/github#token`)).toBeInTheDocument();
});

it("registers what the form describes, and shows the brain's refusal", async () => {
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
  expect(JSON.parse(String(post?.body))).toEqual({ name: "linear", url: "https://mcp.linear.app" });
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

describe("editing a server", () => {
  // What a PUT carries: the server as it stands, without what the brain owns.
  const { id, team_id, discovered_at, created_at, updated_at, ...definition } = github;

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
    const row = (await screen.findByText(name)).closest("li") as HTMLElement;
    await userEvent.click(within(row).getByRole("button", { name: /^edit$/i }));
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
    expect(within(dialog).getByRole("checkbox", { name: /bare/i })).not.toBeChecked();
    expect(within(dialog).getByRole("button", { name: /^save$/i })).toBeInTheDocument();
  });

  it("opens a command server's form filled with its command and references", async () => {
    brain({
      ...team(["tool:write"]),
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
    expect(within(dialog).getByRole("checkbox", { name: /bare/i })).toBeChecked();
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
      "GET /teams/{id}/mcp-servers": { body: { items: [files] } },
      "PUT /teams/{id}/mcp-servers/{serverID}": { body: files },
    });
    show(Servers);

    const dialog = await editing("files");
    await userEvent.click(within(dialog).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(seen.some((s) => s.method === "PUT")).toBe(true));
    const put = seen.find((s) => s.method === "PUT");
    expect(JSON.parse(String(put?.body))).toEqual({
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
    const put = seen.find((s) => s.method === "PUT");
    expect(JSON.parse(String(put?.body))).toEqual({
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
