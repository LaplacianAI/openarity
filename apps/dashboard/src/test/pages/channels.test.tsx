import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, STAMP, show, showAt, TEAM, team } from "@/test/brain";

const {
  Route,
  PROVIDERS,
  channelRequest,
  hookAddress,
  hookPath,
  isQuiet,
  opensToLink,
  setWebhookCommand,
  slackManifestURL,
  SLACK_PLACEHOLDER_URL,
  triggerWordOK,
} = await import("@/routes/ui.channels.index");
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
    answersIn: "everywhere" as const,
    notify: true,
    mine: false,
    triggerWord: "",
  };

  // A person's own number answers where they chose and tells strangers
  // nothing; the bot's own sends neither setting, so the brain's defaults stand.
  it("sends where a channel answers and its notices only when they differ", () => {
    const wa = { ...draft, provider: "whatsapp" };
    expect(channelRequest(wa)).toEqual({ name: "support", provider: "whatsapp" });
    expect(
      channelRequest({ ...wa, mine: true, answersIn: "groups", triggerWord: "@Fduv" }),
    ).toEqual({
      name: "support",
      provider: "whatsapp",
      answers_in: "groups",
      notify_strangers: false,
      trigger_word: "@Fduv",
    });
    expect(channelRequest({ ...wa, notify: false, answersIn: "direct" })).toEqual({
      name: "support",
      provider: "whatsapp",
      answers_in: "direct",
      notify_strangers: false,
    });
  });

  // On someone's own number the word is what keeps the agent quiet, so a
  // channel marked as theirs is not made without one; a word is sent trimmed,
  // and only for WhatsApp, the one provider called by a word.
  it("sends the trigger word, and asks for one on a number that is yours", () => {
    const wa = { ...draft, provider: "whatsapp" };
    expect(channelRequest({ ...wa, mine: true })).toBe(
      "Pick the word that calls the agent, such as @Fduv.",
    );
    expect(channelRequest({ ...wa, mine: true, triggerWord: "  @Fduv " })).toEqual({
      name: "support",
      provider: "whatsapp",
      notify_strangers: false,
      trigger_word: "@Fduv",
    });
    expect(channelRequest({ ...wa, triggerWord: "two words" })).toMatch(/one word/);
    expect(channelRequest({ ...draft, triggerWord: "@Fduv" })).toEqual({
      name: "support",
      provider: "custom",
    });
  });

  it("takes a trigger word that is one word with a letter or digit in it", () => {
    expect(triggerWordOK("@Fduv")).toBe(true);
    expect(triggerWordOK("ok2")).toBe(true);
    expect(triggerWordOK("@@@")).toBe(false);
    expect(triggerWordOK("two words")).toBe(false);
    expect(triggerWordOK("a".repeat(33))).toBe(false);
    expect(triggerWordOK("a".repeat(32))).toBe(true);
  });

  // A custom webhook cannot tell strangers anything, so it never says so.
  it("sends no notices setting for a provider that sends none", () => {
    expect(channelRequest({ ...draft, notify: false, answersIn: "groups" })).toEqual({
      name: "support",
      provider: "custom",
      answers_in: "groups",
    });
  });

  // A linked number has no webhook to sign: no secret is sent, even one typed
  // before the provider was changed.
  it("sends a WhatsApp channel with no secret", () => {
    expect(
      channelRequest({ ...draft, provider: "whatsapp", ownSecret: true, secret: "typed" }),
    ).toEqual({ name: "support", provider: "whatsapp" });
  });

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

  describe("for slack", () => {
    const sl = { ...draft, provider: "slack", botToken: " xoxb-TEST-ONLY ", secret: " s3cret " };

    it("sends the bot token as a credential and Slack's own secret as typed", () => {
      expect(channelRequest(sl)).toEqual({
        name: "support",
        provider: "slack",
        signing_secret: " s3cret ",
        credentials: { bot_token: "xoxb-TEST-ONLY" },
      });
    });

    it("refuses a missing token or secret before asking the brain", () => {
      expect(channelRequest({ ...sl, botToken: " " })).toMatch(/bot token/i);
      expect(channelRequest({ ...sl, secret: " " })).toMatch(/signing secret/i);
    });
  });

  // Slack's create-from-manifest page, filled in: a bot that hears what it
  // is sent and what is said where it is, answers, and reads shared files.
  it("builds Slack's manifest link for the app", () => {
    const url = new URL(slackManifestURL("  acme  "));
    expect(url.origin + url.pathname).toBe("https://api.slack.com/apps");
    expect(url.searchParams.get("new_app")).toBe("1");
    const m = JSON.parse(url.searchParams.get("manifest_json") ?? "{}");
    expect(m.display_information.name).toBe("acme");
    expect(m.features.bot_user.display_name).toBe("acme");
    expect(m.features.app_home.messages_tab_read_only_enabled).toBe(false);
    expect(m.oauth_config.scopes.bot).toEqual([
      "channels:history",
      "groups:history",
      "im:history",
      "mpim:history",
      "chat:write",
      "files:read",
    ]);
    expect(m.settings.event_subscriptions).toEqual({
      request_url: SLACK_PLACEHOLDER_URL,
      bot_events: ["message.channels", "message.groups", "message.im", "message.mpim"],
    });
    // The buttons under an approval answer through Interactivity, beside the events.
    expect(m.settings.interactivity).toEqual({
      is_enabled: true,
      request_url: `${SLACK_PLACEHOLDER_URL}/interactivity`,
    });
    expect(m.settings.socket_mode_enabled).toBe(false);
    const unnamed = JSON.parse(
      new URL(slackManifestURL(" ")).searchParams.get("manifest_json") ?? "{}",
    );
    expect(unnamed.display_information.name).toBe("Openarity");
    const long = JSON.parse(
      new URL(slackManifestURL("x".repeat(50))).searchParams.get("manifest_json") ?? "{}",
    );
    expect(long.display_information.name).toHaveLength(35);
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
    expect(within(sheet).queryByText(/interactivity/i)).not.toBeInTheDocument();
  });

  // Closing the setup dialog loses nothing: Slack's second address is here too.
  it("gives a Slack channel's Interactivity URL beside its events URL", async () => {
    const hook = "https://hooks.example.com/hooks/slack/s1";
    const slack = { ...support, id: "s1", provider: "slack", name: "acme", hook_url: hook };
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [slack] } } });
    at();

    const sheet = await opened("acme");
    expect(within(sheet).getByText(`POST ${hook}`)).toBeInTheDocument();
    expect(within(sheet).getByText("Interactivity URL")).toBeInTheDocument();
    expect(within(sheet).getByText(`POST ${hook}/interactivity`)).toBeInTheDocument();
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

  // Every team has the dashboard's channel, made with the team: there is
  // nothing to disconnect, and no webhook reaches it.
  it("shows the dashboard's channel as built in, with no disconnect and no webhook", async () => {
    const dashboard = {
      ...support,
      id: "dash",
      name: "Dashboard",
      provider: "dashboard",
      hook_url: undefined,
    };
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [dashboard] } },
    });
    at();

    const sheet = await opened("Dashboard");
    expect(
      within(sheet).getByText("Built in: every team has this channel, so it cannot be deleted."),
    ).toBeInTheDocument();
    expect(within(sheet).queryByRole("button", { name: /disconnect/i })).not.toBeInTheDocument();
    expect(within(sheet).queryByText(/Webhook/)).not.toBeInTheDocument();
    expect(within(sheet).queryByText(/Signing secret/)).not.toBeInTheDocument();
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

