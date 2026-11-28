import { describe, expect, it } from "vitest";

import type { Schedule, Watcher } from "@/api/types";

import {
  emptyScheduleDraft,
  emptyWatcherDraft,
  scheduleDraftOf,
  scheduleRequest,
  scheduleUpdate,
  watcherDraftOf,
  watcherRequest,
  watcherUpdate,
} from "./schedule-draft";

const draft = (over: Partial<ReturnType<typeof emptyScheduleDraft>> = {}) => ({
  ...emptyScheduleDraft("Asia/Kolkata"),
  name: " standup ",
  objective: " Summarise yesterday. ",
  ...over,
});

describe("a schedule's request", () => {
  it("sends what the form says, trimmed", () => {
    expect(scheduleRequest(draft())).toEqual({
      name: "standup",
      objective: "Summarise yesterday.",
      when: { kind: "cron", cron: "0 9 * * 1-5" },
      timezone: "Asia/Kolkata",
    });
    expect(
      scheduleRequest(
        draft({
          mode: "repeat",
          every: "2",
          unit: "h",
          agent: { id: "a1", name: "triage" },
          target: { kind: "own" },
        }),
      ),
    ).toMatchObject({
      when: { kind: "every", every_seconds: 7200 },
      agent_id: "a1",
      target: { kind: "own" },
    });
    expect(
      scheduleRequest(draft({ mode: "once", date: "2026-12-01", time: "09:00" })),
    ).toMatchObject({
      when: { kind: "at", at: "2026-12-01T03:30:00.000Z" },
    });
  });

  it("says why it sends nothing", () => {
    const cases: [Partial<ReturnType<typeof emptyScheduleDraft>>, string][] = [
      [{ name: "  " }, "Name it."],
      [{ name: "x".repeat(81) }, "80 characters at most"],
      [{ objective: "" }, "Say what each run is to do."],
      [{ objective: "é".repeat(4001) }, "4001 characters; 4000 at most"],
      [{ cron: " " }, "Write a cron rule"],
      [{ mode: "repeat", every: "1.5" }, "whole number"],
      [{ mode: "repeat", every: "0", unit: "m" }, "at least a minute"],
      [{ mode: "repeat", every: "32", unit: "d" }, "at most 31 days"],
      [{ mode: "once", date: "" }, "Say the day and the time"],
      [
        { mode: "once", date: "2026-03-29", time: "01:30", zone: "Europe/London" },
        "the clocks skip it",
      ],
    ];
    for (const [over, want] of cases) {
      const got = scheduleRequest(draft(over));
      expect(
        typeof got === "string" && got.includes(want),
        `${JSON.stringify(over)}: ${JSON.stringify(got)}`,
      ).toBe(true);
    }
    expect(scheduleRequest(draft({ objective: "é".repeat(4000) }))).not.toBeTypeOf("string");
  });
});

const schedule: Schedule = {
  id: "s1",
  name: "standup",
  objective: "Summarise yesterday.",
  when: { kind: "every", every_seconds: 7200 },
  timezone: "Asia/Kolkata",
  hub: false,
  agent_id: "a1",
  target: { kind: "own" },
  session_id: "x",
  status: "active",
  upcoming: [],
  created_at: "",
  updated_at: "",
};

describe("a schedule's edit", () => {
  it("reads back into the form, and sends nothing when nothing changed", () => {
    const d = scheduleDraftOf(schedule, "triage");
    expect(d).toMatchObject({
      mode: "repeat",
      every: "2",
      unit: "h",
      agent: { id: "a1", name: "triage" },
    });
    expect(scheduleUpdate(schedule, d)).toEqual({});
  });

  it("sends only what changed", () => {
    const d = scheduleDraftOf(schedule);
    expect(scheduleUpdate(schedule, { ...d, name: "daily", every: "3" })).toEqual({
      name: "daily",
      when: { kind: "every", every_seconds: 10_800 },
    });
    expect(scheduleUpdate(schedule, { ...d, agent: null })).toEqual({ hub: true });
    expect(scheduleUpdate(schedule, { ...d, zone: "UTC" })).toEqual({ timezone: "UTC" });
  });

  it("leaves a once alone until a new day is typed", () => {
    const once = { ...schedule, when: { kind: "at" as const, at: "2026-12-01T03:30:00Z" } };
    const d = scheduleDraftOf(once);
    expect(scheduleUpdate(once, d)).toEqual({});
    expect(scheduleUpdate(once, { ...d, date: "2026-12-02", time: "09:00" })).toEqual({
      when: { kind: "at", at: "2026-12-02T03:30:00.000Z" },
    });
  });
});

const wdraft = (over: Partial<ReturnType<typeof emptyWatcherDraft>> = {}) => ({
  ...emptyWatcherDraft(),
  name: "prices",
  objective: "Tell me what moved.",
  url: "https://example.com/prices",
  ...over,
});

describe("a watcher's request", () => {
  it("sends what each source takes", () => {
    expect(watcherRequest(wdraft({ selector: " #table ", every: "2", cooldown: "0" }))).toEqual({
      name: "prices",
      objective: "Tell me what moved.",
      source: "web_page",
      url: "https://example.com/prices",
      selector: "#table",
      every_seconds: 7200,
      cooldown_seconds: 0,
    });
    expect(watcherRequest(wdraft({ source: "feed", selector: "#x" }))).not.toHaveProperty(
      "selector",
    );
    const hook = watcherRequest(wdraft({ source: "webhook", url: "", every: "2" }));
    expect(hook).toEqual({ name: "prices", objective: "Tell me what moved.", source: "webhook" });
  });

  it("says why it sends nothing", () => {
    expect(watcherRequest(wdraft({ url: "example.com" }))).toContain("starting https://");
    expect(watcherRequest(wdraft({ url: "ftp://x.example" }))).toContain("starting https://");
    expect(watcherRequest(wdraft({ cooldown: "8", cooldownUnit: "d" }))).toContain(
      "at most 7 days",
    );
    expect(watcherRequest(wdraft({ every: "30", everyUnit: "m" }))).not.toBeTypeOf("string");
  });
});

describe("a watcher's edit", () => {
  const w: Watcher = {
    id: "w1",
    name: "prices",
    objective: "Tell me what moved.",
    source: "web_page",
    url: "https://example.com/prices",
    selector: "#t",
    every_seconds: 7200,
    cooldown_seconds: 3600,
    hub: true,
    target: { kind: "own" },
    session_id: "x",
    status: "active",
    created_at: "",
    updated_at: "",
  };
  it("sends only what changed, and clears a selector", () => {
    const d = watcherDraftOf(w);
    expect(watcherUpdate(w, d)).toEqual({});
    expect(watcherUpdate(w, { ...d, selector: "" })).toEqual({ selector: "" });
    expect(watcherUpdate(w, { ...d, cooldown: "0" })).toEqual({ cooldown_seconds: 0 });
    expect(watcherUpdate(w, { ...d, agent: { id: "a2", name: "x" } })).toEqual({ agent_id: "a2" });
  });
});
