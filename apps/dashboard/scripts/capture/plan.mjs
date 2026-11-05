/**
 * What a capture is made of: the screens, the themes and the widths, and the
 * file each shot lands in. Pure, so it can be tested without a browser; the
 * browser half is capture.mjs.
 */

export const THEMES = ["light", "dark"];
export const WIDTHS = [1440, 768, 390];

/**
 * Every screen the dashboard serves. `follow` opens a link under that path
 * from the screen, for a page that needs an id: the one named `followName`
 * when the team has it, else the first; `tabs` are shot after the
 * first one is; `open` is the button that opens the screen's dialog; a
 * `signedOut` screen is shot with no session; a `devOnly` one exists only on
 * a dev server and is skipped elsewhere.
 */
export const SCREENS = [
  { name: "overview", path: "/ui/", open: "Search, ⌘K" },
  { name: "teams", path: "/ui/teams" },
  { name: "deleted-teams", path: "/ui/teams/deleted" },
  { name: "channels", path: "/ui/channels", open: "New channel" },
  { name: "runs", path: "/ui/runs" },
  { name: "approvals", path: "/ui/approvals" },
  { name: "sessions", path: "/ui/sessions" },
  {
    name: "session",
    path: "/ui/sessions",
    follow: "/ui/sessions/",
    followName: "eng-support",
    tabs: ["Runs"],
  },
  // The run states the reference screen must be judged in, from the showcase
  // seed's sessions: a failed step, a spent budget, ordinary tool calls and a
  // cancelled run. A team without them falls back to its first session.
  ...[
    ["session-failed", "ops-alerts:t-2005"],
    ["session-budget", "ops-alerts:t-2003"],
    ["session-tools", "ops-alerts:dm-u-ines"],
    ["session-cancelled", "eng-support:t-1004"],
  ].map(([name, followName]) => ({
    name,
    path: "/ui/sessions",
    follow: "/ui/sessions/",
    followName,
    tabs: ["Runs"],
  })),
  { name: "agents", path: "/ui/agents", open: "Define agent" },
  { name: "servers", path: "/ui/servers", open: "Register server" },
  { name: "skills", path: "/ui/skills" },
  { name: "graph", path: "/ui/graph" },
  { name: "settings", path: "/ui/settings" },
  { name: "sign-in", path: "/ui/", signedOut: true },
  { name: "gallery", path: "/ui/_gallery", devOnly: true },
];

/** The shots one screen owes: itself, each further tab, and its dialog. */
export function shotsOf(screen) {
  return [
    { variant: null },
    ...(screen.tabs ?? []).map((tab) => ({ variant: tab.toLowerCase(), tab })),
    ...(screen.open ? [{ variant: "dialog", open: screen.open }] : []),
  ];
}

export function fileFor(label, screen, variant, theme, width) {
  return `${label}/${screen}${variant ? `-${variant}` : ""}-${theme}-${width}.png`;
}

/** A label is a folder under .captures/, so it is a plain name and nothing more. */
export function checkLabel(label) {
  if (!/^[a-z0-9][a-z0-9._-]*$/.test(label ?? "") || label.includes("..")) {
    throw new Error("a label is lowercase letters, digits, dots and dashes, such as before");
  }
  return label;
}

/**
 * The dashboard's origin, from CAPTURE_BASE_URL. A refusal never repeats the
 * value: the output of this script gets pasted into conversations, and the
 * address of a staging brain does not belong in them.
 */
export function resolveBase(env) {
  const raw = env.CAPTURE_BASE_URL;
  if (!raw) throw new Error("set CAPTURE_BASE_URL to the dashboard's origin, such as https://host");
  let url;
  try {
    url = new URL(raw);
  } catch {
    throw new Error("CAPTURE_BASE_URL is not an address");
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error("CAPTURE_BASE_URL must be http or https");
  }
  if (url.pathname !== "/" || url.search || url.hash) {
    throw new Error("CAPTURE_BASE_URL is the origin only; the /ui paths are added for you");
  }
  return url.origin;
}

/**
 * The screens to shoot: every one, or those `names` lists (comma-separated),
 * kept in the plan's order. A name the plan does not have is refused rather
 * than matching nothing and reporting success.
 */
export function only(screens, names) {
  if (!names) return screens;
  const wanted = names.split(",").map((n) => n.trim());
  const unknown = wanted.filter((n) => !screens.some((s) => s.name === n));
  if (unknown.length > 0) throw new Error(`no such screen: ${unknown.join(", ")}`);
  return screens.filter((s) => wanted.includes(s.name));
}
