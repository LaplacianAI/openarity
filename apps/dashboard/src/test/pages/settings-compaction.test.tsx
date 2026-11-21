import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it, vi } from "vitest";

import { setToken } from "@/api/token";
import type { TeamSettings } from "@/api/types";
import { brain, showAt, team } from "@/test/brain";

const { Route } = await import("@/routes/ui.settings");
await (Route.options.component as { preload?: () => Promise<void> }).preload?.();

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const STORED: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 4096 },
  search: { provider: "none", url: null, key_set: false },
  sandbox: { provider: "none", url: null, key_set: false },
  improvement: false,
  judge_model: null,
  improvement_sample_share: 10,
  improvement_daily_passes: 20,
  improvement_since: null,
  compaction_messages: 100,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
  hub: null,
  updated_at: "2026-10-21T09:00:00Z",
};

function put(seen: { method: string; body: BodyInit | null | undefined }[]) {
  const p = seen.find((s) => s.method === "PUT");
  return p ? JSON.parse(String(p.body)) : undefined;
}

async function open(stored: TeamSettings = STORED, perms = ["team:settings"]) {
  const seen = brain({
    ...team(perms),
    "GET /teams/{id}/settings": { body: stored },
    "PUT /teams/{id}/settings": {
      body: { ...stored, compaction_tokens_min: 40_000, compaction_tokens_max: 70_000 },
    },
  });
  showAt("/ui/settings", [["/ui/settings", Route]]);
  const section = await screen.findByRole("region", { name: "Compaction" });
  return { seen, section: within(section) };
}

const least = (s: ReturnType<typeof within>) => s.getByLabelText(/^at least$/i);
const most = (s: ReturnType<typeof within>) => s.getByLabelText(/^at most$/i);

it("is a section of its own, holding the messages and the two token lines", async () => {
  const { section } = await open();

  expect(section.getByLabelText(/messages before a run compacts/i)).toHaveValue("100");
  expect(least(section)).toHaveValue("50,000");
  expect(most(section)).toHaveValue("60,000");
  expect(section.getByRole("group", { name: /tokens a request may read/i })).toBeInTheDocument();
  expect(
    section.getByText(
      "A run compacts at a tenth of its token budget, but never below 50,000 or above 60,000 tokens.",
    ),
  ).toBeInTheDocument();
  // The hub no longer carries it.
  expect(
    within(screen.getByRole("region", { name: "The hub" })).queryByLabelText(/compacts/i),
  ).toBeNull();
});

it("says nothing while a field is typed in, and groups it when the reader leaves", async () => {
  const { section } = await open();

  await userEvent.clear(most(section));
  await userEvent.type(most(section), "9");
  expect(most(section)).not.toHaveAttribute("aria-invalid");
  expect(section.queryByText(/use a whole number/i)).toBeNull();

  await userEvent.type(most(section), "0000");
  await userEvent.tab();
  expect(most(section)).toHaveValue("90,000");
  expect(section.getByText(/never below 50,000 or above 90,000 tokens/)).toBeInTheDocument();
});

it("names a field outside its range once it is left", async () => {
  const { section } = await open();

  await userEvent.clear(least(section));
  await userEvent.type(least(section), "1000");
  await userEvent.tab();

  expect(least(section)).toHaveAttribute("aria-invalid", "true");
  expect(section.getByText("Use a whole number from 30,000 to 1,000,000.")).toBeInTheDocument();
  // No sentence explains a line the brain would refuse.
  expect(section.queryByText(/a run compacts at a tenth/i)).toBeNull();
});

it("says once, naming both, when at least is above at most, and saves nothing", async () => {
  const { seen, section } = await open();

  await userEvent.clear(least(section));
  await userEvent.type(least(section), "70000");
  await userEvent.tab();
  await userEvent.click(section.getByRole("button", { name: /^save$/i }));

  const said = "At least, 70,000, is above at most, 60,000. Lower the first or raise the second.";
  expect(section.getAllByText(said)).toHaveLength(2); // under the pair, and beside Save
  expect(least(section)).toHaveAttribute("aria-invalid", "true");
  expect(most(section)).toHaveAttribute("aria-invalid", "true");
  expect(least(section)).toHaveFocus();
  expect(put(seen)).toBeUndefined();
});

it("calls equal lines one fixed line", async () => {
  const { section } = await open();

  await userEvent.clear(least(section));
  await userEvent.type(least(section), "60000");
  await userEvent.tab();

  expect(section.getByText("Every run compacts at 60,000 tokens.")).toBeInTheDocument();
});

it("saves both lines as numbers", async () => {
  const { seen, section } = await open();

  await userEvent.clear(least(section));
  await userEvent.type(least(section), "40 000");
  await userEvent.clear(most(section));
  await userEvent.type(most(section), "70,000");
  await userEvent.click(section.getByRole("button", { name: /^save$/i }));

  await waitFor(() => expect(put(seen)).toBeDefined());
  expect(put(seen)).toMatchObject({
    compaction_messages: 100,
    compaction_tokens_min: 40_000,
    compaction_tokens_max: 70_000,
  });
  expect(await section.findByText("Saved")).toBeInTheDocument();
});

it("offers the defaults only when they are not what is there, and fills them without saving", async () => {
  const { seen, section } = await open({
    ...STORED,
    compaction_messages: 300,
    compaction_tokens_min: 80_000,
    compaction_tokens_max: 90_000,
  });

  await userEvent.click(section.getByRole("button", { name: /use the defaults/i }));

  expect(section.getByLabelText(/messages before a run compacts/i)).toHaveValue("100");
  expect(least(section)).toHaveValue("50,000");
  expect(most(section)).toHaveValue("60,000");
  expect(section.queryByRole("button", { name: /use the defaults/i })).toBeNull();
  // The button is gone, so focus moves to the first field rather than the page.
  expect(section.getByLabelText(/messages before a run compacts/i)).toHaveFocus();
  expect(section.getByText("Unsaved changes")).toBeInTheDocument();
  expect(put(seen)).toBeUndefined();
});

it("does not offer the defaults to a team already on them", async () => {
  const { section } = await open();

  expect(section.queryByRole("button", { name: /use the defaults/i })).toBeNull();
});

it("shows a reader who cannot change it what it does", async () => {
  const { section } = await open(
    { ...STORED, compaction_tokens_min: 80_000, compaction_tokens_max: 90_000 },
    [],
  );

  expect(least(section)).toBeDisabled();
  expect(section.getByText(/never below 80,000 or above 90,000 tokens/)).toBeInTheDocument();
  expect(section.queryByRole("button", { name: /use the defaults/i })).toBeNull();
});
