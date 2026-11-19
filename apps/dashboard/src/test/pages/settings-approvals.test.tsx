import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { ApprovalRule, TeamSettings } from "@/api/types";
import { brain, pageOf, showAt, team } from "@/test/brain";

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
};

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
  expect(within(section).getByLabelText("Ask before")).toHaveValue("writes");
  expect(within(section).queryByRole("button", { name: /^save$/i })).toBeNull();

  await userEvent.selectOptions(within(section).getByLabelText("Ask before"), "destructive");
  expect(within(section).getByText(/only before a call that destroys/i)).toBeInTheDocument();
  const ttl = within(section).getByLabelText("Minutes a request waits");
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
  const table = await within(section).findByRole("table", { name: "Tool approval rules" });
  expect(within(table).getByText("github")).toBeInTheDocument();
  expect(within(table).getByText("Always ask")).toBeInTheDocument();

  await userEvent.type(within(section).getByLabelText("Target"), "shell__run");
  await userEvent.selectOptions(within(section).getByLabelText("Answer"), "deny");
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  expect(await within(section).findByText("shell__run")).toBeInTheDocument();
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
  await userEvent.type(await within(section).findByLabelText("Target"), "github");
  await userEvent.click(within(section).getByRole("button", { name: "Add rule" }));
  expect(await within(section).findByRole("alert")).toHaveTextContent("already has a rule");
});

it("is read-only without team:settings: the rules are listed, nothing is offered", async () => {
  brain({ ...team(["approval:read"]), ...ROUTES });
  open();
  const section = await approvals();
  expect(within(section).getByLabelText("Ask before")).toBeDisabled();
  expect(within(section).queryByRole("button", { name: /^save$/i })).toBeNull();
  const rules = await screen.findByRole("region", { name: "Tool approval rules" });
  expect(await within(rules).findByText("github")).toBeInTheDocument();
  expect(within(rules).queryByRole("button", { name: "Add rule" })).toBeNull();
  expect(within(rules).queryByRole("button", { name: /Remove the rule/ })).toBeNull();
});
