import { createRoute } from "@tanstack/react-router";
import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, onTestFinished, vi } from "vitest";

import { setToken } from "@/api/token";
import type { Schedule, ScheduleSettings } from "@/api/types";
import { TriggerStrip } from "@/components/trigger-strip";
import { brain, pageOf, type Seen, STAMP, showAt, team } from "@/test/brain";

const { Route: ListRoute } = await import("@/routes/ui.schedules.index");
const { Route: NewsRoute } = await import("@/routes/ui.schedules.news");
const { Route: NewRoute } = await import("@/routes/ui.schedules.new");
const { Route: DetailRoute } = await import("@/routes/ui.schedules.$scheduleId");
const { Route: SettingsRoute } = await import("@/routes/ui.settings.schedules");
await Promise.all([
  pageOf(ListRoute),
  pageOf(NewRoute),
  pageOf(DetailRoute),
  pageOf(SettingsRoute),
]);

afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  setToken(null);
});

const ALL = ["schedule:read", "schedule:write"];

function schedule(over: Partial<Schedule> = {}): Schedule {
  return {
    id: "s1",
    name: "standup",
    objective: "Summarise yesterday.",
    when: { kind: "cron", cron: "0 9 * * 1-5" },
    timezone: "UTC",
    hub: true,
    target: { kind: "own" },
    session_id: "sess-1",
    status: "active",
    next_fire_at: "2026-12-01T09:00:00Z",
    upcoming: ["2026-12-01T09:00:00Z", "2026-12-02T09:00:00Z", "2026-12-03T09:00:00Z"],
    created_at: STAMP,
    updated_at: STAMP,
    ...over,
  };
}

const EMPTY_NEWS = { body: { items: [], count: 0, more: false } };

function at(path: string) {
  return showAt(path, [
    ["/ui/schedules/", ListRoute],
    ["/ui/schedules/news", NewsRoute],
    ["/ui/schedules/new", NewRoute],
    ["/ui/schedules/$scheduleId", DetailRoute],
    ["/ui/settings/schedules", SettingsRoute],
  ]);
}

function bodyOf(seen: Seen[], method: string, path: string): unknown {
  const s = seen.find((x) => x.method === method && x.path.split("?")[0] === path);
  return s ? JSON.parse(String(s.body)) : undefined;
}

describe("the schedules list", () => {
  it("lists the reader's own, each in words, and offers New", async () => {
    const seen = brain({
      ...team(ALL),
      "GET /teams/{id}/scheduled/news": EMPTY_NEWS,
      "GET /teams/{id}/schedules": {
        body: {
          items: [
            schedule(),
            schedule({ id: "s2", name: "digest", status: "proposed", next_fire_at: undefined }),
          ],
        },
      },
    });
    at("/ui/schedules");

    const list = await screen.findByRole("region", { name: "Your schedules" });
    expect(await within(list).findAllByText("weekdays at 09:00")).toHaveLength(2);
    expect(within(list).getByText("once confirmed")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "New schedule" })).toHaveAttribute(
      "href",
      "/ui/schedules/new",
    );
    // Its own, not everyone's.
    expect(
      seen.find((s) => s.path.startsWith("/teams/") && s.path.includes("/schedules?"))?.path,
    ).not.toContain("all=true");
  });

  it("says what it takes to read, and what it takes to make one", async () => {
    brain({ ...team([]), "GET /teams/{id}/scheduled/news": EMPTY_NEWS });
    at("/ui/schedules");
    expect(await screen.findByText(/schedule:read/)).toBeInTheDocument();

    brain({
      ...team(["schedule:read"]),
      "GET /teams/{id}/scheduled/news": EMPTY_NEWS,
      "GET /teams/{id}/schedules": { body: { items: [] } },
    });
    at("/ui/schedules");
    const make = await screen.findByRole("button", { name: /New schedule · needs schedule:write/ });
    expect(make).toBeDisabled();
  });

  it("offers everyone's only to a reader who manages them", async () => {
    brain({
      ...team(ALL),
      "GET /teams/{id}/scheduled/news": EMPTY_NEWS,
      "GET /teams/{id}/schedules": { body: { items: [] } },
    });
    at("/ui/schedules?view=all");
    await screen.findByRole("region", { name: "Your schedules" });
    expect(screen.queryByRole("tab", { name: "Everyone's" })).not.toBeInTheDocument();
  });
});

