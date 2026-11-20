import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { ApprovalRule, TeamSettings } from "@/api/types";
import { brain, choose, pageOf, showAt, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.settings");
await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const SETTINGS: TeamSettings = {
  model: null,
  search: { provider: "none", url: null, key_set: false },
  sandbox: { provider: "none", url: null, key_set: false },
  learning: false,
  compaction_messages: 100,
  hub: null,
  updated_at: null,
};

const RULE: ApprovalRule = {
  id: "ru1",
  target_kind: "server",
  target: "github",
  mode: "ask",
  require_other: true,
  created_at: "2026-10-01T00:00:00Z",
};

const ROUTES = {
  "GET /teams/{id}/settings": { body: SETTINGS },
  "GET /teams/{id}/approval-settings": { body: { mode: "writes", ttl_minutes: 60 } },
  "GET /teams/{id}/approval-rules": { body: { items: [RULE] } },
  "GET /teams/{id}/mcp-servers": {
    body: {
      items: [
        { id: "srv-gh", name: "github" },
        { id: "srv-sh", name: "shell" },
        { id: "srv-empty", name: "quiet" },
      ],
    },
  },
  "GET /teams/{id}/mcp-servers/srv-sh/tools": {
    body: { items: [{ name: "run", description: "", input_schema: {} }] },
  },
  "GET /teams/{id}/mcp-servers/srv-empty/tools": { body: { items: [] } },
};

/** Chooses a server in the rule form's picker, and closes it. */
async function chooseServer(section: HTMLElement, name: string) {
  await userEvent.click(within(section).getByRole("button", { name: "Server" }));
  await userEvent.click(await screen.findByRole("option", { name }));
  await userEvent.keyboard("{Escape}");
}

function open() {
  return showAt("/ui/settings", [["/ui/settings", Route]]);
}

const approvals = () => screen.findByRole("region", { name: "Tool approvals" });

it("saves the mode and the wait through the approval route, apart from the settings", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "PUT /teams/{id}/approval-settings": { body: { mode: "destructive", ttl_minutes: 30 } },
  });
  open();
  const section = await approvals();
  expect(
    within(section).getByRole("radio", { name: "Writes and destructive calls" }),
  ).toBeChecked();
  expect(within(section).queryByRole("button", { name: /^save$/i })).toBeNull();

  await userEvent.click(within(section).getByRole("radio", { name: "Destructive calls only" }));
  expect(within(section).getByRole("radio", { name: "Destructive calls only" })).toBeChecked();
  const ttl = within(section).getByLabelText("Minutes a request waits");
  await userEvent.clear(ttl);
  await userEvent.type(ttl, "30");
  expect(within(section).getByText("minutes")).toBeInTheDocument();
  await userEvent.clear(ttl);
  await userEvent.type(ttl, "90");
  expect(within(section).getByText("minutes · 1 hour 30 minutes")).toBeInTheDocument();
  await userEvent.clear(ttl);
  await userEvent.type(ttl, "30");
  await userEvent.click(within(section).getByRole("button", { name: /^save$/i }));

  expect(await within(section).findByText("Saved")).toBeInTheDocument();
  const put = seen.find((s) => s.method === "PUT");
  expect(put?.path).toMatch(/\/approval-settings$/);
  expect(JSON.parse(String(put?.body))).toEqual({ mode: "destructive", ttl_minutes: 30 });
  expect(seen.some((s) => s.path.endsWith("/settings") && s.method === "PUT")).toBe(false);
});

it("refuses a wait outside the brain's bounds before asking", async () => {
  const seen = brain({ ...team(["team:settings"]), ...ROUTES });
  open();
  const section = await approvals();
  const ttl = within(section).getByLabelText("Minutes a request waits");
  await userEvent.clear(ttl);
  await userEvent.type(ttl, "2");
  await userEvent.click(within(section).getByRole("button", { name: /^save$/i }));
  expect(await within(section).findByRole("alert")).toHaveTextContent("between 5 and 1440");
  expect(seen.some((s) => s.method === "PUT")).toBe(false);
});