describe("a slack channel", () => {
  const TOKEN = "xoxb-TEST-ONLY-NOT-A-TOKEN";
  const SECRET = "TEST-ONLY-signing-secret";
  const SL_HOOK = "https://hooks.example.com/hooks/slack/s1";
  const slack = {
    ...support,
    id: "s1",
    provider: "slack",
    name: "acme",
    can_reply: true,
    takes_reply_url: false,
  };

  async function chosen() {
    Element.prototype.scrollIntoView = vi.fn();
    onTestFinished(() => {
      delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    });
    await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
    await userEvent.type(screen.getByLabelText(/^name$/i), "acme");
    screen.getByRole("combobox", { name: /^provider$/i }).focus();
    await userEvent.keyboard("{ArrowDown}");
    await userEvent.click(await screen.findByRole("option", { name: "Slack" }));
  }

  it("walks through making the app, and asks for both values as passwords", async () => {
    brain({ ...team(["channel:write"]), "GET /teams/{id}/channels": { body: { items: [] } } });
    show(Channels);
    await chosen();
    const steps = screen.getByRole("list", { name: /connecting slack/i });
    expect(within(steps).getAllByRole("listitem")).toHaveLength(3);
    const create = within(steps).getByRole("link", { name: /create the slack app/i });
    expect(create).toHaveAttribute("target", "_blank");
    expect(create).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(create.getAttribute("href")).toContain("manifest_json=");
    expect(screen.getByLabelText(/^bot token$/i)).toHaveAttribute("type", "password");
    expect(screen.getByLabelText(/^signing secret$/i)).toHaveAttribute("type", "password");
    expect(screen.queryByLabelText(/gave me a signing secret/i)).toBeNull();
  });

  it("sends both, then gives the Request URL to paste, and never shows either back", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": { status: 201, body: { ...slack, hook_url: SL_HOOK } },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.type(screen.getByLabelText(/^signing secret$/i), SECRET);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

    const dialog = await screen.findByRole("dialog", { name: /one step left in slack/i });
    expect(posted(seen)).toEqual({
      name: "acme",
      provider: "slack",
      signing_secret: SECRET,
      credentials: { bot_token: TOKEN },
    });
    const text = dialog.textContent ?? "";
    expect(text).toContain(SL_HOOK);
    expect(text).toMatch(/event subscriptions/i);
    expect(
      within(dialog).getByRole("button", { name: `Copy the Request URL ${SL_HOOK}` }),
    ).toBeInTheDocument();
    // The approval buttons' address, under Interactivity, beside the events'.
    expect(text).toMatch(/interactivity & shortcuts/i);
    expect(
      within(dialog).getByRole("button", {
        name: `Copy the Interactivity Request URL ${SL_HOOK}/interactivity`,
      }),
    ).toBeInTheDocument();
    expect(within(within(dialog).getByRole("list")).getAllByRole("listitem")).toHaveLength(3);
    expect(text).not.toContain(TOKEN);
    expect(text).not.toContain(SECRET);
  });

  it("says Slack cannot reach a brain with no public address", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": { status: 201, body: slack },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.type(screen.getByLabelText(/^signing secret$/i), SECRET);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));
    const dialog = await screen.findByRole("dialog", { name: /one step left in slack/i });
    // Said once for both addresses, not under each step.
    expect(dialog.textContent?.match(/no public address/gi)).toHaveLength(1);
    expect(within(dialog).queryByRole("button", { name: /^copy the/i })).not.toBeInTheDocument();
    expect(dialog.textContent).toContain("/hooks/slack/s1");
    expect(dialog.textContent).toContain("/hooks/slack/s1/interactivity");
  });

  it("keeps the form open with the brain's reason when Slack refuses the token", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": { status: 400, body: "the bot token is not one Slack knows" },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.type(screen.getByLabelText(/^signing secret$/i), SECRET);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent("not one Slack knows");
    expect(screen.getByLabelText(/^bot token$/i)).toHaveValue(TOKEN);
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

  // A bot whose privacy mode is on is connected, and the person is told what
  // it will miss in groups and how to fix it.
  it("shows the brain's warning about the bot", async () => {
    const WARNING = "This bot's privacy mode is on, so in a group it only sees commands to it.";
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": {
        status: 201,
        body: { ...telegram, hook_url: TG_HOOK, connected: true, warnings: [WARNING] },
      },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

    const dialog = await screen.findByRole("dialog", { name: /connected bot/i });
    expect(within(dialog).getByRole("alert")).toHaveTextContent(WARNING);
  });

  it("shows no warning when there is none", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": {
        status: 201,
        body: { ...telegram, hook_url: TG_HOOK, connected: true },
      },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

    const dialog = await screen.findByRole("dialog", { name: /connected bot/i });
    expect(within(dialog).queryByRole("alert")).toBeNull();
  });

  // The brain pointed the bot at the channel itself: there is nothing to run,
  // and no secret the person needs.
  it("says the bot is connected, with no step left, when the brain connected it", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": {
        status: 201,
        body: { ...telegram, hook_url: TG_HOOK, signing_secret: GENERATED, connected: true },
      },
    });
    show(Channels);
    await chosen();
    await userEvent.type(screen.getByLabelText(/^bot token$/i), TOKEN);
    await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

    const dialog = await screen.findByRole("dialog", { name: /connected bot/i });
    const text = dialog.textContent ?? "";
    expect(text).toMatch(/the bot now sends its messages here/i);
    expect(text).not.toContain("setWebhook");
    expect(text).not.toContain(GENERATED);
    expect(within(dialog).getByRole("button", { name: "Done" })).toBeInTheDocument();
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

