import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { Improvement, ImproverPass } from "@/api/types";
import { brain, choose, pageOf, type Seen, showAt, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.improvements");
await pageOf(Route);

function at(path = "/ui/improvements?tab=passes") {
  return showAt(path, [["/ui/improvements", Route]]);
}

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const PASSES = "GET /teams/{id}/improvements/passes";
const LIST = "GET /teams/{id}/improvements";
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

function pass(id: string, extra: Partial<ImproverPass> = {}): ImproverPass {
  return {
    id,
    agent: { id: "a1", name: "support-bot" },
    trigger: "pattern",
    signatures: ["instructions::skipped_step"],
    pruned: [],
    prune_proposed: [],
    status: "done",
    cut_short: false,
    report: "It closed tickets before confirming the order.\n\nSecond paragraph.",
    report_withheld: false,
    proposed: 1,
    screened_out: 0,
    model: "gpt-5",
    input_tokens: 9_000,
    output_tokens: 1_234,
    error: null,
    started_at: new Date(Date.now() - 2 * 3_600_000).toISOString(),
    finished_at: new Date(Date.now() - 2 * 3_600_000 + 60_000).toISOString(),
    ...extra,
  };
}

function serve(items: ImproverPass[]) {
  return {
    ...team(APPROVER),
    ...agents,
    [LIST]: { body: { items: [] } },
    [PASSES]: { body: { items } },
  };
}

const rowOf = async (name: RegExp | string) =>
  (await screen.findByRole("button", { name })).closest("[data-pass]") as HTMLElement;

describe("the Passes tab", () => {
  it("is offered to an approver after the others, and opened by the address", async () => {
    brain(serve([pass("p1")]));
    const router = at("/ui/improvements");
    await screen.findByRole("tab", { name: "Passes" });
    // A tab's name, without the flagged count the Flagged tab may carry.
    expect(
      screen.getAllByRole("tab").map((t) => (t.textContent ?? "").replace(/\d+\+? flagged$/, "")),
    ).toEqual(["Waiting", "Applied", "Flagged", "Closed", "Passes"]);
    await userEvent.click(screen.getByRole("tab", { name: "Passes" }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ tab: "passes" }));
    expect(await screen.findByRole("list", { name: "Improver passes" })).toBeInTheDocument();
    // Proposals are not the screen's concern while passes are.
    expect(screen.queryByRole("searchbox")).toBeNull();
  });

  it("is not offered to a member, who never asks for passes", async () => {
    const seen = brain({
      ...team([]),
      ...agents,
      [LIST]: { body: { items: [] } },
      [PASSES]: { status: 403, text: "forbidden" },
    });
    at("/ui/improvements?tab=passes");
    await screen.findByText("Nothing here");
    expect(screen.queryByRole("tab", { name: "Passes" })).toBeNull();
    expect(seen.some((s) => s.path.includes("/improvements/passes"))).toBe(false);
  });
});

