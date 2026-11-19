import { createRoute } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { brain, showAt, team } from "@/test/brain";

const { Route, NO_MODEL, draftOf, groupRequest, isDirty, settingsRequest } = await import(
  "@/routes/ui.settings"
);
await (Route.options.component as { preload?: () => Promise<void> }).preload?.();

/** Somewhere to go: showAt hangs it off its own root. */
const Elsewhere = createRoute({
  getParentRoute: () => Route,
  path: "/ui/elsewhere",
  component: () => <p>elsewhere</p>,
});

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const NEVER: TeamSettings = {
  model: null,
  search: { provider: "none", url: null, key_set: false },
  sandbox: { provider: "none", url: null, key_set: false },
  learning: false,
  compaction_messages: 100,
  hub: null,
  updated_at: null,
};

const BRAVE: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
  search: { provider: "brave", url: null, key_set: true },
  sandbox: { provider: "none", url: null, key_set: false },
  learning: false,
  compaction_messages: 250,
  hub: null,
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
  it("offers 200000 max tokens for a team with no model yet, so long answers are not cut off", () => {
    expect(draftOf(NEVER).maxTokens).toBe("200000");
  });

  it("keeps everything as stored when nothing changed, and sends no key", () => {
    expect(settingsRequest(draftOf(BRAVE), BRAVE)).toEqual({
      model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
      search: { provider: "brave" },
      sandbox: { provider: "none" },
      browser: { provider: "none" },
      learning: false,
      compaction_messages: 250,
      hub: null,
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

describe("each group on its own", () => {
  it.each([
    ["model", { modelName: "gpt-6" }, true],
    ["model", { learning: true }, false],
    ["hub", { compaction: "300" }, true],
    ["search", { key: "k" }, true],
    ["search", { dropKey: true }, true],
    ["learning", { learning: true }, true],
  ] as const)("%s is changed by %o: %s", (group, patch, said) => {
    expect(isDirty(group, { ...draftOf(BRAVE), ...patch }, BRAVE)).toBe(said);
  });

  // The picker hands back a new object for the same agent.
  it("is not changed by picking the hub it already has", () => {
    const WITH_HUB: TeamSettings = { ...BRAVE, hub: { id: "a1", name: "triage" } };
    expect(
      isDirty("hub", { ...draftOf(WITH_HUB), hub: { id: "a1", name: "triage" } }, WITH_HUB),
    ).toBe(false);
  });

  // The brain takes the settings whole: a group's Save sends the others as
  // they are stored, not as half-typed.
  it("sends the other groups as stored", () => {
    const d = { ...draftOf(BRAVE), learning: true, modelName: "half-typed", compaction: "9" };
    expect(groupRequest("learning", d, BRAVE)).toEqual({
      model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
      search: { provider: "brave" },
      sandbox: { provider: "none" },
      browser: { provider: "none" },
      learning: true,
      compaction_messages: 250,
      hub: null,
    });
  });

  it("checks only its own fields", () => {
    const d = { ...draftOf(BRAVE), compaction: "9" };
    expect(groupRequest("learning", d, BRAVE)).not.toBeTypeOf("string");
    expect(groupRequest("hub", d, BRAVE)).toMatch(/Compaction/);
  });
});

/** The page under a real router, with somewhere else to go. */
function at() {
  return showAt("/ui/settings", [
    ["/ui/settings", Route],
    ["/ui/elsewhere", Elsewhere],
  ]);
}

/** A group of the page, by its title. */
function group(title: string): HTMLElement {
  return screen.getByRole("region", { name: title });
}

async function saveIn(title: string) {
  await userEvent.click(within(group(title)).getByRole("button", { name: /^save$/i }));
}

it("shows the defaults for a team that never saved", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
  at();

  expect(await screen.findByText(/No model —/)).toBeInTheDocument();
  expect(screen.getByLabelText(/^provider$/i)).toHaveValue("none");
  expect(screen.getByLabelText(/learn from each run/i)).not.toBeChecked();
  expect(screen.getByLabelText(/messages before a transcript compacts/i)).toHaveValue("100");
});

// Settings are per team; the title says whose they are.
it("names the team whose settings these are", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
  at();

  expect(
    await screen.findByRole("heading", { name: "Team settings — platform" }),
  ).toBeInTheDocument();
});

describe("the finding", () => {
  it("says the hub can't run when no model is set", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
    at();

    expect(await screen.findByText(NO_MODEL)).toBeInTheDocument();
  });

  it("says nothing once a model is set", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
    at();

    await screen.findByLabelText(/^provider$/i);
    expect(screen.queryByText(NO_MODEL)).not.toBeInTheDocument();
  });
});