describe("telling senders nobody has let in", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const bot = { ...support, provider: "telegram", can_notify: true, notify_strangers: true };

  function patched(seen: { method: string; body: BodyInit | null | undefined }[]) {
    const patch = seen.find((s) => s.method === "PATCH");
    return patch ? JSON.parse(String(patch.body)) : undefined;
  }

  it("says whether they are told", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [bot] } } });
    at();
    const sheet = await opened("support");
    expect(within(sheet).getByText("Waiting senders")).toBeInTheDocument();
    expect(within(sheet).getByText(/told once that they are waiting/i)).toBeInTheDocument();
  });

  // A custom channel sends no notices, and a brain from before the field is
  // read as one that sends none: there is nothing to switch.
  it("is not offered where the provider sends no notices", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [support] } },
    });
    at();
    const sheet = await opened("support");
    expect(within(sheet).queryByText("Waiting senders")).toBeNull();
  });

  it("offers no switch without channel:write", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [bot] } } });
    at();
    const sheet = await opened("support");
    expect(within(sheet).queryByRole("button", { name: /stop telling them/i })).toBeNull();
  });

  // Only the switch is sent: the reply URL is left as it is.
  it("switches it off, sending only that, and says so", async () => {
    let off = false;
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": () => ({
        body: { items: [off ? { ...bot, notify_strangers: false } : bot] },
      }),
      "PATCH /teams/{id}/channels/{cid}": () => {
        off = true;
        return { body: { ...bot, notify_strangers: false } };
      },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: /stop telling them/i }));

    expect(await within(sheet).findByText(/not told anything/i)).toBeInTheDocument();
    expect(patched(seen)).toEqual({ notify_strangers: false });
    expect(await within(sheet).findByRole("button", { name: /^tell them$/i })).toBeInTheDocument();
  });

  it("shows the brain's refusal", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [bot] } },
      "PATCH /teams/{id}/channels/{cid}": {
        status: 400,
        body: "the custom provider sends no notices",
      },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: /stop telling them/i }));
    expect(await within(sheet).findByRole("alert")).toHaveTextContent(/sends no notices/);
  });
});

