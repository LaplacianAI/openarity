import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { RunOutcome } from "@/api/types";
import { brain, chunks, feed, pageOf, type Seen, STAMP, showAt, TEAM, team } from "@/test/brain";

const { Route: PageRoute } = await import("@/routes/ui.sessions.$sessionId");
await pageOf(PageRoute);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const SID = "5e55a0b1-0000-4000-8000-000000000001";
const PAGE = `/ui/sessions/${SID}`;
const FEEDBACK = "/teams/{id}/runs/{rid}/feedback";
const SETTINGS = "GET /teams/{id}/settings";
const SESSION_FEEDBACK = "GET /teams/{id}/sessions/{sid}/feedback";

let seqs = 0;
function sf(kind: string, data: Record<string, unknown>): string {
  seqs += 1;
  return `id: ${seqs}\nevent: ${kind}\ndata: ${JSON.stringify({ at: STAMP, ...data })}\n\n`;
}

const answered = (id = "r1", status = "succeeded") => [
  sf("run_started", { run_id: id, agent: "hub" }),
  sf("run_finished", { run_id: id, status, output: "The export is fixed." }),
];

function settings(improvement: boolean) {
  return {
    body: {
      model: null,
      search: { provider: "none", url: null, key_set: false },
      sandbox: { provider: "none", url: null, key_set: false },
      improvement,
      judge_model: null,
      improvement_sample_share: 10,
      improvement_daily_passes: 20,
      improvement_since: null,
      compaction_messages: 100,
      compaction_tokens_min: 50_000,
      compaction_tokens_max: 60_000,
      hub: null,
      updated_at: null,
    },
  };
}

function mine(runId: string, value: "up" | "down", note = "") {
  return { run_id: runId, value, note, updated_at: STAMP };
}

/** The session with its conversation, and what the caller gave so far. */
function feedbackBrain(
  opts: {
    actions?: string[];
    improvement?: boolean;
    frames?: string[];
    given?: ReturnType<typeof mine>[];
    extra?: Parameters<typeof brain>[0];
  } = {},
) {
  const f = feed();
  for (const x of opts.frames ?? answered()) f.push(x);
  return brain({
    ...team(opts.actions ?? ["run:write"]),
    "GET /teams/{id}/sessions/{sid}": {
      body: {
        id: SID,
        channel_id: "c1",
        provider_ref: "ticket-42",
        kind: "group",
        status: "active",
        started_at: STAMP,
        last_message_at: STAMP,
      },
    },
    "GET /teams/{id}/sessions/{sid}/events": () => ({ stream: f.stream }),
    "GET /teams/{id}/sessions/{sid}/attachments": { body: { items: [] } },
    "GET /teams/{id}/sessions/{sid}/runs": { body: { items: [] } },
    "GET /teams/{id}/channels": { body: { items: [] } },
    [SETTINGS]: settings(opts.improvement ?? true),
    [SESSION_FEEDBACK]: { body: { items: opts.given ?? [] } },
    ...opts.extra,
  });
}

async function openMessages() {
  showAt(`${PAGE}?tab=messages`, [["/ui/sessions/$sessionId", PageRoute]]);
  await screen.findByRole("heading", { level: 1, name: "ticket-42" });
}

const right = () => screen.findByRole("button", { name: "This answer was right" });
const wrong = () => screen.getByRole("button", { name: "This answer was wrong" });
const writes = (seen: Seen[], method: string) => seen.filter((s) => s.method === method);

