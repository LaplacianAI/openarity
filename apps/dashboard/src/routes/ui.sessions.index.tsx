import { createFileRoute } from "@tanstack/react-router";

import { EmptyState, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/sessions/")({
  component: NotBuilt,
});

function NotBuilt() {
  return (
    <div className="space-y-5">
      <PageHeader title="Sessions" />
      <EmptyState
        title="Not built yet"
        body="This screen is waiting on the data layer being wired to the brain. The approval queue is the one that works today."
      />
    </div>
  );
}
