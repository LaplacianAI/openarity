import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, show, showAt, TEAM, team } from "@/test/brain";

const { Route, channelRequest, hookAddress, hookPath, isQuiet, setWebhookCommand } = await import(
  "@/routes/ui.channels.index"
);
const Channels = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const support = {
  id: "c1",
  team_id: TEAM,
  provider: "custom",
  name: "support",
  last_message_at: STAMP,
  sessions_24h: 3,
  can_reply: false,
  takes_reply_url: false,
};
const GENERATED = "oawh_generated-in-the-brain";
const PASTED = " pasted secret ";
// Not what the page would build from its own origin: the listener is another host.
const HOOK_URL = "https://hooks.example.com/hooks/custom/c1";

/** The row a channel's name sits in. */
function rowOf(name: string): HTMLElement {
  return screen.getByRole("button", { name }).closest("tr") as HTMLElement;
}

/** Opens a channel's sheet from its row. */
async function opened(name: string): Promise<HTMLElement> {
  await userEvent.click(await screen.findByRole("button", { name }));
  return screen.findByRole("dialog", { name });
}

function posted(seen: { method: string; body: BodyInit | null | undefined }[]) {
  const post = seen.find((s) => s.method === "POST");
  return post ? JSON.parse(String(post.body)) : undefined;
}

describe("the create form", () => {
  const draft = {
    name: " support ",
    provider: " custom ",
    ownSecret: false,
    secret: "",
    botToken: "",
  };

  it("trims the name and provider, and leaves the secret to the brain", () => {
    expect(channelRequest(draft)).toEqual({ name: "support", provider: "custom" });
  });

  it("sends nothing typed in the secret field once the box is unticked", () => {
    expect(channelRequest({ ...draft, secret: "left behind" })).toEqual({
      name: "support",
      provider: "custom",
    });
  });

  // Trimming a credential changes it, and the provider would sign with the
  // untrimmed one: every webhook would then fail its signature.
  it("sends the provider's secret exactly as typed", () => {
    expect(channelRequest({ ...draft, ownSecret: true, secret: PASTED })).toEqual({
      name: "support",
      provider: "custom",
      signing_secret: PASTED,
    });
  });

  it.each([
    [{ ...draft, name: "  " }, /needs a name/],
    [{ ...draft, name: "x".repeat(201) }, /at most 200/],
    [{ ...draft, provider: " " }, /needs a provider/],
    [{ ...draft, ownSecret: true, secret: "   " }, /signing secret/],
  ])("refuses %o before asking the brain", (d, said) => {
    expect(channelRequest(d)).toMatch(said);
  });

  describe("for telegram", () => {
    const tg = { ...draft, provider: "telegram", botToken: " 123:TEST-ONLY " };

    it("sends the bot token as a credential, and no signing secret", () => {
      expect(channelRequest({ ...tg, ownSecret: true, secret: "ignored" })).toEqual({
        name: "support",
        provider: "telegram",
        credentials: { bot_token: "123:TEST-ONLY" },
      });
    });

    it("refuses a missing bot token before asking the brain", () => {
      expect(channelRequest({ ...tg, botToken: "  " })).toMatch(/bot token/i);
    });

    it("does not ask custom for a bot token", () => {
      expect(channelRequest({ ...draft, botToken: "left behind" })).toEqual({
        name: "support",
        provider: "custom",
      });
    });
  });

  it("names the path the provider posts to", () => {
    expect(hookPath(support)).toBe("/hooks/custom/c1");
  });

  it("gives the brain's own URL when it has one, and the path when it does not", () => {
    expect(hookAddress({ ...support, hook_url: HOOK_URL })).toBe(HOOK_URL);
    expect(hookAddress(support)).toBe("/hooks/custom/c1");
  });
});

it("offers no write action without channel:write", async () => {
  brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [support] } } });
  show(Channels);

  await screen.findByText("support");
  expect(await screen.findByText("channel:write")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /connect channel/i })).not.toBeInTheDocument();
});