describe("what a conversation is sent while a run works", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const bot = { ...support, provider: "telegram", can_show_events: true, reply_mode: "final" };

  function patched(seen: { method: string; body: BodyInit | null | undefined }[]) {
    const patch = seen.find((s) => s.method === "PATCH");
    return patch ? JSON.parse(String(patch.body)) : undefined;
  }

  it("says the answer goes alone by default", async () => {
    brain({ ...team([]), "GET /teams/{id}/channels": { body: { items: [bot] } } });
    at();
    const sheet = await opened("support");
    expect(within(sheet).getByText("While it works")).toBeInTheDocument();
    expect(within(sheet).getByText(/nothing until the answer/i)).toBeInTheDocument();
    expect(within(sheet).queryByRole("button", { name: /send each step/i })).toBeNull();
  });

  // The dashboard's stream is every step already, and a brain from before
  // the field is read as one whose provider sends none.
  it("is not offered where the provider sends no steps", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [support] } },
    });
    at();
    const sheet = await opened("support");
    expect(within(sheet).queryByText("While it works")).toBeNull();
  });

  it("switches to each step, sending only that, and says each one stays", async () => {
    let events = false;
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": () => ({
        body: { items: [events ? { ...bot, reply_mode: "events" } : bot] },
      }),
      "PATCH /teams/{id}/channels/{cid}": () => {
        events = true;
        return { body: { ...bot, reply_mode: "events" } };
      },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: /send each step/i }));

    expect(await within(sheet).findByText(/every message stays in the chat/i)).toBeInTheDocument();
    expect(patched(seen)).toEqual({ reply_mode: "events" });
    expect(
      await within(sheet).findByRole("button", { name: /send the answer alone/i }),
    ).toBeInTheDocument();
  });

  it("shows the brain's refusal", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [bot] } },
      "PATCH /teams/{id}/channels/{cid}": {
        status: 400,
        body: "the telegram provider sends no steps",
      },
    });
    at();
    const sheet = await opened("support");
    await userEvent.click(within(sheet).getByRole("button", { name: /send each step/i }));
    expect(await within(sheet).findByRole("alert")).toHaveTextContent(/sends no steps/);
  });
});

