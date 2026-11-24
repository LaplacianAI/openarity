import { createFileRoute, Navigate } from "@tanstack/react-router";

import { SettingsShell } from "@/components/settings/shell";
import { useMediaQuery } from "@/lib/media";

export const Route = createFileRoute("/ui/settings/")({
  component: SettingsIndex,
});

/**
 * On a desk the settings open on General, with the other pages listed beside
 * it. On a phone this is the list of pages, each with what it decides.
 */
function SettingsIndex() {
  const tablet = useMediaQuery("(min-width: 48rem)");
  if (tablet) return <Navigate to="/ui/settings/general" replace />;
  return <SettingsShell page={null} />;
}
