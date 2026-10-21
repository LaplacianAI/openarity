import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, Suspense } from "react";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, chunks, feed, pageOf, type Seen, STAMP, show, TEAM, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.sessions.index");
const Sessions = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  window.sessionStorage.clear();
  setToken(null);
});

const SID = "5e55a0b1-0000-4000-8000-000000000001";
const session = {
  id: SID,
  channel_id: "c1",
  provider_ref: "ticket-42",
  kind: "group",
  status: "active",
  started_at: STAMP,
  last_message_at: STAMP,
};

function message(id: string, user: string, text: string, at: string) {
  return { id, external_id: `x-${id}`, user_id: user, text, received_at: at };
}

const MESSAGES = `GET /teams/{id}/sessions/{sid}/messages`;
const ATTACHMENTS = `GET /teams/{id}/sessions/{sid}/attachments`;
const RUNS = `GET /teams/{id}/sessions/{sid}/runs`;
const EVENTS = `GET /teams/{id}/runs/{rid}/events`;

function listed(extra: Parameters<typeof brain>[0]) {
  return brain({
    ...team([]),
    "GET /teams/{id}/sessions": { body: { items: [session] } },
    [ATTACHMENTS]: { body: { items: [] } },
    [RUNS]: { body: { items: [] } },
    ...extra,
  });
}

async function open() {
  show(Sessions);
  await userEvent.click(await screen.findByRole("button", { name: "ticket-42" }));
  return screen.findByRole("dialog");
}

/** The texts of the messages, top to bottom, as the reader sees them. */
function said(dialog: HTMLElement): string[] {
  return within(dialog)
    .getAllByRole("listitem")
    .filter((li) => li.dataset.message !== undefined)
    .map((li) => li.querySelector("[data-text]")?.textContent ?? "");
}

it("opens a session and reads it oldest first, with who said it", async () => {
  // The brain answers newest first.
  listed({
    [MESSAGES]: {
      body: {
        items: [
          message("m2", "user-bob", "second", "2026-10-01T00:02:00Z"),
          message("m1", "user-ann", "first", "2026-10-01T00:01:00Z"),
        ],
      },
    },
  });

  const dialog = await open();
  await within(dialog).findByText("second");
  expect(said(dialog)).toEqual(["first", "second"]);
  expect(within(dialog).getByText("user-ann")).toBeInTheDocument();
  expect(within(dialog).getByText("user-bob")).toBeInTheDocument();

  await userEvent.click(within(dialog).getByRole("button", { name: /close/i }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(screen.getByRole("button", { name: "ticket-42" })).toBeInTheDocument();
});

it("loads earlier messages with the cursor and keeps chat order", async () => {
  const seen = listed({
    [MESSAGES]: (s) =>
      s.path.includes("cursor=older")
        ? {
            body: {
              items: [
                message("m2", "user-ann", "two", "2026-10-01T00:02:00Z"),
                message("m1", "user-ann", "one", "2026-10-01T00:01:00Z"),
              ],
            },
          }
        : {
            body: {
              items: [
                message("m4", "user-bob", "four", "2026-10-01T00:04:00Z"),
                message("m3", "user-bob", "three", "2026-10-01T00:03:00Z"),
              ],
              next_cursor: "older",
            },
          },
  });

  const dialog = await open();
  await within(dialog).findByText("four");
  expect(said(dialog)).toEqual(["three", "four"]);

  await userEvent.click(within(dialog).getByRole("button", { name: /load more/i }));
  await within(dialog).findByText("one");
  expect(said(dialog)).toEqual(["one", "two", "three", "four"]);
  expect(seen.some((s) => s.path === `/teams/${TEAM}/sessions/${SID}/messages?cursor=older`)).toBe(
    true,
  );
});

it("says so when nothing has been said", async () => {
  listed({ [MESSAGES]: { body: { items: [] } } });

  const dialog = await open();
  expect(await within(dialog).findByText("Nothing has been said here.")).toBeInTheDocument();
});

it("shows what someone typed as text, never as markup", async () => {
  listed({
    [MESSAGES]: {
      body: { items: [message("m1", "user-ann", "<b>hi</b>", STAMP)] },
    },
  });

  const dialog = await open();
  expect(await within(dialog).findByText("<b>hi</b>")).toBeInTheDocument();
  expect(dialog.querySelector("b")).toBeNull();
});

it("lists the session's attachments by name", async () => {
  listed({
    [MESSAGES]: { body: { items: [message("m1", "user-ann", "see attached", STAMP)] } },
    [ATTACHMENTS]: {
      body: {
        items: [
          {
            id: "a1",
            message_id: "m1",
            filename: "invoice.pdf",
            media_type: "application/pdf",
            size_bytes: 2048,
            created_at: STAMP,
          },
        ],
      },
    },
  });

  const dialog = await open();
  expect(await within(dialog).findByText("invoice.pdf")).toBeInTheDocument();
  expect(within(dialog).getByText(/application\/pdf/)).toBeInTheDocument();
  expect(within(dialog).getByText(/2 KB/)).toBeInTheDocument();
});

it("shows the brain's reason when the session cannot be read", async () => {
  listed({
    [MESSAGES]: { status: 403, text: "you are not part of this conversation" },
    [ATTACHMENTS]: { status: 403, text: "you are not part of this conversation" },
  });

  const dialog = await open();
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    "you are not part of this conversation",
  );
});

// The token is not a cookie, so a bare link could not fetch the file. And a
// file a stranger sent must never open as a page on the dashboard's origin, so
// it is handed to the browser as a download of bytes, whatever type it claims.
it("downloads an attachment with the token, as bytes to save", async () => {
  const seen = listed({
    [MESSAGES]: { body: { items: [message("m1", "user-ann", "see attached", STAMP)] } },
    [ATTACHMENTS]: {
      body: {
        items: [
          {
            id: "a1",
            message_id: "m1",
            filename: "page.html",
            media_type: "text/html",
            size_bytes: 20,
            created_at: STAMP,
          },
        ],
      },
    },
    "GET /teams/{id}/sessions/{sid}/attachments/{aid}": { text: "<script>x</script>" },
  });
  const made: Blob[] = [];
  const createObjectURL = vi.fn((b: Blob) => {
    made.push(b);
    return "blob:fake";
  });
  const revokeObjectURL = vi.fn();
  vi.stubGlobal("URL", Object.assign(URL, { createObjectURL, revokeObjectURL }));
  const clicked: HTMLAnchorElement[] = [];
  vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    clicked.push(this);
  });

  const dialog = await open();
  await userEvent.click(
    await within(dialog).findByRole("button", { name: /download page\.html/i }),
  );

  await waitFor(() => expect(clicked).toHaveLength(1));
  expect(seen.some((s) => s.path === `/teams/${TEAM}/sessions/${SID}/attachments/a1`)).toBe(true);
  expect(clicked[0]?.download).toBe("page.html");
  expect(made[0]?.type).toBe("application/octet-stream");
  expect(made[0]?.size).toBe("<script>x</script>".length);
  await waitFor(() => expect(revokeObjectURL).toHaveBeenCalledWith("blob:fake"));
});