describe("rating an answer", () => {
  it("offers both buttons under a finished top-level answer, unpressed", async () => {
    feedbackBrain();
    await openMessages();

    expect(await right()).toHaveAttribute("aria-pressed", "false");
    expect(wrong()).toHaveAttribute("aria-pressed", "false");
  });

  it("reads the caller's feedback once for the whole session, and shows it pressed", async () => {
    const seen = feedbackBrain({
      frames: [...answered("r1"), ...answered("r2")],
      given: [mine("r1", "up"), mine("r2", "down")],
    });
    await openMessages();

    await waitFor(() => {
      const ups = screen.getAllByRole("button", { name: "This answer was right" });
      expect(ups.map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false"]);
    });
    const downs = screen.getAllByRole("button", { name: "This answer was wrong" });
    expect(downs.map((b) => b.getAttribute("aria-pressed"))).toEqual(["false", "true"]);
    expect(seen.filter((s) => s.path.endsWith(`/sessions/${SID}/feedback`))).toHaveLength(1);
  });

  it("sends a 👍 and shows it pressed", async () => {
    const seen = feedbackBrain({
      extra: {
        [`PUT ${FEEDBACK}`]: { body: { value: "up", note: "", updated_at: STAMP } },
      },
    });
    await openMessages();
    await userEvent.click(await right());

    await expect(right()).resolves.toHaveAttribute("aria-pressed", "true");
    await waitFor(() => expect(writes(seen, "PUT")).toHaveLength(1));
    const put = writes(seen, "PUT")[0] as Seen;
    expect(put.path).toBe(`/teams/${TEAM}/runs/r1/feedback`);
    expect(JSON.parse(String(put.body))).toEqual({ value: "up", via: "dashboard" });
    expect(screen.queryByRole("textbox", { name: "What was wrong?" })).toBeNull();
  });

  it("clears the one that is pressed when it is pressed again", async () => {
    const seen = feedbackBrain({
      given: [mine("r1", "up")],
      extra: { [`DELETE ${FEEDBACK}`]: { status: 204 } },
    });
    await openMessages();
    await waitFor(async () => expect(await right()).toHaveAttribute("aria-pressed", "true"));
    await userEvent.click(await right());

    await waitFor(() => expect(writes(seen, "DELETE")).toHaveLength(1));
    expect(writes(seen, "DELETE")[0]?.path).toBe(`/teams/${TEAM}/runs/r1/feedback`);
    expect(writes(seen, "PUT")).toHaveLength(0);
    expect(await right()).toHaveAttribute("aria-pressed", "false");
  });

  it("switches from 👍 to 👎 with one request, and asks what was wrong", async () => {
    const seen = feedbackBrain({
      given: [mine("r1", "up")],
      extra: {
        [`PUT ${FEEDBACK}`]: { body: { value: "down", note: "", updated_at: STAMP } },
      },
    });
    await openMessages();
    await waitFor(async () => expect(await right()).toHaveAttribute("aria-pressed", "true"));
    await userEvent.click(wrong());

    expect(wrong()).toHaveAttribute("aria-pressed", "true");
    expect(await right()).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("textbox", { name: "What was wrong?" })).toHaveFocus();
    expect(writes(seen, "PUT")).toHaveLength(1);
    expect(writes(seen, "DELETE")).toHaveLength(0);
  });
});