describe("a WhatsApp channel's number", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const wa = {
    ...support,
    id: "w1",
    name: "whatsapp-support",
    provider: "whatsapp",
    can_pair: true,
    can_show_events: true,
  };
  const QR_TEXT = "2@not-to-be-shown,abc=,def=,1";

  /** A pairing route that answers each GET with the next state, the last repeating. */
  function states(...ps: object[]) {
    let n = 0;
    return () => ({ body: ps[Math.min(n++, ps.length - 1)] });
  }

  // A paired channel opens beside the list to link its number; any other
  // shows its secret once, in the dialog.
  it("opens a channel linked by pairing to link it, and any other to show its secret", () => {
    expect(opensToLink({ ...wa, can_pair: true })).toBe(true);
    expect(opensToLink({ ...support, can_pair: false })).toBe(false);
    expect(opensToLink(support)).toBe(false);
  });

  it("names WhatsApp among the providers", () => {
    expect(PROVIDERS.map(([value]) => value)).toContain("whatsapp");
  });

  it("draws the QR it was given, never its text, and says when the number is linked", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "POST /teams/{id}/channels/{channelID}/pairing": {
        status: 202,
        body: { state: "pairing", method: "qr", held: true },
      },
      "GET /teams/{id}/channels/{channelID}/pairing": states(
        { state: "unpaired", held: true },
        { state: "pairing", method: "qr", qr: QR_TEXT, held: true },
        { state: "connected", phone: "919876543210", held: true },
      ),
    });
    at();

    const sheet = await opened("whatsapp-support");
    await userEvent.click(await within(sheet).findByRole("button", { name: "Show a QR code" }));
    const pairs = seen.filter((s) => s.method === "POST" && s.path.endsWith("/pairing"));
    expect(pairs.map((s) => JSON.parse(String(s.body)))).toEqual([{ method: "qr" }]);
    expect(pairs[0]?.path).toBe(`/teams/${TEAM}/channels/w1/pairing`);

    expect(await within(sheet).findByRole("img", { name: /QR code to scan/ })).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(QR_TEXT);
    expect(await within(sheet).findByText("+919876543210")).toBeInTheDocument();
    expect(within(sheet).queryByRole("img", { name: /QR code/ })).not.toBeInTheDocument();
  });

  it("asks a code for the number typed, and shows it", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "POST /teams/{id}/channels/{channelID}/pairing": {
        status: 202,
        body: { state: "pairing", method: "code", code: "ABCD-1234", held: true },
      },
      "GET /teams/{id}/channels/{channelID}/pairing": states(
        { state: "unpaired", held: true },
        { state: "pairing", method: "code", code: "ABCD-1234", held: true },
      ),
    });
    at();

    const sheet = await opened("whatsapp-support");
    await userEvent.click(await within(sheet).findByRole("button", { name: "Use a pairing code" }));
    expect(within(sheet).getByRole("button", { name: "Get a code" })).toBeDisabled();
    await userEvent.type(within(sheet).getByLabelText(/country code/), " +91 98765 43210 ");
    await userEvent.click(within(sheet).getByRole("button", { name: "Get a code" }));

    const body = posted(seen.filter((s) => s.path.endsWith("/pairing")));
    expect(body).toEqual({ method: "code", phone: "+91 98765 43210" });
    expect(await within(sheet).findByText("ABCD-1234")).toBeInTheDocument();
  });

  // The QR is what lets a phone link the channel: someone who cannot change
  // channels is never shown it, and it is never even asked for.
  it("never reads the pairing for someone without channel:write", async () => {
    const seen = brain({
      ...team([]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": {
        body: { state: "pairing", qr: QR_TEXT, held: true },
      },
    });
    at();

    const sheet = await opened("whatsapp-support");
    expect(within(sheet).getByText(/needs channel:write/)).toBeInTheDocument();
    expect(seen.some((s) => s.path.endsWith("/pairing"))).toBe(false);
    expect(within(sheet).queryByRole("button", { name: /QR code/ })).not.toBeInTheDocument();
  });

  it("says why the last pairing stopped, and when no role runs", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": {
        body: {
          state: "unpaired",
          error: "The QR code expired before it was scanned.",
          held: false,
        },
      },
    });
    at();

    const sheet = await opened("whatsapp-support");
    expect(await within(sheet).findByText(/expired before it was scanned/)).toBeInTheDocument();
    expect(within(sheet).getByRole("alert")).toHaveTextContent(/runs no WhatsApp role/);
  });

  // Linking another number logs this one out, so it asks first.
  it("asks before replacing a linked number", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "POST /teams/{id}/channels/{channelID}/pairing": {
        status: 202,
        body: { state: "pairing", method: "qr", held: true },
      },
      "GET /teams/{id}/channels/{channelID}/pairing": {
        body: { state: "connected", phone: "919876543210", held: true },
      },
    });
    at();

    const sheet = await opened("whatsapp-support");
    await userEvent.click(
      await within(sheet).findByRole("button", { name: "Link another number" }),
    );
    expect(within(sheet).getByText(/logs \+919876543210 out/)).toBeInTheDocument();
    expect(seen.some((s) => s.method === "POST")).toBe(false);
    await userEvent.click(within(sheet).getByRole("button", { name: "Keep this one" }));
    expect(seen.some((s) => s.method === "POST")).toBe(false);

    await userEvent.click(within(sheet).getByRole("button", { name: "Link another number" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Link another number" }));
    expect(posted(seen)).toEqual({ method: "qr" });
  });

  it("shows the brain's reason when it refuses", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": { body: { state: "unpaired", held: true } },
      "POST /teams/{id}/channels/{channelID}/pairing": {
        status: 400,
        text: "phone must be a number with its country code",
      },
    });
    at();

    const sheet = await opened("whatsapp-support");
    await userEvent.click(await within(sheet).findByRole("button", { name: "Use a pairing code" }));
    await userEvent.type(within(sheet).getByLabelText(/country code/), "12");
    await userEvent.click(within(sheet).getByRole("button", { name: "Get a code" }));
    expect(await within(sheet).findByRole("alert")).toHaveTextContent(
      "phone must be a number with its country code",
    );
  });
});