// Review Focus 1 from the dashboard: it never has the key, so it must never
// send one it did not type.
it("saves the learning switch without touching the key", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, learning: true }) },
  });
  at();

  await userEvent.click(await screen.findByLabelText(/learn from each run/i));
  await saveIn("Learning");

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen)).toEqual({
    model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
    search: { provider: "brave" },
    sandbox: { provider: "none" },
    browser: { provider: "none" },
    learning: true,
    compaction_messages: 250,
    hub: null,
  });
  expect(await within(group("Learning")).findByText("Saved")).toBeInTheDocument();
});

it("keeps another group's unsaved edit, unsent, through a save", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, learning: true }) },
  });
  at();

  const model = await screen.findByRole("textbox", { name: /^model$/i });
  await userEvent.clear(model);
  await userEvent.type(model, "gpt-6");
  await userEvent.click(screen.getByLabelText(/learn from each run/i));
  await saveIn("Learning");

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen).model.name).toBe("gpt-5");
  expect(await within(group("Learning")).findByText("Saved")).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: /^model$/i })).toHaveValue("gpt-6");
  expect(within(group("Model")).getByText("Unsaved changes")).toBeInTheDocument();
});

it("offers Save only once something in the group changed", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  at();

  await screen.findByLabelText(/learn from each run/i);
  expect(within(group("Learning")).queryByRole("button", { name: /^save$/i })).toBeNull();
  await userEvent.click(screen.getByLabelText(/learn from each run/i));
  expect(within(group("Learning")).getByRole("button", { name: /^save$/i })).toBeEnabled();
  expect(within(group("Model")).queryByRole("button", { name: /^save$/i })).toBeNull();
});

it("asks before leaving with a change unsaved", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  const router = at();

  await userEvent.click(await screen.findByLabelText(/learn from each run/i));
  act(() => {
    // Not awaited: a blocked move settles only once the dialog is answered.
    void router.navigate({ to: "/ui/elsewhere" } as never);
  });

  expect(await screen.findByRole("heading", { name: "Leave without saving?" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
  await waitFor(() => expect(screen.queryByRole("heading", { name: /leave/i })).toBeNull());
  expect(router.state.location.pathname).toBe("/ui/settings");
});

it("leaves without asking once the change is saved", async () => {
  brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, learning: true }) },
  });
  const router = at();

  await userEvent.click(await screen.findByLabelText(/learn from each run/i));
  await saveIn("Learning");
  await within(group("Learning")).findByText("Saved");
  act(() => {
    // Not awaited: a blocked move settles only once the dialog is answered.
    void router.navigate({ to: "/ui/elsewhere" } as never);
  });

  await waitFor(() => expect(router.state.location.pathname).toBe("/ui/elsewhere"));
  expect(screen.queryByRole("heading", { name: /leave/i })).toBeNull();
});

it("says a key is set without showing one", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  at();

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
  at();

  await userEvent.selectOptions(await screen.findByLabelText(/^provider$/i), "brave");
  const field = screen.getByLabelText(/api key/i);
  expect(field).toHaveAttribute("type", "password");
  expect(field).toHaveAttribute("autocomplete", "new-password");
  await userEvent.type(field, KEY);
  await saveIn("Web search");

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
  at();

  await userEvent.selectOptions(await screen.findByLabelText(/^provider$/i), "tavily");
  await saveIn("Web search");

  expect(await within(group("Web search")).findByRole("alert")).toHaveTextContent(/API key/);
  expect(put(seen)).toBeUndefined();
});

