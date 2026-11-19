import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { ToolApproval } from "@/api/types";
import { brain, pageOf, STAMP, showAt, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.requests");
await pageOf(Route);
const { Route: SessionRoute } = await import("@/routes/ui.sessions.$sessionId");

function at(path = "/ui/requests") {
  return showAt(path, [
    ["/ui/requests", Route],
    ["/ui/sessions/$sessionId", SessionRoute],
  ]);
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

function request(id: string, extra: Partial<ToolApproval> = {}): ToolApproval {
  return {
    id,
    run_id: "r1",
    root_run_id: "r1",
    call_id: `c-${id}`,
    tool: "github__create_issue",
    class: "write",
    arguments: { title: id },
    summary: `title: ${id}`,
    code: id.toUpperCase().padEnd(4, "X"),
    status: "pending",
    requested_for: "me",
    require_other: false,
    decided_by: null,
    decided_via: null,
    scope: null,
    reason: null,
    expires_at: new Date(Date.now() + 30 * 60_000).toISOString(),
    decided_at: null,
    used_at: null,
    created_at: STAMP,
    ...extra,
  };
}

const LIST = "GET /teams/{id}/approvals";

it("lists what is waiting, each with its card and a way to its conversation", async () => {
  const seen = brain({
    ...team(["approval:read", "approval:self"]),
    "GET /whoami": { body: { id: "me" } },
    [LIST]: { body: { items: [request("a1"), request("b2")] } },
    "GET /teams/{id}/runs/{rid}": { body: { id: "r1", session_id: "s1" } },
  });
  at();
  const list = await screen.findByRole("list", { name: "Requests" });
  expect(await within(list).findAllByRole("button", { name: "Approve" })).toHaveLength(2);
  expect(screen.getByText("2 requests waiting for an answer.")).toBeInTheDocument();
  const link = (await within(list).findAllByRole("link", { name: /Open its conversation/ }))[0];
  expect(link).toHaveAttribute("href", expect.stringContaining("/ui/sessions/s1"));
  expect(seen.some((s) => s.path.includes("status=pending"))).toBe(true);
});

it("shows the status the address names, and changes it from the tabs", async () => {
  const seen = brain({
    ...team(["approval:read"]),
    [LIST]: () => ({ body: { items: [] } }),
  });
  const router = at("/ui/requests?status=denied");
  expect(await screen.findByText("No request has ended this way.")).toBeInTheDocument();
  expect(seen.some((s) => s.path.includes("status=denied"))).toBe(true);
  expect(screen.getByRole("tab", { name: "Declined" })).toHaveAttribute("aria-selected", "true");

  await userEvent.click(screen.getByRole("tab", { name: "Waiting" }));
  await waitFor(() => expect(router.state.location.search).not.toHaveProperty("status"));
  expect(await screen.findByText("Nothing is waiting")).toBeInTheDocument();
});

it("ignores a status it does not know", async () => {
  const seen = brain({ ...team(["approval:read"]), [LIST]: { body: { items: [] } } });
  at("/ui/requests?status=bogus");
  await screen.findByText("Nothing is waiting");
  expect(seen.some((s) => s.path.includes("status=pending"))).toBe(true);
});

it("names the permission it takes when the reader lacks it, and asks for nothing", async () => {
  const seen = brain({ ...team(["agent:write"]), [LIST]: { body: { items: [] } } });
  at();
  expect(await screen.findByText(/approval:read/)).toBeInTheDocument();
  expect(seen.some((s) => s.path.includes("/approvals"))).toBe(false);
});

it("shows the brain's refusal as it came", async () => {
  brain({ ...team(["approval:read"]), [LIST]: { status: 500, text: "the database is busy" } });
  at();
  expect(await screen.findByText(/the database is busy/)).toBeInTheDocument();
});

it("pages with the cursor", async () => {
  const seen = brain({
    ...team(["approval:read"]),
    "GET /whoami": { body: { id: "me" } },
    [LIST]: (s) =>
      s.path.includes("cursor=c2")
        ? { body: { items: [request("z9")] } }
        : { body: { items: [request("a1")], next_cursor: "c2" } },
  });
  at();
  await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
  await waitFor(() => expect(screen.getAllByText(/^title: /)).toHaveLength(2));
  expect(seen.some((s) => s.path.includes("cursor=c2"))).toBe(true);
});
