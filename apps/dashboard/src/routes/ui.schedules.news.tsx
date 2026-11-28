import { createFileRoute } from "@tanstack/react-router";

import { Schedules } from "./ui.schedules.index";

/** What the reader's schedules and watchers did that they have not seen: Needs you opens it. */
export const Route = createFileRoute("/ui/schedules/news")({
  component: () => <Schedules view="news" />,
});