describe("where a channel answers", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const mine = {
    ...support,
    name: "mine",
    provider: "whatsapp",
    answers_in: "everywhere" as const,
  };

  it("is changed in place, one choice saved as it is made", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [mine] } },
      "GET /teams/{id}/channels/{channelID}/pairing": { body: { state: "unpaired", held: true } },
      "PATCH /teams/{id}/channels/{channelID}": { body: { ...mine, answers_in: "groups" } },
    });
    at();

    const sheet = await opened("mine");
    expect(
      within(sheet).getByText(/Direct chats, and groups where it is mentioned\./),
    ).toBeInTheDocument();
    await userEvent.click(within(sheet).getByRole("button", { name: "Change where it answers" }));
    await userEvent.click(
      within(sheet).getByRole("radio", { name: "Only groups, where it is mentioned" }),
    );

    const patch = seen.find((s) => s.method === "PATCH");
    expect(patch?.path).toBe(`/teams/${TEAM}/channels/c1`);
    expect(JSON.parse(String(patch?.body))).toEqual({ answers_in: "groups" });
  });

  it("is only read by someone without channel:write", async () => {
    brain({
      ...team([]),
      "GET /teams/{id}/channels": { body: { items: [{ ...mine, answers_in: "direct" }] } },
    });
    at();

    const sheet = await opened("mine");
    expect(within(sheet).getByText(/Only direct chats\./)).toBeInTheDocument();
    expect(
      within(sheet).queryByRole("button", { name: /where it answers/ }),
    ).not.toBeInTheDocument();
  });

  // The form offers where it answers for every provider, and the notices
  // only for one that can tell a stranger anything: a custom webhook cannot.
  it("is chosen when connecting, and a custom webhook is not offered notices", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [] } },
      "POST /teams/{id}/channels": { status: 201, body: { ...support, signing_secret: GENERATED } },
    });
    show(Channels);

    await userEvent.click(await screen.findByRole("button", { name: /connect channel/i }));
    expect(screen.queryByText(/Tell someone not yet let in/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Whose number is it/)).not.toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Name"), "support");
    await userEvent.click(screen.getByRole("radio", { name: "Only direct chats" }));
    await userEvent.click(screen.getByRole("button", { name: "Connect" }));

    expect(posted(seen)).toEqual({ name: "support", provider: "custom", answers_in: "direct" });
  });
});

