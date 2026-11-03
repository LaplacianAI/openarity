import { readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import {
  checkLabel,
  fileFor,
  only,
  resolveBase,
  SCREENS,
  shotsOf,
  THEMES,
  WIDTHS,
} from "./plan.mjs";

// The runner starts in apps/dashboard; import.meta.url is not a file: URL under jsdom.
const ROUTES = join(process.cwd(), "src/routes");

// A route file's path, as TanStack Router reads its name: dots are slashes,
// `index` is the parent, `[_]` is a literal underscore and `$x` a parameter.
function pathOf(file) {
  const parts = file
    .replace(/\.tsx$/, "")
    .split(".")
    .filter((p) => p !== "index")
    .map((p) => p.replace("[_]", "_"));
  return `/${parts.join("/")}`;
}

describe("the capture plan", () => {
  // The redesign is judged on these captures, so a screen left out of them
  // is a screen nobody compares before and after.
  it("covers every screen the dashboard serves", () => {
    const pages = readdirSync(ROUTES)
      .filter((f) => f.startsWith("ui.") && f !== "ui.tsx" && f !== "ui.callback.tsx")
      .map(pathOf)
      .map((p) => p.replace(/\/\$[^/]+$/, "/$"));

    const captured = new Set(
      SCREENS.map((s) => (s.follow ? `${s.follow}$` : s.path.replace(/(.)\/$/, "$1"))),
    );
    for (const page of pages) expect(captured, `no capture for ${page}`).toContain(page);
  });

  // Served under /ui/: a bare /ui reaches a dev server's "did you mean" page.
  it("opens the overview at /ui/, with its slash", () => {
    expect(SCREENS.find((s) => s.name === "overview").path).toBe("/ui/");
    expect(SCREENS.find((s) => s.name === "sign-in").path).toBe("/ui/");
  });

  // The seed starts its runs in the eng-support group session; the newest
  // session, which a plain "first link" opens, has none, so its Runs tab
  // would be shot empty.
  it("opens the session the showcase seed ran its agents in", () => {
    const session = SCREENS.find((s) => s.name === "session");
    expect(session.followName).toBe("eng-support");
  });

  it("is every theme at every width", () => {
    expect(THEMES).toEqual(["light", "dark"]);
    expect(WIDTHS).toEqual([1440, 768, 390]);
  });

  it("takes a shot per tab and per dialog, as well as the screen", () => {
    const session = SCREENS.find((s) => s.name === "session");
    expect(shotsOf(session).map((s) => s.variant)).toEqual([null, "runs"]);

    const agents = SCREENS.find((s) => s.name === "agents");
    expect(shotsOf(agents).map((s) => s.variant)).toEqual([null, "dialog"]);

    const teams = SCREENS.find((s) => s.name === "teams");
    expect(shotsOf(teams).map((s) => s.variant)).toEqual([null]);
  });

  it("names each file by label, screen, variant, theme and width", () => {
    expect(fileFor("before", "agents", null, "dark", 390)).toBe("before/agents-dark-390.png");
    expect(fileFor("before", "session", "runs", "light", 1440)).toBe(
      "before/session-runs-light-1440.png",
    );
  });
});

describe("a label", () => {
  it("is a plain folder name", () => {
    expect(checkLabel("before")).toBe("before");
    expect(checkLabel("pr-3.after")).toBe("pr-3.after");
  });

  // The label becomes a directory under .captures/, so it must not climb out.
  it.each(["", "..", "../home", "a/b", "a\\b", ".hidden", "Before"])("refuses %j", (label) => {
    expect(() => checkLabel(label)).toThrow();
  });
});

describe("the base address", () => {
  it("is required, and named without ever being printed", () => {
    expect(() => resolveBase({})).toThrow(/CAPTURE_BASE_URL/);
  });

  it("keeps the origin only", () => {
    expect(resolveBase({ CAPTURE_BASE_URL: "https://brain.example/" })).toBe(
      "https://brain.example",
    );
    expect(resolveBase({ CAPTURE_BASE_URL: "http://127.0.0.1:5173" })).toBe(
      "http://127.0.0.1:5173",
    );
  });

  it.each(["brain.example", "ftp://brain.example", "https://brain.example/ui"])(
    "refuses %j",
    (url) => {
      expect(() => resolveBase({ CAPTURE_BASE_URL: url })).toThrow();
    },
  );

  it("never repeats the address in its refusal", () => {
    expect(() => resolveBase({ CAPTURE_BASE_URL: "https://secret.example/ui" })).toThrow(
      expect.objectContaining({ message: expect.not.stringContaining("secret.example") }),
    );
  });
});

describe("only", () => {
  it("keeps every screen when none is named", () => {
    expect(only(SCREENS, undefined)).toBe(SCREENS);
  });

  it("keeps the named screens, in the plan's order", () => {
    expect(only(SCREENS, "session,overview").map((s) => s.name)).toEqual(["overview", "session"]);
  });

  // A misspelt name would otherwise capture nothing and report success.
  it("refuses a screen the plan does not have", () => {
    expect(() => only(SCREENS, "sesion")).toThrow(/sesion/);
  });
});