it("is read-only without team:settings", async () => {
  brain({ ...team([]), "GET /teams/{id}/settings": { body: BRAVE } });
  at();

  expect(await screen.findByText("team:settings")).toBeInTheDocument();
  expect(screen.getByLabelText(/^provider$/i)).toBeDisabled();
  expect(screen.queryByRole("button", { name: /^save$/i })).not.toBeInTheDocument();
});

it("shows the brain's error with a way to try again", async () => {
  brain({ ...team([]), "GET /teams/{id}/settings": { status: 500, text: "the store is down" } });
  at();

  expect(await screen.findByText(/the store is down/)).toBeInTheDocument();
  expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument();
});

it("shows the brain's refusal in the group that was saved", async () => {
  brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": {
      status: 400,
      text: "search.url must not point at a private, loopback or link-local address",
    },
  });
  at();

  await userEvent.selectOptions(await screen.findByLabelText(/^provider$/i), "searxng");
  await userEvent.type(screen.getByLabelText(/^address$/i), "http://127.0.0.1");
  await saveIn("Web search");

  expect(await within(group("Web search")).findByRole("alert")).toHaveTextContent(/link-local/);
  expect(within(group("Learning")).queryByRole("alert")).toBeNull();
});

describe("the hub", () => {
  const TRIAGE = { id: "a-triage", name: "triage" };
  const WITH_HUB: TeamSettings = { ...BRAVE, hub: TRIAGE };

  it("sends the chosen hub by name, and null for the built-in orchestrator", () => {
    expect(settingsRequest(draftOf(WITH_HUB), WITH_HUB)).toMatchObject({ hub: "triage" });
    expect(settingsRequest({ ...draftOf(WITH_HUB), hub: null }, WITH_HUB)).toMatchObject({
      hub: null,
    });
  });

  it("says the built-in orchestrator answers when none is chosen", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
    at();

    expect(await screen.findByText("The built-in orchestrator")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Every message reaches the hub first. Leave empty for the built-in orchestrator.",
      ),
    ).toBeInTheDocument();
  });

  it("keeps the chosen hub through a save that changes something else", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: WITH_HUB },
      "PUT /teams/{id}/settings": { body: saved({ ...WITH_HUB, learning: true }) },
    });
    at();

    const hub = await screen.findByRole("list", { name: "Hub" });
    expect(hub).toHaveTextContent("triage");
    await userEvent.click(screen.getByLabelText(/learn from each run/i));
    await saveIn("Learning");

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen).hub).toBe("triage");
  });

  it("clears the hub back to the built-in orchestrator, sending null", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: WITH_HUB },
      "PUT /teams/{id}/settings": { body: saved(BRAVE) },
    });
    at();

    await userEvent.click(await screen.findByRole("button", { name: "Remove triage" }));
    await saveIn("The hub");

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen)).toHaveProperty("hub", null);
  });

  it("chooses an agent of the team as its hub", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: NEVER },
      "GET /teams/{id}/agents": { body: { items: [TRIAGE, { id: "a-billing", name: "billing" }] } },
      "PUT /teams/{id}/settings": {
        body: saved({ ...NEVER, hub: { id: "a-billing", name: "billing" } }),
      },
    });
    at();

    await userEvent.click(await screen.findByRole("button", { name: /add agent/i }));
    await userEvent.click(await screen.findByRole("option", { name: "billing" }));
    await userEvent.keyboard("{Escape}");
    await saveIn("The hub");

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen).hub).toBe("billing");
  });
});

const DAYTONA: TeamSettings = {
  ...NEVER,
  sandbox: {
    provider: "daytona",
    url: "https://daytona.example/api",
    key_set: true,
  },
  updated_at: "2026-11-18T09:00:00Z",
};