describe("the note on a 👎", () => {
  const down = {
    [`PUT ${FEEDBACK}`]: ({ body }: Seen) => ({
      body: { value: "down", note: JSON.parse(String(body)).note ?? "", updated_at: STAMP },
    }),
  };

  it("is sent with Send, replacing the 👎 with the same value and a note, inline", async () => {
    const seen = feedbackBrain({ extra: down });
    await openMessages();
    await right();
    await userEvent.click(wrong());
    await userEvent.type(screen.getByRole("textbox", { name: "What was wrong?" }), "Wrong file");
    await userEvent.click(screen.getByRole("button", { name: "Send" }));

    await screen.findByText("Note saved");
    expect(screen.queryByRole("textbox", { name: "What was wrong?" })).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
    const puts = writes(seen, "PUT").map((s) => JSON.parse(String(s.body)));
    expect(puts).toEqual([
      { value: "down", via: "dashboard" },
      { value: "down", note: "Wrong file", via: "dashboard" },
    ]);
    expect(wrong()).toHaveAttribute("aria-pressed", "true");
  });

  it("is sent with Enter", async () => {
    const seen = feedbackBrain({ extra: down });
    await openMessages();
    await right();
    await userEvent.click(wrong());
    await userEvent.type(
      screen.getByRole("textbox", { name: "What was wrong?" }),
      "Too long{Enter}",
    );

    await screen.findByText("Note saved");
    expect(writes(seen, "PUT")).toHaveLength(2);
  });

  it("is skipped without a second request, and the 👎 stays", async () => {
    const seen = feedbackBrain({ extra: down });
    await openMessages();
    await right();
    await userEvent.click(wrong());
    await userEvent.click(screen.getByRole("button", { name: "Skip" }));

    expect(screen.queryByRole("textbox", { name: "What was wrong?" })).toBeNull();
    expect(wrong()).toHaveAttribute("aria-pressed", "true");
    expect(writes(seen, "PUT")).toHaveLength(1);
  });

  it("closes on Escape and when sent empty, without a request", async () => {
    const seen = feedbackBrain({ extra: { ...down, [`DELETE ${FEEDBACK}`]: { status: 204 } } });
    await openMessages();
    await right();
    await userEvent.click(wrong());
    await userEvent.type(screen.getByRole("textbox", { name: "What was wrong?" }), "{Escape}");
    expect(screen.queryByRole("textbox", { name: "What was wrong?" })).toBeNull();

    await userEvent.click(wrong()); // clears
    await userEvent.click(wrong()); // presses again, reopens
    await userEvent.click(await screen.findByRole("button", { name: "Send" }));
    expect(screen.queryByRole("textbox", { name: "What was wrong?" })).toBeNull();
    expect(writes(seen, "PUT")).toHaveLength(2);
  });

  it("keeps the note and says why when it cannot be sent", async () => {
    let calls = 0;
    feedbackBrain({
      extra: {
        [`PUT ${FEEDBACK}`]: () => {
          calls += 1;
          return calls === 1
            ? { body: { value: "down", note: "", updated_at: STAMP } }
            : { status: 500, text: "the store is down" };
        },
      },
    });
    await openMessages();
    await right();
    await userEvent.click(wrong());
    await userEvent.type(screen.getByRole("textbox", { name: "What was wrong?" }), "Nope{Enter}");

    expect(await screen.findByRole("alert")).toHaveTextContent("the store is down");
    expect(screen.getByRole("textbox", { name: "What was wrong?" })).toHaveValue("Nope");
    expect(wrong()).toHaveAttribute("aria-pressed", "true");
  });
});

describe("when the brain says no", () => {
  it("rolls a refused press back and shows the brain's reason", async () => {
    feedbackBrain({
      extra: {
        [`PUT ${FEEDBACK}`]: {
          status: 409,
          text: "self-improvement is off for this team, so feedback is not recorded",
        },
      },
    });
    await openMessages();
    await userEvent.click(await right());

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "self-improvement is off for this team, so feedback is not recorded",
    );
    expect(await right()).toHaveAttribute("aria-pressed", "false");
  });

  it("puts a refused clear back as it was", async () => {
    feedbackBrain({
      given: [mine("r1", "up")],
      extra: { [`DELETE ${FEEDBACK}`]: { status: 500, text: "could not clear it" } },
    });
    await openMessages();
    await waitFor(async () => expect(await right()).toHaveAttribute("aria-pressed", "true"));
    await userEvent.click(await right());

    expect(await screen.findByRole("alert")).toHaveTextContent("could not clear it");
    expect(await right()).toHaveAttribute("aria-pressed", "true");
  });

  it("closes the note and rolls back when a 👎 is refused", async () => {
    feedbackBrain({
      extra: { [`PUT ${FEEDBACK}`]: { status: 409, text: "a delegate's run is not rated" } },
    });
    await openMessages();
    await right();
    await userEvent.click(wrong());

    expect(await screen.findByRole("alert")).toHaveTextContent("a delegate's run is not rated");
    expect(screen.queryByRole("textbox", { name: "What was wrong?" })).toBeNull();
    expect(wrong()).toHaveAttribute("aria-pressed", "false");
  });
});

