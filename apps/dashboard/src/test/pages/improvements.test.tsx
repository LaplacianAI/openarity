import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { Improvement } from "@/api/types";
import { brain, choose, pageOf, type Seen, showAt, TEAM, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.improvements");
await pageOf(Route);
const { Route: SessionRoute } = await import("@/routes/ui.sessions.$sessionId");

function at(path = "/ui/improvements") {
  return showAt(path, [
    ["/ui/improvements", Route],
    ["/ui/sessions/$sessionId", SessionRoute],
  ]);
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const LIST = "GET /teams/{id}/improvements";
const DECIDE = "POST /teams/{id}/improvements/{id}/decision";

function proposal(id: string, extra: Partial<Improvement> = {}): Improvement {
  return {
    id,
    kind: "instructions",
    status: "waiting",
    agent: { id: "a1", name: "support-bot" },
    skill: null,
    why: `Closed the ticket before confirming the order id (${id}).`,
    hypothesis: "tickets close only after the order id is confirmed.",
    scope: "invariant",
    marks: [],
    reaches: [],
    changes: [
      {
        field: "instructions",
        type: "text",
        before: "Be brief.\nAnswer the customer, then close the ticket.",
        after: "Be brief.\nConfirm the order id with the customer before closing the ticket.",
      },
    ],
    runs: [],
    outsider: false,
    created_at: "2026-11-17T00:00:00Z",
    ...extra,
  };
}

const APPROVER = ["improvement:approve", "agent:write", "skill:write"];

const agents = {
  "GET /teams/{id}/agents": {
    body: {
      items: [
        { id: "a1", name: "support-bot" },
        { id: "a2", name: "triage" },
      ],
    },
  },
};

function lists(items: Improvement[]) {
  return { ...agents, [LIST]: { body: { items } } };
}

describe("the tabs", () => {
  it("offers an approver all three, opening on Waiting, and asks for what each holds", async () => {
    const seen = brain({ ...team(APPROVER), ...lists([proposal("p1")]) });
    const router = at();
    await screen.findByText(/Closed the ticket/);
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual([
      "Waiting",
      "Applied",
      "Closed",
    ]);
    expect(seen.some((s) => s.path.includes("status=waiting"))).toBe(true);

    await userEvent.click(screen.getByRole("tab", { name: "Applied" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "applied" }));
    await waitFor(() => expect(seen.some((s) => s.path.includes("status=applied"))).toBe(true));

    await userEvent.click(screen.getByRole("tab", { name: "Closed" }));
    await waitFor(() =>
      expect(
        seen.some(
          (s) =>
            s.path.includes("status=rejected") &&
            s.path.includes("status=reverted") &&
            s.path.includes("status=withdrawn"),
        ),
      ).toBe(true),
    );
  });

  it("shows a member Applied only, and never asks for what waits", async () => {
    const seen = brain({
      ...team([]),
      ...lists([proposal("p1", { status: "applied" })]),
    });
    // The address names a tab a member may not open.
    at("/ui/improvements?tab=waiting");
    await screen.findByText(/Closed the ticket/);
    expect(screen.getAllByRole("tab").map((t) => t.textContent)).toEqual(["Applied"]);
    const asked = seen.filter((s) => s.path.includes("/improvements"));
    expect(asked.length).toBeGreaterThan(0);
    expect(asked.every((s) => s.path.includes("status=applied"))).toBe(true);
    expect(screen.queryByText(/wait for approval/)).toBeNull();
  });

  it("says in one line what waits and for how long", async () => {
    brain({
      ...team(APPROVER),
      ...lists([
        proposal("p1", { created_at: new Date(Date.now() - 86_400_000).toISOString() }),
        proposal("p2", { created_at: new Date(Date.now() - 3 * 86_400_000).toISOString() }),
      ]),
    });
    at();
    expect(
      await screen.findByText("2 changes wait for approval; the oldest for 3 days."),
    ).toBeInTheDocument();
  });

  it("says when nothing waits, and draws the empty tab", async () => {
    brain({ ...team(APPROVER), ...lists([]) });
    at();
    expect(await screen.findByText("No changes wait for approval.")).toBeInTheDocument();
    expect(screen.getByText("Nothing is waiting")).toBeInTheDocument();
  });

  it("narrows by agent and by words, in the address and in the request", async () => {
    const seen = brain({ ...team(APPROVER), ...lists([proposal("p1")]) });
    const router = at();
    await screen.findByText(/Closed the ticket/);

    await choose(screen.getByRole("combobox"), "triage");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ agent: "a2" }));
    await waitFor(() => expect(seen.some((s) => s.path.includes("agent=a2"))).toBe(true));

    await userEvent.type(screen.getByRole("searchbox"), "order");
    await waitFor(() => expect(seen.some((s) => s.path.includes("q=order"))).toBe(true));
    expect(router.state.location.search).toMatchObject({ q: "order" });
  });

  it("says what it could not read, and does not offer the list to a refused caller", async () => {
    brain({ ...team(APPROVER), ...agents, [LIST]: { status: 500, text: "the database is busy" } });
    at();
    expect(await screen.findByText(/the database is busy/)).toBeInTheDocument();
  });

  it("pages with the cursor", async () => {
    const seen = brain({
      ...team(APPROVER),
      ...agents,
      [LIST]: (s: Seen) =>
        s.path.includes("cursor=c2")
          ? { body: { items: [proposal("z9", { why: "second page" })] } }
          : { body: { items: [proposal("p1")], next_cursor: "c2" } },
    });
    at();
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByText("second page")).toBeInTheDocument();
    expect(seen.some((s) => s.path.includes("cursor=c2"))).toBe(true);
  });
});