it("shows why an attachment could not be downloaded", async () => {
  listed({
    [MESSAGES]: { body: { items: [message("m1", "user-ann", "see attached", STAMP)] } },
    [ATTACHMENTS]: {
      body: {
        items: [
          {
            id: "a1",
            message_id: "m1",
            filename: "invoice.pdf",
            media_type: "application/pdf",
            size_bytes: 2048,
            created_at: STAMP,
          },
        ],
      },
    },
    "GET /teams/{id}/sessions/{sid}/attachments/{aid}": {
      status: 503,
      text: "the object store is unavailable",
    },
  });

  const dialog = await open();
  await userEvent.click(
    await within(dialog).findByRole("button", { name: /download invoice\.pdf/i }),
  );

  expect(await within(dialog).findByText(/the object store is unavailable/)).toBeInTheDocument();
});

/* ------------------------------------------------------------------- runs */

function run(id: string, agent: string, status: string, extra: Record<string, unknown> = {}) {
  return {
    id,
    session_id: SID,
    agent,
    status,
    input: `input of ${id}`,
    output: null,
    error: null,
    input_tokens: 120,
    output_tokens: 30,
    created_at: STAMP,
    started_at: STAMP,
    finished_at: null,
    ...extra,
  };
}

function frame(seq: number, kind: string, data: unknown): string {
  return `id: ${seq}\nevent: ${kind}\ndata: ${JSON.stringify(data)}\n\n`;
}

const END = 'event: end\ndata: {"status":"succeeded"}\n\n';

function runsOf(dialog: HTMLElement): HTMLElement {
  return within(dialog).getByRole("region", { name: "Runs" });
}

/** The agents of the listed runs, top to bottom. */
async function agents(dialog: HTMLElement): Promise<string[]> {
  const section = await within(dialog).findByRole("region", { name: "Runs" });
  return Array.from(section.querySelectorAll("[data-run] [aria-expanded] .font-medium")).map(
    (e) => e.textContent ?? "",
  );
}

