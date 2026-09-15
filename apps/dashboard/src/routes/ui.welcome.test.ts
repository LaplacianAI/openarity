import { afterEach, expect, it } from "vitest";

import { dismissWelcome, welcomeDismissed } from "./ui.welcome";

afterEach(() => {
  window.localStorage.clear();
});

it("shows the welcome until it is dismissed", () => {
  expect(welcomeDismissed()).toBe(false);

  dismissWelcome();
  expect(welcomeDismissed()).toBe(true);
});

// localStorage, not sessionStorage: skipping has to be permanent. Somebody who
// chose to look around instead of creating a team must not be asked again on
// every reload, and the Overview redirects on every render while the answer is
// false.
it("remembers the dismissal across a new session", () => {
  dismissWelcome();

  window.sessionStorage.clear();
  expect(welcomeDismissed()).toBe(true);
});

// A browser refusing storage — private mode, a blocked origin — makes the
// welcome reappear, which is annoying. Throwing would leave the Overview
// unable to render at all.
it("survives a browser that refuses storage", () => {
  const broken = {
    getItem() {
      throw new Error("denied");
    },
    setItem() {
      throw new Error("denied");
    },
  };

  const original = window.localStorage;
  Object.defineProperty(window, "localStorage", { value: broken, configurable: true });

  try {
    expect(() => dismissWelcome()).not.toThrow();
    expect(welcomeDismissed()).toBe(false);
  } finally {
    Object.defineProperty(window, "localStorage", { value: original, configurable: true });
  }
});
