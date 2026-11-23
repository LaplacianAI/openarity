import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { brain, showAt, team } from "@/test/brain";

const { Route, draftOf, isDirty, settingsRequest } = await import("@/routes/ui.settings");
await (Route.options.component as { preload?: () => Promise<void> }).preload?.();

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const ON: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 4096 },
  search: { provider: "none", url: null, key_set: false },
  sandbox: { provider: "none", url: null, key_set: false },
  improvement: true,
  judge_model: null,
  improvement_sample_share: 10,
  improvement_daily_passes: 20,
  improvement_pass_tokens: 400_000,
  improvement_pass_minutes: 20,
  improvement_daily_runs: 5,
  improvement_efficiency: false,
  improvement_efficiency_runs: 20,
  improvement_efficiency_days: 14,
  improvement_efficiency_spread: 200,
  improvement_efficiency_mark_runs: 3,
  improvement_efficiency_margin: 1,
  improvement_replay: false,
  improvement_replay_runs: 4,
  improvement_replay_tokens: 200_000,
  improvement_replay_minutes: 10,
  improvement_replay_daily: 40,
  improvement_since: "2026-11-01T00:00:00Z",
  compaction_messages: 100,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
  hub: null,
  updated_at: "2026-10-21T09:00:00Z",
};
const TRYING: TeamSettings = { ...ON, improvement_replay: true };

function at(stored: TeamSettings) {
  const seen = brain({
    ...team(["team:settings"]),
    "GET /teams/{id}/settings": { body: stored },
    "PUT /teams/{id}/settings": ({ body }) => ({
      body: { ...stored, ...JSON.parse(String(body)) },
    }),
  });
  showAt("/ui/settings", [["/ui/settings", Route]]);
  return seen;
}

const section = () => screen.getByRole("region", { name: "Improvement" });

describe("the request", () => {
  it("leaves out every replay setting while none changed", () => {
    const body = settingsRequest(draftOf(TRYING), TRYING) as Record<string, unknown>;
    for (const k of Object.keys(body)) expect(k.startsWith("improvement_replay")).toBe(false);
  });

  it("sends the switch, and each number that changed, a grouped number read as one", () => {
    const d = {
      ...draftOf(ON),
      replay: true,
      replayRuns: " 6 ",
      replayTokens: "300,000",
      replayMinutes: "15",
      replayDaily: "40",
    };
    const body = settingsRequest(d, ON) as Record<string, unknown>;
    expect(body).toMatchObject({
      improvement_replay: true,
      improvement_replay_runs: 6,
      improvement_replay_tokens: 300_000,
      improvement_replay_minutes: 15,
    });
    expect(body).not.toHaveProperty("improvement_replay_daily");
  });

  it.each([
    [{ replayRuns: "0" }, /Past runs to try it on: a whole number from 1 to 10/],
    [{ replayRuns: "11" }, /1 to 10/],
    [{ replayTokens: "19999" }, /Tokens for one try: a whole number from 20,000 to 1,000,000/],
    [{ replayMinutes: "31" }, /Minutes for one try: a whole number from 1 to 30/],
    [{ replayDaily: "lots" }, /Tries a day: a whole number from 1 to 500/],
  ])("refuses %j, saying why", (patch, why) => {
    expect(settingsRequest({ ...draftOf(TRYING), ...patch }, TRYING)).toMatch(why);
  });

  it("says nothing of numbers while trying is off, and they are not unsaved", () => {
    const d = { ...draftOf(ON), replayRuns: "99" };
    const body = settingsRequest(d, ON) as Record<string, unknown>;
    expect(body).not.toBeTypeOf("string");
    expect(body).not.toHaveProperty("improvement_replay_runs");
    expect(isDirty("improvement", d, ON)).toBe(false);
    expect(isDirty("improvement", { ...d, replay: true }, ON)).toBe(true);
  });
});

describe("the page", () => {
  it("offers the switch, and its numbers only while it is on, each explained", async () => {
    at(ON);
    const s = await screen.findByRole("region", { name: "Improvement" });
    const sw = within(s).getByRole("switch", { name: "Try each change on past runs first" });
    expect(sw).toHaveAccessibleDescription(/Nothing is sent or changed while it is tried/);
    expect(within(s).queryByLabelText("Past runs to try it on")).toBeNull();
    await userEvent.click(sw);
    for (const label of [
      "Past runs to try it on",
      "Tokens for one try",
      "Minutes for one try",
      "Tries a day",
    ]) {
      expect(within(s).getByLabelText(label)).toHaveAccessibleDescription(/\w{3,}.*\w{3,}/);
    }
  });

  it("refuses a wrong number before saving, and saves what was typed", async () => {
    const seen = at(TRYING);
    await screen.findByRole("region", { name: "Improvement" });
    const runs = within(section()).getByLabelText("Past runs to try it on");
    await userEvent.clear(runs);
    await userEvent.type(runs, "12");
    await userEvent.click(within(section()).getByRole("button", { name: "Save" }));
    expect(await within(section()).findByRole("alert")).toHaveTextContent(/1 to 10/);
    expect(runs).toHaveAttribute("aria-invalid", "true");
    expect(seen.some((x) => x.method === "PUT")).toBe(false);

    await userEvent.clear(runs);
    await userEvent.type(runs, "6");
    await userEvent.click(within(section()).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(seen.some((x) => x.method === "PUT")).toBe(true));
    const body = JSON.parse(String(seen.find((x) => x.method === "PUT")?.body));
    expect(body).toMatchObject({ improvement_replay_runs: 6 });
    expect(body).not.toHaveProperty("improvement_replay_tokens");
  });
});
