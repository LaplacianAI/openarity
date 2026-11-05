import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, type Seen, show, TEAM, team } from "@/test/brain";

const { Route, adminFinding, noAdmin } = await import("@/routes/ui.teams.index");
const Teams = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const alice = { user_id: "u1", subject: "alice", role: "admin" };
const people = {
  "GET /teams/{id}/members": { body: { items: [alice] } },
  "GET /users": {
    body: {
      items: [
        { id: "u1", issuer: "idp", subject: "alice" },
        { id: "u2", issuer: "idp", subject: "bala", email: "bala@example.com" },
      ],
    },
  },
};

// A Radix select opens from the keyboard in jsdom, which has no pointer
// capture, and scrolls its highlighted option into view, which jsdom lacks.
function stubScroll() {
  Element.prototype.scrollIntoView = vi.fn();
  onTestFinished(() => {
    delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
  });
}

async function choose(combobox: string, option: string) {
  screen.getByRole("combobox", { name: combobox }).focus();
  await userEvent.keyboard("{ArrowDown}");
  await userEvent.click(await screen.findByRole("option", { name: option }));
}

async function addSheet(): Promise<HTMLElement> {
  await userEvent.click(await screen.findByRole("button", { name: /^add member$/i }));
  return screen.findByRole("dialog", { name: /add a member to platform/i });
}

function sent(seen: Seen[], method: string) {
  return seen.filter((s) => s.method === method);
}

describe("a team with no admin", () => {
  const page = (items: { role: string }[], next_cursor?: string) =>
    ({ items: items.map((m, i) => ({ ...alice, user_id: `u${i}`, ...m })), next_cursor }) as never;

  it.each([
    [[{ role: "member" }], undefined, true],
    [[{ role: "member" }, { role: "admin" }], undefined, false],
    // The admin may be on the next page.
    [[{ role: "member" }], "more", false],
    [[], undefined, true],
  ])("%o with cursor %s has none: %s", (items, cursor, said) => {
    expect(noAdmin(page(items, cursor))).toBe(said);
  });

  it("is not claimed before its members are read", () => {
    expect(noAdmin(undefined)).toBe(false);
  });

  it.each([
    [[], null],
    [[{ id: "t1", name: "platform" }], "platform has no admin."],
    [
      [
        { id: "t1", name: "platform" },
        { id: "t2", name: "ops" },
      ],
      "2 teams have no admin.",
    ],
  ])("%o reads %s", (teams, said) => {
    expect(adminFinding(teams)).toBe(said);
  });

  it("is named at the top, and marked on the team", async () => {
    brain({
      ...team([]),
      ...people,
      "GET /teams/{id}/members": { body: { items: [{ ...alice, role: "member" }] } },
    });
    show(Teams);

    expect(await screen.findByText("platform has no admin.")).toBeInTheDocument();
    expect(screen.getByText("No admin")).toBeInTheDocument();
  });

  it("is not claimed of a team that has one", async () => {
    brain({ ...team([]), ...people });
    show(Teams);

    await screen.findByText("alice");
    expect(screen.queryByText(/no admin/i)).not.toBeInTheDocument();
  });
});

