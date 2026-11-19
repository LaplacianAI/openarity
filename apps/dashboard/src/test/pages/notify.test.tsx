import { act, cleanup, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { ToolApproval } from "@/api/types";
import { brain, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: UiRoute } = await import("@/routes/ui");

// What the browser was asked to show, and the permission it holds.
type Shown = {
  title: string;
  body?: string;
  tag?: string;
  renotify?: boolean;
  onclick: (() => void) | null;
};
let shown: Shown[] = [];
let permission: NotificationPermission = "granted";
let asked = 0;

class FakeNotification {
  static get permission() {
    return permission;
  }
  static async requestPermission() {
    asked++;
    permission = "granted";
    return permission;
  }
  onclick: (() => void) | null = null;
  constructor(title: string, options?: NotificationOptions) {
    const s: Shown = {
      title,
      body: options?.body,
      tag: options?.tag,
      renotify: (options as { renotify?: boolean } | undefined)?.renotify,
      onclick: null,
    };
    shown.push(s);
    Object.defineProperty(this, "onclick", {
      set: (fn) => {
        s.onclick = fn;
      },
    });
  }
  close() {}
}

function request(id: string, extra: Partial<ToolApproval> = {}): ToolApproval {
  return {
    id,
    run_id: "r1",
    root_run_id: "r1",
    call_id: `c-${id}`,
    tool: "github__create_issue",
    class: "write",
    arguments: {},
    summary: `title: ${id}`,
    code: "ABCD",
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

// The brain the shell reads: what is pending and how many wait to be let in,
// each changed between polls by the test.
let pending: ToolApproval[] = [];
let more = false;
let waiting = 0;
// What a test holds back: the viewer, or their permissions, answered late.
let me: Promise<void> = Promise.resolve();
let perms: Promise<void> = Promise.resolve();

function later(): [Promise<void>, () => void] {
  let release = () => {};
  const held = new Promise<void>((r) => {
    release = r;
  });
  return [held, release];
}

function shell(actions = ["approval:read", "approval:self", "channel:read"]) {
  const t = team(actions);
  const permissions = t["GET /teams/{id}/permissions"] as { body: unknown };
  brain({
    ...t,
    "GET /teams/{id}/permissions": async () => {
      await perms;
      return permissions;
    },
    "GET /whoami": async () => {
      await me;
      return { body: { id: "me", subject: "ann", email: "ann@example.com" } };
    },
    "GET /teams/{id}/approvals": () => ({
      body: { items: pending, next_cursor: more ? "next" : null },
    }),
    "GET /teams/{id}/summary": () => ({
      body: {
        runs_24h: { queued: 0, running: 0, succeeded: 0, failed: 0, cancelled: 0 },
        sessions: 0,
        waiting_senders: waiting,
        servers_failing: 0,
      },
    }),
  });
  return showAt("/ui", [["/ui", UiRoute]]);
}

/** One poll: the next look, and what it fetches settled. */
async function poll() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(30_000);
  });
}

const realMatchMedia = window.matchMedia;

