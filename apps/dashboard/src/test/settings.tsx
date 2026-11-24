import type { AnyRoute } from "@tanstack/react-router";
import { showAt } from "@/test/brain";

const pages = await Promise.all([
  import("@/routes/ui.settings.index"),
  import("@/routes/ui.settings.general"),
  import("@/routes/ui.settings.tools"),
  import("@/routes/ui.settings.approvals"),
  import("@/routes/ui.settings.improvement"),
]);
for (const { Route } of pages) {
  await (Route.options.component as { preload?: () => Promise<void> }).preload?.();
}

const [index, general, tools, approvals, improvement] = pages.map((p) => p.Route as AnyRoute);

/** The settings list a phone opens on; a parent for a test's own routes. */
export const SETTINGS_INDEX = index as AnyRoute;

/** Every page of the team's settings, at the addresses the app gives them. */
export const SETTINGS_ROUTES: [string, AnyRoute][] = [
  ["/ui/settings/", index as AnyRoute],
  ["/ui/settings/general", general as AnyRoute],
  ["/ui/settings/tools", tools as AnyRoute],
  ["/ui/settings/approvals", approvals as AnyRoute],
  ["/ui/settings/improvement", improvement as AnyRoute],
];

/** The team's settings under a real router, open at one of its pages. */
export function showSettings(at: string, more: [string, AnyRoute][] = []) {
  return showAt(at, [...SETTINGS_ROUTES, ...more]);
}
