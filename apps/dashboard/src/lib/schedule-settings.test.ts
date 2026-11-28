import { describe, expect, it } from "vitest";

import { scheduleSettingsRequest, settingsDraftOf } from "./schedule-settings";

const stored = {
  agents_may_schedule: true,
  timezone: "Asia/Kolkata",
  schedule_min_interval_minutes: 60,
  schedules_per_member: 5,
  schedules_per_team: 20,
  fires_per_hour: 20,
};

describe("a team's schedule settings", () => {
  it("send back what was read, with what changed", () => {
    expect(scheduleSettingsRequest(settingsDraftOf(stored))).toEqual(stored);
    expect(
      scheduleSettingsRequest({ ...settingsDraftOf(stored), agents: false, perTeam: "30" }),
    ).toEqual({
      ...stored,
      agents_may_schedule: false,
      schedules_per_team: 30,
    });
  });

  it("take the brain's edges, and refuse past them", () => {
    const d = settingsDraftOf(stored);
    expect(
      scheduleSettingsRequest({
        ...d,
        interval: "5",
        perMember: "1",
        perTeam: "1",
        firesPerHour: "1",
      }),
    ).not.toBeTypeOf("string");
    expect(
      scheduleSettingsRequest({
        ...d,
        interval: "10080",
        perMember: "100",
        perTeam: "1000",
        firesPerHour: "1000",
      }),
    ).not.toBeTypeOf("string");
    const refused: [Partial<typeof d>, string][] = [
      [{ interval: "4" }, "5 minutes to 7 days"],
      [{ interval: "10081" }, "5 minutes to 7 days"],
      [{ perMember: "0" }, "between 1 and 100"],
      [{ perMember: "101", perTeam: "1000" }, "between 1 and 100"],
      [{ perTeam: "1001" }, "between 1 and 1,000"],
      [{ perTeam: "4" }, "fewer than the 5 one person may"],
      [{ firesPerHour: "0" }, "between 1 and 1,000"],
      [{ firesPerHour: "1.5" }, "a whole number"],
      [{ zone: "Mars/Olympus" }, "time zone"],
    ];
    for (const [over, want] of refused) {
      const got = scheduleSettingsRequest({ ...d, ...over });
      expect(
        typeof got === "string" && got.includes(want),
        `${JSON.stringify(over)}: ${String(got)}`,
      ).toBe(true);
    }
  });
});
