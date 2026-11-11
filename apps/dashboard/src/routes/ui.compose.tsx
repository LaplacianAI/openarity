import { createFileRoute, redirect } from "@tanstack/react-router";

/** Compose is Blueprint now; an old link or bookmark lands there. */
export const Route = createFileRoute("/ui/compose")({
  beforeLoad: () => {
    throw redirect({ to: "/ui/blueprint" });
  },
});
