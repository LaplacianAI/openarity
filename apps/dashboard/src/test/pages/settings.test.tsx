import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { brain, pageOf, show, team } from "@/test/brain";

const { Route, draftOf, settingsRequest } = await import("@/routes/ui.settings");
const Settings = await pageOf(Route);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const NEVER: TeamSettings = {
  model: null,
  search: { provider: "none", url: null, key_set: false },
  learning: false,
  compaction_messages: 100,
  updated_at: null,
};

const BRAVE: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
  search: { provider: "brave", url: null, key_set: true },
  learning: false,
  compaction_messages: 250,
  updated_at: "2026-10-21T09:00:00Z",
};

const OSS: TeamSettings = {
  ...NEVER,
  search: { provider: "firecrawl-oss", url: "http://firecrawl:3002", key_set: true },
  updated_at: "2026-10-21T09:00:00Z",
};

const KEY = "brave-key-typed-here";

function put(seen: { method: string; body: BodyInit | null | undefined }[]) {
  const p = seen.find((s) => s.method === "PUT");
  return p ? JSON.parse(String(p.body)) : undefined;
}

function saved(s: TeamSettings) {
  return { ...s, updated_at: "2026-10-21T10:00:00Z" };
}

describe("the request the form sends", () => {
  it("keeps everything as stored when nothing changed, and sends no key", () => {
    expect(settingsRequest(draftOf(BRAVE), BRAVE)).toEqual({
      model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
      search: { provider: "brave" },
      learning: false,
      compaction_messages: 250,
    });
  });

  it("sends no model when the model is off", () => {
    expect(settingsRequest({ ...draftOf(BRAVE), modelOn: false }, BRAVE)).toMatchObject({
      model: null,
    });
  });

  it("leaves the temperature to the model when it is empty", () => {
    const body = settingsRequest({ ...draftOf(BRAVE), temperature: "" }, BRAVE);
    expect(body).toMatchObject({ model: { name: "gpt-5", max_tokens: 4096 } });
    expect((body as { model: object }).model).not.toHaveProperty("temperature");
  });

  // Trimming a credential changes it.
  it("sends a typed key exactly as typed", () => {
    expect(settingsRequest({ ...draftOf(BRAVE), key: " k " }, BRAVE)).toMatchObject({
      search: { provider: "brave", api_key: " k " },
    });
  });

  it("asks for a key when switching to another provider that needs one", () => {
    expect(settingsRequest({ ...draftOf(BRAVE), provider: "tavily" }, BRAVE)).toMatch(/API key/);
  });

  it("asks for a key for a keyed provider when none is stored", () => {
    expect(settingsRequest({ ...draftOf(NEVER), provider: "firecrawl-cloud" }, NEVER)).toMatch(
      /API key/,
    );
  });

  it("sends a self-hosted provider's address, and none for the others", () => {
    expect(
      settingsRequest(
        { ...draftOf(NEVER), provider: "searxng", url: " http://searxng:8080 " },
        NEVER,
      ),
    ).toMatchObject({ search: { provider: "searxng", url: "http://searxng:8080" } });
    const brave = settingsRequest({ ...draftOf(BRAVE), url: "http://left-over" }, BRAVE);
    expect((brave as { search: object }).search).not.toHaveProperty("url");
  });

  it("refuses a self-hosted provider with no address", () => {
    expect(settingsRequest({ ...draftOf(NEVER), provider: "searxng" }, NEVER)).toMatch(/address/);
  });

  // The brain refuses the same: a key stays with the address it was given for.
  it("asks for the key again when a self-hosted Firecrawl moves", () => {
    expect(settingsRequest({ ...draftOf(OSS), url: "https://elsewhere.example" }, OSS)).toMatch(
      /old address/,
    );
  });

  it("sends an empty key to run a self-hosted Firecrawl without one", () => {
    expect(settingsRequest({ ...draftOf(OSS), dropKey: true }, OSS)).toMatchObject({
      search: { provider: "firecrawl-oss", url: "http://firecrawl:3002", api_key: "" },
    });
  });

  it("keeps a self-hosted Firecrawl's key when nothing about it changed", () => {
    const body = settingsRequest({ ...draftOf(OSS), learning: true }, OSS);
    expect((body as { search: object }).search).not.toHaveProperty("api_key");
  });

  it.each([
    [{ modelName: "  " }, /Name the model/],
    [{ maxTokens: "0" }, /Max tokens/],
    [{ maxTokens: "12.5" }, /Max tokens/],
    [{ temperature: "2.1" }, /Temperature/],
    [{ temperature: "warm" }, /Temperature/],
    [{ compaction: "9" }, /Compaction/],
    [{ compaction: "10001" }, /Compaction/],
  ])("refuses %o before asking the brain", (patch, said) => {
    expect(settingsRequest({ ...draftOf(BRAVE), ...patch }, BRAVE)).toMatch(said);
  });
});