describe("the news", () => {
  it("names each thing that happened, and marks one seen as it is opened", async () => {
    const seen = brain({
      ...team(ALL),
      "GET /teams/{id}/scheduled/news": {
        body: {
          items: [
            {
              schedule_id: "s1",
              name: "standup",
              session_id: "sess-1",
              fired_at: STAMP,
              outcome: "skipped_busy",
            },
            {
              watcher_id: "w1",
              name: "prices",
              session_id: "sess-2",
              fired_at: STAMP,
              outcome: "error",
            },
          ],
          count: 2,
          more: false,
        },
      },
      "POST /teams/{id}/scheduled/news/seen": { status: 204 },
    });
    const router = at("/ui/schedules/news");
    const news = await screen.findByRole("list", { name: "News" });
    expect(within(news).getByText(/Skipped: its conversation was busy/)).toBeInTheDocument();
    expect(within(news).getByText(/Could not read it/)).toBeInTheDocument();

    await userEvent.click(within(news).getAllByRole("button", { name: "Open" })[1] as HTMLElement);
    await waitFor(() =>
      expect(
        bodyOf(seen, "POST", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/scheduled/news/seen"),
      ).toEqual({ watcher_id: "w1" }),
    );
    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/sessions/sess-2"));
  });
});

describe("making a schedule", () => {
  function maker(more: Parameters<typeof brain>[0] = {}) {
    return brain({
      ...team(ALL),
      "GET /teams/{id}/schedule-settings": {
        body: {
          agents_may_schedule: true,
          timezone: "Asia/Kolkata",
          schedule_min_interval_minutes: 60,
          schedules_per_member: 5,
          schedules_per_team: 20,
          fires_per_hour: 20,
        },
      },
      "POST /teams/{id}/schedules/preview": {
        body: {
          timezone: "Asia/Kolkata",
          upcoming: ["2026-12-01T03:30:00Z", "2026-12-02T03:30:00Z"],
        },
      },
      ...more,
    });
  }

  it("shows the next times as the rule is read, in the team's zone", async () => {
    const seen = maker();
    at("/ui/schedules/new");
    expect(await screen.findByLabelText("Time zone")).toHaveValue("Asia/Kolkata");
    await screen.findByText(/Next/);
    const asked = bodyOf(
      seen,
      "POST",
      "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/schedules/preview",
    );
    expect(asked).toEqual({
      when: { kind: "cron", cron: "0 9 * * 1-5" },
      timezone: "Asia/Kolkata",
    });
  });

  it("asks nothing of a rule that is not one yet, and says what is missing", async () => {
    const seen = maker();
    at("/ui/schedules/new");
    await screen.findByText(/Next/);
    const before = seen.filter((x) => x.path.endsWith("/preview")).length;
    await userEvent.clear(screen.getByLabelText("Cron rule"));
    expect(await screen.findByText("Write a cron rule, or pick one.")).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 400));
    expect(seen.filter((x) => x.path.endsWith("/preview"))).toHaveLength(before);
  });

  it("says the brain's reason for a rule it cannot read, and does not stop Create", async () => {
    maker({
      "POST /teams/{id}/schedules/preview": {
        status: 400,
        body: { error: "a cron rule has five fields" },
      },
    });
    at("/ui/schedules/new");
    expect(await screen.findByText(/five fields/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Make schedule" })).toBeEnabled();
  });

  it("sends what the form says, and lands on the new schedule", async () => {
    const seen = maker({
      "POST /teams/{id}/schedules": { status: 201, body: schedule({ id: "s9" }) },
      "GET /teams/{id}/schedules/{sid}": { body: schedule({ id: "s9" }) },
      "GET /teams/{id}/schedules/{sid}/fires": { body: { items: [] } },
    });
    const router = at("/ui/schedules/new");
    await userEvent.type(await screen.findByLabelText("Name"), "standup");
    await userEvent.type(screen.getByLabelText("What each run does"), "Summarise yesterday.");
    await userEvent.click(screen.getByRole("button", { name: "Make schedule" }));

    await waitFor(() => expect(router.state.location.pathname).toBe("/ui/schedules/s9"));
    expect(bodyOf(seen, "POST", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/schedules")).toEqual({
      name: "standup",
      objective: "Summarise yesterday.",
      when: { kind: "cron", cron: "0 9 * * 1-5" },
      timezone: "Asia/Kolkata",
    });
  });

  it("refuses an empty form beside the button, and sends nothing", async () => {
    const seen = maker();
    at("/ui/schedules/new");
    await userEvent.click(await screen.findByRole("button", { name: "Make schedule" }));
    expect(await screen.findByText("Name it.")).toBeInTheDocument();
    expect(seen.some((s) => s.method === "POST" && s.path.endsWith("/schedules"))).toBe(false);
  });

  it("lists where it may answer, with why a place cannot be picked", async () => {
    Element.prototype.scrollIntoView = vi.fn();
    onTestFinished(() => {
      delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
    });
    const seen = maker({
      "GET /teams/{id}/schedule-targets": {
        body: {
          items: [
            {
              kind: "channel",
              label: "#releases",
              detail: "Public · 14 members",
              channel: { id: "c1", name: "acme", provider: "slack" },
              available: true,
              target: { kind: "channel", channel_id: "c1", ref: "C01" },
            },
            {
              kind: "channel",
              label: "#ops",
              detail: "Public",
              channel: { id: "c1", name: "acme", provider: "slack" },
              available: false,
              reason: "not_in_channel",
              target: { kind: "channel", channel_id: "c1", ref: "C02" },
            },
          ],
          problems: [{ channel_id: "c2", provider: "slack", message: "missing scope groups:read" }],
        },
      },
      "POST /teams/{id}/schedules": { status: 201, body: schedule({ id: "s9" }) },
    });
    at("/ui/schedules/new");
    await userEvent.click(await screen.findByRole("button", { name: /Where it answers/ }));
    const list = await screen.findByRole("listbox", { name: "Places" });
    const ops = await within(list).findByRole("option", { name: /#ops/ });
    expect(ops).toHaveAttribute("aria-disabled", "true");
    expect(within(ops).getByText(/The app is not in it/)).toBeInTheDocument();
    expect(screen.getByText(/missing scope groups:read/)).toBeInTheDocument();

    await userEvent.click(ops);
    expect(list).toBeInTheDocument();
    await userEvent.click(within(list).getByRole("option", { name: /#releases/ }));
    expect(screen.getByRole("button", { name: /Where it answers #releases/ })).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Name"), "x");
    await userEvent.type(screen.getByLabelText("What each run does"), "y");
    await userEvent.click(screen.getByRole("button", { name: "Make schedule" }));
    await waitFor(() =>
      expect(
        bodyOf(seen, "POST", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/schedules"),
      ).toMatchObject({
        target: { kind: "channel", channel_id: "c1", ref: "C01" },
      }),
    );
  });
});

describe("a schedule's page", () => {
  function page(s: Schedule, actions = ALL, more: Parameters<typeof brain>[0] = {}) {
    return brain({
      ...team(actions),
      "GET /teams/{id}/schedules/{sid}/fires": {
        body: {
          items: [
            {
              scheduled_for: "2026-11-28T09:00:00Z",
              fired_at: "2026-11-28T09:07:00Z",
              outcome: "answered",
              run_id: "r1",
            },
            {
              scheduled_for: "2026-11-27T09:00:00Z",
              fired_at: "2026-11-27T09:00:00Z",
              outcome: "skipped_busy",
            },
          ],
        },
      },
      "GET /teams/{id}/schedules/{sid}": { body: s },
      ...more,
    });
  }

  it("opens on what is next, and lists each fire with what it did", async () => {
    page(schedule());
    at("/ui/schedules/s1");
    expect(await screen.findByText(/^Next fires Tue 1 Dec/)).toBeInTheDocument();
    expect(await screen.findByText(/Answered, 7 min after it was due/)).toBeInTheDocument();
    expect(screen.getByText("Skipped: its conversation was busy")).toBeInTheDocument();
    await screen.findByRole("button", { name: "Run now" });
    for (const name of ["Run now", "Pause", "Edit", "Delete"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.queryByRole("button", { name: "Confirm" })).not.toBeInTheDocument();
  });

  it("offers Confirm on a proposal, and nothing that runs it", async () => {
    const seen = page(schedule({ status: "proposed", next_fire_at: undefined }), ALL, {
      "POST /teams/{id}/schedules/{sid}/confirm": { body: schedule() },
    });
    at("/ui/schedules/s1");
    expect(
      await screen.findByText("Waiting for your yes: an agent proposed it."),
    ).toBeInTheDocument();
    const yes = await screen.findByRole("button", { name: "Confirm" });
    expect(screen.queryByRole("button", { name: "Run now" })).not.toBeInTheDocument();
    await userEvent.click(yes);
    await waitFor(() =>
      expect(seen.some((s) => s.method === "POST" && s.path.endsWith("/confirm"))).toBe(true),
    );
  });

  it("says why a paused one paused, and offers Take over only to a manager when its maker is gone", async () => {
    page(schedule({ status: "paused", paused_reason: "maker_gone", next_fire_at: undefined }), [
      ...ALL,
      "schedule:manage",
    ]);
    at("/ui/schedules/s1");
    expect(await screen.findByText("Paused: its maker left the team.")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Take over" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();

    cleanup();
    page(schedule({ status: "paused", paused_reason: "maker_gone", next_fire_at: undefined }));
    at("/ui/schedules/s1");
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Take over" })).not.toBeInTheDocument();
  });

  it("pauses with a PATCH of the status alone", async () => {
    const seen = page(schedule(), ALL, {
      "PATCH /teams/{id}/schedules/{sid}": {
        body: schedule({ status: "paused", paused_reason: "by_person" }),
      },
    });
    at("/ui/schedules/s1");
    await userEvent.click(await screen.findByRole("button", { name: "Pause" }));
    await waitFor(() =>
      expect(
        bodyOf(seen, "PATCH", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/schedules/s1"),
      ).toEqual({ status: "paused" }),
    );
    expect(await screen.findByRole("button", { name: "Resume" })).toBeInTheDocument();
  });

  it("asks before deleting, and says the conversation is kept", async () => {
    const seen = page(schedule(), ALL, { "DELETE /teams/{id}/schedules/{sid}": { status: 204 } });
    at("/ui/schedules/s1");
    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/conversation and its runs are kept/)).toBeInTheDocument();
    expect(seen.some((s) => s.method === "DELETE")).toBe(false);
    await userEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(seen.some((s) => s.method === "DELETE")).toBe(true));
  });

  it("sends only what an edit changed", async () => {
    const seen = page(schedule(), ALL, {
      "PATCH /teams/{id}/schedules/{sid}": { body: schedule({ name: "daily" }) },
      "POST /teams/{id}/schedules/preview": { body: { timezone: "UTC", upcoming: [] } },
    });
    at("/ui/schedules/s1");
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const name = screen.getByLabelText("Name");
    await userEvent.clear(name);
    await userEvent.type(name, "daily");
    expect(screen.queryByRole("button", { name: /Where it answers/ })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(
        bodyOf(seen, "PATCH", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/schedules/s1"),
      ).toEqual({ name: "daily" }),
    );
  });

  it("offers nothing to change without schedule:write, and says so", async () => {
    page(schedule(), ["schedule:read"]);
    at("/ui/schedules/s1");
    expect(await screen.findByText(/Changing a schedule needs schedule:write/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).not.toBeInTheDocument();
  });
});

describe("the team's schedule settings", () => {
  const stored: ScheduleSettings = {
    agents_may_schedule: true,
    timezone: "Asia/Kolkata",
    schedule_min_interval_minutes: 60,
    schedules_per_member: 5,
    schedules_per_team: 20,
    fires_per_hour: 20,
  };

  it("saves the whole, with what changed", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: { model: { name: "m" } } },
      "GET /teams/{id}/schedule-settings": { body: stored },
      "PUT /teams/{id}/schedule-settings": { body: { ...stored, schedules_per_team: 30 } },
    });
    at("/ui/settings/schedules");
    const team_ = await screen.findByLabelText("Per team");
    await userEvent.clear(team_);
    await userEvent.type(team_, "30");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(
        bodyOf(seen, "PUT", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/schedule-settings"),
      ).toEqual({
        ...stored,
        schedules_per_team: 30,
      }),
    );
  });

  it("refuses a team limit below one person's, and saves nothing", async () => {
    const seen = brain({
      ...team(["team:settings"]),
      "GET /teams/{id}/settings": { body: { model: { name: "m" } } },
      "GET /teams/{id}/schedule-settings": { body: stored },
    });
    at("/ui/settings/schedules");
    const team_ = await screen.findByLabelText("Per team");
    await userEvent.clear(team_);
    await userEvent.type(team_, "4");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/fewer than the 5 one person may/)).toBeInTheDocument();
    expect(seen.some((s) => s.method === "PUT")).toBe(false);
  });
});

describe("the line over a schedule's own conversation", () => {
  function strip(sessionExtra: Record<string, string>) {
    const Probe = () => (
      <TriggerStrip
        teamId="6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d"
        session={
          {
            id: "sess-1",
            channel_id: null,
            provider_ref: "",
            kind: "direct",
            status: "open",
            started_at: STAMP,
            last_message_at: STAMP,
            ...sessionExtra,
          } as never
        }
      />
    );
    const probe = createRoute({
      getParentRoute: () => ListRoute as never,
      path: "/x",
      component: Probe,
    });
    return showAt("/x", [
      ["/x", probe as never],
      ["/ui/schedules/$scheduleId", DetailRoute],
    ]);
  }

  it("names it, says when it fires next, and marks what it did as seen", async () => {
    const seen = brain({
      ...team(ALL),
      "GET /teams/{id}/schedules/{sid}": { body: schedule() },
      "POST /teams/{id}/scheduled/news/seen": { status: 204 },
    });
    strip({ schedule_id: "s1" });
    expect(await screen.findByRole("link", { name: "standup" })).toHaveAttribute(
      "href",
      "/ui/schedules/s1",
    );
    expect(screen.getByText(/next Tue 1 Dec/)).toBeInTheDocument();
    // On a phone the two actions fold into one menu.
    await userEvent.click(await screen.findByRole("button", { name: "What to do with standup" }));
    expect(await screen.findByRole("menuitem", { name: "Pause" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Run now" })).toBeInTheDocument();
    await waitFor(() =>
      expect(
        bodyOf(seen, "POST", "/teams/6f1b8f4e-6d2a-4d1e-9a1e-2b8f4e6d2a4d/scheduled/news/seen"),
      ).toEqual({ schedule_id: "s1" }),
    );
  });

  it("says only where it came from when the reader cannot open it", async () => {
    brain({
      ...team(ALL),
      "GET /teams/{id}/schedules/{sid}": { status: 404, body: { error: "not found" } },
    });
    strip({ schedule_id: "s1" });
    expect(await screen.findByText(/From a schedule you cannot open/)).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