it("shows a generated secret once, and forgets it when told it is stored", async () => {
  const seen = brain({
    ...team(["channel:write"]),
    "GET /teams/{id}/channels": { body: { items: [] } },
    "POST /teams/{id}/channels": {
      status: 201,
      body: { ...support, signing_secret: GENERATED },
    },
  });
  show(Channels);

  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "support");
  await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

  expect(await screen.findByText(GENERATED)).toBeInTheDocument();
  expect(screen.getByText("POST /hooks/custom/c1")).toBeInTheDocument();
  expect(posted(seen)).toEqual({ name: "support", provider: "custom" });

  await userEvent.click(screen.getByRole("button", { name: /i have stored it/i }));
  await waitFor(() => expect(screen.queryByText(GENERATED)).not.toBeInTheDocument());

  // The list was asked again, and nothing the brain lists carries a secret:
  // the dialog was the only place it ever lived.
  expect(seen.filter((s) => s.method === "GET" && s.path.includes("/channels")).length).toBe(2);
  await userEvent.click(screen.getByRole("button", { name: /connect channel/i }));
  expect(screen.queryByText(GENERATED)).not.toBeInTheDocument();
});

// A typed provider this build lacks could only be refused, so it is a choice.
it("offers the providers this build has as a choice, by the name people know", async () => {
  brain({ ...team(["channel:write"]), "GET /teams/{id}/channels": { body: { items: [] } } });
  show(Channels);
  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  const provider = screen.getByRole("combobox", { name: /^provider$/i });
  expect(provider).toHaveTextContent("Custom webhook");
  expect(screen.queryByRole("textbox", { name: /^provider$/i })).toBeNull();
});

it("points an empty team at the form, not at a command", async () => {
  brain({ ...team(["channel:write"]), "GET /teams/{id}/channels": { body: { items: [] } } });
  show(Channels);
  expect(await screen.findByText("No channels yet")).toBeInTheDocument();
  expect(screen.queryByText(/oa channels/)).toBeNull();
});

it("sends a pasted secret, and never shows it back", async () => {
  const seen = brain({
    ...team(["channel:write"]),
    "GET /teams/{id}/channels": { body: { items: [] } },
    "POST /teams/{id}/channels": { status: 201, body: support },
  });
  show(Channels);

  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "support");
  await userEvent.click(screen.getByLabelText(/gave me a signing secret/i));
  const field = screen.getByLabelText(/^signing secret$/i);
  expect(field).toHaveAttribute("type", "password");
  expect(field).toHaveAttribute("autocomplete", "new-password");
  await userEvent.type(field, PASTED);
  await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

  expect(await screen.findByText(/in the secret store/i)).toBeInTheDocument();
  expect(posted(seen)).toEqual({ name: "support", provider: "custom", signing_secret: PASTED });
  expect(screen.queryByText(PASTED.trim())).not.toBeInTheDocument();
});

it("keeps the form open with the brain's reason when it refuses", async () => {
  brain({
    ...team(["channel:write"]),
    "GET /teams/{id}/channels": { body: { items: [support] } },
    "POST /teams/{id}/channels": {
      status: 409,
      text: "a channel with that name already exists",
    },
  });
  show(Channels);

  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "support");
  await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

  expect(await screen.findByRole("alert")).toHaveTextContent(
    "a channel with that name already exists",
  );
  expect(screen.getByLabelText(/^name$/i)).toHaveValue("support");
});

it("forgets a typed secret when the form is cancelled", async () => {
  brain({ ...team(["channel:write"]), "GET /teams/{id}/channels": { body: { items: [] } } });
  show(Channels);

  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  await userEvent.click(screen.getByLabelText(/gave me a signing secret/i));
  await userEvent.type(screen.getByLabelText(/^signing secret$/i), "half typed");
  await userEvent.click(screen.getByRole("button", { name: /^cancel$/i }));

  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  expect(screen.queryByLabelText(/^signing secret$/i)).not.toBeInTheDocument();
  expect(screen.getByLabelText(/gave me a signing secret/i)).not.toBeChecked();
});

it("hands over the full URL when the brain knows its public address", async () => {
  brain({
    ...team(["channel:write"]),
    "GET /teams/{id}/channels": { body: { items: [] } },
    "POST /teams/{id}/channels": {
      status: 201,
      body: { ...support, hook_url: HOOK_URL, signing_secret: GENERATED },
    },
  });
  show(Channels);

  await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "support");
  await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

  expect(await screen.findByText(`POST ${HOOK_URL}`)).toBeInTheDocument();
  expect(screen.getByText("Webhook URL")).toBeInTheDocument();
  expect(screen.queryByText("Webhook path")).not.toBeInTheDocument();
});

