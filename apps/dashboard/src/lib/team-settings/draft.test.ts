import { describe, expect, it } from "vitest";

import type { TeamSettings } from "@/api/types";
import { draftOf, FIELDS, type SettingsDraft, withFields } from "@/lib/team-settings/draft";

const STORED = {
  model: { name: "gpt-5", max_tokens: 200_000 },
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
} satisfies TeamSettings;

describe("withFields", () => {
  it("takes only the named fields, and keeps every other as typed", () => {
    const typed = { ...draftOf(STORED), modelName: "gpt-6", compaction: "300", key: "typed" };
    const fresh = { ...draftOf(STORED), compaction: "250", key: "" };

    const out = withFields(typed, fresh, ["compaction", "key"]);

    expect(out.compaction).toBe("250");
    expect(out.key).toBe("");
    expect(out.modelName).toBe("gpt-6");
    expect(typed.compaction).toBe("300");
  });
});

describe("FIELDS", () => {
  // A field in no group is never saved and never asks before leaving; a field
  // in two is saved by either group's Save, behind the other's back.
  it("puts every field of the draft in exactly one group", () => {
    const owners = new Map<keyof SettingsDraft, string[]>();
    for (const [group, fields] of Object.entries(FIELDS)) {
      for (const f of fields) owners.set(f, [...(owners.get(f) ?? []), group]);
    }
    const draft = Object.keys(draftOf(STORED)) as (keyof SettingsDraft)[];

    expect(draft.filter((f) => owners.get(f)?.length !== 1)).toEqual([]);
    expect([...owners.keys()].filter((f) => !draft.includes(f))).toEqual([]);
  });
});
