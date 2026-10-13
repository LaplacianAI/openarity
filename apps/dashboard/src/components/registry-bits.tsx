import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Says which permission a screen's write actions need, when the signed-in
 * person does not hold it. Without this the buttons are simply absent, and
 * absence reads as "this dashboard cannot do that" rather than "you may not".
 */
export function ReadOnlyNote({ permission, what }: { permission: string; what: string }) {
  return (
    <p className="rounded-md border border-border bg-surface px-3 py-2 text-xs text-muted-foreground">
      You can see this team's {what}. Changing them needs{" "}
      <span className="font-mono text-foreground">{permission}</span> in the team, which a team
      admin can grant.
    </p>
  );
}

/** A labelled field with the hint beneath it, for the registry's forms. */
export function Field({
  label,
  hint,
  children,
  htmlFor,
}: {
  label: string;
  hint?: string;
  children: ReactNode;
  htmlFor: string;
}) {
  return (
    <div className="space-y-1">
      <label htmlFor={htmlFor} className="text-xs font-medium">
        {label}
      </label>
      {children}
      {hint ? <p className="text-[11px] text-muted-foreground">{hint}</p> : null}
    </div>
  );
}

export function TextArea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea
      className={cn(
        "min-h-20 w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 dark:bg-input/30",
        className,
      )}
      {...props}
    />
  );
}

/** A small rounded label: a skill's source, a server's transport. */
export function Tag({ children }: { children: ReactNode }) {
  return (
    <span className="rounded border border-border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
      {children}
    </span>
  );
}
