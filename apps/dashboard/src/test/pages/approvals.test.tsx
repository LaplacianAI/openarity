import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, showAt, TEAM, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.approvals");
await pageOf(Route);

/** The page under a router: which list it shows is in the URL. */
function at(path = "/ui/approvals") {
  return showAt(path, [["/ui/approvals", Route]]);
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const HOUR = 60 * 60 * 1000;
const ago = (ms: number) => new Date(Date.now() - ms).toISOString();

/** A waiting sender as the team's queue returns it, on a channel of its own. */
function waiting(ref: string, name: string, channel: string, waitedMs: number) {
  return {
    sender_ref: ref,
    sender_name: name,
    seen_count: 2,
    first_seen: ago(waitedMs),
    last_seen: ago(HOUR / 2),
    channel_id: `id-${channel}`,
    channel_name: channel,
    channel_provider: "slack",
  };
}

const QUEUE = "GET /teams/{id}/senders/pending";

// Oldest first, as the brain serves it, across two channels.
const queue = {
  [QUEUE]: {
    body: {
      items: [
        waiting("U01ABC", "Asha", "sales", 50 * HOUR),
        waiting("U02DEF", "Bala", "support", 3 * HOUR),
      ],
    },
  },
  "GET /teams/{id}/summary": {
    body: {
      runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
      sessions: 0,
      waiting_senders: 2,
      servers_failing: 0,
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

/** A Radix select scrolls its highlighted option into view, which jsdom lacks. */
function stubScroll() {
  Element.prototype.scrollIntoView = vi.fn();
  onTestFinished(() => {
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });
}

function rowOf(name: string): HTMLElement {
  return screen.getByText(name).closest("tr") as HTMLElement;
}

// The queue is a stranger's text, behind channel:read. Without it the page
// says which permission it takes rather than showing a bare refusal, and asks
// the brain for nothing it would refuse.
it("says what it takes to see the queue, and does not ask for it", async () => {
  const seen = brain({ ...team(["agent:write"]), ...queue });
  at();

  expect(await screen.findByText(/You can't see who is waiting/)).toBeInTheDocument();
  expect(screen.getByText("channel:read")).toBeInTheDocument();
  expect(seen.some((s) => s.path.includes("/senders/pending"))).toBe(false);
});

// One queue for every channel, read once: the brain orders it, the longest
// waiting first, and each row says where they wrote in and for how long.
it("lists everyone waiting on any channel, the longest waiting first", async () => {
  const seen = brain({ ...team(["channel:read"]), ...queue });
  at();

  await screen.findByText("Asha");
  const names = screen.getAllByText(/^(Asha|Bala)$/).map((e) => e.textContent);
  expect(names).toEqual(["Asha", "Bala"]);
  expect(within(rowOf("Asha")).getByText("sales · slack")).toBeInTheDocument();
  expect(within(rowOf("Asha")).getByText("2 days")).toBeInTheDocument();
  expect(within(rowOf("Bala")).getByText("3 hours")).toBeInTheDocument();

  expect(seen.filter((s) => s.path.includes("/senders/pending")).map((s) => s.path)).toEqual([
    `/teams/${TEAM}/senders/pending`,
  ]);
  expect(seen.some((s) => s.path.includes("/channels/"))).toBe(false);
});

// Six columns and a bind control do not fit a tablet: the rows stack below a desktop.
it("stacks its rows below a desktop's width", async () => {
  brain({ ...team(["channel:read", "sender:approve"]), ...queue });
  at();

  await screen.findByText("Asha");
  expect(screen.getByRole("table").closest("[data-stack]")).toHaveAttribute("data-stack", "lg");
});

it("says under its title how many are waiting, and for how long the longest has", async () => {
  brain({ ...team(["channel:read"]), ...queue });
  at();
  expect(
    await screen.findByText("2 people are waiting; the longest, for 2 days."),
  ).toBeInTheDocument();
});

// An approver holds sender:approve and not user:read. A sender is bound to a
// member of the team, which is all the brain accepts anyway, so the choices
// come from the team's members and the directory is never asked.
it("offers the team's members to bind a sender to, without the directory", async () => {
  stubScroll();
  const seen = brain({ ...team(["channel:read", "sender:approve"]), ...queue });
  at();

  await screen.findByText("Asha");
  within(rowOf("Asha")).getByRole("combobox", { name: "Bind this sender to" }).focus();
  await userEvent.keyboard("{ArrowDown}");
  const offered = (await screen.findAllByRole("option")).map((o) => o.textContent);

  expect(offered).toEqual(["bala@team.test", "chen"]);
  expect(seen.some((s) => s.path === "/users" || s.path.startsWith("/users?"))).toBe(false);
});

// Approving happens on the channel the sender wrote in on, so a queue of
// many channels must send each approval to that row's own channel, and the
// queue and the count are read again afterwards.
it("approves a sender on their own channel, and reads the queue and the count again", async () => {
  stubScroll();
  const seen = brain({
    ...team(["channel:read", "sender:approve"]),
    ...queue,
    "POST /teams/{id}/channels/{cid}/senders": { status: 201, body: {} },
  });
  at();

  await screen.findByText("Bala");
  const row = rowOf("Bala");
  within(row).getByRole("combobox", { name: "Bind this sender to" }).focus();
  await userEvent.keyboard("{ArrowDown}");
  await userEvent.click(await screen.findByRole("option", { name: "chen" }));
  await userEvent.click(within(row).getByRole("button", { name: "Approve" }));

  await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
  const post = seen.find((s) => s.method === "POST");
  expect(post?.path).toBe(`/teams/${TEAM}/channels/id-support/senders`);
  expect(JSON.parse(String(post?.body))).toEqual({ sender_ref: "U02DEF", user_id: "u-chen" });

  await waitFor(() => {
    const after = seen.slice(seen.indexOf(post as never));
    expect(after.some((s) => s.path.includes("/senders/pending"))).toBe(true);
    expect(after.some((s) => s.path.endsWith("/summary"))).toBe(true);
  });
});

// The name is the stranger's to choose, so a match is a hint, never a choice:
// otherwise a sender could name themselves after an admin to be bound to one.
it("lists the member whose name fits first and marks them, but chooses no one", async () => {
  stubScroll();
  const seen = brain({
    ...team(["channel:read", "sender:approve"]),
    ...queue,
    // The match last, so being listed first is the hint's doing.
    "GET /teams/{id}/members": {
      body: {
        items: [
          { user_id: "u-chen", subject: "chen", role: "approver" },
          { user_id: "u-bala", subject: "bala", email: "bala@team.test", role: "member" },
        ],
      },
    },
  });
  at();

  await screen.findByText("Bala");
  const row = rowOf("Bala");
  const bind = within(row).getByRole("combobox", { name: "Bind this sender to" });
  bind.focus();
  await userEvent.keyboard("{ArrowDown}");
  const offered = (await screen.findAllByRole("option")).map((o) => o.textContent);
  expect(offered).toEqual(["bala@team.test (matches their name)", "chen"]);
  await userEvent.keyboard("{Escape}");

  expect(bind).toHaveTextContent("Bind to a member…");
  await userEvent.click(within(row).getByRole("button", { name: "Approve" }));
  expect(within(row).getByRole("alert")).toHaveTextContent("Choose which member they are first.");
  expect(seen.some((s) => s.method === "POST")).toBe(false);
});

it("says to choose a member when Approve is pressed with none, and sends nothing", async () => {
  const seen = brain({ ...team(["channel:read", "sender:approve"]), ...queue });
  at();

  await screen.findByText("Asha");
  const row = rowOf("Asha");
  const approve = within(row).getByRole("button", { name: "Approve" });
  await waitFor(() => expect(approve).toBeEnabled());
  await userEvent.click(approve);

  expect(within(row).getByRole("alert")).toHaveTextContent("Choose which member they are first.");
  expect(
    within(row).getByRole("combobox", { name: "Bind this sender to" }),
  ).toHaveAccessibleDescription("Choose which member they are first.");
  expect(seen.some((s) => s.method === "POST")).toBe(false);
});

// A role may read the queue without approving: the shipped ones pair the two,
// an operator's own need not. The queue is shown, the controls are not.
it("shows the queue without the controls to someone who may not approve", async () => {
  const seen = brain({ ...team(["channel:read"]), ...queue });
  at();

  expect(await screen.findByText("Asha")).toBeInTheDocument();
  await waitFor(() =>
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument(),
  );
  expect(screen.queryByRole("combobox", { name: "Bind this sender to" })).not.toBeInTheDocument();
  expect(seen.some((s) => s.path.endsWith("/members"))).toBe(false);
});

// A name is whatever the stranger typed into their profile.
it("shows a sender's name as text, never as markup", async () => {
  brain({
    ...team(["channel:read"]),
    ...queue,
    [QUEUE]: { body: { items: [waiting("U9", "<img src=x onerror=alert(1)>", "sales", HOUR)] } },
  });
  at();
  expect(await screen.findByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
  expect(document.querySelector("tbody img")).toBeNull();
});

it("says no one is waiting, in the queue's own frame", async () => {
  brain({ ...team(["channel:read"]), ...queue, [QUEUE]: { body: { items: [] } } });
  at();
  expect(await screen.findByText("No one is waiting")).toBeInTheDocument();
});

it("says why the queue could not be read, and reads it again on request", async () => {
  let fail = true;
  brain({
    ...team(["channel:read"]),
    ...queue,
    [QUEUE]: () => (fail ? { status: 500, text: "the store is down" } : { body: { items: [] } }),
  });
  at();

  expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
  fail = false;
  await userEvent.click(screen.getByRole("button", { name: "Try again" }));
  expect(await screen.findByText("No one is waiting")).toBeInTheDocument();
});

/* ---------------------------------------------------------------- let in */

const letIn = {
  "GET /teams/{id}/channels": {
    body: {
      items: [
        { id: "id-sales", name: "sales", provider: "custom" },
        { id: "id-ops", name: "ops", provider: "custom" },
      ],
    },
  },
  "GET /teams/{id}/channels/{cid}/senders": (r: { path: string }) =>
    r.path.includes("id-sales")
      ? {
          body: {
            items: [
              { sender_ref: "U09XYZ", user_id: "u-bala", created_at: ago(2 * HOUR) },
              { sender_ref: "U08OLD", user_id: "u-gone", created_at: ago(9 * HOUR) },
            ],
          },
        }
      : { body: { items: [] } },
};

it("opens on who is let in when the URL says so, channel by channel", async () => {
  brain({ ...team(["channel:read", "sender:approve"]), ...queue, ...letIn });
  at("/ui/approvals?show=let-in");

  expect(await screen.findByRole("tab", { name: "Let in" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  const sales = await screen.findByRole("region", { name: "sales" });
  const bala = (await within(sales).findByText("U09XYZ")).closest("tr") as HTMLElement;
  expect(await within(bala).findByText("bala@team.test")).toBeInTheDocument();
  // Someone bound to a person since removed from the team says so.
  const old = within(sales).getByText("U08OLD").closest("tr") as HTMLElement;
  expect(within(old).getByText("Someone no longer in the team")).toBeInTheDocument();
  const ops = screen.getByRole("region", { name: "ops" });
  expect(
    await within(ops).findByText("No one has been let in on this channel."),
  ).toBeInTheDocument();
});

it("keeps the chosen list in the URL", async () => {
  brain({ ...team(["channel:read", "sender:approve"]), ...queue, ...letIn });
  const router = at();
  await userEvent.click(await screen.findByRole("tab", { name: "Let in" }));
  await waitFor(() => expect(router.state.location.search).toEqual({ show: "let-in" }));
  await userEvent.click(screen.getByRole("tab", { name: "Waiting" }));
  await waitFor(() => expect(router.state.location.search).toEqual({}));
});

it("stops letting someone in once it is confirmed, on their own channel", async () => {
  const seen = brain({
    ...team(["channel:read", "sender:approve"]),
    ...queue,
    ...letIn,
    "DELETE /teams/{id}/channels/{cid}/senders": { status: 204 },
  });
  at("/ui/approvals?show=let-in");

  await userEvent.click(await screen.findByRole("button", { name: "Remove U09XYZ" }));
  expect(seen.some((s) => s.method === "DELETE")).toBe(false);
  const confirm = await screen.findByRole("dialog", {
    name: "Stop letting U09XYZ in on sales?",
  });
  await userEvent.click(within(confirm).getByRole("button", { name: "Remove" }));

  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  expect(seen.filter((s) => s.method === "DELETE").map((s) => s.path)).toEqual([
    `/teams/${TEAM}/channels/id-sales/senders?ref=U09XYZ`,
  ]);
});

it("shows the brain's refusal in the confirm, and stays", async () => {
  brain({
    ...team(["channel:read", "sender:approve"]),
    ...queue,
    ...letIn,
    "DELETE /teams/{id}/channels/{cid}/senders": {
      status: 409,
      text: "the sender was bound to someone else meanwhile",
    },
  });
  at("/ui/approvals?show=let-in");

  await userEvent.click(await screen.findByRole("button", { name: "Remove U09XYZ" }));
  const confirm = await screen.findByRole("dialog");
  await userEvent.click(within(confirm).getByRole("button", { name: "Remove" }));
  expect(await within(confirm).findByRole("alert")).toHaveTextContent(
    "the sender was bound to someone else meanwhile",
  );
});

it("lists who is let in without Remove to someone who may not approve", async () => {
  brain({ ...team(["channel:read"]), ...queue, ...letIn });
  at("/ui/approvals?show=let-in");
  await screen.findByText("U09XYZ");
  expect(screen.queryByRole("button", { name: /^remove/i })).toBeNull();
});