async function expand(dialog: HTMLElement, agent: string) {
  const section = runsOf(dialog);
  await userEvent.click(await within(section).findByRole("button", { name: new RegExp(agent) }));
}

function eventsSeen(seen: Seen[]): Seen[] {
  return seen.filter((s) => s.path.includes("/events"));
}

it("lists the session's runs newest first, and loads older ones with the cursor", async () => {
  const seen = listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: (s) =>
      s.path.includes("cursor=older")
        ? { body: { items: [run("r1", "first-agent", "succeeded")] } }
        : {
            body: {
              items: [run("r3", "third-agent", "running"), run("r2", "second-agent", "failed")],
              next_cursor: "older",
            },
          },
  });

  const dialog = await open();
  await within(dialog).findByText("third-agent");
  expect(await agents(dialog)).toEqual(["third-agent", "second-agent"]);
  // Status is said in words, not only by the dot's colour.
  expect(within(runsOf(dialog)).getByText("running")).toBeInTheDocument();
  expect(within(runsOf(dialog)).getByText("failed")).toBeInTheDocument();

  await userEvent.click(within(runsOf(dialog)).getByRole("button", { name: /load more/i }));
  await within(dialog).findByText("first-agent");
  expect(await agents(dialog)).toEqual(["third-agent", "second-agent", "first-agent"]);
  expect(seen.some((s) => s.path === `/teams/${TEAM}/sessions/${SID}/runs?cursor=older`)).toBe(
    true,
  );
});

it("says so when no agent has run in the session", async () => {
  listed({ [MESSAGES]: { body: { items: [] } } });

  const dialog = await open();
  expect(await within(dialog).findByText("No agent has run in this session.")).toBeInTheDocument();
});

it("shows the brain's reason when the runs cannot be listed", async () => {
  listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { status: 500, text: "the run store is unavailable" },
  });

  const dialog = await open();
  expect(await within(dialog).findByRole("alert")).toHaveTextContent(
    "the run store is unavailable",
  );
});

it("expands a finished run into its trace, read off the stream with the token", async () => {
  // CRLF line endings, a keep-alive, and a frame cut mid-line and between a
  // CR and its LF — what a proxy is free to do to a stream.
  const wire = (
    ": following run r1\n\n" +
    frame(1, "started", {
      agent: "helper",
      model: "gpt-x",
      pattern: "react",
      tools: ["search", "graph"],
      skills: [],
    }) +
    ": keep-alive\n\n" +
    frame(2, "step", { step: 1 }) +
    frame(3, "tool_call", { id: "c1", name: "search", arguments: '{"q":"refund policy"}' }) +
    frame(4, "tool_result", {
      id: "c1",
      name: "search",
      output: "three results",
      duration_ms: 42,
    }) +
    frame(5, "tool_result", { id: "c2", name: "graph", output: "", error: "graph is down" }) +
    frame(6, "tool_unavailable", { name: "slack", reason: "the server is unreachable" }) +
    frame(7, "usage", {
      model: "gpt-x",
      input_tokens: 900,
      output_tokens: 80,
      cached_input_tokens: 512,
      spent: 980,
    }) +
    frame(8, "resumed", { messages: 4 }) +
    frame(9, "novel_kind", { hello: "world" }) +
    frame(10, "finished", { status: "failed", error: "the budget ran out" }) +
    END
  ).replaceAll("\n", "\r\n");
  const cutMidLine = wire.indexOf("refund") + 3;
  const cutInCRLF = wire.indexOf("\r\n", wire.indexOf("three results")) + 1;

  const seen = listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "failed", { error: "the budget ran out" })] } },
    [EVENTS]: () => ({
      stream: chunks(
        wire.slice(0, cutMidLine),
        wire.slice(cutMidLine, cutInCRLF),
        wire.slice(cutInCRLF),
      ),
    }),
  });

  const dialog = await open();
  await expand(dialog, "helper");

  const runs = runsOf(dialog);
  expect(await within(runs).findByText("Ended: succeeded")).toBeInTheDocument();
  expect(within(runs).getByText("input of r1")).toBeInTheDocument();
  expect(within(runs).getByText(/helper on gpt-x \(react\) · tools: search, graph/)).toBeVisible();
  expect(within(runs).getByText('{"q":"refund policy"}')).toBeInTheDocument();
  expect(within(runs).getByText("three results")).toBeInTheDocument();
  expect(within(runs).getByText("failed: graph is down")).toBeInTheDocument();
  expect(within(runs).getByText(/slack: the server is unreachable/)).toBeInTheDocument();
  expect(within(runs).getByText(/900 in \/ 80 out \(512 cached\) · 980 spent/)).toBeVisible();
  expect(within(runs).getByText(/with 4 messages/)).toBeInTheDocument();
  expect(within(runs).getByText("novel_kind")).toBeInTheDocument();
  expect(within(runs).getByText('{"hello":"world"}')).toBeInTheDocument();
  expect(within(runs).getAllByText("the budget ran out").length).toBeGreaterThan(0);
  const kinds = Array.from(runs.querySelectorAll("[data-event]")).map(
    (e) => (e as HTMLElement).dataset.event,
  );
  expect(kinds).toEqual([
    "started",
    "step",
    "tool_call",
    "tool_result",
    "tool_result",
    "tool_unavailable",
    "usage",
    "resumed",
    "novel_kind",
    "finished",
  ]);

  const [stream] = eventsSeen(seen);
  expect(stream?.path).toBe(`/teams/${TEAM}/runs/r1/events`);
  expect(stream?.headers.get("Authorization")).toBe("Bearer a-token");
  expect(stream?.headers.get("Accept")).toBe("text/event-stream");
});