it("shows the defaults for a team that never saved", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
  show(Settings);

  expect(await screen.findByText(/No model/)).toBeInTheDocument();
  expect(screen.getByLabelText(/^provider$/i)).toHaveValue("none");
  expect(screen.getByLabelText(/learn from each run/i)).not.toBeChecked();
  expect(screen.getByLabelText(/messages before a transcript compacts/i)).toHaveValue("100");
});

// Settings are per team; the title says whose they are.
it("names the team whose settings these are", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
  show(Settings);

  expect(
    await screen.findByRole("heading", { name: "Team settings — platform" }),
  ).toBeInTheDocument();
});

// Review Focus 1 from the dashboard: it never has the key, so it must never
// send one it did not type.
it("saves the learning switch without touching the key", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, learning: true }) },
  });
  show(Settings);

  await userEvent.click(await screen.findByLabelText(/learn from each run/i));
  await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen)).toEqual({
    model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
    search: { provider: "brave" },
    learning: true,
    compaction_messages: 250,
  });
});

it("says a key is set without showing one", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  show(Settings);

  expect(await screen.findByText(/a key is set/i)).toBeInTheDocument();
  expect(screen.getByLabelText(/api key/i)).toHaveValue("");
});

it("sends a typed key once and forgets it", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: NEVER },
    "PUT /teams/{id}/settings": {
      body: saved({ ...NEVER, search: { provider: "brave", url: null, key_set: true } }),
    },
  });
  show(Settings);

  await userEvent.selectOptions(await screen.findByLabelText(/^provider$/i), "brave");
  const field = screen.getByLabelText(/api key/i);
  expect(field).toHaveAttribute("type", "password");
  expect(field).toHaveAttribute("autocomplete", "new-password");
  await userEvent.type(field, KEY);
  await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

  await waitFor(() => expect(screen.getByLabelText(/api key/i)).toHaveValue(""));
  expect(put(seen).search).toEqual({ provider: "brave", api_key: KEY });
  expect(document.body.innerHTML).not.toContain(KEY);
});

it("asks for a key when switching to another provider that needs one", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: BRAVE },
  });
  show(Settings);

  await userEvent.selectOptions(await screen.findByLabelText(/^provider$/i), "tavily");
  await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

  expect(await screen.findByRole("alert")).toHaveTextContent(/API key/);
  expect(put(seen)).toBeUndefined();
});

it("is read-only without team:settings", async () => {
  brain({ ...team([]), "GET /teams/{id}/settings": { body: BRAVE } });
  show(Settings);

  expect(await screen.findByText("team:settings")).toBeInTheDocument();
  expect(screen.getByLabelText(/^provider$/i)).toBeDisabled();
  expect(screen.queryByRole("button", { name: /^save$/i })).not.toBeInTheDocument();
});

it("shows the brain's refusal", async () => {
  brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": {
      status: 400,
      text: "search.url must not point at a private, loopback or link-local address",
    },
  });
  show(Settings);

  await userEvent.selectOptions(await screen.findByLabelText(/^provider$/i), "searxng");
  await userEvent.type(screen.getByLabelText(/^address$/i), "http://127.0.0.1");
  await userEvent.click(screen.getByRole("button", { name: /^save$/i }));

  expect(await screen.findByRole("alert")).toHaveTextContent(/link-local/);
});
