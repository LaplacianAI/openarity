import { createRoute } from "@tanstack/react-router";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { NO_MODEL } from "@/lib/ask";
import { draftOf, groupRequest, isDirty, settingsRequest } from "@/lib/team-settings";
import { brain, choose, team } from "@/test/brain";
import { SETTINGS_INDEX, showSettings } from "@/test/settings";

/** Somewhere to go: showAt hangs it off its own root. */
const Elsewhere = createRoute({
  getParentRoute: () => SETTINGS_INDEX,
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
  improvement: false,
  judge_model: null,
  improvement_sample_share: 10,
  improvement_daily_passes: 20,
  improvement_pass_tokens: 400_000,
  improvement_pass_minutes: 20,
  improvement_daily_runs: 5,
  improvement_since: null,
  compaction_messages: 100,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
  hub: null,
  updated_at: null,
};

const BRAVE: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
  search: { provider: "brave", url: null, key_set: true },
  sandbox: { provider: "none", url: null, key_set: false },
  improvement: false,
  judge_model: null,
  improvement_sample_share: 10,
  improvement_daily_passes: 20,
  improvement_pass_tokens: 400_000,
  improvement_pass_minutes: 20,
  improvement_daily_runs: 5,
  improvement_since: null,
  compaction_messages: 250,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
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
      improvement: false,
      compaction_messages: 250,
      compaction_tokens_min: 50_000,
      compaction_tokens_max: 60_000,
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
    const body = settingsRequest({ ...draftOf(OSS), improvement: true }, OSS);
    expect((body as { search: object }).search).not.toHaveProperty("api_key");
  });

  it.each([
    [{ modelName: "  " }, /Name the model/],
    [{ maxTokens: "0" }, /Max tokens/],
    [{ maxTokens: "12.5" }, /Max tokens/],
    [{ temperature: "2.1" }, /Temperature/],
    [{ temperature: "warm" }, /Temperature/],
    [{ compaction: "9" }, /Messages before a run compacts/],
    [{ compaction: "10001" }, /Messages before a run compacts/],
    [{ tokensMin: "29,999" }, /^At least/],
    [{ tokensMax: "1,000,001" }, /^At most/],
    [{ tokensMin: "70,000" }, /is above at most/],
  ])("refuses %o before asking the brain", (patch, said) => {
    expect(settingsRequest({ ...draftOf(BRAVE), ...patch }, BRAVE)).toMatch(said);
  });
});

describe("each group on its own", () => {
  it.each([
    ["model", { modelName: "gpt-6" }, true],
    ["model", { improvement: true }, false],
    ["hub", { compaction: "300" }, false],
    ["compaction", { compaction: "300" }, true],
    ["compaction", { tokensMax: "70,000" }, true],
    // The same number however it is typed.
    ["compaction", { tokensMax: "60000", compaction: "250" }, false],
    ["search", { key: "k" }, true],
    ["search", { dropKey: true }, true],
    ["improvement", { improvement: true }, true],
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
    const d = { ...draftOf(BRAVE), improvement: true, modelName: "half-typed", compaction: "9" };
    expect(groupRequest("improvement", d, BRAVE)).toEqual({
      model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
      search: { provider: "brave" },
      sandbox: { provider: "none" },
      browser: { provider: "none" },
      improvement: true,
      compaction_messages: 250,
      hub: null,
    });
  });

  // The token lines are optional on the wire: only Compaction's own Save
  // sends them, so a stale tab saving another group cannot reset them.
  it("sends the token lines only from Compaction's Save", () => {
    const d = { ...draftOf(BRAVE), tokensMin: "70,000", tokensMax: "90,000" };
    expect(groupRequest("improvement", d, BRAVE)).not.toHaveProperty("compaction_tokens_min");
    expect(groupRequest("improvement", d, BRAVE)).not.toHaveProperty("compaction_tokens_max");
    expect(groupRequest("compaction", d, BRAVE)).toMatchObject({
      compaction_tokens_min: 70_000,
      compaction_tokens_max: 90_000,
    });
  });

  it("checks only its own fields", () => {
    const d = { ...draftOf(BRAVE), compaction: "9" };
    expect(groupRequest("improvement", d, BRAVE)).not.toBeTypeOf("string");
    expect(groupRequest("hub", d, BRAVE)).not.toBeTypeOf("string");
    expect(groupRequest("compaction", d, BRAVE)).toMatch(/Messages before a run compacts/);
  });
});

