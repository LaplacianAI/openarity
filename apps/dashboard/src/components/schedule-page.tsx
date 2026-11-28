import { Link } from "@tanstack/react-router";
import { ChevronRight } from "lucide-react";
import { type ReactNode, useState } from "react";

import { Time } from "@/components/facts";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";

/** The breadcrumb, title and one line every schedule and watcher page opens with. */
export function TriggerHeader({
  crumb,
  title,
  line,
  actions,
}: {
  crumb: string;
  title: string;
  line?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className="space-y-2">
      <nav aria-label="Breadcrumb">
        <ol className="flex min-w-0 items-center gap-1 text-xs text-muted-foreground">
          <li>
            <Link
              to="/ui/schedules"
              search={{ view: crumb === "Watchers" ? "watchers" : undefined }}
              className="rounded underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-2 focus-visible:outline-ring"
            >
              {crumb}
            </Link>
          </li>
          <li aria-hidden>
            <ChevronRight className="size-3" />
          </li>
          <li aria-current="page" className="min-w-0 truncate text-foreground">
            {title}
          </li>
        </ol>
      </nav>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <h1 className="min-w-0 truncate text-xl font-semibold tracking-tight">{title}</h1>
        {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
      </div>
      {line ? <div className="text-sm text-muted-foreground">{line}</div> : null}
    </header>
  );
}

/** Delete, asked first: what goes and what is kept. */
export function DeleteTrigger({
  name,
  kept,
  pending,
  problem,
  onDelete,
}: {
  name: string;
  kept: string;
  pending: boolean;
  problem: string | null;
  onDelete: () => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="text-destructive"
        onClick={() => setOpen(true)}
      >
        Delete
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Delete {name}?</DialogTitle>
            <DialogDescription>{kept}</DialogDescription>
          </DialogHeader>
          {problem ? (
            <p role="alert" className="text-sm text-destructive">
              {problem}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setOpen(false)}>
              Keep it
            </Button>
            <Button type="button" variant="destructive" loading={pending} onClick={onDelete}>
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}

/** What an agent asked of one that runs, waiting for its maker's yes. */
export function AskStrip({
  says,
  since,
  canConfirm,
  pending,
  onConfirm,
}: {
  says: string;
  since: string;
  canConfirm: boolean;
  pending: boolean;
  onConfirm: () => void;
}) {
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-md bg-surface px-3 py-2 text-sm">
      <p className="min-w-0 flex-1">
        An agent asked {says}, <Time iso={since} />. It runs as it is until you confirm; unconfirmed
        within the hour, the ask is dropped.
      </p>
      {canConfirm ? (
        <Button size="sm" loading={pending} onClick={onConfirm}>
          Confirm
        </Button>
      ) : null}
    </div>
  );
}
