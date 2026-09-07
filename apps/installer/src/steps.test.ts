import { expect, it } from "vitest";

import { apply, nothingYet, readLines, STEPS, type Event } from "./steps";

function run(events: Event[]) {
  return events.reduce(apply, nothingYet);
}

it("marks a step done when the installer says so", () => {
  const p = run([
    { step: "resolve", state: "started" },
    { step: "resolve", state: "done" },
    { step: "cluster", state: "started" },
  ]);

  expect(p.states.resolve).toBe("done");
  expect(p.states.cluster).toBe("started");
  expect(p.failure).toBeNull();
});

// The whole reason this window exists rather than a spinner.
it("follows a download's percentage", () => {
  const p = run([
    { step: "download", state: "started" },
    { step: "download", state: "progress", percent: 1 },
    { step: "download", state: "progress", percent: 74 },
  ]);

  expect(p.percent).toBe(74);
  expect(p.states.download).toBe("started");
});

// Otherwise the bar sits at 100% through every later step, which reads as
// finished while the install is still going.
it("resets the bar when a step finishes", () => {
  const p = run([
    { step: "download", state: "started" },
    { step: "download", state: "progress", percent: 100 },
    { step: "download", state: "done" },
  ]);

  expect(p.percent).toBe(0);
});

it("keeps the reason a step failed", () => {
  const p = run([
    { step: "resolve", state: "started" },
    { step: "resolve", state: "failed", detail: "dex is not published yet" },
  ]);

  expect(p.states.resolve).toBe("failed");
  expect(p.failure).toBe("dex is not published yet");
});

// The passphrase is not stored anywhere, so this event is the only chance the
// window has to show it.
it("takes the address and the passphrase from the last event", () => {
  const p = run([
    { step: "start", state: "done" },
    { step: "ready", state: "done", url: "http://127.0.0.1:21120/ui", passphrase: "abc123" },
  ]);

  expect(p.url).toBe("http://127.0.0.1:21120/ui");
  expect(p.passphrase).toBe("abc123");
});

// stdout arrives in chunks that do not respect line boundaries, so a split
// event must not take the whole install down.
it("ignores a line that is not whole JSON", () => {
  const events = readLines('{"step":"cluster","state":"done"}\n{"step":"migr');

  expect(events).toHaveLength(1);
  expect(events[0]?.step).toBe("cluster");
});

it("reads several events out of one chunk", () => {
  const events = readLines(
    '{"step":"resolve","state":"started"}\n{"step":"resolve","state":"done"}\n',
  );

  expect(events).toHaveLength(2);
});

// A step the installer reports but the window does not name would be progress
// nobody sees.
it("names every step the installer emits", () => {
  const named = new Set(STEPS.map((s) => s.id));

  for (const step of ["resolve", "download", "cluster", "migrate", "identity", "start"]) {
    expect(named.has(step)).toBe(true);
  }
});

// The gateway password rides on the same event as the passphrase and has the
// same bargain: shown once, never again. A reducer that dropped it would send
// somebody to a dashboard they cannot open.
it("keeps the gateway password from the ready event", () => {
  const progress = readLines(
    JSON.stringify({
      step: "ready",
      state: "done",
      url: "http://127.0.0.1:21120/ui",
      passphrase: "a-sign-in-passphrase",
      gateway_password: "a-dashboard-password",
    }),
  ).reduce(apply, nothingYet);

  expect(progress.gatewayPassword).toBe("a-dashboard-password");
  expect(progress.passphrase).toBe("a-sign-in-passphrase");
});

// An install that points at a gateway rather than running one generates no
// password, and the done screen must not offer an empty box.
it("has no gateway password when none was generated", () => {
  const progress = readLines(
    JSON.stringify({
      step: "ready",
      state: "done",
      url: "http://127.0.0.1:21120/ui",
      passphrase: "a-sign-in-passphrase",
    }),
  ).reduce(apply, nothingYet);

  expect(progress.gatewayPassword).toBeNull();
});

// The step exists in the list, or the window shows five steps while the
// installer reports six and the last one appears to hang.
it("names the gateway step", () => {
  expect(STEPS.map((s) => s.id)).toContain("gateway");

  const progress = readLines(
    JSON.stringify({ step: "gateway", state: "started", detail: "omniroute" }),
  ).reduce(apply, nothingYet);

  expect(progress.states.gateway).toBe("started");
});
