import type { ReactNode } from "react";

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

export { Field } from "@/components/ui/field";

export { Textarea as TextArea } from "@/components/ui/input";

/** A small rounded label: a skill's source, a server's transport. */
export function Tag({ children }: { children: ReactNode }) {
  return (
    <span className="rounded-lg px-1.5 py-0.5 font-mono text-xs text-muted-foreground shadow-[inset_0_0_0_1px_var(--color-rule)]">
      {children}
    </span>
  );
}
