import { screen, waitFor } from "@testing-library/react";
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
  for (const name of [/register server/i, /^discover$/i, /^delete$/i]) {
    expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  }
});

it("offers register, discover and delete with tool:write", async () => {
  brain({ ...team(["tool:write"]), ...servers });
  show(Servers);

  for (const name of [/register server/i, /^discover$/i, /^delete$/i]) {
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
