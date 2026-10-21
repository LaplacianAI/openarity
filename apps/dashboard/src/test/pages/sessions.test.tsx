import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, show, TEAM, team } from "@/test/brain";

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

function listed(extra: Parameters<typeof brain>[0]) {
  return brain({
    ...team([]),
    "GET /teams/{id}/sessions": { body: { items: [session] } },
    [ATTACHMENTS]: { body: { items: [] } },
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
  expect(await made[0]?.text()).toBe("<script>x</script>");
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
