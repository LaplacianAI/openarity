/**
 * A list of things with the same facts: rules between rows, no zebra and no
 * header band. Numbers are right-aligned and tabular so they compare down a
 * column. On a phone each row stacks into its facts, each cell labelled.
 *
 * Rows that open something take `onOpen`: they are reached with Tab once,
 * then moved between with the arrow keys, and opened with Enter, the way a
 * list is worked from the keyboard. A row's actions show on hover and on
 * focus, and always on a touch screen, where there is no hover.
 */
import { cn } from "cn";
import type * as React from "react";
import { useLayoutEffect, useRef } from "react";

const ROW = "[data-openable]";

function Table({ className, ...props }: React.ComponentProps<"table">) {
  return (
    <div className="relative w-full">
      <table
        data-slot="table"
        className={cn("w-full border-collapse text-sm max-sm:block", className)}
        {...props}
      />
    </div>
  );
}

function TableHeader({ className, ...props }: React.ComponentProps<"thead">) {
  return (
    <thead
      data-slot="table-header"
      className={cn("sticky top-0 z-10 bg-sheet max-sm:sr-only", className)}
      {...props}
    />
  );
}

/** Holds the rows, and moves the keyboard between those that open. */
function TableBody({ className, onKeyDown, onFocus, ...props }: React.ComponentProps<"tbody">) {
  const ref = useRef<HTMLTableSectionElement>(null);

  // One row in the tab order at a time: the first, until another is focused.
  useLayoutEffect(() => {
    const rows = ref.current?.querySelectorAll<HTMLElement>(ROW) ?? [];
    if (rows.length === 0) return;
    const current = [...rows].find((r) => r.tabIndex === 0) ?? rows[0];
    for (const r of rows) r.tabIndex = r === current ? 0 : -1;
  });

  return (
    <tbody
      ref={ref}
      data-slot="table-body"
      className={cn("max-sm:block", className)}
      onFocus={(e) => {
        const row = (e.target as HTMLElement).closest<HTMLElement>(ROW);
        if (row && ref.current?.contains(row)) {
          for (const r of ref.current.querySelectorAll<HTMLElement>(ROW)) {
            r.tabIndex = r === row ? 0 : -1;
          }
        }
        onFocus?.(e);
      }}
      onKeyDown={(e) => {
        onKeyDown?.(e);
        const row = e.target as HTMLElement;
        if (e.defaultPrevented || !row.matches(ROW)) return;
        const rows = [...(ref.current?.querySelectorAll<HTMLElement>(ROW) ?? [])];
        const at = rows.indexOf(row);
        const to =
          e.key === "ArrowDown"
            ? rows[at + 1]
            : e.key === "ArrowUp"
              ? rows[at - 1]
              : e.key === "Home"
                ? rows[0]
                : e.key === "End"
                  ? rows.at(-1)
                  : undefined;
        if (to) {
          e.preventDefault();
          to.focus();
        }
      }}
      {...props}
    />
  );
}

function TableRow({
  className,
  onOpen,
  onKeyDown,
  onClick,
  ...props
}: React.ComponentProps<"tr"> & {
  /** Opens what the row stands for; makes the row reachable from the keyboard. */
  onOpen?: () => void;
}) {
  const openable = Boolean(onOpen);
  return (
    <tr
      data-slot="table-row"
      data-openable={openable ? "" : undefined}
      tabIndex={openable ? -1 : undefined}
      onClick={(e) => {
        onClick?.(e);
        // A click on a link or button inside the row is that control's, not the row's.
        if (!e.defaultPrevented && !(e.target as HTMLElement).closest("a,button,input,label")) {
          onOpen?.();
        }
      }}
      onKeyDown={(e) => {
        onKeyDown?.(e);
        if (!e.defaultPrevented && e.key === "Enter" && e.target === e.currentTarget) {
          e.preventDefault();
          onOpen?.();
        }
      }}
      className={cn(
        // One column no wider than the screen: an auto column grows to its longest value.
        "group/row border-b border-rule last:border-b-0 max-sm:grid max-sm:grid-cols-[minmax(0,1fr)] max-sm:gap-1 max-sm:py-3",
        openable &&
          "cursor-pointer outline-none hover:bg-foreground/3 focus-visible:bg-accent focus-visible:shadow-[inset_3px_0_0_0_var(--color-primary)]",
        className,
      )}
      {...props}
    />
  );
}

function TableHead({
  className,
  numeric = false,
  ...props
}: React.ComponentProps<"th"> & { numeric?: boolean }) {
  return (
    <th
      data-slot="table-head"
      scope="col"
      className={cn(
        "h-9 border-b border-rule px-3 text-left align-bottom text-xs font-medium whitespace-nowrap text-muted-foreground",
        numeric && "text-right",
        className,
      )}
      {...props}
    />
  );
}

function TableCell({
  className,
  numeric = false,
  label,
  ...props
}: React.ComponentProps<"td"> & {
  numeric?: boolean;
  /** The column's name, shown before the value when the row stacks on a phone. */
  label?: string;
}) {
  return (
    <td
      data-slot="table-cell"
      data-label={label}
      className={cn(
        "px-3 py-2.5 align-middle",
        numeric && "text-right tabular-nums",
        "max-sm:flex max-sm:min-w-0 max-sm:items-baseline max-sm:justify-between max-sm:gap-3 max-sm:px-0 max-sm:py-0 max-sm:text-left",
        label &&
          "max-sm:before:shrink-0 max-sm:before:text-xs max-sm:before:whitespace-nowrap max-sm:before:text-muted-foreground max-sm:before:content-[attr(data-label)]",
        className,
      )}
      {...props}
    />
  );
}

/** A row's actions: shown on hover or focus, always on a touch screen. */
function RowActions({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "flex items-center justify-end gap-1 transition-opacity duration-150 group-focus-within/row:opacity-100 group-hover/row:opacity-100 pointer-fine:opacity-0",
        className,
      )}
      {...props}
    />
  );
}

export { RowActions, Table, TableBody, TableCell, TableHead, TableHeader, TableRow };