describe("a pass", () => {
  it("says a pattern in words, with its target, and what it proposed", async () => {
    brain(
      serve([
        pass("p1", {
          signatures: ["tool:crm__close_ticket:misused_tool", "instructions::skipped_step"],
          proposed: 2,
        }),
      ]),
    );
    at();
    const row = within(await rowOf(/support-bot/));
    expect(row.getByRole("button")).toHaveTextContent(
      "support-bot · A pattern: tool · crm__close_ticket · misused tool, instructions · skipped step",
    );
    expect(row.getByText("2 proposals")).toBeInTheDocument();
    expect(row.getByText("10,234 tokens")).toBeInTheDocument();
    expect(row.getByText("2h ago")).toBeInTheDocument();
  });

  it("shows no count while a pass has spent no tokens yet", async () => {
    brain(
      serve([
        pass("p1", {
          status: "running",
          report: "",
          input_tokens: 0,
          output_tokens: 0,
          finished_at: null,
        }),
      ]),
    );
    at();
    const row = await rowOf(/support-bot/);
    expect(within(row).getByText("Running…")).toBeInTheDocument();
    expect(row.textContent).not.toMatch(/tokens/);
  });

  it("says the daily pass, the runs it waited for, and how many prune candidates it looked at", async () => {
    brain(
      serve([
        pass("p1", {
          trigger: "daily",
          signatures: [],
          pruned: ["skill:a", "tool:b/c"],
          runs_seen: 6,
        }),
      ]),
    );
    at();
    expect(
      await screen.findByRole("button", {
        name: /The daily pass, after 6 judged runs, 2 prune candidates looked at/,
      }),
    ).toBeTruthy();
  });

  it("marks each prune candidate as kept or proposed for removal", async () => {
    brain(
      serve([
        pass("p1", {
          trigger: "daily",
          signatures: [],
          pruned: ["skill:a", "tool:b/c"],
          prune_proposed: ["skill:a"],
        }),
      ]),
    );
    at();
    await userEvent.click(await screen.findByRole("button", { name: /The daily pass/ }));
    const list = await screen.findByRole("list", { name: "Prune candidates" });
    expect(list).toHaveTextContent("skill a (proposed removing)");
    expect(list).toHaveTextContent("tool b/c (kept)");
    expect(screen.queryByText(/To prune/)).toBeNull();
  });

  it("says nothing to change, and what the screen held back", async () => {
    brain(
      serve([
        pass("p1", { proposed: 0 }),
        pass("p2", { proposed: 1, screened_out: 1, agent: { id: "a2", name: "triage" } }),
      ]),
    );
    at();
    expect(await screen.findByText("Nothing to change")).toBeInTheDocument();
    expect(screen.getByText("1 proposal, 1 held back by the screen")).toBeInTheDocument();
  });

  it("says a pass is running, and a failed one with its reason", async () => {
    brain(
      serve([
        pass("p1", { status: "running", report: "", finished_at: null }),
        pass("p2", { status: "failed", error: "the model timed out", proposed: 0 }),
      ]),
    );
    at();
    expect(await screen.findByText("Running…")).toBeInTheDocument();
    expect(screen.getByText("Failed: the model timed out")).toBeInTheDocument();
  });

  it("opens its report from the keyboard, and closes it", async () => {
    brain(serve([pass("p1", { pruned: ["skill:triage"] })]));
    at();
    const button = await screen.findByRole("button", { name: /support-bot/ });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).not.toHaveAttribute("aria-controls");
    expect(screen.queryByText(/closed tickets before/)).toBeNull();

    button.focus();
    await userEvent.keyboard("{Enter}");
    expect(button).toHaveAttribute("aria-expanded", "true");
    const report = screen.getByText(/It closed tickets before confirming the order\./);
    expect(report).toHaveTextContent("Second paragraph.");
    expect(report.className).toContain("whitespace-pre-wrap");
    expect(report.closest("[id]")?.id).toBe(button.getAttribute("aria-controls"));
    expect(screen.getByRole("list", { name: "Prune candidates" })).toHaveTextContent(
      "skill triage (kept)",
    );
    expect(screen.getByText(/gpt-5 · 9,000 in · 1,234 out/)).toBeInTheDocument();

    await userEvent.keyboard(" ");
    expect(button).toHaveAttribute("aria-expanded", "false");
  });

  it("says a report is withheld when it draws on a conversation the reader can't read", async () => {
    brain(serve([pass("p1", { report: "", report_withheld: true })]));
    at();
    await userEvent.click(await screen.findByRole("button", { name: /support-bot/ }));
    expect(
      screen.getByText(/The report draws on a conversation you can't read, so it isn't shown\./),
    ).toBeInTheDocument();
    expect(screen.queryByText("It left no report.")).toBeNull();
  });

  it("says when a pass left no report", async () => {
    brain(serve([pass("p1", { report: "", status: "failed", error: "x" })]));
    at();
    await userEvent.click(await screen.findByRole("button", { name: /support-bot/ }));
    expect(screen.getByText("It left no report.")).toBeInTheDocument();
  });
});

