/**
 * ⌘K: every screen, and the team's agents, skills and sessions by name.
 *
 * It opens and closes with no animation: a thing reached for many times a
 * day should never make anyone wait for it. The team's lists are read only
 * while it is open, and the first page of each is enough to find by name.
 */
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { BookOpen, Bot, CornerDownLeft, MessagesSquare, Search } from "lucide-react";
import { Dialog as DialogPrimitive } from "radix-ui";
import { type ComponentType, useEffect, useId, useMemo, useState } from "react";

import { listAgents, listSessions, listSkills } from "@/api";
import { cn } from "@/lib/utils";

export type Destination = {
  label: string;
  /** What kind of thing it is, shown at the right: a screen, an agent. */
  kind: string;
  icon: ComponentType<{ className?: string; "aria-hidden"?: boolean }>;
  to: string;
  params?: Record<string, string>;
};

/** Opens on ⌘K or Ctrl+K anywhere in the dashboard. */
export function useCommandKey(onOpen: () => void) {
  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() === "k" && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        onOpen();
      }
    };
    window.addEventListener("keydown", down);
    return () => window.removeEventListener("keydown", down);
  }, [onOpen]);
}

export function SearchButton({
  onOpen,
  compact = false,
}: {
  onOpen: () => void;
  compact?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onOpen}
      aria-label="Search, ⌘K"
      className={cn(
        "flex h-8 items-center gap-2 rounded-md text-sm text-muted-foreground hover:bg-sheet/60 hover:text-foreground focus-visible:outline-2 focus-visible:outline-offset-[-2px]",
        compact
          ? "w-8 justify-center"
          : "w-64 px-2.5 shadow-[inset_0_0_0_1px_var(--color-sheet-edge)]",
      )}
    >
      <Search aria-hidden className="size-4 shrink-0" />
      {compact ? null : (
        <>
          <span className="flex-1 text-left">Search</span>
          <kbd className="font-sans text-xs">⌘K</kbd>
        </>
      )}
    </button>
  );
}

export function CommandPalette({
  open,
  onOpenChange,
  teamId,
  screens,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  teamId: string | null;
  screens: readonly Destination[];
}) {
  const navigate = useNavigate();
  const [query, setQuery] = useState("");
  const [at, setAt] = useState(0);
  const listId = useId();

  const enabled = open && Boolean(teamId);
  const agents = useQuery({
    queryKey: ["agents", teamId, "palette"],
    queryFn: () => listAgents(teamId as string),
    enabled,
  });
  const skills = useQuery({
    queryKey: ["skills", teamId, "palette"],
    queryFn: () => listSkills(teamId as string),
    enabled,
  });
  const sessions = useQuery({
    queryKey: ["sessions", teamId, "palette"],
    queryFn: () => listSessions(teamId as string),
    enabled,
  });

  const all = useMemo<Destination[]>(
    () => [
      ...screens,
      ...(agents.data?.items ?? []).map((a) => ({
        label: a.name,
        kind: "Agent",
        icon: Bot,
        to: "/ui/agents/$agentId",
        params: { agentId: a.id },
      })),
      ...(skills.data?.items ?? []).map((s) => ({
        label: s.name,
        kind: "Skill",
        icon: BookOpen,
        to: "/ui/skills",
      })),
      ...(sessions.data?.items ?? []).map((s) => ({
        label: s.provider_ref,
        kind: "Session",
        icon: MessagesSquare,
        to: "/ui/sessions/$sessionId",
        params: { sessionId: s.id },
      })),
    ],
    [screens, agents.data, skills.data, sessions.data],
  );

  const q = query.trim().toLowerCase();
  const found = q === "" ? [...screens] : all.filter((d) => d.label.toLowerCase().includes(q));
  const current = Math.min(at, Math.max(0, found.length - 1));

  const close = (next: boolean) => {
    onOpenChange(next);
    if (!next) {
      setQuery("");
      setAt(0);
    }
  };
  const go = (d: Destination | undefined) => {
    if (!d) return;
    close(false);
    void navigate({ to: d.to, params: d.params } as never);
  };

  return (
    <DialogPrimitive.Root open={open} onOpenChange={close}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-black/30" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="fixed top-[12vh] left-1/2 z-50 w-[min(36rem,calc(100vw-2rem))] -translate-x-1/2 overflow-hidden rounded-xl bg-popover text-popover-foreground shadow-xl ring-1 ring-sheet-edge outline-none"
        >
          <DialogPrimitive.Title className="sr-only">Go to</DialogPrimitive.Title>
          <div className="flex items-center gap-2 border-b border-rule px-3">
            <Search aria-hidden className="size-4 shrink-0 text-muted-foreground" />
            <input
              role="combobox"
              aria-expanded
              aria-controls={listId}
              aria-activedescendant={found.length > 0 ? `${listId}-${current}` : undefined}
              aria-label="Go to a screen, an agent, a skill or a session"
              placeholder="Screens, agents, skills, sessions"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setAt(0);
              }}
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setAt((current + 1) % Math.max(1, found.length));
                } else if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setAt((current - 1 + found.length) % Math.max(1, found.length));
                } else if (e.key === "Enter") {
                  e.preventDefault();
                  go(found[current]);
                }
              }}
              className="h-12 flex-1 bg-transparent text-base outline-none placeholder:text-muted-foreground sm:text-sm"
            />
          </div>
          <div
            id={listId}
            role="listbox"
            aria-label="Results"
            className="max-h-80 overflow-y-auto p-1.5"
          >
            {found.length === 0 ? (
              <p className="px-3 py-6 text-center text-sm text-muted-foreground">
                Nothing by that name.
              </p>
            ) : (
              found.map((d, i) => {
                const Icon = d.icon;
                const selected = i === current;
                return (
                  // biome-ignore lint/a11y/useKeyWithClickEvents: the input above handles the keys
                  <div
                    key={`${d.kind}-${d.label}-${Object.values(d.params ?? {}).join("")}`}
                    id={`${listId}-${i}`}
                    role="option"
                    tabIndex={-1}
                    aria-selected={selected}
                    onMouseMove={() => setAt(i)}
                    onClick={() => go(d)}
                    className={cn(
                      "flex cursor-pointer items-center gap-2.5 rounded-md px-2.5 py-2 text-sm",
                      selected && "bg-card",
                    )}
                  >
                    <Icon aria-hidden className="size-4 shrink-0 text-muted-foreground" />
                    <span className="min-w-0 flex-1 truncate">{d.label}</span>
                    <span className="text-xs text-muted-foreground">{d.kind}</span>
                    {selected ? (
                      <CornerDownLeft aria-hidden className="size-3.5 text-muted-foreground" />
                    ) : null}
                  </div>
                );
              })
            )}
          </div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
