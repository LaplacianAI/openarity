import { describe, expect, it } from "vitest";

import type { TeamSettings } from "@/api/types";
import { draftOf, withFields } from "@/lib/team-settings/draft";

const STORED: TeamSettings = {
  model: { name: "gpt-5", max_tokens: 200_000 },
  search: { provider: "none", url: null, key_set: false },
  improvement: false,
  compaction_messages: 100,
  compaction_tokens_min: 50_000,
  compaction_tokens_max: 60_000,
  hub: null,
  updated_at: null,
} as TeamSettings;

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
