import { TriangleAlert } from "lucide-react";
import { type ReactNode, useId } from "react";
import { Button } from "@/components/ui/button";

/** What a save would cost that is not undone, said before it is made. */
export function Notice({ children }: { children: ReactNode }) {
  return (
    <p role="status" className="flex items-start gap-1.5 text-xs text-foreground">
      <TriangleAlert aria-hidden className="mt-px size-3.5 shrink-0 text-muted-foreground" />
      <span>{children}</span>
    </p>
  );
}

/** One group: what it decides, its fields, and its own Save. */
export function Section({
  title,
  about,
  canWrite,
  dirty,
  saved,
  problem,
  pending,
  busy,
  onSave,
  children,
}: {
  title: string;
  about: ReactNode;
  canWrite: boolean;
  dirty: boolean;
  /** Saved since it last changed: said beside the button until the next edit. */
  saved: boolean;
  problem: string | null;
  pending: boolean;
  /** Another group is saving; the settings are sent whole, so one at a time. */
  busy: boolean;
  onSave: () => void;
  children: ReactNode;
}) {
  const id = useId();
  return (
    <section
      aria-labelledby={id}
      className="grid gap-x-8 gap-y-3 border-t border-rule py-6 first:border-t-0 first:pt-0 lg:grid-cols-[13rem_minmax(0,1fr)]"
    >
      <div className="space-y-1">
        <h2 id={id} className="text-base font-medium">
          {title}
        </h2>
        <p className="text-xs text-muted-foreground">{about}</p>
      </div>
      <form
        className="min-w-0 max-w-2xl space-y-4"
        onSubmit={(e) => {
          e.preventDefault();
          onSave();
        }}
      >
        <fieldset disabled={!canWrite} className="space-y-4">
          {children}
        </fieldset>
        {canWrite ? (
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {/* Offered only for a change: a row of greyed buttons says nothing. */}
            {dirty || pending ? (
              <Button type="submit" size="sm" disabled={busy} loading={pending}>
                Save
              </Button>
            ) : null}
            {problem ? (
              <p role="alert" className="text-sm text-destructive">
                {problem}
              </p>
            ) : dirty ? (
              <p className="text-sm text-muted-foreground">Unsaved changes</p>
            ) : saved ? (
              <p role="status" className="text-sm text-succeeded">
                Saved
              </p>
            ) : null}
          </div>
        ) : null}
      </form>
    </section>
  );
}

export type SectionProps = Omit<Parameters<typeof Section>[0], "title" | "about" | "children">;