/** A page of the settings under a real router, with somewhere else to go. */
function at(page: "general" | "tools" | "improvement" = "general") {
  return showSettings(`/ui/settings/${page}`, [["/ui/elsewhere", Elsewhere]]);
}

/** A group of the page, by its title. */
function group(title: string): HTMLElement {
  return screen.getByRole("region", { name: title });
}

async function saveIn(title: string) {
  await userEvent.click(within(group(title)).getByRole("button", { name: /^save$/i }));
}

describe("the defaults for a team that never saved", () => {
  it("on General", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
    at();

    expect(await screen.findByText(/No model —/)).toBeInTheDocument();
    expect(screen.getByLabelText(/messages before a run compacts/i)).toHaveValue("100");
    expect(screen.getByLabelText(/^at least$/i)).toHaveValue("50,000");
    expect(screen.getByLabelText(/^at most$/i)).toHaveValue("60,000");
  });

  it("on Tools", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
    at("tools");

    expect(await screen.findByLabelText(/^provider$/i)).toHaveTextContent("Off");
  });

  it("on Improvement", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
    at("improvement");

    expect(await screen.findByLabelText(/improve agents from their runs/i)).not.toBeChecked();
    expect(screen.queryByText(/learn from each run/i)).toBeNull();
    expect(screen.getByText(/each finished run is judged in the background/i)).toBeInTheDocument();
    expect(screen.queryByText(/this changes nothing/i)).toBeNull();
  });
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

    await screen.findByRole("textbox", { name: /^model$/i });
    expect(screen.queryByText(NO_MODEL)).not.toBeInTheDocument();
  });
});

// Review Focus 1 from the dashboard: it never has the key, so it must never
// send one it did not type.
it("saves the improvement switch without touching the key", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, improvement: true }) },
  });
  at("improvement");

  await userEvent.click(await screen.findByLabelText(/improve agents from their runs/i));
  await saveIn("Improvement");

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen)).toEqual({
    model: { name: "gpt-5", max_tokens: 4096, temperature: 0.2 },
    search: { provider: "brave" },
    sandbox: { provider: "none" },
    browser: { provider: "none" },
    improvement: true,
    compaction_messages: 250,
    hub: null,
  });
  expect(await within(group("Improvement")).findByText("Saved")).toBeInTheDocument();
});

it("keeps another group's unsaved edit, unsent, through a save", async () => {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, compaction_messages: 300 }) },
  });
  at();

  const model = await screen.findByRole("textbox", { name: /^model$/i });
  await userEvent.clear(model);
  await userEvent.type(model, "gpt-6");
  const messages = screen.getByLabelText(/messages before a run compacts/i);
  await userEvent.clear(messages);
  await userEvent.type(messages, "300");
  await saveIn("Compaction");

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen).model.name).toBe("gpt-5");
  expect(put(seen).compaction_messages).toBe(300);
  expect(await within(group("Compaction")).findByText("Saved")).toBeInTheDocument();
  expect(screen.getByRole("textbox", { name: /^model$/i })).toHaveValue("gpt-6");
  expect(within(group("Model")).getByText("Unsaved changes")).toBeInTheDocument();
});

it("offers Save only once something in the group changed", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  at("tools");

  await screen.findByLabelText(/^provider$/i);
  expect(within(group("Web search")).queryByRole("button", { name: /^save$/i })).toBeNull();
  await choose(screen.getByLabelText(/^provider$/i), "Tavily");
  expect(within(group("Web search")).getByRole("button", { name: /^save$/i })).toBeEnabled();
  expect(within(group("Browser")).queryByRole("button", { name: /^save$/i })).toBeNull();
});

