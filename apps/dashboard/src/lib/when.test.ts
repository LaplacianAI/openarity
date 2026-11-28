import { describe, expect, it } from "vitest";

import { cronSays, lengthSays, timeBoth, timeIn, wallToInstant, whenSays, zones } from "./when";

describe("a time in a zone", () => {
  it("reads one instant in each zone", () => {
    expect(timeIn("2026-12-01T03:30:00Z", "Asia/Kolkata")).toBe("Tue 1 Dec 09:00");
    expect(timeIn("2026-12-01T03:30:00Z", "UTC")).toBe("Tue 1 Dec 03:30");
  });

  it("falls back to UTC for a zone this browser does not know", () => {
    expect(timeIn("2026-12-01T03:30:00Z", "Mars/Olympus")).toBe("Tue 1 Dec 03:30");
  });

  it("says the schedule's own zone only when it reads differently", () => {
    expect(timeBoth("2026-12-01T03:30:00Z", "Asia/Kolkata", "Europe/London")).toEqual({
      here: "Tue 1 Dec 03:30",
      there: "Tue 1 Dec 09:00 in Asia/Kolkata",
    });
    expect(timeBoth("2026-12-01T03:30:00Z", "Asia/Kolkata", "Asia/Kolkata").there).toBeNull();
    // Another name for the same clock today reads the same, so it is not said.
    expect(timeBoth("2026-12-01T03:30:00Z", "Europe/London", "UTC").there).toBeNull();
  });
});

describe("a rule in words", () => {
  it("says the rules people write most", () => {
    expect(cronSays("0 9 * * 1-5")).toBe("weekdays at 09:00");
    expect(cronSays("30 7 * * *")).toBe("every day at 07:30");
    expect(cronSays("0 9 * * 1")).toBe("Mondays at 09:00");
    expect(cronSays("0 9 * * 7")).toBe("Sundays at 09:00");
    expect(cronSays("0 10 * * 0,6")).toBe("weekends at 10:00");
    expect(cronSays("0 * * * *")).toBe("every hour");
    expect(cronSays("15 * * * *")).toBe("every hour at :15");
    expect(cronSays("@daily")).toBe("every day at 00:00");
  });

  it("leaves any other rule as it was written", () => {
    for (const r of ["*/5 * * * *", "0 9 1 * *", "0 25 * * *", "0 9 * * 1-3", "nonsense"]) {
      expect(cronSays(r)).toBeNull();
    }
    expect(whenSays({ kind: "cron", cron: "*/5 * * * *" }, "UTC")).toBe("*/5 * * * *");
  });

  it("says lengths and a once", () => {
    expect(lengthSays(1800)).toBe("30 min");
    expect(lengthSays(7200)).toBe("2 h");
    expect(lengthSays(86_400)).toBe("1 day");
    expect(lengthSays(1_209_600)).toBe("2 weeks");
    expect(lengthSays(0)).toBe("none");
    expect(whenSays({ kind: "every", every_seconds: 7200 }, "UTC")).toBe("every 2 h");
    expect(
      whenSays({ kind: "at", at: "2026-12-01T03:30:00Z" }, "Asia/Kolkata", "Asia/Kolkata"),
    ).toBe("once, Tue 1 Dec 09:00");
  });
});

describe("a wall time read in a zone", () => {
  it("is the instant that zone's clock shows it", () => {
    expect(wallToInstant("2026-12-01", "09:00", "Asia/Kolkata")).toBe("2026-12-01T03:30:00.000Z");
    expect(wallToInstant("2026-07-01", "09:00", "Europe/London")).toBe("2026-07-01T08:00:00.000Z");
    expect(wallToInstant("2026-12-01", "9:00", "UTC")).toBe("2026-12-01T09:00:00.000Z");
  });

  it("is none when the clocks skip it, or it is no time", () => {
    expect(wallToInstant("2026-03-29", "01:30", "Europe/London")).toBeNull();
    expect(wallToInstant("2026-02-30", "09:00", "UTC")).toBeNull();
    expect(wallToInstant("2026-12-01", "24:00", "UTC")).toBeNull();
    expect(wallToInstant("2026-12-01", "09:00", "Mars/Olympus")).toBeNull();
  });

  it("is one of the two when the clocks pass it twice", () => {
    const at = wallToInstant("2026-10-25", "01:30", "Europe/London");
    expect(["2026-10-25T00:30:00.000Z", "2026-10-25T01:30:00.000Z"]).toContain(at);
  });
});

it("lists the zones, with one it is asked to keep", () => {
  expect(zones()).toContain("UTC");
  expect(zones("Mars/Olympus")[0]).toBe("Mars/Olympus");
});