describe("the sandbox the form sends", () => {
  it("keeps the stored key while the provider and address are unchanged", () => {
    const body = settingsRequest({ ...draftOf(DAYTONA), learning: true }, DAYTONA);
    expect(body).toMatchObject({
      sandbox: { provider: "daytona", url: "https://daytona.example/api" },
    });
    expect((body as { sandbox: object }).sandbox).not.toHaveProperty("api_key");
  });

  it("asks for the key again when a self-hosted Daytona moves", () => {
    expect(
      settingsRequest({ ...draftOf(DAYTONA), sandboxURL: "https://other.example/api" }, DAYTONA),
    ).toMatch(/old address/);
  });

  it("never carries a key from one provider to another", () => {
    expect(
      settingsRequest({ ...draftOf(DAYTONA), sandbox: "e2b", sandboxURL: "" }, DAYTONA),
    ).toMatch(/never carried/);
  });

  it("sends a typed key exactly as typed, and no address to E2B", () => {
    expect(
      settingsRequest(
        { ...draftOf(NEVER), sandbox: "e2b", sandboxURL: "http://left-over", sandboxKey: " k " },
        NEVER,
      ),
    ).toMatchObject({ sandbox: { provider: "e2b", api_key: " k " } });
    const body = settingsRequest(
      { ...draftOf(NEVER), sandbox: "e2b", sandboxURL: "http://left-over", sandboxKey: "k" },
      NEVER,
    );
    expect((body as { sandbox: object }).sandbox).not.toHaveProperty("url");
  });

  it("refuses sandboxd with no address", () => {
    expect(
      settingsRequest({ ...draftOf(NEVER), sandbox: "sandboxd", sandboxKey: "t" }, NEVER),
    ).toMatch(/address/);
  });

  it("sends only the provider when the sandbox is off", () => {
    expect(settingsRequest({ ...draftOf(DAYTONA), sandbox: "none" }, DAYTONA)).toMatchObject({
      sandbox: { provider: "none" },
    });
  });

  // A brain from before sandboxes sends no sandbox at all.
  it("reads a team with no sandbox field as having none", () => {
    const { sandbox: _, ...old } = NEVER;
    expect(draftOf(old as TeamSettings).sandbox).toBe("none");
  });

  it("saves the sandbox on its own, leaving the search as stored", () => {
    const body = groupRequest(
      "sandbox",
      { ...draftOf(BRAVE), provider: "tavily", sandbox: "e2b", sandboxKey: "k" },
      BRAVE,
    );
    expect(body).toMatchObject({ search: { provider: "brave" }, sandbox: { provider: "e2b" } });
  });
});

describe("an E2B team's template", () => {
  const e2b = (patch: Partial<ReturnType<typeof draftOf>>) =>
    settingsRequest({ ...draftOf(NEVER), sandbox: "e2b", sandboxKey: "k", ...patch }, NEVER);

  it("sends a named template, trimmed", () => {
    expect(e2b({ sandboxTemplate: " acme/openarity-sandbox " })).toMatchObject({
      sandbox: { provider: "e2b", template: "acme/openarity-sandbox" },
    });
  });

  it("sends none when it is left empty, for the default", () => {
    expect((e2b({}) as { sandbox: object }).sandbox).not.toHaveProperty("template");
  });

  it("refuses a template that is not a name", () => {
    expect(e2b({ sandboxTemplate: "my template" })).toMatch(/E2B template/);
  });

  it("is never sent for another provider", () => {
    const body = settingsRequest(
      { ...draftOf(NEVER), sandbox: "daytona", sandboxKey: "k", sandboxTemplate: "x" },
      NEVER,
    );
    expect((body as { sandbox: object }).sandbox).not.toHaveProperty("template");
  });
});

const LIGHTPANDA: TeamSettings = {
  ...NEVER,
  browser: { provider: "lightpanda", url: "ws://lightpanda:9222", key_set: true },
  updated_at: "2026-11-19T09:00:00Z",
};

const KERNEL: TeamSettings = {
  ...NEVER,
  browser: { provider: "kernel", url: null, key_set: true },
  updated_at: "2026-11-19T09:00:00Z",
};

