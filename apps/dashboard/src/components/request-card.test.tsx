import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { ToolApproval } from "@/api/types";
import { fromApproval } from "@/lib/approvals";
import { brain, STAMP, show, TEAM, team } from "@/test/brain";

import { RequestCard } from "./request-card";

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const soon = () => new Date(Date.now() + 10 * 60_000).toISOString();

function approval(extra: Partial<ToolApproval> = {}): ToolApproval {
  return {
    id: "a1",
    run_id: "r1",
    root_run_id: "r1",
    call_id: "c1",
    tool: "github__create_issue",
    class: "write",
    arguments: { title: "Bug", labels: ["a"] },
    summary: "title: Bug\nrepo: openarity",
    code: "K7QX",
    status: "pending",
    requested_for: "me",
    require_other: false,
    decided_by: null,
    decided_via: null,
    scope: null,
    reason: null,
    expires_at: soon(),
    decided_at: null,
    used_at: null,
    created_at: STAMP,
    ...extra,
  };
}

const DECISION = "POST /teams/{id}/approvals/{id}/decision";

function card(a: ToolApproval, props: { fetchFull?: boolean } = {}) {
  const Page = () => <RequestCard teamId={TEAM} request={fromApproval(a)} {...props} />;
  return show(Page);
}

const base = (actions: string[]) => ({
  ...team(actions),
  "GET /whoami": { body: { id: "me", subject: "me" } },
});

describe("RequestCard", () => {
  it("shows the tool, its summary, the class, the code and the time left", async () => {
    brain(base(["approval:self"]));
    card(approval());
    expect(await screen.findByText("github")).toBeInTheDocument();
    expect(screen.getByText("create_issue")).toBeInTheDocument();
    expect(screen.getByText(/title: Bug/)).toBeInTheDocument();
    expect(screen.getByText("write")).toBeInTheDocument();
    expect(screen.getByText("K7QX")).toBeInTheDocument();
    expect(screen.getByText(/min left/)).toBeInTheDocument();
  });

  it("approves once, for the run, or declines with a reason, and says which", async () => {
    const seen = brain({
      ...base(["approval:self"]),
      [DECISION]: {
        body: approval({ status: "approved", decided_by: "me", decided_via: "dashboard" }),
      },
    });
    card(approval());
    await userEvent.type(await screen.findByLabelText("Reason, optional"), "ok");
    await userEvent.click(screen.getByRole("button", { name: "Approve for this run" }));
    await screen.findByText(/Approved/);
    const sent = seen.find((s) => s.path.endsWith("/decision"));
    expect(JSON.parse(String(sent?.body))).toEqual({
      approve: true,
      scope: "run",
      reason: "ok",
      via: "dashboard",
    });
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("sends a decline without a scope", async () => {
    const seen = brain({
      ...base(["approval:self"]),
      [DECISION]: { body: approval({ status: "denied", reason: "not now" }) },
    });
    card(approval());
    await userEvent.click(await screen.findByRole("button", { name: "Decline" }));
    expect(await screen.findByText(/“not now”/)).toBeInTheDocument();
    const sent = seen.find((s) => s.path.endsWith("/decision"));
    expect(JSON.parse(String(sent?.body))).toEqual({ approve: false, via: "dashboard" });
  });

  it("disables the buttons while an answer is in flight", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => {
      release = r;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/decision")) {
          await gate;
          return new Response(JSON.stringify(approval({ status: "approved" })), { status: 200 });
        }
        if (url.endsWith("/whoami")) return new Response(JSON.stringify({ id: "me" }));
        if (url.endsWith("/permissions")) {
          return new Response(JSON.stringify({ actions: ["approval:self"], super_admin: false }));
        }
        return new Response(JSON.stringify({ items: [] }));
      }),
    );
    card(approval());
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Decline" })).toBeDisabled();
    release();
    await screen.findByText(/Approved/);
  });

  it("offers no answer without approval:self, and says what it takes", async () => {
    brain(base(["approval:read"]));
    card(approval());
    expect(await screen.findByText(/approval:self/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("offers no answer to the person it was asked for when a rule wants someone else", async () => {
    brain(base(["approval:self", "approval:decide"]));
    card(approval({ require_other: true }));
    expect(await screen.findByText(/someone other than the person/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("offers no answer to someone who may neither decide nor is the person it was for", async () => {
    brain(base(["approval:self"]));
    card(approval({ requested_for: "someone-else" }));
    expect(await screen.findByText(/Only the person it was asked for/)).toBeInTheDocument();
  });

  it("lets a decider answer another person's request", async () => {
    brain(base(["approval:self", "approval:decide"]));
    card(approval({ requested_for: "someone-else" }));
    expect(await screen.findByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("takes a 403 as the final word, whatever the permissions said", async () => {
    brain({
      ...base(["approval:self"]),
      [DECISION]: { status: 403, text: "you may not answer this request" },
    });
    card(approval());
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("you may not answer this request")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("shows what a request already is when a 409 says it was answered", async () => {
    brain({
      ...base(["approval:self"]),
      [DECISION]: {
        status: 409,
        body: approval({
          status: "denied",
          decided_by: "u9",
          decided_via: "channel",
          reason: "no",
        }),
      },
    });
    card(approval());
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Declined")).toBeInTheDocument();
    expect(screen.getByText(/in chat/)).toBeInTheDocument();
    expect(screen.getByText(/already settled/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("shows an expired request as expired, with nothing to answer", async () => {
    brain(base(["approval:self"]));
    card(approval({ expires_at: "2020-01-01T00:00:00Z" }));
    expect(await screen.findByText(/Expired: nobody answered/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).not.toBeInTheDocument();
  });

  it("shows each outcome and who answered", async () => {
    brain(base(["approval:self"]));
    card(approval({ status: "approved", scope: "run", decided_by: "u2", decided_via: "cli" }), {});
    expect(await screen.findByText("Approved for this run")).toBeInTheDocument();
    expect(screen.getByText(/with oa/)).toBeInTheDocument();
  });

  it("says a request set aside by the person writing again", async () => {
    brain(base(["approval:self"]));
    const Page = () => (
      <RequestCard
        teamId={TEAM}
        request={{ ...fromApproval(approval({ status: "cancelled" })), superseded: true }}
      />
    );
    show(Page);
    expect(await screen.findByText("Set aside: you wrote again")).toBeInTheDocument();
  });

  it("shows the full arguments when expanded", async () => {
    brain(base(["approval:self"]));
    card(approval());
    await userEvent.click(await screen.findByRole("button", { name: "Arguments" }));
    expect(await screen.findByText(/"labels"/)).toBeInTheDocument();
  });

  it("reads the arguments from the API for a request the stream described", async () => {
    const seen = brain({
      ...base(["approval:self"]),
      "GET /teams/{id}/approvals/{id}": { body: approval() },
    });
    const Page = () => (
      <RequestCard
        teamId={TEAM}
        fetchFull
        request={{ ...fromApproval(approval()), arguments: null, requestedFor: null }}
      />
    );
    show(Page);
    await userEvent.click(await screen.findByRole("button", { name: "Arguments" }));
    expect(await screen.findByText(/"labels"/)).toBeInTheDocument();
    await waitFor(() => expect(seen.some((s) => s.path.includes("/approvals/a1"))).toBe(true));
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
  });
});