describe("a channel's trigger word", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const wa = {
    ...support,
    name: "mine",
    provider: "whatsapp",
    answers_in: "everywhere" as const,
    can_trigger_word: true,
    trigger_word: null,
  };
  const pairing = { body: { state: "connected", phone: "919000000001", held: true } };

  it("is set in place, and the word sent is the one typed, trimmed", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": pairing,
      "PATCH /teams/{id}/channels/{channelID}": { body: { ...wa, trigger_word: "@Fduv" } },
    });
    at();

    const sheet = await opened("mine");
    expect(within(sheet).getByText(/^None\. Direct chats ask the agent/)).toBeInTheDocument();
    await userEvent.click(within(sheet).getByRole("button", { name: "Set a word" }));
    await userEvent.type(within(sheet).getByRole("textbox", { name: "Trigger word" }), " @Fduv ");
    await userEvent.click(within(sheet).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(seen.some((s) => s.method === "PATCH")).toBe(true));
    const patch = seen.find((s) => s.method === "PATCH");
    expect(JSON.parse(String(patch?.body))).toEqual({ trigger_word: "@Fduv" });
  });

  // A word the brain would refuse is said so before anything is sent.
  it("refuses two words without asking the brain", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": pairing,
    });
    at();

    const sheet = await opened("mine");
    await userEvent.click(within(sheet).getByRole("button", { name: "Set a word" }));
    await userEvent.type(within(sheet).getByRole("textbox", { name: "Trigger word" }), "hey bot");
    expect(within(sheet).getByText(/one word of at most 32 characters/)).toBeInTheDocument();
    expect(within(sheet).getByRole("button", { name: "Save" })).toBeDisabled();
    expect(seen.some((s) => s.method === "PATCH")).toBe(false);
  });

  it("is taken away with an empty word", async () => {
    const set = { ...wa, trigger_word: "@Fduv" };
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [set] } },
      "GET /teams/{id}/channels/{channelID}/pairing": pairing,
      "PATCH /teams/{id}/channels/{channelID}": { body: { ...set, trigger_word: null } },
    });
    at();

    const sheet = await opened("mine");
    expect(within(sheet).getByText("@Fduv")).toBeInTheDocument();
    await userEvent.click(within(sheet).getByRole("button", { name: "Change the word" }));
    await userEvent.click(within(sheet).getByRole("button", { name: "Take it away" }));

    await waitFor(() => expect(seen.some((s) => s.method === "PATCH")).toBe(true));
    const patch = seen.find((s) => s.method === "PATCH");
    expect(JSON.parse(String(patch?.body))).toEqual({ trigger_word: "" });
  });

  it("is not offered where a channel cannot be called by a word", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [{ ...support, name: "mine" }] } },
    });
    at();

    const sheet = await opened("mine");
    expect(within(sheet).queryByText("Trigger word")).not.toBeInTheDocument();
  });
});