it("keeps a running run's trace live, and stops following it when collapsed", async () => {
  const live = feed();
  const seen = listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "running")] } },
    [EVENTS]: () => ({ stream: live.stream }),
  });

  const dialog = await open();
  await expand(dialog, "helper");
  const runs = runsOf(dialog);
  expect(await within(runs).findByText("Waiting for events…")).toBeInTheDocument();

  live.push(frame(1, "tool_call", { name: "search", arguments: "{}" }));
  expect(await within(runs).findByText("search")).toBeInTheDocument();
  expect(within(runs).getByText("Live…")).toBeInTheDocument();

  live.push(frame(2, "tool_result", { name: "search", output: "found it", duration_ms: 5 }));
  expect(await within(runs).findByText("found it")).toBeInTheDocument();
  expect(runs.querySelectorAll("[data-event]")).toHaveLength(2);

  const signal = eventsSeen(seen)[0]?.signal;
  expect(signal?.aborted).toBe(false);
  await userEvent.click(within(runs).getByRole("button", { name: /helper/ }));
  await waitFor(() => expect(signal?.aborted).toBe(true));
  expect(within(runs).queryByText("found it")).not.toBeInTheDocument();
});

it("stops following a run when the dialog closes", async () => {
  const live = feed();
  const seen = listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "running")] } },
    [EVENTS]: () => ({ stream: live.stream }),
  });

  const dialog = await open();
  await expand(dialog, "helper");
  live.push(frame(1, "step", { step: 1 }));
  await waitFor(() => expect(runsOf(dialog).querySelectorAll("[data-event]")).toHaveLength(1));

  const signal = eventsSeen(seen)[0]?.signal;
  await userEvent.click(within(dialog).getByRole("button", { name: /close/i }));
  await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  expect(signal?.aborted).toBe(true);
});

it("reads the run again once its stream says it has ended", async () => {
  const live = feed();
  let listings = 0;
  listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: () => {
      listings += 1;
      return { body: { items: [run("r1", "helper", listings === 1 ? "running" : "succeeded")] } };
    },
    [EVENTS]: () => ({ stream: live.stream }),
  });

  const dialog = await open();
  await expand(dialog, "helper");
  live.push(frame(1, "finished", { status: "succeeded" }) + END);

  const runs = runsOf(dialog);
  expect(await within(runs).findByText("Ended: succeeded")).toBeInTheDocument();
  expect(await within(runs).findByText("succeeded", { selector: "span" })).toBeInTheDocument();
  expect(listings).toBe(2);
});

// The brain sends no `end` only when the stream was cut, so the run is
// followed again from the last event seen rather than from the start.
it("follows a cut stream again from the last event it saw", async () => {
  const seen = listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "running")] } },
    [EVENTS]: (s) =>
      s.path.endsWith("?after=2")
        ? { stream: chunks(frame(3, "step", { step: 2 }), END) }
        : { stream: chunks(frame(1, "step", { step: 1 }), frame(2, "step", { step: 1.5 })) },
  });

  const dialog = await open();
  await expand(dialog, "helper");

  const runs = runsOf(dialog);
  expect(await within(runs).findByText("Ended: succeeded")).toBeInTheDocument();
  expect(runs.querySelectorAll("[data-event]")).toHaveLength(3);
  expect(eventsSeen(seen).map((s) => s.path)).toEqual([
    `/teams/${TEAM}/runs/r1/events`,
    `/teams/${TEAM}/runs/r1/events?after=2`,
  ]);
});