describe("the list", () => {
  it("says when each channel last carried a message, and how much in a day", async () => {
    const fresh = { ...support, id: "c2", name: "fresh", last_message_at: null, sessions_24h: 0 };
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [support, fresh] } } });
    show(Channels);

    await screen.findByRole("button", { name: "support" });
    expect(within(rowOf("support")).getByText("custom")).toBeInTheDocument();
    expect(within(rowOf("support")).getByText("3 sessions")).toBeInTheDocument();
    expect(rowOf("support").querySelector("time")).toHaveAttribute("datetime", STAMP);
    expect(within(rowOf("fresh")).getByText("Never")).toBeInTheDocument();
    expect(within(rowOf("fresh")).getByText("0 sessions")).toBeInTheDocument();
  });

  it("shows the brain's error with a way to try again", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { status: 500, text: "the store is down" } });
    show(Channels);

    expect(await screen.findByText(/the store is down/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
  });
});

// Quiet means it was used and then went silent; never used is not quiet.
describe("a quiet channel", () => {
  it.each([
    [{ last_message_at: STAMP, sessions_24h: 0 }, true],
    [{ last_message_at: STAMP, sessions_24h: 2 }, false],
    [{ last_message_at: null, sessions_24h: 0 }, false],
  ])("%o is quiet: %s", (patch, said) => {
    expect(isQuiet({ ...support, ...patch })).toBe(said);
  });

  const lists = (items: unknown[], next?: string) => ({
    "GET /teams/{id}/channels": { body: { items, ...(next ? { next_cursor: next } : {}) } },
  });
  const silent = { ...support, id: "c2", name: "silent", sessions_24h: 0 };

  it("is counted for someone who reads every conversation", async () => {
    brain({ ...team(["session:read_all"]), ...lists([support, silent]) });
    show(Channels);

    expect(
      await screen.findByText("1 channel had no message in the last day."),
    ).toBeInTheDocument();
  });

  // The list counts only the sessions this person may open: their own
  // quiet is not the channel's.
  it("is not counted for someone who sees only their own conversations", async () => {
    brain({ ...team([]), ...lists([support, silent]) });
    show(Channels);

    await screen.findByRole("button", { name: "silent" });
    expect(screen.queryByText(/no message in the last day/)).not.toBeInTheDocument();
  });

  it("is not counted from a page of the list", async () => {
    brain({ ...team(["session:read_all"]), ...lists([support, silent], "more") });
    show(Channels);

    await screen.findByRole("button", { name: "silent" });
    expect(screen.queryByText(/no message in the last day/)).not.toBeInTheDocument();
  });
});