describe("when the buttons are not offered", () => {
  it("hides them on a delegate's run whose parent the conversation never saw", async () => {
    feedbackBrain({
      frames: [
        sf("run_started", { run_id: "r2", agent: "log-reader", parent_run_id: "r1" }),
        sf("run_finished", { run_id: "r2", status: "succeeded", output: "found it" }),
      ],
    });
    await openMessages();
    await screen.findByText("found it");
    expect(screen.queryByRole("button", { name: "This answer was right" })).toBeNull();
  });

  const none = () => {
    expect(screen.queryByRole("button", { name: "This answer was right" })).toBeNull();
    expect(screen.queryByRole("button", { name: "This answer was wrong" })).toBeNull();
  };

  it("hides them while improvement is off, and never asks for the feedback", async () => {
    const seen = feedbackBrain({ improvement: false });
    await openMessages();
    await screen.findByText("The export is fixed.");
    none();
    expect(seen.some((s) => s.path.endsWith("/feedback"))).toBe(false);
  });

  it("hides them from a reader without run:write", async () => {
    const seen = feedbackBrain({ actions: [] });
    await openMessages();
    await screen.findByText("The export is fixed.");
    none();
    expect(seen.some((s) => s.path.endsWith("/feedback"))).toBe(false);
  });

  it("hides them under a run that is still working, or that failed", async () => {
    feedbackBrain({
      frames: [sf("run_started", { run_id: "r1", agent: "hub" }), ...answered("r2", "failed")],
    });
    await openMessages();
    await screen.findByText("Working on it…");
    none();
  });

  it("offers them on the answer and not on a run it handed work to", async () => {
    feedbackBrain({
      frames: [
        sf("run_started", { run_id: "r1", agent: "ops-lead" }),
        sf("step_started", { run_id: "r1", step: 1 }),
        sf("run_started", { run_id: "r2", agent: "log-reader", parent_run_id: "r1" }),
        sf("run_finished", { run_id: "r2", status: "succeeded", output: "found it" }),
        sf("run_finished", { run_id: "r1", status: "succeeded", output: "All sorted." }),
      ],
    });
    await openMessages();
    await right();
    expect(screen.getAllByRole("button", { name: "This answer was right" })).toHaveLength(1);
    const turn = document.querySelector('[data-turn="r1"]') as HTMLElement;
    expect(within(turn).getByRole("button", { name: "This answer was right" })).toBeInTheDocument();
  });
});

/* ------------------------------------------------------------ the trace line */

const ROW = (id: string, agent: string) => ({
  id,
  session_id: SID,
  agent,
  status: "succeeded",
  input: "fix it",
  output: "fixed",
  error: null,
  input_tokens: 1,
  output_tokens: 1,
  created_at: STAMP,
  started_at: STAMP,
  finished_at: STAMP,
  parent_run_id: null,
  depth: 0,
  hub: false,
});

const OUTCOME: RunOutcome = {
  verdict: "partly",
  missed: "The refund total was not checked.",
  why: "It fixed the export only.",
  implicit: "none",
  effective: "bad",
  judged_at: STAMP,
  feedback: { up: 0, down: 1 },
};

