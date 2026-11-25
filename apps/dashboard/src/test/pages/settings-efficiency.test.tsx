import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { draftOf, isDirty } from "@/lib/team-settings/draft";
import { settingsRequest } from "@/lib/team-settings/request";
import { brain, team } from "@/test/brain";
import { showSettings } from "@/test/settings";

const { spreadOf } = await import("@/lib/efficiency-settings");

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
  improvement_since: "2026-11-01T00:00:00Z",
  compaction_messages: 100,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
  hub: null,
  updated_at: "2026-10-21T09:00:00Z",
};
const EFFICIENT: TeamSettings = { ...ON, improvement_efficiency: true };

const SETTINGS = "/teams/{id}/settings";

function at(stored: TeamSettings) {
  const seen = brain({
    ...team(["team:settings"]),
    [`GET ${SETTINGS}`]: { body: stored },
    [`PUT ${SETTINGS}`]: ({ body }) => ({ body: { ...stored, ...JSON.parse(String(body)) } }),
  });
  showSettings("/ui/settings/improvement");
  return seen;
}

const section = () => screen.getByRole("region", { name: "Improvement" });

describe("the spread", () => {
  it.each([
    ["2", 200],
    ["1.25", 125],
    [" 2× ", 200],
    ["10x", 1000],
    ["1,5", 150],
  ])("reads %j as %d percent", (raw, pct) => {
    expect(spreadOf(raw)).toBe(pct);
  });

  it.each(["", "two", "1.255", "-2"])("refuses %j", (raw) => {
    expect(spreadOf(raw)).toBeNull();
  });

  it("is shown as a multiple of the usual run", () => {
    expect(draftOf(EFFICIENT).effSpread).toBe("2");
    expect(draftOf({ ...EFFICIENT, improvement_efficiency_spread: 150 }).effSpread).toBe("1.5");
  });
});

describe("the request", () => {
  it("leaves out every efficiency setting while none changed", () => {
    const body = settingsRequest(draftOf(EFFICIENT), EFFICIENT) as Record<string, unknown>;
    for (const k of [
      "improvement_efficiency",
      "improvement_efficiency_runs",
      "improvement_efficiency_days",
      "improvement_efficiency_spread",
      "improvement_efficiency_mark_runs",
      "improvement_efficiency_margin",
    ]) {
      expect(body).not.toHaveProperty(k);
    }
  });

  it("sends the switch, and each number that changed, as the brain names it", () => {
    const d = {
      ...draftOf(ON),
      efficiency: true,
      effRuns: " 30 ",
      effDays: "21",
      effSpread: "1.5",
      effMarkRuns: "4",
      effMargin: "0" as const,
    };
    expect(settingsRequest(d, ON)).toMatchObject({
      improvement_efficiency: true,
      improvement_efficiency_runs: 30,
      improvement_efficiency_days: 21,
      improvement_efficiency_spread: 150,
      improvement_efficiency_mark_runs: 4,
      improvement_efficiency_margin: 0,
    });
  });

  it("is dirty for a spread typed differently only when it means another number", () => {
    expect(isDirty("improvement", { ...draftOf(EFFICIENT), effSpread: "2.00" }, EFFICIENT)).toBe(
      false,
    );
    expect(isDirty("improvement", { ...draftOf(EFFICIENT), effSpread: "3" }, EFFICIENT)).toBe(true);
  });

  it("is not dirty for numbers edited while the pass is off, which are never sent", () => {
    expect(isDirty("improvement", { ...draftOf(ON), effRuns: "40", effMargin: "3" }, ON)).toBe(
      false,
    );
    expect(isDirty("improvement", { ...draftOf(ON), efficiency: true, effRuns: "40" }, ON)).toBe(
      true,
    );
  });

  it.each([
    [{ effRuns: "9" }, /Good runs needed: a whole number from 10 to 500/],
    [{ effRuns: "501" }, /10 to 500/],
    [{ effDays: "6" }, /Days to look back: a whole number from 7 to 60/],
    [{ effDays: "61" }, /7 to 60/],
    [{ effMarkRuns: "1" }, /2 to 50/],
    [{ effMarkRuns: "51" }, /2 to 50/],
    [{ effSpread: "1.2" }, /1.25 to 10/],
    [{ effSpread: "11" }, /1.25 to 10/],
    [{ effSpread: "lots" }, /1.25 to 10/],
    [{ effRuns: "10", effMarkRuns: "12" }, /no more than the good runs needed, 10/],
  ])("refuses %j, saying why", (patch, why) => {
    expect(settingsRequest({ ...draftOf(EFFICIENT), ...patch }, EFFICIENT)).toMatch(why);
  });

  it("says nothing of numbers hidden while the pass is off, and sends none of them", () => {
    const body = settingsRequest(
      { ...draftOf(EFFICIENT), efficiency: false, effRuns: "5" },
      EFFICIENT,
    );
    expect(body).toMatchObject({ improvement_efficiency: false });
    expect(body).not.toHaveProperty("improvement_efficiency_runs");
  });

  it("reads a margin the page does not offer as the default", () => {
    expect(draftOf({ ...EFFICIENT, improvement_efficiency_margin: 7 }).effMargin).toBe("1");
  });
});

