/**
 * What a block shows when it has nothing, could not load, or may not be read.
 * Each keeps the block's own frame, so the page does not jump when the data
 * arrives, and each says what to do next.
 */
import { Lock, RotateCw, SearchX, TriangleAlert } from "lucide-react";
import type { ReactNode } from "react";

import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** Nothing here yet, and the one step that puts something here. */
export function EmptyState({
  title,
  body,
  action,
  hint,
  className,
}: {
  title: string;
  body: string;
  action?: ReactNode;
  hint?: string;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "rounded-lg border border-dashed border-border-strong px-6 py-10 text-center",
        className,
      )}
    >
      <h2 className="text-sm font-semibold">{title}</h2>
      <p className="mx-auto mt-2 max-w-md text-sm text-muted-foreground">{body}</p>
      {action ? <div className="mt-4 flex justify-center gap-2">{action}</div> : null}
      {hint ? <p className="mt-3 text-xs text-muted-foreground">{hint}</p> : null}
    </div>
  );
}

/**
 * The brain's words on a refusal, when they say more than the guard's bare
 * "forbidden": "you are not part of this conversation" is worth showing.
 */
function telling(error: ApiError): string | undefined {
  const said = error.message.trim();
  return said === "" || said.toLowerCase() === "forbidden" ? undefined : said;
}

function reason(error: unknown): string | null {
  if (error instanceof Error && error.message.trim() !== "") return error.message;
  return null;
}

/**
 * A read that failed: what could not be loaded, the brain's reason, and a
 * retry. It replaces an error toast on a read, since a toast leaves a blank
 * block behind it when it fades.
 *
 * A refusal and a missing thing are not failures, and trying again changes
 * neither: a 403 is the forbidden state and a 404 says it is not in this
 * team, each without a retry.
 */
export function ErrorState({
  what,
  error,
  onRetry,
  retrying = false,
  permission,
  className,
}: {
  /** What failed to load, as a phrase: "the agents", "this run's steps". */
  what: string;
  error: unknown;
  onRetry?: () => void;
  retrying?: boolean;
  /** The permission the read needs, named when the brain refuses it. */
  permission?: string;
  className?: string;
}) {
  if (error instanceof ApiError && error.status === 403) {
    return (
      <ForbiddenState
        what={what}
        permission={permission}
        why={telling(error)}
        className={className}
      />
    );
  }
  if (error instanceof ApiError && error.status === 404) {
    return <NotHereState what={what} className={className} />;
  }
  const why = reason(error);
  return (
    <div
      role="alert"
      className={cn(
        "flex flex-col items-start gap-3 rounded-lg bg-failed/6 px-4 py-4 text-sm sm:flex-row sm:items-center",
        className,
      )}
    >
      <TriangleAlert aria-hidden className="size-4 shrink-0 text-failed" />
      <div className="min-w-0 flex-1">
        <p className="font-medium">Couldn't load {what}.</p>
        <p className="mt-0.5 text-muted-foreground">
          {why ? `${why} ` : ""}
          {onRetry ? "Try again; if it keeps failing, the brain may be down." : null}
        </p>
      </div>
      {onRetry ? (
        <Button variant="outline" size="sm" onClick={onRetry} loading={retrying}>
          <RotateCw aria-hidden />
          Try again
        </Button>
      ) : null}
    </div>
  );
}

/**
 * A read the brain refused: names the permission it takes, so the reader
 * knows whom to ask rather than wondering whether the page is broken.
 */
export function ForbiddenState({
  what,
  permission,
  why,
  className,
}: {
  what: string;
  /** Left out when the screen cannot know it: then it says whom to ask. */
  permission?: string;
  /** The brain's own reason, shown when no permission is named. */
  why?: string;
  className?: string;
}) {
  return (
    <div className={cn("flex items-start gap-3 rounded-lg bg-card px-4 py-4 text-sm", className)}>
      <Lock aria-hidden className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0">
        <p className="font-medium">You can't see {what}.</p>
        <p className="mt-0.5 text-muted-foreground">
          {permission ? (
            <>
              Reading them needs <span className="font-mono text-foreground">{permission}</span> in
              this team, which a team admin can grant.
            </>
          ) : why ? (
            `${why.charAt(0).toUpperCase()}${why.slice(1)}.`
          ) : (
            "A team admin can give you access."
          )}
        </p>
      </div>
    </div>
  );
}

/** A read of something this team does not have: deleted, or a link from another team. */
function NotHereState({ what, className }: { what: string; className?: string }) {
  return (
    <div className={cn("flex items-start gap-3 rounded-lg bg-card px-4 py-4 text-sm", className)}>
      <SearchX aria-hidden className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0">
        <p className="font-medium">Couldn't find {what} in this team.</p>
        <p className="mt-0.5 text-muted-foreground">
          It may have been deleted, or the link is from another team.
        </p>
      </div>
    </div>
  );
}