function judged(outcome: RunOutcome | undefined, listedToo = false, readFails = false) {
  feedbackBrain({
    extra: {
      "GET /teams/{id}/sessions/{sid}/runs": {
        body: {
          items: [
            ...(listedToo ? [ROW("r9", "billing")] : []),
            {
              id: "r2",
              session_id: SID,
              agent: "newer",
              status: "succeeded",
              input: "",
              output: "",
              error: null,
              input_tokens: 1,
              output_tokens: 1,
              created_at: STAMP,
              started_at: STAMP,
              finished_at: STAMP,
              parent_run_id: null,
              depth: 0,
              hub: false,
            },
          ],
        },
      },
      "GET /teams/{id}/runs/{rid}": readFails
        ? { status: 500, text: "the store is down" }
        : {
            body: {
              id: "r9",
              session_id: SID,
              agent: "billing",
              status: "succeeded",
              input: "fix it",
              output: "fixed",
              error: null,
              input_tokens: 1,
              output_tokens: 1,
              created_at: STAMP,
              started_at: STAMP,
              finished_at: STAMP,
              parent_run_id: null,
              depth: 0,
              hub: false,
              ...(outcome ? { outcome } : {}),
            },
          },
      "GET /teams/{id}/runs/{rid}/events": () => ({ stream: chunks() }),
    },
  });
}

async function openRun() {
  showAt(`${PAGE}?tab=runs&run=r9`, [["/ui/sessions/$sessionId", PageRoute]]);
  return within(await screen.findByTestId("run-pane")).findByRole("heading", {
    level: 2,
    name: "billing",
  });
}

describe("a judged run's trace", () => {
  it("says the verdict in words, what was missed, and what people said apart from it", async () => {
    judged(OUTCOME);
    await openRun();

    const line = await waitFor(() => {
      const el = document.querySelector("[data-outcome]");
      if (!el) throw new Error("no outcome line");
      return el as HTMLElement;
    });
    expect(line).toHaveAttribute("data-outcome", "partly");
    expect(within(line).getByText("Partly done")).toBeInTheDocument();
    expect(within(line).getByText("The refund total was not checked.")).toBeInTheDocument();
    expect(within(line).getByText("1 member said it was wrong")).toBeInTheDocument();
    expect(within(line).queryByText(/said it was right/)).toBeNull();
  });

  it("shows a done verdict with nothing missed and no counts", async () => {
    judged({ ...OUTCOME, verdict: "done", missed: "", feedback: { up: 0, down: 0 } });
    await openRun();

    const line = await waitFor(() => {
      const el = document.querySelector("[data-outcome]");
      if (!el) throw new Error("no outcome line");
      return el as HTMLElement;
    });
    expect(within(line).getByText("Done")).toBeInTheDocument();
    expect(within(line).queryByText(/Missed/)).toBeNull();
    expect(line.textContent).not.toMatch(/member/);
  });

  it.each([
    ["not_done", "Not done"],
    ["wrong", "Wrong"],
    ["cant_tell", "Can't tell"],
  ] as const)("names a %s verdict %s", async (verdict, word) => {
    judged({ ...OUTCOME, verdict, feedback: { up: 2, down: 0 } });
    await openRun();

    await waitFor(() => expect(document.querySelector("[data-outcome]")).not.toBeNull());
    const line = document.querySelector("[data-outcome]") as HTMLElement;
    expect(within(line).getByText(word)).toBeInTheDocument();
    expect(within(line).getByText("2 members said it was right")).toBeInTheDocument();
  });

  it("reads the verdict for a run the list already holds, as lists carry none", async () => {
    judged(OUTCOME, true);
    await openRun();
    await waitFor(() => expect(document.querySelector("[data-outcome]")).not.toBeNull());
  });

  it("says so when how it was judged cannot be read, rather than showing nothing", async () => {
    judged(OUTCOME, true, true);
    await openRun();
    expect(
      await screen.findByText("How this run was judged could not be read."),
    ).toBeInTheDocument();
    expect(document.querySelector("[data-outcome]")).toBeNull();
  });

  it("shows no line for a run that was not judged", async () => {
    judged(undefined);
    await openRun();
    await screen.findByText(/Ended|Model/);
    expect(document.querySelector("[data-outcome]")).toBeNull();
  });
});