describe("a channel's sheet", () => {
  // Under a real router: the sheet links to the channel's sessions.
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);

  it("gives the brain's URL, and the secret only as where it is kept", async () => {
    brain({
      ...team([]),
      "GET /teams/{id}/channels": { body: { items: [{ ...support, hook_url: HOOK_URL }] } },
    });
    at();

    const sheet = await opened("support");
    expect(within(sheet).getByText("Webhook URL")).toBeInTheDocument();
    expect(within(sheet).getByText(`POST ${HOOK_URL}`)).toBeInTheDocument();
    expect(within(sheet).getByText(/in the secret store/i)).toBeInTheDocument();
  });

  // One the brain gave no URL for keeps its path, rather than a guess.
  it("gives the path when the brain has no public address", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [support] } } });
    at();

    const sheet = await opened("support");
    expect(within(sheet).getByText("Webhook path")).toBeInTheDocument();
    expect(within(sheet).getByText("POST /hooks/custom/c1")).toBeInTheDocument();
  });

  it("opens the channel's sessions", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [support] } } });
    at();

    const sheet = await opened("support");
    expect(within(sheet).getByRole("link", { name: /open its sessions/i })).toHaveAttribute(
      "href",
      "/ui/sessions?channel=c1",
    );
  });

  it("opens from the keyboard", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [support] } } });
    at();

    await screen.findByRole("button", { name: "support" });
    rowOf("support").focus();
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("dialog", { name: "support" })).toBeInTheDocument();
  });

  it("offers no disconnect without channel:write", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [support] } } });
    at();

    const sheet = await opened("support");
    expect(within(sheet).queryByRole("button", { name: /disconnect/i })).not.toBeInTheDocument();
  });

  it("disconnects once it is confirmed, and shows the brain's refusal", async () => {
    let refused = true;
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [support] } },
      "DELETE /teams/{id}/channels/{channelID}": () =>
        refused ? { status: 500, text: "the secret store is sealed" } : { status: 204 },
    });
    at();

    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: /^disconnect$/i }));
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);

    const confirm = await screen.findByRole("dialog", { name: "Disconnect support?" });
    await userEvent.click(within(confirm).getByRole("button", { name: /^disconnect$/i }));
    expect(await within(confirm).findByRole("alert")).toHaveTextContent(
      "the secret store is sealed",
    );

    refused = false;
    await userEvent.click(within(confirm).getByRole("button", { name: /^disconnect$/i }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Disconnect support?" })).not.toBeInTheDocument(),
    );
    expect(seen.filter((s) => s.method === "DELETE").map((s) => s.path)).toEqual([
      `/teams/${TEAM}/channels/c1`,
      `/teams/${TEAM}/channels/c1`,
    ]);
    // The list is read again, so the channel leaves it.
    expect(seen.filter((s) => s.method === "GET" && s.path.includes("/channels")).length).toBe(2);
  });
});

describe("where a channel's replies go", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const replying = { ...support, can_reply: true, takes_reply_url: true };
  const URL_ = "https://support.example.com/replies";

  function patched(seen: { method: string; path?: string; body: BodyInit | null | undefined }[]) {
    const patch = seen.find((s) => s.method === "PATCH");
    return patch ? JSON.parse(String(patch.body)) : undefined;
  }

  it("says where they go, or that they are not sent", async () => {
    brain({
      ...team([]),
      "GET /teams/{id}/channels": { body: { items: [{ ...replying, reply_url: URL_ }] } },
    });
    at();
    const sheet = await opened("support");
    expect(within(sheet).getByText("Replies to")).toBeInTheDocument();
    expect(within(sheet).getByText(URL_)).toBeInTheDocument();
  });

  it("says a channel with no reply URL sends no replies", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [replying] } } });
    at();
    const sheet = await opened("support");
    expect(
      within(sheet).getByText(/not set, so the team's replies are not sent/i),
    ).toBeInTheDocument();
  });

  // A provider that cannot reply has nothing to set, and a brain from before
  // the field is read as one that cannot.
  it("is not offered where the provider cannot reply", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [support] } },
    });
    at();
    const sheet = await opened("support");
    expect(within(sheet).queryByText("Replies to")).toBeNull();
    expect(within(sheet).queryByRole("button", { name: /where replies go/i })).toBeNull();
  });

  it("offers no edit without channel:write", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [replying] } } });
    at();
    const sheet = await opened("support");
    expect(within(sheet).queryByRole("button", { name: /where replies go/i })).toBeNull();
  });

  // The spaces are dropped by the url input itself; as a text input it would
  // send them, and the brain would refuse the URL.
  it("saves a reply URL, without the spaces around it, and shows it", async () => {
    let saved = false;
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": () => ({
        body: { items: [saved ? { ...replying, reply_url: URL_ } : replying] },
      }),
      "PATCH /teams/{id}/channels/{cid}": () => {
        saved = true;
        return { body: { ...replying, reply_url: URL_ } };
      },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: "Set where replies go" }));
    const dialog = await screen.findByRole("dialog", { name: "Replies from support" });
    await userEvent.type(within(dialog).getByLabelText("Reply URL"), `  ${URL_} `);
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(patched(seen)).toEqual({ reply_url: URL_ }));
    expect(await screen.findByText(URL_)).toBeInTheDocument();
  });

  it("stops replies with null, and only once there is something to stop", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [{ ...replying, reply_url: URL_ }] } },
      "PATCH /teams/{id}/channels/{cid}": { body: replying },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: "Change where replies go" }));
    const dialog = await screen.findByRole("dialog", { name: "Replies from support" });
    expect(within(dialog).getByText(/gives up any reply still waiting/i)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Stop replies" }));

    await waitFor(() => expect(patched(seen)).toEqual({ reply_url: null }));
  });

  // The brain's reason is the useful thing: it names what to change.
  it("shows why the brain refused a URL, and keeps the dialog open", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [replying] } },
      "PATCH /teams/{id}/channels/{cid}": {
        status: 400,
        text: "reply_url must not have a query or a fragment; the signature is what authenticates a reply",
      },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: "Set where replies go" }));
    const dialog = await screen.findByRole("dialog", { name: "Replies from support" });
    await userEvent.type(within(dialog).getByLabelText("Reply URL"), `${URL_}?token=1`);
    await userEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("must not have a query");
    expect(screen.getByRole("dialog", { name: "Replies from support" })).toBeInTheDocument();
  });
});

