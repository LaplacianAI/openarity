import { createFileRoute } from "@tanstack/react-router";

import { SettingsBody, SettingsShell } from "@/components/settings/shell";

export const Route = createFileRoute("/ui/settings/tools")({
  component: Tools,
});

/** Web search, the sandbox and its workspaces, and the browser. */
function Tools() {
  return (
    <SettingsShell page="tools">
      {(ready) => <SettingsBody ready={ready} page="tools" />}
    </SettingsShell>
  );
}