it("asks before leaving with a change unsaved", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  const router = at("improvement");

  await userEvent.click(await screen.findByLabelText(/improve agents from their runs/i));
  act(() => {
    // Not awaited: a blocked move settles only once the dialog is answered.
    void router.navigate({ to: "/ui/elsewhere" } as never);
  });

  expect(await screen.findByRole("heading", { name: "Leave without saving?" })).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Keep editing" }));
  await waitFor(() => expect(screen.queryByRole("heading", { name: /leave/i })).toBeNull());
  expect(router.state.location.pathname).toBe("/ui/settings/improvement");
});

it("leaves without asking once the change is saved", async () => {
  brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: BRAVE },
    "PUT /teams/{id}/settings": { body: saved({ ...BRAVE, improvement: true }) },
  });
  const router = at("improvement");

  await userEvent.click(await screen.findByLabelText(/improve agents from their runs/i));
  await saveIn("Improvement");
  await within(group("Improvement")).findByText("Saved");
  act(() => {
    // Not awaited: a blocked move settles only once the dialog is answered.
    void router.navigate({ to: "/ui/elsewhere" } as never);
  });

  await waitFor(() => expect(router.state.location.pathname).toBe("/ui/elsewhere"));
  expect(screen.queryByRole("heading", { name: /leave/i })).toBeNull();
});

it("says a key is set without showing one", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
  at("tools");

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
  at("tools");

  await choose(await screen.findByLabelText(/^provider$/i), "Brave Search");
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
  at("tools");

  await choose(await screen.findByLabelText(/^provider$/i), "Tavily");
  await saveIn("Web search");

  expect(await within(group("Web search")).findByRole("alert")).toHaveTextContent(/API key/);
  expect(put(seen)).toBeUndefined();
});

it("is read-only without team:settings", async () => {
  brain({ ...team([]), "GET /teams/{id}/settings": { body: BRAVE } });
  at("tools");

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
  at("tools");

  await choose(await screen.findByLabelText(/^provider$/i), "SearXNG (self-hosted)");
  await userEvent.type(screen.getByLabelText(/^address$/i), "http://127.0.0.1");
  await saveIn("Web search");

  expect(await within(group("Web search")).findByRole("alert")).toHaveTextContent(/link-local/);
  expect(within(group("Browser")).queryByRole("alert")).toBeNull();
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
        "Every message to the team reaches the hub first, and the hub answers it or hands the work to the agent best placed to. Leave empty for the built-in orchestrator, which does this for you.",
      ),
    ).toBeInTheDocument();
  });

  it("keeps the chosen hub through a save that changes something else", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: WITH_HUB },
      "PUT /teams/{id}/settings": { body: saved({ ...WITH_HUB, compaction_messages: 300 }) },
    });
    at();

    const hub = await screen.findByRole("list", { name: "Hub" });
    expect(hub).toHaveTextContent("triage");
    const messages = screen.getByLabelText(/messages before a run compacts/i);
    await userEvent.clear(messages);
    await userEvent.type(messages, "300");
    await saveIn("Compaction");

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
    const body = settingsRequest({ ...draftOf(DAYTONA), improvement: true }, DAYTONA);
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

const SANDBOXD: TeamSettings = {
  ...NEVER,
  sandbox: {
    provider: "sandboxd",
    url: "http://sandboxd:8080",
    key_set: true,
    keep_hours: 24,
    idle_minutes: null,
    workspace_size: null,
  },
  updated_at: "2026-11-21T09:00:00Z",
};

const SIZED: TeamSettings = {
  ...SANDBOXD,
  sandbox: {
    ...SANDBOXD.sandbox,
    idle_minutes: 45,
    workspace_size: { memory_mb: 8192, cpus: 4, disk_mb: 20480 },
  },
};

