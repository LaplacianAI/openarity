import { createFileRoute } from "@tanstack/react-router";

import { EmptyState, PageHeader } from "@/components/ui-bits";

export const Route = createFileRoute("/ui/setup")({
  component: NotBuilt,
});

function NotBuilt() {
  return (
    <div className="mx-auto w-full max-w-2xl px-6 py-16">
      <PageHeader title="First-run setup" />
      <div className="mt-5">
        <EmptyState
          title="Not built yet"
          body="The wizard needs endpoints the brain does not serve — there is no model-gateway API, and setup state has to be derived from teams and channels rather than stored."
        />
      </div>
    </div>
  );
}
