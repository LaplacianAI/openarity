/**
 * The small facts a detail panel is made of: a label and its value, an id a
 * reader may copy, and a time said the way a person says it.
 */
import { Check, Copy } from "lucide-react";
import { type ReactNode, useEffect, useState, useSyncExternalStore } from "react";
import { toast } from "sonner";

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { absoluteTime, relativeTime } from "@/lib/format";
import { cn } from "@/lib/utils";

/** Facts as a list: each label in muted ink, its value in full ink beside it. */
export function KeyValues({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <dl className={cn("grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm", className)}>
      {children}
    </dl>
  );
}

export function KeyValue({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 text-foreground tabular-nums">{children}</dd>
    </>
  );
}

/**
 * An id in mono, cut to fit, with a button that copies the whole of it. The
 * full id is the button's name, so a screen reader hears what it will copy.
 */
export function CopyId({ value, label = "id" }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(t);
  }, [copied]);

  const copy = () => {
    void navigator.clipboard?.writeText(value).then(
      () => {
        setCopied(true);
        toast("Copied");
      },
      () => toast.error(`Couldn't copy the ${label}. Select it and copy it by hand.`),
    );
  };

  return (
    <span className="inline-flex max-w-full min-w-0 items-center gap-1">
      <span className="truncate font-mono text-xs text-muted-foreground select-all" title={value}>
        {value}
      </span>
      <button
        type="button"
        onClick={copy}
        aria-label={`Copy the ${label} ${value}`}
        className="flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/6 hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
      >
        {copied ? (
          <Check aria-hidden className="size-3.5 text-succeeded" />
        ) : (
          <Copy aria-hidden className="size-3.5" />
        )}
      </button>
    </span>
  );
}

// One clock for every time on the page, ticking once a minute: a list of
// fifty rows runs one interval, not fifty.
const minute = {
  listeners: new Set<() => void>(),
  timer: 0,
  now: Date.now(),
  subscribe(listener: () => void) {
    minute.listeners.add(listener);
    if (minute.listeners.size === 1) {
      minute.now = Date.now();
      minute.timer = window.setInterval(() => {
        minute.now = Date.now();
        for (const l of minute.listeners) l();
      }, 60_000);
    }
    return () => {
      minute.listeners.delete(listener);
      if (minute.listeners.size === 0) window.clearInterval(minute.timer);
    };
  },
};

function useMinute(): number {
  return useSyncExternalStore(
    minute.subscribe,
    () => minute.now,
    () => minute.now,
  );
}

/** "4m ago", with the exact time on hover, kept current once a minute. */
export function Time({ iso, className }: { iso: string; className?: string }) {
  useMinute();
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <time dateTime={iso} className={cn("tabular-nums", className)}>
          {relativeTime(iso)}
        </time>
      </TooltipTrigger>
      <TooltipContent>{absoluteTime(iso)}</TooltipContent>
    </Tooltip>
  );
}