describe("a team's workspaces", () => {
  const ask = (patch: Partial<ReturnType<typeof draftOf>>, stored: TeamSettings = SANDBOXD) =>
    settingsRequest({ ...draftOf(stored), ...patch }, stored);
  const sandboxOf = (body: unknown) => (body as { sandbox: Record<string, unknown> }).sandbox;

  // Left out, the brain keeps them: a tab opened before another's change never undoes it.
  it("sends no time or size that was not changed", () => {
    for (const stored of [SANDBOXD, SIZED]) {
      const sent = sandboxOf(ask({ improvement: true }, stored));
      expect(sent).not.toHaveProperty("keep_hours");
      expect(sent).not.toHaveProperty("idle_minutes");
      expect(sent).not.toHaveProperty("workspace_size");
    }
  });

  it("reads a brain that predates stopping as the provider's size and a day's keep", () => {
    const d = draftOf(DAYTONA);
    expect(d).toMatchObject({ sizeCustom: false, idleOwn: false, keepHours: "24" });
    expect(isDirty("sandbox", d, DAYTONA)).toBe(false);
  });

  it("shows a stored size in GB, and sends a custom size in MB", () => {
    expect(draftOf(SIZED)).toMatchObject({ sizeMemory: "8", sizeCPUs: "4", sizeDisk: "20" });
    expect(sandboxOf(ask({ sizeCustom: true, sizeMemory: "1.5", sizeDisk: "0.25" }))).toEqual({
      provider: "sandboxd",
      url: "http://sandboxd:8080",
      workspace_size: { memory_mb: 1536, cpus: 2, disk_mb: 256 },
    });
  });

  it("sends null to go back to the provider's size and the deployment's idle time", () => {
    expect(sandboxOf(ask({ sizeCustom: false, idleOwn: false }, SIZED))).toMatchObject({
      workspace_size: null,
      idle_minutes: null,
    });
  });

  it("sends the team's idle minutes and keep hours", () => {
    expect(sandboxOf(ask({ idleOwn: true, idleMinutes: "15", keepHours: "72" }))).toMatchObject({
      idle_minutes: 15,
      keep_hours: 72,
    });
  });

  it("is no change when a size is typed and then put back", () => {
    const d = { ...draftOf(SIZED), sizeMemory: "8.0", sizeCustom: true };
    expect(isDirty("sandbox", d, SIZED)).toBe(false);
    // Off, the numbers typed into the hidden fields do not count.
    expect(isDirty("sandbox", { ...draftOf(SANDBOXD), sizeMemory: "99" }, SANDBOXD)).toBe(false);
    expect(isDirty("sandbox", { ...draftOf(SANDBOXD), idleMinutes: "5" }, SANDBOXD)).toBe(false);
    expect(isDirty("sandbox", { ...draftOf(SANDBOXD), keepHours: "48" }, SANDBOXD)).toBe(true);
  });

  it.each([
    [{ sizeCustom: true, sizeMemory: "0.2" }, /Memory is between 0.25 and 64 GB/],
    [{ sizeCustom: true, sizeMemory: "65" }, /Memory is between 0.25 and 64 GB/],
    [{ sizeCustom: true, sizeMemory: "lots" }, /Memory/],
    [{ sizeCustom: true, sizeCPUs: "0" }, /CPUs is a whole number from 1 to 16/],
    [{ sizeCustom: true, sizeCPUs: "17" }, /CPUs/],
    [{ sizeCustom: true, sizeCPUs: "1.5" }, /CPUs/],
    [{ sizeCustom: true, sizeDisk: "0.1" }, /Disk is between 0.25 and 256 GB/],
    [{ sizeCustom: true, sizeDisk: "257" }, /Disk/],
    [{ idleOwn: true, idleMinutes: "0" }, /from 1 to 1,440/],
    [{ idleOwn: true, idleMinutes: "1441" }, /from 1 to 1,440/],
    [{ keepHours: "0" }, /from 1 to 720/],
    [{ keepHours: "721" }, /from 1 to 720/],
    [{ keepHours: "" }, /from 1 to 720/],
  ])("refuses %o", (patch, message) => {
    expect(ask(patch)).toMatch(message);
  });

  it("takes the edges of every range", () => {
    expect(
      sandboxOf(
        ask({
          sizeCustom: true,
          sizeMemory: "64",
          sizeCPUs: "16",
          sizeDisk: "256",
          idleOwn: true,
          idleMinutes: "1440",
          keepHours: "720",
        }),
      ),
    ).toMatchObject({
      workspace_size: { memory_mb: 65536, cpus: 16, disk_mb: 262144 },
      idle_minutes: 1440,
      keep_hours: 720,
    });
    expect(sandboxOf(ask({ idleOwn: true, idleMinutes: "1", keepHours: "1" }))).toMatchObject({
      idle_minutes: 1,
      keep_hours: 1,
    });
  });

  it("sends an E2B team's own size, which its template is built at", () => {
    expect(sandboxOf(ask({ sandbox: "e2b", sandboxKey: "k", sizeCustom: true }))).toMatchObject({
      provider: "e2b",
      workspace_size: { memory_mb: 2048, cpus: 2, disk_mb: 10240 },
    });
  });

  it("sends nothing about workspaces when the sandbox is off", () => {
    expect(sandboxOf(ask({ sandbox: "none", keepHours: "0" }, SIZED))).toEqual({
      provider: "none",
    });
  });

  it("keeps a team's size when it moves to E2B, and says the template is built first", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: SIZED },
      "PUT /teams/{id}/settings": { body: saved(SIZED) },
    });
    at("tools");

    const sandbox = await screen.findByRole("region", { name: "Sandbox" });
    await choose(within(sandbox).getByLabelText("Sandbox provider"), /^E2B/);
    expect(within(sandbox).getByLabelText("Memory")).toHaveValue("8");
    expect(within(sandbox).getByRole("radio", { name: "Custom" })).toBeEnabled();
    expect(within(sandbox).getByText(/builds a template at this size/)).toBeInTheDocument();

    await userEvent.type(within(sandbox).getByLabelText("API key"), "k");
    await saveIn("Sandbox");
    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen).sandbox).toEqual({ provider: "e2b", api_key: "k" });
  });

  // A team's own template was built at its own size, which a workspace keeps.
  it("says a team's own E2B template sets the size, and warns of no new workspaces", async () => {
    const own: TeamSettings = {
      ...SIZED,
      sandbox: {
        ...SIZED.sandbox,
        provider: "e2b",
        url: null,
        template: "acme/ours",
        key_set: true,
      },
    };
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: own } });
    at("tools");
    const sandbox = await screen.findByRole("region", { name: "Sandbox" });
    expect(
      within(sandbox).getByText(/Your own template sets each workspace's size/),
    ).toBeInTheDocument();

    await userEvent.click(within(sandbox).getByRole("radio", { name: "Provider's" }));
    expect(within(sandbox).queryByText(/without the files in its old one/)).toBeNull();
  });

  // Either way a size changes, every conversation gets a new workspace.
  it("warns that files go whenever a save would change the size", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: SIZED } });
    at("tools");
    const sandbox = await screen.findByRole("region", { name: "Sandbox" });
    const warning = /without the files in its old one/;
    expect(within(sandbox).queryByText(warning)).toBeNull();

    await userEvent.click(within(sandbox).getByRole("radio", { name: "Provider's" }));
    expect(within(sandbox).getByText(warning)).toBeInTheDocument();

    await userEvent.click(within(sandbox).getByRole("radio", { name: "Custom" }));
    expect(within(sandbox).queryByText(warning)).toBeNull();
    const memory = within(sandbox).getByLabelText("Memory");
    await userEvent.clear(memory);
    await userEvent.type(memory, "4");
    expect(within(sandbox).getByText(warning)).toBeInTheDocument();
  });

  // A new sandbox is new rooms too; lowering the keep hours reaches back to
  // rooms already stopped. Both are said before the save, to screen readers
  // as well.
  it("warns before a provider switch or a lower keep time costs files", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: SANDBOXD } });
    at("tools");
    const sandbox = await screen.findByRole("region", { name: "Sandbox" });
    expect(within(sandbox).queryAllByRole("status")).toHaveLength(0);

    const keep = within(sandbox).getByLabelText("Keep stopped");
    await userEvent.clear(keep);
    await userEvent.type(keep, "6");
    expect(within(sandbox).getByRole("status")).toHaveTextContent(
      "Lowering it from 24 hours deletes workspaces already stopped longer than 6",
    );
    await userEvent.clear(keep);
    await userEvent.type(keep, "48");
    expect(within(sandbox).queryAllByRole("status")).toHaveLength(0);

    await choose(within(sandbox).getByLabelText("Sandbox provider"), /^Daytona/);
    expect(within(sandbox).getByRole("status")).toHaveTextContent(
      "a new workspace in this sandbox",
    );
    // The size warning would say the same: one notice, not two.
    await userEvent.click(within(sandbox).getByRole("radio", { name: "Custom" }));
    expect(within(sandbox).getAllByRole("status")).toHaveLength(1);
  });

  it("saves a custom size typed into the page", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: SANDBOXD },
      "PUT /teams/{id}/settings": { body: saved(SIZED) },
    });
    at("tools");

    const sandbox = await screen.findByRole("region", { name: "Sandbox" });
    expect(within(sandbox).queryByLabelText("Memory")).toBeNull();
    await userEvent.click(within(sandbox).getByRole("radio", { name: "Custom" }));
    const memory = within(sandbox).getByLabelText("Memory");
    await userEvent.clear(memory);
    await userEvent.type(memory, "8");
    await saveIn("Sandbox");

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen).sandbox).toMatchObject({
      provider: "sandboxd",
      workspace_size: { memory_mb: 8192, cpus: 2, disk_mb: 10240 },
    });
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
    const body = ask({ improvement: true }, LIGHTPANDA);
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
  at("tools");

  await choose(await screen.findByLabelText(/browser provider/i), "Lightpanda");
  await userEvent.type(screen.getByLabelText(/^address$/i), "ws://lightpanda:9222");
  expect(screen.getByLabelText(/^token$/i)).toHaveValue("");
  await saveIn("Browser");

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen).browser).toEqual({ provider: "lightpanda", url: "ws://lightpanda:9222" });
  expect(await within(group("Browser")).findByText("Saved")).toBeInTheDocument();
});