describe("the members", () => {
  // Adding and removing people is membership:write in the team. A member
  // without it still sees who is in the team.
  it("offers no membership controls without membership:write", async () => {
    brain({ ...team(["agent:write"]), ...people });
    show(Teams);

    const table = await screen.findByRole("table", { name: "Members of platform" });
    expect(within(table).getByText("alice")).toBeInTheDocument();
    expect(within(table).getByText("admin")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^remove/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /add member/i })).not.toBeInTheDocument();
  });

  it("offers them with membership:write", async () => {
    brain({ ...team(["membership:write"]), ...people });
    show(Teams);

    expect(await screen.findByRole("button", { name: "Remove alice" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /add member/i })).toBeInTheDocument();
  });

  it("shows the brain's error with a way to try again", async () => {
    brain({
      ...team([]),
      "GET /teams/{id}/members": { status: 500, text: "the store is down" },
    });
    show(Teams);

    expect(await screen.findByText(/the store is down/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });

  it("removes someone once it is confirmed, and shows the brain's refusal", async () => {
    let refused = true;
    const seen = brain({
      ...team(["membership:write"]),
      ...people,
      "DELETE /teams/{id}/members/{userID}": () =>
        refused ? { status: 409, text: "the last admin cannot leave" } : { status: 204 },
    });
    show(Teams);

    await userEvent.click(await screen.findByRole("button", { name: "Remove alice" }));
    expect(sent(seen, "DELETE")).toEqual([]);

    const confirm = await screen.findByRole("dialog", { name: "Remove alice from platform?" });
    await userEvent.click(within(confirm).getByRole("button", { name: /^remove$/i }));
    expect(await within(confirm).findByRole("alert")).toHaveTextContent(
      "the last admin cannot leave",
    );

    refused = false;
    await userEvent.click(within(confirm).getByRole("button", { name: /^remove$/i }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(sent(seen, "DELETE").map((s) => s.path)).toEqual([
      `/teams/${TEAM}/members/u1`,
      `/teams/${TEAM}/members/u1`,
    ]);
    // The members are read again.
    expect(sent(seen, "GET").filter((s) => s.path.includes("/members")).length).toBeGreaterThan(1);
  });
});

describe("adding a member", () => {
  // The brain ships three roles, nested: member, then approver (who also lets
  // waiting senders in), then admin. Each one must be grantable from here.
  it("offers every role the brain ships", async () => {
    stubScroll();
    brain({ ...team(["membership:write"]), ...people });
    show(Teams);

    await addSheet();
    screen.getByRole("combobox", { name: "Role" }).focus();
    await userEvent.keyboard("{ArrowDown}");
    const offered = (await screen.findAllByRole("option")).map((o) => o.textContent);
    expect(offered).toEqual(["member", "approver", "admin"]);
  });

  it("offers only people not already in the team, and sends who and what role", async () => {
    stubScroll();
    const seen = brain({
      ...team(["membership:write"]),
      ...people,
      "POST /teams/{id}/members": { status: 204 },
    });
    show(Teams);

    await addSheet();
    screen.getByRole("combobox", { name: "Person" }).focus();
    await userEvent.keyboard("{ArrowDown}");
    const offered = (await screen.findAllByRole("option")).map((o) => o.textContent);
    expect(offered).toEqual(["bala@example.com"]);
    await userEvent.click(screen.getByRole("option", { name: "bala@example.com" }));
    await choose("Role", "approver");
    await userEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const [post] = sent(seen, "POST");
    expect(post?.path).toBe(`/teams/${TEAM}/members`);
    expect(JSON.parse(String(post?.body))).toEqual({ user_id: "u2", role: "approver" });
  });

  it("keeps the sheet open with the brain's refusal", async () => {
    stubScroll();
    brain({
      ...team(["membership:write"]),
      ...people,
      "POST /teams/{id}/members": { status: 409, text: "already a member" },
    });
    show(Teams);

    const sheet = await addSheet();
    await choose("Person", "bala@example.com");
    await userEvent.click(within(sheet).getByRole("button", { name: /^add$/i }));

    expect(await within(sheet).findByRole("alert")).toHaveTextContent("already a member");
    expect(screen.getByRole("dialog", { name: /add a member/i })).toBeInTheDocument();
  });

  // The directory is user:read; managing members is not. Someone who may
  // add people but not browse them names the person instead.
  it("names the person when the directory cannot be read", async () => {
    const seen = brain({
      ...team(["membership:write"]),
      ...people,
      "GET /users": { status: 403, text: "forbidden" },
      "POST /teams/{id}/members": { status: 204 },
    });
    show(Teams);

    const sheet = await addSheet();
    expect(within(sheet).queryByRole("combobox", { name: "Person" })).not.toBeInTheDocument();
    await userEvent.type(await within(sheet).findByLabelText(/sign-in name/i), " bala ");
    await userEvent.click(within(sheet).getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(sent(seen, "POST")).toHaveLength(1));
    expect(JSON.parse(String(sent(seen, "POST")[0]?.body))).toEqual({
      subject: "bala",
      role: "member",
    });
  });
});

describe("a new team", () => {
  it("is offered to a super admin", async () => {
    brain({ ...team([], true), ...people });
    show(Teams);

    expect(await screen.findByRole("button", { name: /new team/i })).toBeInTheDocument();
  });

  it("is not offered to a member of a team", async () => {
    brain({ ...team(["membership:write"]), ...people });
    show(Teams);

    await screen.findByText("alice");
    expect(screen.queryByRole("button", { name: /new team/i })).not.toBeInTheDocument();
  });

  // On a fresh install the first person to sign in is a super admin with no
  // team to be told so by.
  it("is offered to someone in no team, and says why it is refused", async () => {
    const seen = brain({
      "GET /teams": { body: { items: [] } },
      "POST /teams": { status: 403, text: "forbidden" },
    });
    show(Teams);

    await userEvent.click(await screen.findByRole("button", { name: /new team/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), " ops ");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Creating a team requires super admin.",
    );
    expect(JSON.parse(String(sent(seen, "POST")[0]?.body))).toEqual({ name: "ops" });
  });

  it("becomes the current team once created", async () => {
    let created = false;
    brain({
      "GET /teams": () => ({
        body: {
          items: created
            ? [
                { id: TEAM, name: "platform", role: "member" },
                { id: "t2", name: "ops", role: "admin" },
              ]
            : [{ id: TEAM, name: "platform", role: "member" }],
        },
      }),
      "GET /teams/{id}/permissions": { body: { actions: [], super_admin: true } },
      ...people,
      "POST /teams": () => {
        created = true;
        return { status: 201, body: { id: "t2", name: "ops" } };
      },
    });
    show(Teams);

    await userEvent.click(await screen.findByRole("button", { name: /new team/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "ops");
    await userEvent.click(screen.getByRole("button", { name: /^create$/i }));

    const ops = await screen.findByRole("region", { name: "ops" });
    expect(within(ops).getByText("Current team")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Switch to platform" })).toBeInTheDocument();
  });
});