beforeEach(() => {
  // A wide screen: the sidebar, with the account menu at its foot.
  window.matchMedia = ((query: string) => ({
    matches: true,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
  vi.useFakeTimers({ shouldAdvanceTime: true });
  vi.stubGlobal("Notification", FakeNotification);
  vi.spyOn(document, "hasFocus").mockReturnValue(false);
  shown = [];
  permission = "granted";
  asked = 0;
  pending = [];
  more = false;
  waiting = 0;
  me = Promise.resolve();
  perms = Promise.resolve();
  window.localStorage.setItem("oa.notifications", "on");
});

afterEach(() => {
  window.matchMedia = realMatchMedia;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.localStorage.clear();
  document.title = "openarity";
});

describe("notifications of approvals", () => {
  it("tells of a new request the viewer may answer, once, and opens Tool approvals", async () => {
    pending = [request("old")];
    const router = shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();
    expect(shown).toEqual([]);

    pending = [request("old"), request("new")];
    await poll();
    await waitFor(() => expect(shown).toHaveLength(1));
    expect(shown[0]).toMatchObject({
      title: "An agent asks before create_issue on github",
      body: "Open Tool approvals to answer.",
      tag: "approval:new",
    });

    await poll();
    expect(shown).toHaveLength(1);

    act(() => shown[0]?.onclick?.());
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/requests"));
  });

  it("says nothing of a request someone else must answer", async () => {
    shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();

    pending = [request("theirs", { requested_for: "sam" })];
    await poll();
    await poll();
    expect(shown).toEqual([]);
  });

  it("tells when more people wait to be let in", async () => {
    shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();

    waiting = 2;
    await poll();
    await waitFor(() => expect(shown).toHaveLength(1));
    expect(shown[0]).toMatchObject({
      title: "Waiting to be let in",
      tag: `senders:${TEAM}`,
      renotify: true,
    });
  });

  it("still tells of more people waiting when the viewer's permissions arrive late", async () => {
    const [held, release] = later();
    perms = held;
    waiting = 2;
    shell();
    await poll();
    await poll();

    await act(async () => release());
    await poll();
    waiting = 3;
    await poll();
    await waitFor(() => expect(shown).toHaveLength(1));
    expect(shown[0]).toMatchObject({ title: "Waiting to be let in" });
  });

  it("waits to know the viewer before telling of their requests, and does not flood", async () => {
    const [held, release] = later();
    me = held;
    pending = ["a", "b", "c", "d", "e"].map((id) => request(id));
    shell();
    await poll();
    await poll();
    expect(shown).toEqual([]);

    await act(async () => release());
    await poll();
    await poll();
    expect(shown).toEqual([]);

    pending = [...pending, request("f")];
    await poll();
    await waitFor(() => expect(shown).toHaveLength(1));
    expect(shown[0]).toMatchObject({ tag: "approval:f" });
  });

  it("tells of a burst of new requests as one, and of a few one by one", async () => {
    shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();

    pending = ["a", "b", "c", "d"].map((id) => request(id));
    await poll();
    await waitFor(() => expect(shown).toHaveLength(1));
    expect(shown[0]).toMatchObject({
      title: "Agents ask before acting",
      body: "4 new requests wait for your answer.",
      tag: `approvals:${TEAM}`,
      renotify: true,
    });

    pending = [...pending, ...["e", "f", "g"].map((id) => request(id))];
    await poll();
    await waitFor(() => expect(shown).toHaveLength(4));
    expect(shown.slice(1).map((n) => n.tag)).toEqual(["approval:e", "approval:f", "approval:g"]);
  });

  it("says nothing of people who were already waiting, or of a count that stands", async () => {
    waiting = 2;
    shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();
    await poll();
    expect(shown).toEqual([]);

    waiting = 1;
    await poll();
    expect(shown).toEqual([]);
  });

  it("is off where the browser no longer allows it, whatever was chosen", async () => {
    permission = "default";
    shell();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await screen.findByRole("navigation", { name: "Main" });
    await poll();

    pending = [request("new")];
    waiting = 1;
    await poll();
    await poll();
    expect(shown).toEqual([]);

    await user.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    expect(
      await screen.findByRole("menuitem", { name: "Notify me of approvals" }),
    ).toBeInTheDocument();
  });

  it("says nothing while the viewer is looking at the dashboard", async () => {
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();

    pending = [request("new")];
    waiting = 1;
    await poll();
    await poll();
    expect(shown).toEqual([]);
  });

  it("says nothing when they are off", async () => {
    window.localStorage.removeItem("oa.notifications");
    shell();
    await screen.findByRole("navigation", { name: "Main" });
    await poll();

    pending = [request("new")];
    waiting = 1;
    await poll();
    await poll();
    expect(shown).toEqual([]);
  });

  it("counts what waits in the tab's title, and leaves it when the shell goes", async () => {
    pending = [request("a"), request("b")];
    waiting = 1;
    shell();
    await waitFor(() => expect(document.title).toBe("(3) openarity"));
    cleanup();
    expect(document.title).toBe("openarity");
  });

  it("keeps the + of a page of requests in the title", async () => {
    pending = [request("a"), request("b")];
    more = true;
    waiting = 1;
    shell();
    await waitFor(() => expect(document.title).toBe("(3+) openarity"));
  });
});

describe("the switch in the account menu", () => {
  it("asks the browser once, and keeps the answer", async () => {
    window.localStorage.removeItem("oa.notifications");
    permission = "default";
    shell();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });

    await user.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    await user.click(await screen.findByRole("menuitem", { name: "Notify me of approvals" }));
    await waitFor(() => expect(window.localStorage.getItem("oa.notifications")).toBe("on"));
    expect(asked).toBe(1);

    await user.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    await user.click(await screen.findByRole("menuitem", { name: "Turn off notifications" }));
    expect(window.localStorage.getItem("oa.notifications")).toBeNull();
  });

  it("hears another tab turn them off", async () => {
    shell();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    await user.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    expect(
      await screen.findByRole("menuitem", { name: "Turn off notifications" }),
    ).toBeInTheDocument();
    await user.keyboard("{Escape}");

    window.localStorage.removeItem("oa.notifications");
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key: "oa.notifications" }));
    });
    await user.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    expect(
      await screen.findByRole("menuitem", { name: "Notify me of approvals" }),
    ).toBeInTheDocument();
  });

  it("says when the browser has blocked them", async () => {
    permission = "denied";
    shell();
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });

    await user.click(await screen.findByRole("button", { name: /ann@example.com/ }));
    const item = await screen.findByRole("menuitem", {
      name: "Notifications blocked by the browser",
    });
    expect(item).toHaveAttribute("aria-disabled", "true");
  });
});