it("says why a run's events could not be followed", async () => {
  listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "succeeded", { output: "all done" })] } },
    [EVENTS]: { status: 404, text: "no such run" },
  });

  const dialog = await open();
  await expand(dialog, "helper");
  expect(await within(runsOf(dialog)).findByRole("alert")).toHaveTextContent("no such run");
  expect(within(runsOf(dialog)).getByText("all done")).toBeInTheDocument();
});

it("shows what a tool or a model produced as text, never as markup", async () => {
  const markup = '<img src=x onerror="alert(1)"><b>bold</b>';
  listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "succeeded", { output: markup })] } },
    [EVENTS]: () => ({
      stream: chunks(
        frame(1, "tool_call", { name: "<i>search</i>", arguments: markup }),
        frame(2, "tool_result", { name: "search", output: markup, duration_ms: 1 }),
        frame(3, "<script>", markup),
        END,
      ),
    }),
  });

  const dialog = await open();
  await expand(dialog, "helper");
  const runs = runsOf(dialog);
  await within(runs).findByText("Ended: succeeded");
  expect(within(runs).getByText("<i>search</i>")).toBeInTheDocument();
  expect(within(runs).getAllByText(markup).length).toBeGreaterThanOrEqual(3);
  expect(within(runs).getByText("<script>")).toBeInTheDocument();
  expect(runs.querySelector("img, b, i, script")).toBeNull();
});

it("cancels a running run for someone who may", async () => {
  let listings = 0;
  const seen = listed({
    ...team(["run:write"]),
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: () => {
      listings += 1;
      return {
        body: {
          items: [
            run("r2", "busy-agent", listings === 1 ? "running" : "cancelled"),
            run("r1", "done-agent", "succeeded"),
          ],
        },
      };
    },
    "POST /teams/{id}/runs/{rid}/cancel": { status: 202, body: {} },
  });

  const dialog = await open();
  await within(dialog).findByText("busy-agent");
  const runs = runsOf(dialog);
  const cancels = within(runs).getAllByRole("button", { name: "Cancel" });
  // A finished run has nothing to cancel.
  expect(cancels).toHaveLength(1);

  await userEvent.click(cancels[0] as HTMLElement);
  expect(await within(runs).findByText("cancelled")).toBeInTheDocument();
  expect(seen.some((s) => s.method === "POST" && s.path === `/teams/${TEAM}/runs/r2/cancel`)).toBe(
    true,
  );
});

it("says why a run could not be cancelled", async () => {
  listed({
    ...team(["run:write"]),
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "queued")] } },
    "POST /teams/{id}/runs/{rid}/cancel": { status: 409, text: "the run has already finished" },
  });

  const dialog = await open();
  await userEvent.click(await within(dialog).findByRole("button", { name: "Cancel" }));
  expect(await within(runsOf(dialog)).findByRole("alert")).toHaveTextContent(
    "the run has already finished",
  );
});

it("offers no cancel to someone who may not", async () => {
  listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "running")] } },
  });

  const dialog = await open();
  await within(dialog).findByText("helper");
  expect(within(runsOf(dialog)).queryByRole("button", { name: "Cancel" })).toBeNull();
});

// StrictMode mounts, unmounts and remounts in development, so the first
// stream is aborted while the same trace is still on screen. That abort is
// the trace's own doing and is not a failure to report.
it("does not report its own abort as a failure", async () => {
  setToken("a-token");
  let live = feed();
  listed({
    [MESSAGES]: { body: { items: [] } },
    [RUNS]: { body: { items: [run("r1", "helper", "running")] } },
    [EVENTS]: () => {
      live = feed();
      return { stream: live.stream };
    },
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <StrictMode>
      <QueryClientProvider client={client}>
        <Suspense fallback={null}>
          <Sessions />
        </Suspense>
      </QueryClientProvider>
    </StrictMode>,
  );
  await userEvent.click(await screen.findByRole("button", { name: "ticket-42" }));
  const dialog = await screen.findByRole("dialog");
  await expand(dialog, "helper");

  live.push(frame(1, "step", { step: 1 }));
  const runs = runsOf(dialog);
  await waitFor(() => expect(runs.querySelectorAll("[data-event]")).toHaveLength(1));
  expect(within(runs).queryByRole("alert")).toBeNull();
});