describe("a proposal", () => {
  it("says who, what kind and how wide, why, and what it expects", async () => {
    brain({
      ...team(APPROVER),
      ...lists([
        proposal("p1", { kind: "tools_narrow", scope: "domain", why: "It read the wrong tool." }),
      ]),
    });
    at();
    const card = (await screen.findByText("It read the wrong tool.")).closest("[data-improvement]");
    expect(card).not.toBeNull();
    const c = within(card as HTMLElement);
    expect(c.getByText("support-bot")).toBeInTheDocument();
    expect(c.getByText(/Tool limits · domain/)).toBeInTheDocument();
    expect(
      c.getByText("Expects: tickets close only after the order id is confirmed."),
    ).toBeInTheDocument();
  });

  it("warns of an outside sender, and says nothing otherwise", async () => {
    brain({
      ...team(APPROVER),
      ...lists([proposal("p1", { outsider: true }), proposal("p2", { why: "calm one" })]),
    });
    at();
    await screen.findByText("calm one");
    expect(screen.getAllByText(/Outside sender/)).toHaveLength(1);
  });

  it("labels what it widens, and whom a skill edit reaches", async () => {
    brain({
      ...team(APPROVER),
      ...lists([
        proposal("p1", {
          kind: "skill_edit",
          skill: { id: "s1", name: "refunds" },
          marks: [
            "widens_access",
            "widens_reach",
            "raises_cost",
            "changes_code",
            "changes_discovery",
            "changes_behaviour",
          ],
          reaches: [
            { id: "a1", name: "support-bot" },
            { id: "a2", name: "triage" },
          ],
        }),
      ]),
    });
    at();
    const marks = await screen.findByRole("list", { name: "What kind of change" });
    expect(
      within(marks)
        .getAllByRole("listitem")
        .map((l) => l.textContent),
    ).toEqual([
      "widens access",
      "widens reach",
      "raises cost",
      "changes code",
      "changes how it is found",
      "changes how it works",
      "Reaches 2 agents: support-bot, triage",
    ]);
    expect(screen.getByText(/Skill edit of refunds/)).toBeInTheDocument();
  });

  it("draws a text change as a line diff, the unchanged line as it stood", async () => {
    brain({ ...team(APPROVER), ...lists([proposal("p1")]) });
    at();
    const del = await screen.findByText("Answer the customer, then close the ticket.");
    expect(del.closest("del")).not.toBeNull();
    const ins = screen.getByText(
      "Confirm the order id with the customer before closing the ticket.",
    );
    expect(ins.closest("ins")).not.toBeNull();
    expect(screen.getByText("Be brief.").closest("ins")).toBeNull();
    expect(screen.getByText("Be brief.").closest("del")).toBeNull();
  });

  it("folds a long unchanged stretch of text, but never a script", async () => {
    const lines = Array.from({ length: 20 }, (_, i) => `line ${i}`);
    const edited = lines.map((l) => (l === "line 10" ? "CHANGED" : l)).join("\n");
    brain({
      ...team(APPROVER),
      ...lists([
        proposal("p1", {
          changes: [
            { field: "body", type: "text", before: lines.join("\n"), after: edited },
            {
              field: "files/scripts/fetch.py",
              type: "text",
              before: lines.join("\n"),
              after: edited,
            },
          ],
        }),
      ]),
    });
    at();
    await screen.findByText("scripts/fetch.py");
    // The body folds its far lines: only one "line 0" would be in view if it were drawn,
    // and it is drawn once, by the script.
    expect(screen.getAllByText("line 0")).toHaveLength(1);
    expect(screen.getAllByText("line 19")).toHaveLength(1);
    expect(screen.getAllByText("CHANGED")).toHaveLength(2);
    expect(screen.getAllByText(/unchanged lines/).length).toBeGreaterThan(0);
  });

  it("draws a list change as the names removed and added, and a value as before then after", async () => {
    brain({
      ...team(APPROVER),
      ...lists([
        proposal("p1", {
          changes: [
            { field: "skills", type: "list", before: ["pdf", "csv"], after: ["csv", "refunds"] },
            { field: "max_steps", type: "value", before: "5", after: "8" },
          ],
        }),
      ]),
    });
    at();
    const removed = await screen.findByText("pdf");
    expect(removed.tagName).toBe("DEL");
    expect(screen.getByText("refunds").tagName).toBe("INS");
    // The name kept is shown for context, neither taken away nor added.
    expect(screen.getByText("csv").closest("del, ins")).toBeNull();
    expect(screen.getByText("5").tagName).toBe("DEL");
    expect(screen.getByText("8").tagName).toBe("INS");
  });

  it("links a run only when it can be opened", async () => {
    brain({
      ...team(APPROVER),
      ...lists([
        proposal("p1", {
          runs: [
            { id: "7f3a0000-0000", readable: true, session_id: "s1" },
            { id: "91c00000-0000", readable: false },
          ],
        }),
      ]),
    });
    at();
    const link = await screen.findByRole("link", { name: "Run 7f3a0000" });
    expect(link).toHaveAttribute("href", expect.stringContaining("/ui/sessions/s1"));
    expect(screen.getByText("a run you cannot open")).toBeInTheDocument();
    expect(screen.getAllByRole("link")).toHaveLength(1);
  });
});