it("lists the rules, adds one and removes one", async () => {
  let rules = [RULE];
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "GET /teams/{id}/approval-rules": () => ({ body: { items: rules } }),
    "POST /teams/{id}/approval-rules": () => {
      rules = [
        ...rules,
        {
          ...RULE,
          id: "ru2",
          target_kind: "tool",
          target: "shell__run",
          mode: "deny",
          require_other: false,
        },
      ];
      return { status: 201, body: rules[1] };
    },
    "DELETE /teams/{id}/approval-rules/{rid}": () => {
      rules = [];
      return { status: 204 };
    },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  const list = await within(section).findByRole("list", { name: "Tool approval rules" });
  expect(within(list).getByText("github")).toBeInTheDocument();
  expect(within(list).getByText("Every tool of this server")).toBeInTheDocument();
  expect(within(list).getByText("Always ask")).toBeInTheDocument();
  expect(within(list).getByText("Someone else answers")).toBeInTheDocument();

  await chooseServer(section, "shell");
  const tool = within(section).getByLabelText("Tool");
  await waitFor(() => expect(tool).toBeEnabled());
  await choose(tool, "run");
  await userEvent.click(within(section).getByRole("radio", { name: "Never" }));
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  expect(await within(section).findByText("on shell")).toBeInTheDocument();
  expect(within(section).getByText("Never allow")).toBeInTheDocument();
  const post = seen.find((s) => s.method === "POST");
  expect(JSON.parse(String(post?.body))).toEqual({
    target_kind: "tool",
    target: "shell__run",
    mode: "deny",
    require_other: false,
  });

  await userEvent.click(
    within(section).getByRole("button", { name: "Remove the rule for github" }),
  );
  await waitFor(() => expect(seen.some((s) => s.method === "DELETE")).toBe(true));
  expect(seen.find((s) => s.method === "DELETE")?.path).toMatch(/approval-rules\/ru1$/);
});

it("shows the brain's words when a target already has a rule", async () => {
  brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "POST /teams/{id}/approval-rules": {
      status: 409,
      text: "that target already has a rule; remove it first",
    },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await userEvent.click(
    await within(section).findByRole("radio", { name: "Every tool of a server" }),
  );
  await chooseServer(section, "github");
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  expect(await within(section).findByRole("alert")).toHaveTextContent("already has a rule");
});

it("names a built-in tool by its own name", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "POST /teams/{id}/approval-rules": { body: { ...RULE, id: "ru2" } },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await chooseServer(section, "Built-in tools");
  await choose(within(section).getByLabelText("Tool"), "web_fetch");
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  expect(JSON.parse(String(seen.find((s) => s.method === "POST")?.body))).toMatchObject({
    target_kind: "tool",
    target: "web_fetch",
  });
});

it("offers no tool from a server that has none, and adds nothing unchosen", async () => {
  const seen = brain({ ...team(["team:settings"]), ...ROUTES });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  expect(within(section).getByLabelText("Tool")).toBeDisabled();
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  expect(await within(section).findByRole("alert")).toHaveTextContent(
    "Choose the tool the rule is for.",
  );

  await chooseServer(section, "quiet");
  expect(
    await within(section).findByText("This server has said it has no tools."),
  ).toBeInTheDocument();
  expect(within(section).getByLabelText("Tool")).toBeDisabled();
  expect(seen.some((s) => s.method === "POST")).toBe(false);
});

it("starts again when what the rule applies to changes", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "POST /teams/{id}/approval-rules": { body: { ...RULE, id: "ru3" } },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await chooseServer(section, "shell");
  await waitFor(() => expect(within(section).getByLabelText("Tool")).toBeEnabled());
  await choose(within(section).getByLabelText("Tool"), "run");
  await userEvent.click(within(section).getByRole("radio", { name: "Every tool of a server" }));
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  expect(await within(section).findByRole("alert")).toHaveTextContent(
    "Choose the server the rule is for.",
  );
  expect(seen.some((s) => s.method === "POST")).toBe(false);
});

it("is read-only without team:settings: the rules are listed, nothing is offered", async () => {
  brain({ ...team(["approval:read"]), ...ROUTES });
  open();
  const section = await approvals();
  expect(within(section).getByRole("radio", { name: "Every call" })).toBeDisabled();
  expect(within(section).queryByRole("button", { name: /^save$/i })).toBeNull();
  const rules = await screen.findByRole("region", { name: "Tool approval rules" });
  expect(await within(rules).findByText("github")).toBeInTheDocument();
  expect(within(rules).queryByRole("button", { name: "Add rule" })).toBeNull();
  expect(within(rules).queryByRole("button", { name: /Remove the rule/ })).toBeNull();
});