describe("the page", () => {
  it("offers the switch only while improvement is on, and its numbers only while it is on", async () => {
    at(ON);
    const s = await screen.findByRole("region", { name: "Improvement" });
    const sw = within(s).getByRole("switch", { name: "Also make good runs cheaper and faster" });
    expect(sw).toHaveAccessibleDescription(/never a bigger model or more access/);
    expect(within(s).queryByLabelText("Good runs needed")).toBeNull();

    await userEvent.click(sw);
    for (const label of [
      "Good runs needed",
      "Days to look back",
      "Costliest runs, against the usual",
      "Runs a sign of waste needs",
      "Watch these changes more strictly",
    ]) {
      // Every number says what it does, for someone new to it.
      expect(within(s).getByLabelText(label)).toHaveAccessibleDescription(/\w{3,}.*\w{3,}/);
    }
  });

  it("hides the efficiency switch with improvement off", async () => {
    at({ ...ON, improvement: false });
    await screen.findByRole("region", { name: "Improvement" });
    expect(
      within(section()).queryByRole("switch", { name: "Also make good runs cheaper and faster" }),
    ).toBeNull();
  });

  it("keeps each field's error after a refused save until that field is fixed", async () => {
    at(EFFICIENT);
    await screen.findByRole("region", { name: "Improvement" });
    const runs = within(section()).getByLabelText("Good runs needed");
    const days = within(section()).getByLabelText("Days to look back");
    await userEvent.clear(runs);
    await userEvent.type(runs, "5");
    await userEvent.clear(days);
    await userEvent.type(days, "3");
    expect(runs).not.toHaveAttribute("aria-invalid", "true");
    await userEvent.click(within(section()).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(runs).toHaveAttribute("aria-invalid", "true"));
    expect(days).toHaveAttribute("aria-invalid", "true");

    await userEvent.clear(runs);
    await userEvent.type(runs, "40");
    expect(runs).not.toHaveAttribute("aria-invalid", "true");
    expect(days).toHaveAttribute("aria-invalid", "true");

    // A save that goes through puts them away: a new mistake waits for Save.
    await userEvent.clear(days);
    await userEvent.type(days, "21");
    await userEvent.click(within(section()).getByRole("button", { name: "Save" }));
    await within(section()).findByText(/Saved/);
    await userEvent.clear(within(section()).getByLabelText("Days to look back"));
    await userEvent.type(within(section()).getByLabelText("Days to look back"), "3");
    expect(within(section()).getByLabelText("Days to look back")).not.toHaveAttribute(
      "aria-invalid",
      "true",
    );
  });

  it("saves what was typed, and a refused value sends nothing", async () => {
    const seen = at(EFFICIENT);
    await screen.findByRole("region", { name: "Improvement" });
    const runs = within(section()).getByLabelText("Good runs needed");
    await userEvent.clear(runs);
    await userEvent.type(runs, "5");
    await userEvent.click(within(section()).getByRole("button", { name: "Save" }));
    expect(await within(section()).findByRole("alert")).toHaveTextContent(/10 to 500/);
    expect(seen.some((x) => x.method === "PUT")).toBe(false);
    // The field itself says what is wrong, and is marked invalid.
    expect(runs).toHaveAttribute("aria-invalid", "true");
    expect(runs).toHaveAccessibleDescription(/A whole number from 10 to 500/);

    await userEvent.clear(runs);
    await userEvent.type(runs, "40");
    const spread = within(section()).getByLabelText("Costliest runs, against the usual");
    await userEvent.clear(spread);
    await userEvent.type(spread, "3");
    await userEvent.click(within(section()).getByRole("button", { name: "Save" }));
    await waitFor(() => expect(seen.some((x) => x.method === "PUT")).toBe(true));
    const body = JSON.parse(String(seen.find((x) => x.method === "PUT")?.body));
    expect(body).toMatchObject({
      improvement_efficiency_runs: 40,
      improvement_efficiency_spread: 300,
    });
    expect(body).not.toHaveProperty("improvement_efficiency_days");
  });
});
