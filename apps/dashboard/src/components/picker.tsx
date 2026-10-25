/**
 * A search-first picker for lists too long to show whole: what is chosen as
 * chips, and an "Add" popover that asks the brain as you type.
 *
 * The popover is a combobox over a multi-select listbox, after the WAI-ARIA
 * pattern: focus stays in the search, arrows move the active option, Enter
 * toggles it and the popover stays open for the next. Matches come back in
 * the brain's order, never reshuffled, and the list says when more match than
 * it shows, so a person knows to keep typing rather than scroll.
 */
import { useQuery } from "@tanstack/react-query";
import { Check, Plus, X } from "lucide-react";
import { Popover } from "radix-ui";
import { type ReactNode, useEffect, useId, useRef, useState } from "react";

import { cn } from "@/lib/utils";

export type Picked = { id: string; name: string };

export type SearchResult = { items: Picked[]; more: boolean };

/** How long typing has to pause before the brain is asked. */
const SETTLE_MS = 200;

function useSettled(value: string, ms: number): string {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return settled;
}

export function Picker({
  label,
  noun,
  value,
  onChange,
  search,
  searchKey,
  exclude = [],
  extra,
}: {
  /** Names the chips' list and the search, as a person reads them. */
  label: string;
  /** One of what is picked, for "Add skill" and "No skill matches". */
  noun: string;
  value: Picked[];
  onChange: (next: Picked[]) => void;
  search: (q: string) => Promise<SearchResult>;
  /** Caches results apart per list: the query key's prefix. */
  searchKey: readonly unknown[];
  /** Ids never offered, such as the agent being edited. */
  exclude?: string[];
  /** More on a chip, after its name. */
  extra?: (p: Picked) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const q = useSettled(typed, SETTLE_MS);
  // The active option belongs to one search: a new search starts at its first match.
  const [cursor, setCursor] = useState({ q: "", i: 0 });
  const active = cursor.q === q ? cursor.i : 0;
  const setActive = (next: (i: number) => number) => setCursor({ q, i: next(active) });
  const listId = useId();
  const trigger = useRef<HTMLButtonElement>(null);

  const results = useQuery({
    queryKey: [...searchKey, "search", q],
    queryFn: () => search(q),
    enabled: open,
    placeholderData: (previous) => previous,
  });
  const options = (results.data?.items ?? []).filter((o) => !exclude.includes(o.id));
  const chosen = new Set(value.map((v) => v.id));

  const toggle = (o: Picked) =>
    onChange(chosen.has(o.id) ? value.filter((v) => v.id !== o.id) : [...value, o]);

  const optionId = (i: number) => `${listId}-${i}`;

  const onKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setActive((i) => Math.min(i + 1, Math.max(options.length - 1, 0)));
        break;
      case "ArrowUp":
        e.preventDefault();
        setActive((i) => Math.max(i - 1, 0));
        break;
      case "Enter": {
        e.preventDefault();
        const o = options[active];
        if (o) toggle(o);
        break;
      }
      case "Backspace":
        if (typed === "" && value.length > 0) onChange(value.slice(0, -1));
        break;
    }
  };

  // The active option follows the keyboard into view: the focus never leaves
  // the search, so the browser does not scroll to it.
  useEffect(() => {
    document.getElementById(optionId(active))?.scrollIntoView?.({ block: "nearest" });
  });

  return (
    <div className="space-y-1.5">
      {value.length > 0 ? (
        <ul aria-label={label} className="flex flex-wrap gap-1">
          {value.map((p) => (
            <li
              key={p.id}
              className="inline-flex max-w-full items-center gap-1 rounded border border-border bg-surface py-0.5 pr-0.5 pl-1.5 text-xs"
            >
              <span className="truncate">{p.name}</span>
              {extra?.(p)}
              <button
                type="button"
                aria-label={`Remove ${p.name}`}
                onClick={() => onChange(value.filter((v) => v.id !== p.id))}
                className="rounded p-0.5 text-muted-foreground hover:bg-secondary hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
              >
                <X aria-hidden className="size-3" />
              </button>
            </li>
          ))}
        </ul>
      ) : null}
      <Popover.Root
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (!o) setTyped("");
        }}
      >
        <Popover.Trigger asChild>
          <button
            ref={trigger}
            type="button"
            className="inline-flex items-center gap-1 rounded border border-dashed border-border px-1.5 py-0.5 text-xs text-muted-foreground hover:border-border-strong hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
          >
            <Plus aria-hidden className="size-3" />
            Add {noun}
          </button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="start"
            sideOffset={4}
            onCloseAutoFocus={(e) => {
              e.preventDefault();
              trigger.current?.focus();
            }}
            className="z-50 w-72 rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-md"
          >
            <input
              role="combobox"
              aria-label={`Search ${label.toLowerCase()}`}
              aria-expanded
              aria-controls={listId}
              aria-autocomplete="list"
              aria-activedescendant={options[active] ? optionId(active) : undefined}
              // biome-ignore lint/a11y/noAutofocus: the popover opens to search; focus belongs here
              autoFocus
              value={typed}
              placeholder={`Search ${label.toLowerCase()}…`}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={onKey}
              className="mb-1 h-8 w-full rounded border border-input bg-transparent px-2 text-sm outline-none focus-visible:ring-1 focus-visible:ring-ring"
            />
            <div
              id={listId}
              role="listbox"
              aria-label={label}
              aria-multiselectable="true"
              className="max-h-64 overflow-y-auto"
            >
              {options.map((o, i) => (
                // biome-ignore lint/a11y/useKeyWithClickEvents: the combobox takes the keys, pointing at this option with aria-activedescendant
                <div
                  key={o.id}
                  id={optionId(i)}
                  role="option"
                  tabIndex={-1}
                  aria-selected={chosen.has(o.id)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => toggle(o)}
                  onMouseEnter={() => setActive(() => i)}
                  className={cn(
                    "flex cursor-pointer items-center gap-2 rounded px-2 py-1 text-xs",
                    i === active && "bg-secondary",
                  )}
                >
                  <span
                    aria-hidden
                    className={cn(
                      "flex size-3.5 shrink-0 items-center justify-center rounded-sm border border-border",
                      chosen.has(o.id) && "border-primary bg-primary text-primary-foreground",
                    )}
                  >
                    {chosen.has(o.id) ? <Check className="size-3" /> : null}
                  </span>
                  <span className="truncate">{o.name}</span>
                </div>
              ))}
            </div>
            <p role="status" className="px-2 py-1 text-[11px] text-muted-foreground">
              {results.isError
                ? results.error.message
                : results.isPending
                  ? "Searching…"
                  : options.length === 0
                    ? `No ${noun} matches.`
                    : results.data?.more
                      ? "More match — keep typing to narrow it."
                      : ""}
            </p>
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
    </div>
  );
}
