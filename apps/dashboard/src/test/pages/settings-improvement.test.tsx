import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { brain, choose, showAt, team } from "@/test/brain";

const { Route, draftOf, groupRequest, isDirty, settingsRequest } = await import(
  "@/routes/ui.settings"
);
await (Route.options.component as { preload?: () => Promise<void> }).preload?.();

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const OFF: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 4096 },
  search: { provider: "none", url: null, key_set: false },
  sandbox: { provider: "none", url: null, key_set: false },
  improvement: false,
  judge_model: null,
  improvement_sample_share: 10,
  improvement_since: null,
  compaction_messages: 100,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
  hub: null,
  updated_at: "2026-10-21T09:00:00Z",
};
const ON: TeamSettings = { ...OFF, improvement: true, improvement_since: "2026-11-01T00:00:00Z" };
const JUDGED: TeamSettings = {
  ...ON,
  judge_model: { name: "judge-1", max_tokens: 8000, temperature: 0.1 },
  improvement_sample_share: 25,
};

const SETTINGS = "/teams/{id}/settings";

function put(seen: { method: string; body: BodyInit | null | undefined }[]) {
  const p = seen.find((s) => s.method === "PUT");
  return p ? JSON.parse(String(p.body)) : undefined;
}

function at(
  stored: TeamSettings,
  extra: Parameters<typeof brain>[0] = {},
  actions = ["team:settings"],
) {
  const seen = brain({
    ...team(actions),
    [`GET ${SETTINGS}`]: { body: stored },
    [`PUT ${SETTINGS}`]: ({ body }) => ({ body: { ...stored, ...JSON.parse(String(body)) } }),
    ...extra,
  });
  showAt("/ui/settings", [["/ui/settings", Route]]);
  return seen;
}

const section = () => screen.getByRole("region", { name: "Improvement" });

describe("the request", () => {
  it("leaves out the judge model and the share while neither changed", () => {
    const body = settingsRequest(draftOf(JUDGED), JUDGED) as Record<string, unknown>;
    expect(body).not.toHaveProperty("judge_model");
    expect(body).not.toHaveProperty("improvement_sample_share");
  });

  it("sends the judge model when it changed, whole", () => {
    const d = { ...draftOf(ON), judgeOn: true, judgeName: " judge-2 ", judgeTemperature: "0.3" };
    expect(settingsRequest(d, ON)).toMatchObject({
      judge_model: { name: "judge-2", max_tokens: 200000, temperature: 0.3 },
    });
  });

  it("sends null to send the judge back to the team's model", () => {
    const body = settingsRequest({ ...draftOf(JUDGED), judgeOn: false }, JUDGED);
    expect(body).toHaveProperty("judge_model", null);
    expect(body).not.toHaveProperty("improvement_sample_share");
  });

  it("sends the share when it changed, as a number, and not the judge", () => {
    const body = settingsRequest({ ...draftOf(ON), sampleShare: "40" }, ON) as Record<
      string,
      unknown
    >;
    expect(body.improvement_sample_share).toBe(40);
    expect(body).not.toHaveProperty("judge_model");
  });

  it("sends a share of 0, which is a choice and not an absence", () => {
    expect(settingsRequest({ ...draftOf(ON), sampleShare: "0" }, ON)).toMatchObject({
      improvement_sample_share: 0,
    });
  });

  it.each([
    [{ sampleShare: "101" }, /0 to 100/],
    [{ sampleShare: "-1" }, /0 to 100/],
    [{ sampleShare: "" }, /0 to 100/],
    [{ sampleShare: "12.5" }, /0 to 100/],
    [{ judgeOn: true, judgeName: " " }, /Name the judge model/],
    [{ judgeOn: true, judgeName: "j", judgeMaxTokens: "0" }, /Max tokens/],
    [{ judgeOn: true, judgeName: "j", judgeTemperature: "3" }, /Temperature/],
  ])("refuses %o before asking the brain", (patch, said) => {
    expect(settingsRequest({ ...draftOf(ON), ...patch }, ON)).toMatch(said);
  });

  it("is the Improvement group's alone: another group's save sends neither", () => {
    const d = { ...draftOf(JUDGED), sampleShare: "5", judgeOn: false, modelName: "gpt-6" };
    const body = groupRequest("model", d, JUDGED) as Record<string, unknown>;
    expect(body).not.toHaveProperty("judge_model");
    expect(body).not.toHaveProperty("improvement_sample_share");
    expect(isDirty("improvement", d, JUDGED)).toBe(true);
    expect(isDirty("model", { ...draftOf(JUDGED), sampleShare: "5" }, JUDGED)).toBe(false);
  });

  it("reads a brain that predates the fields as the defaults", () => {
    const old = { ...OFF } as Partial<TeamSettings>;
    delete old.judge_model;
    delete old.improvement_sample_share;
    const d = draftOf(old as TeamSettings);
    expect(d.judgeOn).toBe(false);
    expect(d.sampleShare).toBe("10");
  });
});