it("finds the built-in tools by a tool's name", async () => {
  brain({
    ...team(["team:settings"]),
    ...ROUTES,
    // As the brain does: the servers whose name holds what was typed.
    "GET /teams/{id}/mcp-servers": (seen) => {
      const q = new URL(seen.path, "http://brain.test").searchParams.get("q") ?? "";
      const items = ROUTES["GET /teams/{id}/mcp-servers"].body.items;
      return { body: { items: items.filter((s) => s.name.includes(q)) } };
    },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await userEvent.click(within(section).getByRole("button", { name: "Server" }));
  await userEvent.type(await screen.findByRole("combobox", { name: "Search server" }), "fetch");
  await waitFor(() => expect(screen.queryByRole("option", { name: "github" })).toBeNull());
  expect(screen.getByRole("option", { name: "Built-in tools" })).toBeInTheDocument();
});

it("tries a server's tools again when they could not be read", async () => {
  let failed = false;
  brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "GET /teams/{id}/mcp-servers/srv-sh/tools": () => {
      if (!failed) {
        failed = true;
        return { status: 500, body: { error: "the server went away" } };
      }
      return { body: { items: [{ name: "run", description: "", input_schema: {} }] } };
    },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await chooseServer(section, "shell");
  await userEvent.click(await within(section).findByRole("button", { name: "Try again" }));
  const tool = within(section).getByLabelText("Tool");
  await waitFor(() => expect(tool).toBeEnabled());
  await choose(tool, "run");
  expect(tool).toHaveTextContent("run");
});

it("names a server by its name in a server rule", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "POST /teams/{id}/approval-rules": { body: { ...RULE, id: "ru4" } },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await userEvent.click(within(section).getByRole("radio", { name: "Every tool of a server" }));
  await chooseServer(section, "shell");
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  expect(JSON.parse(String(seen.find((s) => s.method === "POST")?.body))).toMatchObject({
    target_kind: "server",
    target: "shell",
  });
});

it("names a browser tool, and says what a rule on the open covers", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "POST /teams/{id}/approval-rules": { body: { ...RULE, id: "ru5" } },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await chooseServer(section, "Built-in tools");
  await choose(within(section).getByLabelText("Tool"), "browser_open");
  expect(within(section).getByText(/asked when a page opens/i)).toBeInTheDocument();
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  expect(JSON.parse(String(seen.find((s) => s.method === "POST")?.body))).toMatchObject({
    target_kind: "tool",
    target: "browser_open",
    mode: "ask",
  });
});

it("offers someone else's answer only for a rule that asks", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "POST /teams/{id}/approval-rules": { body: { ...RULE, id: "ru6" } },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });

  await userEvent.click(within(section).getByRole("switch", { name: "Someone else must answer" }));
  await userEvent.click(within(section).getByRole("radio", { name: "Allow" }));
  expect(within(section).queryByRole("switch", { name: "Someone else must answer" })).toBeNull();
  await userEvent.click(within(section).getByRole("radio", { name: "Every tool of a server" }));
  await chooseServer(section, "shell");
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  expect(JSON.parse(String(seen.find((s) => s.method === "POST")?.body))).toMatchObject({
    target: "shell",
    mode: "allow",
    require_other: false,
  });
});

it("says the rules do nothing while approvals are off", async () => {
  brain({
    ...team(["team:settings"]),
    ...ROUTES,
    "GET /teams/{id}/approval-settings": { body: { mode: "off", ttl_minutes: 60 } },
  });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  expect(
    await within(section).findByText(/approvals are off, so no rule applies/i),
  ).toBeInTheDocument();
});

it("says nothing of the mode while approvals are on", async () => {
  brain({ ...team(["team:settings"]), ...ROUTES });
  open();
  const section = await screen.findByRole("region", { name: "Tool approval rules" });
  await within(section).findByRole("list", { name: "Tool approval rules" });
  expect(within(section).queryByText(/approvals are off/i)).toBeNull();
});
