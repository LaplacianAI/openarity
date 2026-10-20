import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import { brain, pageOf, show, TEAM, team } from "@/test/brain";

const { Route, channelRequest, hookAddress, hookPath } = await import("@/routes/ui.channels.index");
const Channels = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const support = { id: "c1", team_id: TEAM, provider: "custom", name: "support" };
const GENERATED = "oawh_generated-in-the-brain";
const PASTED = " pasted secret ";
// Not what the page would build from its own origin: the listener is another host.
const HOOK_URL = "https://hooks.example.com/hooks/custom/c1";

function posted(seen: { method: string; body: BodyInit | null | undefined }[]) {
  const post = seen.find((s) => s.method === "POST");
  return post ? JSON.parse(String(post.body)) : undefined;
}

describe("the create form", () => {
  const draft = { name: " support ", provider: " custom ", ownSecret: false, secret: "" };

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
  expect(screen.queryByRole("button", { name: /new channel/i })).not.toBeInTheDocument();
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

  await userEvent.click(await screen.findByRole("button", { name: /new channel/i }));
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
  await userEvent.click(screen.getByRole("button", { name: /new channel/i }));
  expect(screen.queryByText(GENERATED)).not.toBeInTheDocument();
});

it("sends a pasted secret, and never shows it back", async () => {
  const seen = brain({
    ...team(["channel:write"]),
    "GET /teams/{id}/channels": { body: { items: [] } },
    "POST /teams/{id}/channels": { status: 201, body: support },
  });
  show(Channels);

  await userEvent.click(await screen.findByRole("button", { name: /new channel/i }));
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

  await userEvent.click(await screen.findByRole("button", { name: /new channel/i }));
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

  await userEvent.click(await screen.findByRole("button", { name: /new channel/i }));
  await userEvent.click(screen.getByLabelText(/gave me a signing secret/i));
  await userEvent.type(screen.getByLabelText(/^signing secret$/i), "half typed");
  await userEvent.click(screen.getByRole("button", { name: /^cancel$/i }));

  await userEvent.click(await screen.findByRole("button", { name: /new channel/i }));
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

  await userEvent.click(await screen.findByRole("button", { name: /new channel/i }));
  await userEvent.type(screen.getByLabelText(/^name$/i), "support");
  await userEvent.click(screen.getByRole("button", { name: /^connect$/i }));

  expect(await screen.findByText(`POST ${HOOK_URL}`)).toBeInTheDocument();
  expect(screen.getByText("Webhook URL")).toBeInTheDocument();
  expect(screen.queryByText("Webhook path")).not.toBeInTheDocument();
});

it("lists each channel's URL, so it can be found again after the dialog closes", async () => {
  const other = { id: "c2", team_id: TEAM, provider: "custom", name: "other" };
  brain({
    ...team([]),
    "GET /teams/{id}/channels": { body: { items: [{ ...support, hook_url: HOOK_URL }, other] } },
  });
  show(Channels);

  expect(await screen.findByText(HOOK_URL)).toBeInTheDocument();
  // One the brain gave no URL for keeps its id, rather than a guess.
  expect(screen.getByText("c2")).toBeInTheDocument();
});
