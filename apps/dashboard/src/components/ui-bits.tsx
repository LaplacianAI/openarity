import { Link } from "@tanstack/react-router";
import { CircleHelp } from "lucide-react";
import { Popover } from "radix-ui";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * The title row every screen opens with: the screen's name, what it finds
 * worth saying right now, and its main action. What the screen is for sits
 * behind "What is this?", for the reader who needs it, rather than above
 * every visit's work.
 */
export function PageHeader({
  title,
  finding,
  description,
  actions,
}: {
  title: string;
  /** One line under the title: the screen's finding, such as who is waiting. */
  finding?: ReactNode;
  /** What the screen is for, read on request. */
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0 space-y-1">
        <div className="flex items-center gap-1.5">
          <h1 className="truncate text-xl font-semibold tracking-tight">{title}</h1>
          {description ? <About title={title}>{description}</About> : null}
        </div>
        {finding ? <div className="text-sm text-muted-foreground">{finding}</div> : null}
      </div>
      {actions ? <div className="flex shrink-0 items-center gap-2">{actions}</div> : null}
    </header>
  );
}

function About({ title, children }: { title: string; children: string }) {
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label={`What is ${title}?`}
          className="flex size-6 items-center justify-center rounded-full text-muted-foreground hover:bg-card hover:text-foreground focus-visible:outline-2"
        >
          <CircleHelp aria-hidden className="size-4" />
        </button>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content
          side="bottom"
          align="start"
          sideOffset={6}
          className="z-50 max-w-80 origin-(--radix-popover-content-transform-origin) rounded-lg bg-popover p-3 text-sm text-popover-foreground shadow-lg ring-1 ring-sheet-edge duration-150 ease-out outline-none data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-96 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-96"
        >
          {children}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

export { EmptyState } from "@/components/states";

export function LoadMore({
  hasMore,
  loading,
  onClick,
  loadedLabel,
}: {
  hasMore: boolean;
  loading: boolean;
  onClick: () => void;
  loadedLabel: string;
}) {
  return (
    <div className="flex items-center justify-between gap-3 pt-3 text-xs text-muted-foreground">
      <span>{loadedLabel}</span>
      {hasMore ? (
        <Button variant="outline" size="sm" onClick={onClick} disabled={loading}>
          {loading ? "Loading…" : "Load more"}
        </Button>
      ) : (
        <span>End of list</span>
      )}
    </div>
  );
}

export function StatusDot({ tone }: { tone: "active" | "idle" | "closed" | "paused" }) {
  return (
    <span
      aria-hidden
      className={cn(
        "inline-block size-1.5 rounded-full",
        tone === "active" && "bg-success",
        tone === "idle" && "bg-warning",
        tone === "closed" && "bg-muted-foreground",
        tone === "paused" && "bg-muted-foreground",
      )}
    />
  );
}

export function Mono({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cn("font-mono text-xs text-muted-foreground", className)}>{children}</span>
  );
}

export function BackLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link
      to={to}
      className="text-xs text-muted-foreground underline-offset-4 hover:text-foreground hover:underline"
    >
      {children}
    </Link>
  );
}

/** Initials on a quiet disc. Not teal: a person is not an action. */
/**
 * The first letter of a name's first two words, where anything that is not a
 * letter or digit separates words: "Priya (contractor)" is PC, not P(.
 */
export function initials(name: string): string {
  return name
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => [...p][0]?.toUpperCase() ?? "")
    .join("");
}

export function Avatar({ name }: { name: string }) {
  const letters = initials(name);
  return (
    <span
      aria-hidden
      className="flex size-7 shrink-0 items-center justify-center rounded-full bg-card text-xs font-medium text-foreground shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]"
    >
      {letters}
    </span>
  );
}