describe("the Improvement section", () => {
  it("shows only the switch while improvement is off", async () => {
    at(OFF);
    await screen.findByLabelText(/improve agents from their runs/i);
    expect(within(section()).queryByLabelText(/^judge model$/i)).toBeNull();
    expect(within(section()).queryByLabelText(/share of good runs/i)).toBeNull();
  });

  it("says runs are judged in the background while it is on", async () => {
    at(OFF);
    expect(
      await screen.findByText(/each finished run is judged in the background/i),
    ).toBeInTheDocument();
  });

  it("offers the judge model as the team's own by default, and the share at 10%", async () => {
    at(ON);
    expect(await screen.findByLabelText(/^judge model$/i)).toHaveTextContent(
      "Same as the team's model",
    );
    expect(screen.getByLabelText(/share of good runs to study/i)).toHaveValue("10");
    expect(screen.queryByLabelText(/judge's model name/i)).toBeNull();
    expect(within(section()).getByText(/costs another model call/i)).toBeInTheDocument();
  });

  it("shows the judge's own model fields when the team has set one", async () => {
    at(JUDGED);
    expect(await screen.findByLabelText(/judge's model name/i)).toHaveValue("judge-1");
    expect(screen.getByLabelText(/judge's max tokens/i)).toHaveValue("8000");
    expect(screen.getByLabelText(/judge's temperature/i)).toHaveValue("0.1");
    expect(screen.getByLabelText(/share of good runs/i)).toHaveValue("25");
  });

  it("reveals the fields when turned on, and sends the whole improvement change on Save", async () => {
    const seen = at(OFF);
    await userEvent.click(await screen.findByLabelText(/improve agents from their runs/i));
    await userEvent.type(screen.getByLabelText(/share of good runs/i), "{Control>}a{/Control}30");
    await userEvent.click(within(section()).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen)).toMatchObject({ improvement: true, improvement_sample_share: 30 });
    expect(put(seen)).not.toHaveProperty("judge_model");
    expect(await within(section()).findByText("Saved")).toBeInTheDocument();
  });

  it("sends a judge model chosen here", async () => {
    const seen = at(ON);
    await choose(await screen.findByLabelText(/^judge model$/i), "A different model");
    await userEvent.type(screen.getByLabelText(/judge's model name/i), "judge-3");
    await userEvent.click(within(section()).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen).judge_model).toEqual({ name: "judge-3", max_tokens: 200000 });
    expect(put(seen)).not.toHaveProperty("improvement_sample_share");
  });

  it("sends null when the judge goes back to the team's model", async () => {
    const seen = at(JUDGED);
    await choose(await screen.findByLabelText(/^judge model$/i), "Same as the team's model");
    await userEvent.click(within(section()).getByRole("button", { name: /^save$/i }));

    await waitFor(() => expect(put(seen)).toBeDefined());
    expect(put(seen)).toHaveProperty("judge_model", null);
    expect(screen.queryByLabelText(/judge's model name/i)).toBeNull();
  });

  it("says what is wrong with a share out of range, and sends nothing", async () => {
    const seen = at(ON);
    const share = await screen.findByLabelText(/share of good runs/i);
    await userEvent.clear(share);
    await userEvent.type(share, "150");
    await userEvent.click(within(section()).getByRole("button", { name: /^save$/i }));

    expect(await within(section()).findByText(/whole number from 0 to 100/)).toBeInTheDocument();
    expect(put(seen)).toBeUndefined();
  });

  it("shows the brain's reason when the save is refused", async () => {
    at(ON, { [`PUT ${SETTINGS}`]: { status: 400, text: "the judge model is not known" } });
    const share = await screen.findByLabelText(/share of good runs/i);
    await userEvent.clear(share);
    await userEvent.type(share, "50");
    await userEvent.click(within(section()).getByRole("button", { name: /^save$/i }));

    expect(await within(section()).findByText("the judge model is not known")).toBeInTheDocument();
  });

  it("offers no Save to a reader without team:settings", async () => {
    at(ON, {}, []);
    expect(await screen.findByLabelText(/share of good runs/i)).toBeDisabled();
    expect(screen.getByLabelText(/^judge model$/i)).toBeDisabled();
    expect(within(section()).queryByRole("button", { name: /^save$/i })).toBeNull();
    expect(screen.getByText(/team:settings/)).toBeInTheDocument();
  });
});