describe("deciding", () => {
  const decided = (item: Improvement, status: Improvement["status"]) => ({
    body: { ...item, status },
  });

  it("rejects and approves a waiting proposal, and refreshes the list", async () => {
    const item = proposal("p1");
    let answered = false;
    const seen = brain({
      ...team(APPROVER),
      ...agents,
      [LIST]: () => ({ body: { items: answered ? [] : [item] } }),
      [DECIDE]: () => {
        answered = true;
        return decided(item, "applied");
      },
    });
    at();
    const card = (await screen.findByText(/Closed the ticket/)).closest(
      "[data-improvement]",
    ) as HTMLElement;
    expect(within(card).getByRole("button", { name: "Reject" })).toBeEnabled();
    await userEvent.click(within(card).getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(screen.queryByText(/Closed the ticket/)).toBeNull());
    const post = seen.find((s) => s.method === "POST");
    expect(post?.path).toBe(`/teams/${TEAM}/improvements/p1/decision`);
    expect(JSON.parse(String(post?.body))).toEqual({ action: "approve" });
  });

  it("sends reject for Reject", async () => {
    const item = proposal("p1");
    const seen = brain({
      ...team(APPROVER),
      ...lists([item]),
      [DECIDE]: decided(item, "rejected"),
    });
    at();
    await userEvent.click(await screen.findByRole("button", { name: "Reject" }));
    expect(screen.getByText("Reject this change?")).toBeInTheDocument();
    expect(seen.some((s) => s.method === "POST")).toBe(false);
    await userEvent.click(screen.getByRole("button", { name: "Reject" }));
    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(JSON.parse(String(seen.find((s) => s.method === "POST")?.body))).toEqual({
      action: "reject",
    });
  });

  it("offers Revert on an applied proposal, and sends it", async () => {
    const item = proposal("p1", { status: "applied" });
    const seen = brain({
      ...team(APPROVER),
      ...lists([item]),
      [DECIDE]: decided(item, "reverted"),
    });
    at("/ui/improvements?tab=applied");
    const card = (await screen.findByText(/Closed the ticket/)).closest(
      "[data-improvement]",
    ) as HTMLElement;
    expect(within(card).queryByRole("button", { name: "Approve" })).toBeNull();
    await userEvent.click(within(card).getByRole("button", { name: "Revert" }));
    expect(within(card).getByRole("button", { name: "Revert" })).toHaveFocus();
    await userEvent.click(within(card).getByRole("button", { name: "Revert" }));
    await waitFor(() => expect(seen.some((s) => s.method === "POST")).toBe(true));
    expect(JSON.parse(String(seen.find((s) => s.method === "POST")?.body))).toEqual({
      action: "revert",
    });
  });

  it("lets a person change their mind at the confirmation, with focus back where it was", async () => {
    const seen = brain({ ...team(APPROVER), ...lists([proposal("p1")]) });
    at();
    await userEvent.click(await screen.findByRole("button", { name: "Reject" }));
    await userEvent.click(screen.getByRole("button", { name: "Keep it" }));
    expect(screen.queryByText("Reject this change?")).toBeNull();
    expect(screen.getByRole("button", { name: "Reject" })).toHaveFocus();
    expect(seen.some((s) => s.method === "POST")).toBe(false);
  });

  it("shows a 409's reason on the card and leaves the card where it is", async () => {
    const reason =
      "support-bot changed since this was proposed; reject it and let the next one be proposed from the current agent";
    brain({
      ...team(APPROVER),
      ...lists([proposal("p1")]),
      [DECIDE]: { status: 409, text: reason },
    });
    at();
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(reason);
    expect(screen.getByText(/Closed the ticket/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("offers nothing to decide to a member, who may only read what was applied", async () => {
    brain({ ...team(["agent:write"]), ...lists([proposal("p1", { status: "applied" })]) });
    at();
    await screen.findByText(/Closed the ticket/);
    expect(screen.queryByRole("button", { name: "Revert" })).toBeNull();
    expect(screen.queryByText(/Deciding this takes/)).toBeNull();
  });

  it.each([
    ["instructions", ["improvement:approve", "skill:write"], "agent:write"],
    ["skill_edit", ["improvement:approve", "agent:write"], "skill:write"],
    ["skill_new", ["improvement:approve", "agent:write"], "skill:write"],
    ["skill_new", ["improvement:approve", "skill:write"], "agent:write"],
  ] as const)("a %s needs %s... and names the one missing: %s", async (kind, actions, missing) => {
    brain({ ...team([...actions]), ...lists([proposal("p1", { kind })]) });
    at();
    const note = await screen.findByText(/Deciding this takes/);
    expect(note).toHaveTextContent(missing);
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("offers a skill edit with skill:write alone, without agent:write", async () => {
    brain({
      ...team(["improvement:approve", "skill:write"]),
      ...lists([proposal("p1", { kind: "skill_edit" })]),
    });
    at();
    expect(await screen.findByRole("button", { name: "Approve" })).toBeEnabled();
  });
});
