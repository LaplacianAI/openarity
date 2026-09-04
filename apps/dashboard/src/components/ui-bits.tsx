import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: string;
  actions?: ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-3 border-b border-border pb-4">
      <div>
        <h1 className="text-lg font-semibold tracking-tight">{title}</h1>
        {description ? (
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {actions ? <div className="flex items-center gap-2">{actions}</div> : null}
    </header>
  );
}

export function EmptyState({
  title,
  body,
  action,
  hint,
}: {
  title: string;
  body: string;
  action?: ReactNode;
  hint?: string;
}) {
  return (
    <div className="rounded-md border border-dashed border-border-strong bg-surface px-6 py-10 text-center">
      <h2 className="text-sm font-semibold">{title}</h2>
      <p className="mx-auto mt-2 max-w-md text-sm text-muted-foreground">{body}</p>
      {action ? <div className="mt-4 flex justify-center gap-2">{action}</div> : null}
      {hint ? <p className="mt-3 text-xs text-muted-foreground">{hint}</p> : null}
    </div>
  );
}

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

export function Avatar({ name, color }: { name: string; color?: string | undefined }) {
  const letters = name
    .split(/[\s@._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((p) => p[0]?.toUpperCase() ?? "")
    .join("");
  return (
    <span
      className="flex size-7 shrink-0 items-center justify-center rounded-full text-[10px] font-semibold text-background"
      style={{ backgroundColor: color ?? "var(--color-primary)" }}
    >
      {letters}
    </span>
  );
}
