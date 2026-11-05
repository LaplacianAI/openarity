import { useBlocker } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/**
 * The bar that carries an editor's save, held to the bottom of the sheet so
 * the action is in reach however far down the change was made. It says what
 * is unsaved and what went wrong, beside the button that answers both. On a
 * phone it sits on the tab bar, not under it.
 */
export function SaveBar({
  status,
  problem,
  children,
}: {
  /** "Unsaved changes", or what saving will do. */
  status: ReactNode;
  /** The brain's refusal, or the draft's, in a sentence. */
  problem?: string | null;
  /** The actions, the filled one last. */
  children: ReactNode;
}) {
  return (
    <div className="sticky bottom-[calc(3.5rem+env(safe-area-inset-bottom))] z-10 md:bottom-0 -mx-4 mt-6 flex flex-wrap items-center justify-between gap-x-4 gap-y-2 border-t border-sheet-edge bg-sheet px-4 py-3 md:-mx-6 md:px-6">
      <div className="min-w-0 space-y-0.5 text-sm">
        <p className="text-muted-foreground">{status}</p>
        {problem ? (
          <p role="alert" className="text-destructive">
            {problem}
          </p>
        ) : null}
      </div>
      <div className="ml-auto flex shrink-0 items-center gap-2">{children}</div>
    </div>
  );
}

/**
 * Asks before leaving an editor with changes unsaved: a move within the
 * dashboard opens a dialog, and closing or reloading the tab gets the
 * browser's own question. `dirty` is read when the move happens, so an
 * editor that has just saved leaves without being asked.
 */
export function LeaveGuard({ dirty, what }: { dirty: () => boolean; what: string }) {
  const blocker = useBlocker({
    shouldBlockFn: () => dirty(),
    enableBeforeUnload: dirty,
    withResolver: true,
  });

  return (
    <Dialog
      open={blocker.status === "blocked"}
      onOpenChange={(o) => (o ? null : blocker.reset?.())}
    >
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Leave without saving?</DialogTitle>
          <DialogDescription>Your changes to {what} have not been saved.</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => blocker.reset?.()}>
            Keep editing
          </Button>
          <Button type="button" variant="destructive" onClick={() => blocker.proceed?.()}>
            Leave
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