it("shows a hosted browser's key field, and no address", async () => {
  brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: KERNEL } });
  at("tools");

  expect(await screen.findByLabelText(/browser provider/i)).toHaveTextContent("Kernel");
  expect(screen.getByLabelText(/^api key$/i)).toBeInTheDocument();
  expect(screen.queryByLabelText(/^address$/i)).not.toBeInTheDocument();
  expect(screen.getByText(/a key is set\. leave this empty to keep it/i)).toBeInTheDocument();
});

describe("the room template an E2B team's rooms start from", () => {
  const E2B: TeamSettings = {
    ...NEVER,
    sandbox: { provider: "e2b", url: null, template: null, key_set: true, template_build: null },
    updated_at: "2026-11-21T09:00:00Z",
  };
  const withBuild = (build: TeamSettings["sandbox"]["template_build"]): TeamSettings => ({
    ...E2B,
    sandbox: { ...E2B.sandbox, template_build: build },
  });
  const build = {
    name: "openarity-sandbox-9c3931a7a1b2",
    error: null,
    updated_at: "2026-11-21T09:00:00Z",
  };

  it.each([
    [withBuild({ ...build, status: "building" }), /building the room template/i],
    [withBuild({ ...build, status: "ready" }), /room template built/i],
    [
      withBuild({ ...build, status: "failed", error: "the image could not be pulled" }),
      /failed to build: the image could not be pulled/i,
    ],
    [E2B, /built in your e2b account when the sandbox is saved/i],
  ])("says how far its build is", async (stored, text) => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: stored } });
    at("tools");

    const status = await within(await screen.findByRole("region", { name: /sandbox/i })).findByRole(
      "status",
    );
    expect(status).toHaveTextContent(text);
  });

  it("says nothing of a build for a team naming its own template", async () => {
    const own: TeamSettings = { ...E2B, sandbox: { ...E2B.sandbox, template: "acme/ours" } };
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: own } });
    at("tools");

    const region = await screen.findByRole("region", { name: /sandbox/i });
    expect(within(region).getByLabelText(/^template$/i)).toHaveValue("acme/ours");
    expect(within(region).queryByText(/room template/i)).toBeNull();
  });
});