describe("a WhatsApp number shared with another channel", () => {
  const at = () => showAt("/ui/channels/", [["/ui/channels/", Route]]);
  const wa = {
    ...support,
    id: "w1",
    name: "dhars-whatsapp",
    provider: "whatsapp",
    can_pair: true,
    can_show_events: true,
    trigger_word: null,
  };
  const OTHER_TEAM = "7a1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4e";
  function sharedWith(word: string, editable: boolean) {
    return {
      state: "connected",
      phone: "918939512050",
      held: true,
      needs_trigger_word: true,
      shared_with: [
        {
          channel_id: "o1",
          team_id: OTHER_TEAM,
          name: "shrijeeth-personal",
          trigger_word: word,
          editable,
        },
      ],
    };
  }

  it("asks for this channel's word, then sets it and stops asking", async () => {
    let answered = false;
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": () => ({
        body: answered
          ? {
              state: "connected",
              phone: "918939512050",
              held: true,
              shared_with: sharedWith("@Fduv", true).shared_with,
            }
          : sharedWith("@Fduv", true),
      }),
      "PATCH /teams/{id}/channels/{cid}": () => {
        answered = true;
        return { body: { ...wa, trigger_word: "@Dhars" } };
      },
    });
    at();

    const sheet = await opened("dhars-whatsapp");
    const form = await within(sheet).findByRole("form", {
      name: /trigger words for a shared number/i,
    });
    expect(form).toHaveTextContent("shrijeeth-personal");
    const start = within(form).getByRole("button", { name: /start answering/i });
    expect(start).toBeDisabled();

    await userEvent.type(within(form).getByLabelText(/this channel's trigger word/i), "@Dhars");
    await userEvent.click(start);

    await waitFor(() => expect(seen.some((s) => s.method === "PATCH")).toBe(true));
    const patches = seen.filter((s) => s.method === "PATCH");
    // The other's word is already its own, so only this channel's is sent.
    expect(patches.map((s) => s.path)).toEqual([`/teams/${TEAM}/channels/w1`]);
    expect(JSON.parse(String(patches[0]?.body))).toEqual({ trigger_word: "@Dhars" });
    expect(
      await within(sheet).findByText(/also answers for shrijeeth-personal \(@Fduv\)/),
    ).toBeInTheDocument();
  });

  it("sets the other channel's word first, when it has none and the caller may", async () => {
    const seen = brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": { body: sharedWith("", true) },
      "PATCH /teams/{id}/channels/{cid}": { body: wa },
    });
    at();

    const sheet = await opened("dhars-whatsapp");
    const form = await within(sheet).findByRole("form", {
      name: /trigger words for a shared number/i,
    });
    await userEvent.type(within(form).getByLabelText(/this channel's trigger word/i), "@Dhars");
    await userEvent.type(
      within(form).getByLabelText(/shrijeeth-personal's trigger word/i),
      "@Fduv",
    );
    await userEvent.click(within(form).getByRole("button", { name: /start answering/i }));

    await waitFor(() => expect(seen.filter((s) => s.method === "PATCH")).toHaveLength(2));
    expect(seen.filter((s) => s.method === "PATCH").map((s) => s.path)).toEqual([
      `/teams/${OTHER_TEAM}/channels/o1`,
      `/teams/${TEAM}/channels/w1`,
    ]);
  });

  it("says whom to ask when the other channel's word is not the caller's to set", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": { body: sharedWith("", false) },
    });
    at();

    const sheet = await opened("dhars-whatsapp");
    const form = await within(sheet).findByRole("form", {
      name: /trigger words for a shared number/i,
    });
    expect(within(form).queryByLabelText(/shrijeeth-personal's trigger word/i)).toBeNull();
    expect(form).toHaveTextContent(/ask an admin of its team/i);
  });

  it("shows the brain's refusal of a word", async () => {
    brain({
      ...team(["channel:write"]),
      "GET /teams/{id}/channels": { body: { items: [wa] } },
      "GET /teams/{id}/channels/{channelID}/pairing": { body: sharedWith("@Fduv", true) },
      "PATCH /teams/{id}/channels/{cid}": {
        status: 409,
        body: "shrijeeth-personal shares this channel's number and answers @Fduv; one message could hold both words, so pick another",
      },
    });
    at();

    const sheet = await opened("dhars-whatsapp");
    const form = await within(sheet).findByRole("form", {
      name: /trigger words for a shared number/i,
    });
    await userEvent.type(within(form).getByLabelText(/this channel's trigger word/i), "@fduv");
    await userEvent.click(within(form).getByRole("button", { name: /start answering/i }));
    expect(await within(form).findByRole("alert")).toHaveTextContent(/pick another/);
  });
});