describe("a telegram channel", () => {
  const TOKEN = "123456:TEST-ONLY-NOT-A-TOKEN";
  const telegram = {
    ...support,
    id: "t1",
    provider: "telegram",
    name: "bot",
    can_reply: true,
    takes_reply_url: false,
  };
  const TG_HOOK = "https://hooks.example.com/hooks/telegram/t1";

  // A Radix select opens from the keyboard in jsdom, which has no pointer
  // capture, and scrolls its highlighted option into view, which it lacks.
  async function chosen() {
    Element.prototype.scrollIntoView = vi.fn();
    onTestFinished(() => {
      delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    });
    await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "bot");
    screen.getByRole("combobox", { name: /^provider$/i }).focus();
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.click(await screen.findByRole("option", { name: "Telegram" }));
  }

  it("asks for a bot token, and not for the provider's signing secret", async () => {
    brain({ ...team(["channel:write"]), "GET /teams/{id}/channels": { body: { items: [] } } });
    show(Channels);
    await chosen();
    const field = screen.getByLabelText(/^bot token$/i);
    expect(field).toHaveAttribute("type", "password");
    expect(screen.getByText(/from @BotFather/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/gave me a signing secret/i)).toBeNull();
  });

  it("sends the token, shows the setWebhook step with a placeholder, and never the token", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": {
        status: 201,
        body: { ...telegram, hook_url: TG_HOOK, signing_secret: GENERATED },
      },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

    const dialog = await screen.findByRole("dialog", { name: /connected bot/i });
    expect(posted(seen)).toEqual({
      name: "bot",
      provider: "telegram",
      credentials: { bot_token: TOKEN },
    });
    const text = dialog.textContent ?? "";
    expect(text).toContain("bot<bot token>/setWebhook");
    expect(text).toContain(`url=${TG_HOOK}`);
    expect(text).toContain(`secret_token=${GENERATED}`);
    expect(text).not.toContain(TOKEN);
  });

  it("does not show the setWebhook step for a custom channel", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": {
        status: 201,
        body: { ...support, hook_url: HOOK_URL, signing_secret: GENERATED },
      },
    });
    show(Channels);
    await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "support");
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));
    await screen.findByRole("dialog", { name: /connected support/i });
    expect(screen.queryByText(/setWebhook/)).toBeNull();
  });

  it("keeps the form open with the brain's reason when Telegram refuses the token", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": { status: 400, text: "Telegram refused the bot token" },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Telegram refused the bot token");
    expect(screen.getByLabelText(/^name$/i)).toHaveValue("bot");
  });

  it("says replies go into the chat, and offers no reply URL even to a writer", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [telegram] } },
    });
    showAt("/ui/channels/", [["/ui/channels/", Route]]);
    const sheet = await opened("bot");
    expect(within(sheet).getByText(/back into the telegram chat/i)).toBeInTheDocument();
    expect(within(sheet).queryByRole("button", { name: /where replies go/i })).toBeNull();
  });

  it("builds the command with the placeholder, the URL and the secret", () => {
    expect(setWebhookCommand({ ...telegram, hook_url: TG_HOOK, signing_secret: GENERATED })).toBe(
      [
        "curl https://api.telegram.org/bot<bot token>/setWebhook \\",
        `  -d url=${TG_HOOK} \\`,
        `  -d secret_token=${GENERATED} \\`,
        `  -d 'allowed_updates=["message"]'`,
      ].join("\n"),
    );
  });
});