describe("the browser the form sends", () => {
  const ask = (patch: Partial<ReturnType<typeof draftOf>>, stored: TeamSettings = NEVER) =>
    settingsRequest({ ...draftOf(stored), ...patch }, stored);
  const browserOf = (body: unknown) => (body as { browser: Record<string, unknown> }).browser;

  it("keeps the stored key while the provider and address are unchanged", () => {
    const body = ask({ learning: true }, LIGHTPANDA);
    expect(browserOf(body)).toEqual({ provider: "lightpanda", url: "ws://lightpanda:9222" });
  });

  it("keeps a hosted browser's key, and sends it no address", () => {
    expect(browserOf(ask({ browserURL: "ws://left-over" }, KERNEL))).toEqual({
      provider: "kernel",
    });
  });

  it("needs a key for a hosted browser, and never carries one across providers", () => {
    expect(ask({ browser: "browserbase" })).toMatch(/never carried/);
    expect(ask({ browser: "anchor", browserURL: "" }, KERNEL)).toMatch(/never carried/);
  });

  it("takes a self-hosted browser with no key", () => {
    expect(browserOf(ask({ browser: "lightpanda", browserURL: " ws://lightpanda:9222 " }))).toEqual(
      {
        provider: "lightpanda",
        url: "ws://lightpanda:9222",
      },
    );
    expect(browserOf(ask({ browser: "cdp", browserURL: "ws://chrome:9222" }))).toEqual({
      provider: "cdp",
      url: "ws://chrome:9222",
    });
  });

  it("asks for the key again when a self-hosted browser moves, or to drop it", () => {
    expect(ask({ browserURL: "ws://lightpanda-2:9222" }, LIGHTPANDA)).toMatch(/old address/);
    expect(
      browserOf(ask({ browserURL: "ws://lightpanda-2:9222", dropBrowserKey: true }, LIGHTPANDA)),
    ).toEqual({
      provider: "lightpanda",
      url: "ws://lightpanda-2:9222",
      api_key: "",
    });
  });

  it("never sends an empty key to a hosted browser, even when told to drop it", () => {
    expect(ask({ dropBrowserKey: true }, KERNEL)).toMatchObject({
      browser: { provider: "kernel" },
    });
    expect(browserOf(ask({ dropBrowserKey: true }, KERNEL))).not.toHaveProperty("api_key");
  });

  it("refuses a CDP browser with no address, and Bright Data without USER:PASS", () => {
    expect(ask({ browser: "cdp" })).toMatch(/address/);
    expect(ask({ browser: "brightdata", browserKey: "token" })).toMatch(/USER:PASS/);
    expect(browserOf(ask({ browser: "brightdata", browserKey: "u:p" }))).toEqual({
      provider: "brightdata",
      api_key: "u:p",
    });
  });

  // A brain from before browsers sends no browser at all.
  it("reads a team with no browser field as having none", () => {
    expect(draftOf(NEVER).browser).toBe("none");
    expect(browserOf(ask({}))).toEqual({ provider: "none" });
  });

  it("saves the browser on its own, leaving the search as stored", () => {
    const body = groupRequest(
      "browser",
      { ...draftOf(BRAVE), provider: "tavily", browser: "kernel", browserKey: "k" },
      BRAVE,
    );
    expect(body).toMatchObject({
      search: { provider: "brave" },
      browser: { provider: "kernel", api_key: "k" },
    });
  });
});

it("saves a self-hosted browser at its address, with no key", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: NEVER },
    "PUT /teams/{id}/settings": {
      body: saved({
        ...NEVER,
        browser: { provider: "lightpanda", url: "ws://lightpanda:9222", key_set: false },
      }),
    },
  });
  at();

  await userEvent.selectOptions(await screen.findByLabelText(/browser provider/i), "lightpanda");
  await userEvent.type(screen.getByLabelText(/^address$/i), "ws://lightpanda:9222");
  expect(screen.getByLabelText(/^token$/i)).toHaveValue("");
  await saveIn("Browser");

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen).browser).toEqual({ provider: "lightpanda", url: "ws://lightpanda:9222" });
  expect(await within(group("Browser")).findByText("Saved")).toBeInTheDocument();
});

it("shows a hosted browser's key field, and no address", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: KERNEL } });
  at();

  expect(await screen.findByLabelText(/browser provider/i)).toHaveValue("kernel");
  expect(screen.getByLabelText(/^api key$/i)).toBeInTheDocument();
  expect(screen.queryByLabelText(/^address$/i)).not.toBeInTheDocument();
  expect(screen.getByText(/a key is set\. leave this empty to keep it/i)).toBeInTheDocument();
});