describe("its pages", () => {
  const real = window.matchMedia;
  function widths(min768: boolean, min1024: boolean) {
    window.matchMedia = ((query: string) => ({
      matches: query.includes("64rem") ? min1024 : query.includes("48rem") ? min768 : false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })) as unknown as typeof window.matchMedia;
  }
  afterEach(() => {
    window.matchMedia = real;
  });

  const REGIONS = [
    "Model",
    "The hub",
    "Compaction",
    "Web search",
    "Sandbox",
    "Browser",
    "Improvement",
  ];
  function shown(): string[] {
    return REGIONS.filter((name) => screen.queryByRole("region", { name }) !== null);
  }

  it.each([
    ["general", ["Model", "The hub", "Compaction"]],
    ["tools", ["Web search", "Sandbox", "Browser"]],
    ["improvement", ["Improvement"]],
  ] as const)("shows only its own sections on %s", async (page, sections) => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
    at(page);
    await screen.findByRole("region", { name: sections[0] });
    expect(shown()).toEqual(sections);
  });

  it("opens on General on a desk, with every page listed beside it", async () => {
    widths(true, true);
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
    const router = showSettings("/ui/settings");

    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/settings/general"));
    const nav = await screen.findByRole("navigation", { name: "Team settings" });
    expect(
      within(nav)
        .getAllByRole("link")
        .map((a) => a.textContent),
    ).toEqual(["General", "Tools", "Approvals", "Improvement"]);
    expect(within(nav).getByRole("link", { name: "General" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("is a list of its pages on a phone, and each page leads back to it", async () => {
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
    const router = showSettings("/ui/settings");

    const nav = await screen.findByRole("navigation", { name: "Team settings" });
    expect(router.state.location.pathname).toBe("/ui/settings");
    expect(within(nav).getByText(/web search, where commands run/i)).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Model" })).toBeNull();

    await userEvent.click(within(nav).getByRole("link", { name: /Tools/ }));
    expect(await screen.findByRole("region", { name: "Web search" })).toBeInTheDocument();
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(within(crumbs).getByText("Tools")).toHaveAttribute("aria-current", "page");
    await userEvent.click(within(crumbs).getByRole("link", { name: "Team settings" }));
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/settings"));
  });

  // Nothing runs until the team has a model, wherever in its settings you are.
  it("says the hub can't run on every page, and marks General", async () => {
    widths(true, true);
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: NEVER } });
    at("improvement");

    expect(await screen.findByText(NO_MODEL)).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Team settings" });
    expect(within(nav).getByRole("link", { name: /General, no model is set/ })).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: "Tools" })).toBeInTheDocument();
  });

  it("lists the pages as a row of tabs on a tablet", async () => {
    widths(true, false);
    brain({ ...team(["team:settings"]), "GET /teams/{id}/settings": { body: BRAVE } });
    at("tools");

    const nav = await screen.findByRole("navigation", { name: "Team settings" });
    expect(within(nav).getByRole("link", { name: "Tools" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(screen.queryByRole("navigation", { name: "Breadcrumb" })).toBeNull();
  });
});