describe("the list of passes", () => {
  it("says when there are none, and what makes one", async () => {
    brain(serve([]));
    at();
    expect(await screen.findByText("No passes yet")).toBeInTheDocument();
    expect(
      screen.getByText("The improver runs when a pattern recurs in three runs, and once a day."),
    ).toBeInTheDocument();
  });

  it("says what it could not read", async () => {
    brain({ ...serve([]), [PASSES]: { status: 500, text: "the database is busy" } });
    at();
    expect(await screen.findByText(/the database is busy/)).toBeInTheDocument();
  });

  it("narrows by agent, in the address and the request", async () => {
    const seen = brain(serve([pass("p1")]));
    const router = at();
    await screen.findByRole("button", { name: /support-bot/ });
    await choose(screen.getByRole("combobox"), "triage");
    await waitFor(() => expect(router.state.location.search).toMatchObject({ agent: "a2" }));
    await waitFor(() =>
      expect(seen.some((s) => s.path.includes("/passes") && s.path.includes("agent=a2"))).toBe(
        true,
      ),
    );
  });

  it("says nothing matches an agent that has had no pass", async () => {
    brain(serve([]));
    at("/ui/improvements?tab=passes&agent=a2");
    expect(await screen.findByText("Nothing matches")).toBeInTheDocument();
  });

  it("pages with the cursor", async () => {
    const seen = brain({
      ...serve([]),
      [PASSES]: (s: Seen) =>
        s.path.includes("cursor=c2")
          ? { body: { items: [pass("z9", { agent: { id: "a2", name: "second-page-agent" } })] } }
          : { body: { items: [pass("p1")], next_cursor: "c2" } },
    });
    at();
    await userEvent.click(await screen.findByRole("button", { name: "Load more" }));
    expect(await screen.findByRole("button", { name: /second-page-agent/ })).toBeInTheDocument();
    expect(seen.some((s) => s.path.includes("cursor=c2"))).toBe(true);
    expect(screen.getByText("2 passes loaded")).toBeInTheDocument();
  });

  it("shows the shape of a row while reading", async () => {
    brain({ ...serve([]), [PASSES]: () => new Promise(() => {}) } as never);
    at();
    expect(await screen.findByRole("status", { name: "Loading passes" })).toBeInTheDocument();
  });
});

describe("a proposal from a pass", () => {
  const proposal = (extra: Partial<Improvement> = {}): Improvement => ({
    id: "i1",
    kind: "instructions",
    status: "waiting",
    agent: { id: "a1", name: "support-bot" },
    skill: null,
    why: "From a pass, this one.",
    hypothesis: "h",
    scope: "invariant",
    marks: [],
    reaches: [],
    changes: [],
    runs: [],
    outsider: false,
    created_at: "2026-11-17T00:00:00Z",
    ...extra,
  });

  it("links an approver to the Passes tab, filtered to its agent", async () => {
    brain({
      ...team(APPROVER),
      ...agents,
      [LIST]: { body: { items: [proposal({ pass_id: "p1" })] } },
      [PASSES]: { body: { items: [pass("p1")] } },
    });
    const router = at("/ui/improvements");
    await userEvent.click(await screen.findByRole("link", { name: /From a pass/ }));
    await waitFor(() =>
      expect(router.state.location.search).toMatchObject({ tab: "passes", agent: "a1" }),
    );
    expect(await screen.findByRole("list", { name: "Improver passes" })).toBeInTheDocument();
  });

  it("offers no link to a proposal that came from no pass, or to a member", async () => {
    brain({
      ...team([]),
      ...agents,
      [LIST]: {
        body: {
          items: [
            proposal({ status: "applied", pass_id: "p1" }),
            proposal({ id: "i2", status: "applied", why: "none" }),
          ],
        },
      },
    });
    at("/ui/improvements");
    await screen.findByText("none");
    expect(screen.queryByRole("link", { name: /From a pass/ })).toBeNull();
  });

  it("offers none when the proposal has no pass", async () => {
    brain({
      ...team(APPROVER),
      ...agents,
      [LIST]: { body: { items: [proposal({ pass_id: null })] } },
    });
    at("/ui/improvements");
    await screen.findByText("From a pass, this one.");
    expect(screen.queryByRole("link", { name: /From a pass/ })).toBeNull();
  });
});
