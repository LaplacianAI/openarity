import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, show, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.approvals");
const Approvals = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const queue = {
  "GET /teams/{id}/channels": {
    body: { items: [{ id: "c1", name: "support", provider: "slack" }] },
  },
  "GET /teams/{id}/channels/{cid}/senders/pending": {
    body: {
      items: [
        {
          sender_ref: "U01ABC",
          sender_name: "Asha",
          seen_count: 2,
          first_seen: "2026-11-01T00:00:00Z",
          last_seen: "2026-11-01T00:05:00Z",
        },
      ],
    },
  },
  "GET /teams/{id}/members": {
    body: {
      items: [
        { user_id: "u-bala", subject: "bala", email: "bala@team.test", role: "member" },
        { user_id: "u-chen", subject: "chen", role: "approver" },
      ],
    },
  },
};

// The queue is a stranger's text, behind channel:read. Without it the page
// says which permission it takes rather than showing a bare refusal, and asks
// the brain for nothing it would refuse.
it("says what it takes to see the queue, and does not ask for it", async () => {
  const seen = brain({ ...team(["agent:write"]), ...queue });
  show(Approvals);

  expect(await screen.findByText(/You can't see who is waiting/)).toBeInTheDocument();
  expect(screen.getByText("channel:read")).toBeInTheDocument();
  expect(seen.some((s) => s.path.includes("/senders/pending"))).toBe(false);
});

// An approver holds sender:approve and not user:read. A sender is bound to a
// member of the team, which is all the brain accepts anyway, so the choices
// come from the team's members and the directory is never asked.
it("offers the team's members to bind a sender to, without the directory", async () => {
  const seen = brain({ ...team(["channel:read", "sender:approve"]), ...queue });
  show(Approvals);

  // A Radix select opens from the keyboard in jsdom, and scrolls its
  // highlighted option into view, which jsdom lacks.
  Element.prototype.scrollIntoView = vi.fn();
  onTestFinished(() => {
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });
  (await screen.findByRole("combobox", { name: "Bind this sender to" })).focus();
  await userEvent.keyboard("{ArrowDown}");
  const offered = (await screen.findAllByRole("option")).map((o) => o.textContent);

  expect(offered).toEqual(["bala@team.test", "chen"]);
  expect(seen.some((s) => s.path === "/users" || s.path.startsWith("/users?"))).toBe(false);
});

// A role may read the queue without approving: the shipped ones pair the two,
// an operator's own need not. The queue is shown, the controls are not.
it("shows the queue without the controls to someone who may not approve", async () => {
  brain({ ...team(["channel:read"]), ...queue });
  show(Approvals);

  expect(await screen.findByText("Asha")).toBeInTheDocument();
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument(),
  );
  expect(screen.queryByRole("combobox", { name: "Bind this sender to" })).not.toBeInTheDocument();
});
