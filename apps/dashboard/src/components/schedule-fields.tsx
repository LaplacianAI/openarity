/**
 * The fields a schedule or watcher form shares: when it runs, with the next
 * three times as the rule is typed; the zone; who runs it; and where it
 * answers.
 */
import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Check, ChevronDown, Hash, Lock, MessageSquare, Search, User } from "lucide-react";
import { Popover } from "radix-ui";
import { type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";

import { listAgents, listScheduleTargets, previewSchedule } from "@/api";
import { nextCursorOf } from "@/api/client";
import type { SchedulePlace, ScheduleTargetPick } from "@/api/types";
import { Choice, Segments } from "@/components/choices";
import { type Picked, Picker } from "@/components/picker";
import { Field } from "@/components/registry-bits";
import { Input } from "@/components/ui/input";
import {
  type AgentPick,
  type Unit as LengthUnit,
  type ScheduleDraft,
  whenOf,
} from "@/lib/schedule-draft";
import { placeReason } from "@/lib/schedules";
import { cn } from "@/lib/utils";
import { knownZone, timeBoth, zones } from "@/lib/when";

/** How long typing pauses before the brain is asked for the next times. */
const SETTLE_MS = 300;

function useSettled<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(timer);
  }, [value, ms]);
  return settled;
}

export const UNITS: { value: LengthUnit; label: string }[] = [
  { value: "m", label: "minutes" },
  { value: "h", label: "hours" },
  { value: "d", label: "days" },
  { value: "w", label: "weeks" },
];

const PRESETS = [
  { rule: "0 9 * * 1-5", label: "Weekdays at 09:00" },
  { rule: "0 * * * *", label: "Every hour" },
  { rule: "0 9 * * 1", label: "Mondays at 09:00" },
];

/**
 * When a schedule runs: once, every so often, or on a cron rule, and under
 * it the next three times as the brain reads the rule. The preview is
 * advice: a rule it cannot read says why, and Create still asks the brain.
 */
export function WhenField({
  teamId,
  draft,
  set,
}: {
  teamId: string;
  draft: ScheduleDraft;
  set: (patch: Partial<ScheduleDraft>) => void;
}) {
  const id = useId();
  const when = whenOf(draft);
  const settled = useSettled(JSON.stringify([when, draft.zone]), SETTLE_MS);
  const asked = JSON.parse(settled) as [ReturnType<typeof whenOf>, string];
  const preview = useQuery({
    queryKey: ["schedule-preview", teamId, settled],
    queryFn: ({ signal }) =>
      previewSchedule(
        teamId,
        { when: asked[0] as Exclude<ReturnType<typeof whenOf>, string>, timezone: asked[1] },
        signal,
      ),
    enabled: typeof asked[0] !== "string" && knownZone(asked[1]),
    placeholderData: keepPreviousData,
    staleTime: 60_000,
  });

  return (
    <div className="space-y-3">
      <Segments
        label="When it runs"
        value={draft.mode}
        options={[
          { value: "once", label: "Once" },
          { value: "repeat", label: "Repeat" },
          { value: "cron", label: "Cron rule" },
        ]}
        onChange={(mode) => set({ mode })}
      />
      {draft.mode === "once" ? (
        <div className="grid max-w-md gap-3 sm:grid-cols-2">
          <Field label="Day" htmlFor={`${id}-date`}>
            <Input
              id={`${id}-date`}
              type="date"
              value={draft.date}
              onChange={(e) => set({ date: e.target.value })}
            />
          </Field>
          <Field label="Time" htmlFor={`${id}-time`}>
            <Input
              id={`${id}-time`}
              type="time"
              value={draft.time}
              onChange={(e) => set({ time: e.target.value })}
            />
          </Field>
        </div>
      ) : draft.mode === "repeat" ? (
        <div className="flex max-w-md flex-wrap items-end gap-3">
          <Field label="Every" htmlFor={`${id}-every`}>
            <Input
              id={`${id}-every`}
              inputMode="numeric"
              autoComplete="off"
              className="w-24 tabular-nums"
              value={draft.every}
              onChange={(e) => set({ every: e.target.value })}
            />
          </Field>
          <div className="w-36">
            <Choice
              id={`${id}-unit`}
              aria-label="Unit"
              value={draft.unit}
              options={UNITS}
              onChange={(unit) => set({ unit })}
            />
          </div>
        </div>
      ) : (
        <div className="space-y-2">
          <Field
            label="Cron rule"
            htmlFor={`${id}-cron`}
            hint="Five fields: minute, hour, day of the month, month, day of the week."
          >
            <Input
              id={`${id}-cron`}
              className="max-w-xs font-mono"
              spellCheck={false}
              autoComplete="off"
              value={draft.cron}
              onChange={(e) => set({ cron: e.target.value })}
            />
          </Field>
          <fieldset className="flex flex-wrap gap-1.5">
            <legend className="sr-only">Common rules</legend>
            {PRESETS.map((p) => (
              <button
                key={p.rule}
                type="button"
                aria-pressed={draft.cron.trim() === p.rule}
                onClick={() => set({ cron: p.rule })}
                className="h-7 rounded-md px-2.5 text-xs text-muted-foreground shadow-[inset_0_0_0_1px_var(--color-rule)] transition-[color,transform] duration-(--dur-press) ease-out hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring active:scale-[0.97] aria-pressed:bg-surface aria-pressed:text-foreground motion-reduce:active:scale-100"
              >
                {p.label}
              </button>
            ))}
          </fieldset>
        </div>
      )}
      <NextTimes
        problem={typeof when === "string" ? when : preview.isError ? preview.error.message : null}
        times={preview.data?.upcoming ?? null}
        zone={preview.data?.timezone ?? draft.zone}
        stale={preview.isPlaceholderData || settled !== JSON.stringify([when, draft.zone])}
      />
    </div>
  );
}

/** The next three times, in the reader's zone and the schedule's. It does not animate: it changes with every key. */
function NextTimes({
  problem,
  times,
  zone,
  stale,
}: {
  problem: string | null;
  times: string[] | null;
  zone: string;
  stale: boolean;
}) {
  return (
    <div aria-live="polite" className="rounded-md bg-surface px-3 py-2 text-sm">
      {problem ? (
        <p className="text-destructive">{problem}</p>
      ) : times === null ? (
        <p className="text-muted-foreground">Reading the rule…</p>
      ) : times.length === 0 ? (
        <p className="text-muted-foreground">It would not fire again.</p>
      ) : (
        <div className={cn(stale && "text-muted-foreground")}>
          <span className="text-xs text-muted-foreground">Next </span>
          {times.map((t, i) => {
            const { here, there } = timeBoth(t, zone);
            return (
              <span key={t} title={there ?? undefined}>
                {i > 0 ? ", " : ""}
                {here}
              </span>
            );
          })}
          {timeBoth(times[0] as string, zone).there ? (
            <p className="text-xs text-muted-foreground">Read in {zone}; shown in your own zone.</p>
          ) : null}
        </div>
      )}
    </div>
  );
}

/** The zone a rule is read in: any IANA name this browser knows. */
export function ZoneField({
  id,
  value,
  onChange,
  hint,
}: {
  id: string;
  value: string;
  onChange: (zone: string) => void;
  hint?: string;
}) {
  const listId = `${id}-zones`;
  const all = useMemo(() => zones(value), [value]);
  return (
    <Field
      label="Time zone"
      htmlFor={id}
      hint={hint}
      error={
        value && !knownZone(value) ? "Pick a zone from the list, such as Europe/London." : null
      }
    >
      <Input
        id={id}
        list={listId}
        className="max-w-xs"
        autoComplete="off"
        spellCheck={false}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <datalist id={listId}>
        {all.map((z) => (
          <option key={z} value={z} />
        ))}
      </datalist>
    </Field>
  );
}

/** Who runs it: the hub unless one agent is picked. */
export function AgentField({
  teamId,
  value,
  onChange,
}: {
  teamId: string;
  value: AgentPick;
  onChange: (next: AgentPick) => void;
}) {
  const id = useId();
  return (
    <div className="space-y-1.5">
      <span id={`${id}-label`} className="text-sm font-medium">
        Agent
      </span>
      <Picker
        label="Agent"
        noun="agent"
        max={1}
        value={value ? [value] : []}
        onChange={(next: Picked[]) => onChange(next[0] ?? null)}
        search={(q) =>
          listAgents(teamId, null, { q, limit: 20 }).then((p) => ({
            items: p.items.map((a) => ({ id: a.id, name: a.name })),
            more: Boolean(p.next_cursor),
          }))
        }
        searchKey={["agents", teamId, "pick"]}
        field={{ id: `${id}-field`, labelId: `${id}-label`, placeholder: "The hub" }}
      />
      <p className="text-xs text-muted-foreground">
        The hub runs it, and hands work on, unless you pick one agent.{" "}
        {value ? (
          <button
            type="button"
            className="font-medium text-foreground underline underline-offset-4"
            onClick={() => onChange(null)}
          >
            Use the hub
          </button>
        ) : null}
      </p>
    </div>
  );
}

const GROUPS: { kind: SchedulePlace["kind"]; label: string }[] = [
  { kind: "chat", label: "Chats you talked in" },
  { kind: "channel", label: "Channels" },
  { kind: "direct", label: "Your direct messages" },
];

const OWN: SchedulePlace = {
  kind: "chat",
  label: "Its own conversation",
  detail: "On the dashboard, where you can read every run",
  channel: { id: "", name: "", provider: "" },
  available: true,
  target: { kind: "own" },
};

function placeKey(t: ScheduleTargetPick): string {
  return [t.kind, t.session_id ?? "", t.channel_id ?? "", t.ref ?? ""].join(":");
}

function PlaceIcon({ p }: { p: SchedulePlace }) {
  const Icon =
    p.target.kind === "own"
      ? MessageSquare
      : p.kind === "direct"
        ? User
        : p.kind === "channel"
          ? p.detail.startsWith("Private")
            ? Lock
            : Hash
          : MessageSquare;
  return <Icon aria-hidden className="size-4 shrink-0 text-muted-foreground" />;
}

/**
 * Where it answers: one searchable list over its own conversation, the chats
 * the person talked in, channels, and their direct messages. A place they
 * cannot use is shown with why, never left out; there is no box for an id.
 */
export function PlacePicker({
  teamId,
  value,
  label,
  onChange,
}: {
  teamId: string;
  value: ScheduleTargetPick | null;
  label: string;
  onChange: (pick: ScheduleTargetPick | null, label: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [typed, setTyped] = useState("");
  const q = useSettled(typed, 200);
  const [active, setActive] = useState(0);
  const listId = useId();
  const labelId = useId();
  const input = useRef<HTMLInputElement>(null);

  const places = useInfiniteQuery({
    queryKey: ["schedule-targets", teamId, q],
    queryFn: ({ pageParam, signal }) => listScheduleTargets(teamId, { q }, pageParam, signal),
    initialPageParam: null as string | null,
    getNextPageParam: nextCursorOf,
    enabled: open,
    staleTime: 60_000,
  });
  const found = places.data?.pages.flatMap((p) => p.items) ?? [];
  const problems = places.data?.pages[0]?.problems ?? [];
  const showOwn = !q.trim() || "its own conversation".includes(q.trim().toLowerCase());
  const rows = [
    ...(showOwn ? [OWN] : []),
    ...GROUPS.flatMap((g) => found.filter((p) => p.kind === g.kind)),
  ];
  const pickable = rows.map((p, i) => (p.available ? i : -1)).filter((i) => i >= 0);
  const indexed = rows.map((p, i) => ({ p, i }));
  const sections = [
    { key: "own", label: "", rows: indexed.filter(({ p }) => p.target.kind === "own") },
    ...GROUPS.map((g) => ({
      key: g.kind,
      label: g.label,
      rows: indexed.filter(({ p }) => p.target.kind !== "own" && p.kind === g.kind),
    })),
  ].filter((sec) => sec.rows.length > 0);
  const current = value ? placeKey(value) : placeKey(OWN.target);

  const pick = (p: SchedulePlace) => {
    if (!p.available) return;
    onChange(p.target.kind === "own" ? null : p.target, p.target.kind === "own" ? "" : p.label);
    setOpen(false);
  };
  const move = (by: number) => {
    if (pickable.length === 0) return;
    const at = pickable.indexOf(active);
    const next = pickable[(at + by + pickable.length) % pickable.length] ?? 0;
    setActive(next);
    document.getElementById(`${listId}-${next}`)?.scrollIntoView?.({ block: "nearest" });
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      move(1);
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      move(-1);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const p = rows[active];
      if (p) pick(p);
    }
  };

  return (
    <div className="space-y-1.5">
      <span id={labelId} className="text-sm font-medium">
        Where it answers
      </span>
      <Popover.Root
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          if (o) {
            setTyped("");
            setActive(0);
          }
        }}
      >
        <Popover.Trigger asChild>
          <button
            type="button"
            aria-labelledby={`${labelId} ${labelId}-value`}
            className="flex h-9 w-full max-w-md items-center gap-2 rounded-md border border-input bg-transparent px-3 text-left text-sm outline-none transition-[transform] duration-(--dur-press) ease-out focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 active:scale-[0.99] motion-reduce:active:scale-100"
          >
            <span id={`${labelId}-value`} className="min-w-0 flex-1 truncate">
              {value ? label || "A place you picked" : "Its own conversation"}
            </span>
            <ChevronDown aria-hidden className="size-4 shrink-0 text-muted-foreground" />
          </button>
        </Popover.Trigger>
        <Popover.Portal>
          <Popover.Content
            align="start"
            sideOffset={4}
            onOpenAutoFocus={(e) => {
              e.preventDefault();
              input.current?.focus();
            }}
            className="z-50 w-(--radix-popover-trigger-width) min-w-72 origin-(--radix-popover-content-transform-origin) rounded-lg bg-popover text-popover-foreground shadow-lg ring-1 ring-sheet-edge outline-none duration-(--dur-pop) ease-out data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-96 data-[state=open]:animate-in data-[state=open]:fade-in-0 data-[state=open]:zoom-in-96"
          >
            <div className="relative border-b border-rule">
              <Search
                aria-hidden
                className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground"
              />
              <input
                ref={input}
                role="combobox"
                aria-label="Search places"
                aria-expanded="true"
                aria-controls={listId}
                aria-activedescendant={rows[active] ? `${listId}-${active}` : undefined}
                value={typed}
                onChange={(e) => {
                  setTyped(e.target.value);
                  setActive(0);
                }}
                onKeyDown={onKey}
                placeholder="Search chats and channels"
                className="h-10 w-full bg-transparent pr-3 pl-9 text-sm outline-none placeholder:text-muted-foreground"
              />
            </div>
            <div
              id={listId}
              role="listbox"
              aria-label="Places"
              className="relative max-h-80 overflow-y-auto p-1"
            >
              {sections.map((sec) => (
                // biome-ignore lint/a11y/useSemanticElements: a listbox holds options in groups; a fieldset is not one
                <div
                  key={sec.key}
                  role="group"
                  aria-labelledby={sec.label ? `${listId}-${sec.key}` : undefined}
                  aria-label={sec.label ? undefined : "Its own conversation"}
                >
                  {sec.label ? (
                    <div
                      id={`${listId}-${sec.key}`}
                      className="px-2 pt-2 pb-1 text-xs text-muted-foreground"
                    >
                      {sec.label}
                    </div>
                  ) : null}
                  {sec.rows.map(({ p, i }) => {
                    const why = placeReason(p);
                    const selected = placeKey(p.target) === current;
                    return (
                      // biome-ignore lint/a11y/useKeyWithClickEvents: the search field takes the keys, pointing at this option with aria-activedescendant
                      <div
                        key={placeKey(p.target)}
                        id={`${listId}-${i}`}
                        role="option"
                        tabIndex={-1}
                        aria-selected={selected}
                        aria-disabled={!p.available || undefined}
                        onMouseDown={(e) => e.preventDefault()}
                        onMouseMove={() => p.available && setActive(i)}
                        onClick={() => pick(p)}
                        className={cn(
                          "flex cursor-default items-start gap-2 rounded-md px-2 py-1.5 text-sm",
                          i === active && p.available && "bg-accent-wash",
                          !p.available && "opacity-60",
                        )}
                      >
                        <PlaceIcon p={p} />
                        <span className="min-w-0 flex-1">
                          <span className="block truncate">{p.label}</span>
                          <span className="block truncate text-xs text-muted-foreground">
                            {why ?? [p.channel.name, p.detail].filter(Boolean).join(" · ")}
                          </span>
                        </span>
                        {selected ? (
                          <Check aria-hidden className="mt-0.5 size-4 shrink-0 text-primary" />
                        ) : null}
                      </div>
                    );
                  })}
                </div>
              ))}
            </div>
            <div className="px-1 pb-1">
              {places.isPending ? (
                <p className="px-2 py-2 text-xs text-muted-foreground" role="status">
                  Looking…
                </p>
              ) : places.isError ? (
                <p className="px-2 py-2 text-xs text-destructive" role="alert">
                  {places.error.message}
                </p>
              ) : rows.length === 0 ? (
                <p className="px-2 py-2 text-xs text-muted-foreground" role="status">
                  Nothing matches “{q}”.
                </p>
              ) : null}
              {places.hasNextPage ? (
                <button
                  type="button"
                  onClick={() => places.fetchNextPage()}
                  className="w-full rounded-md px-2 py-1.5 text-left text-xs text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
                >
                  {places.isFetchingNextPage ? "Loading…" : "Show more"}
                </button>
              ) : null}
            </div>
            {problems.length > 0 ? (
              <div className="space-y-1 border-t border-rule px-3 py-2 text-xs text-muted-foreground">
                {problems.map((p) => (
                  <p key={p.channel_id}>
                    {p.provider}: {p.message}
                  </p>
                ))}
              </div>
            ) : null}
          </Popover.Content>
        </Popover.Portal>
      </Popover.Root>
      <p className="text-xs text-muted-foreground">
        Its own conversation is on the dashboard. A chat or channel gets its answer where people
        read it.
      </p>
    </div>
  );
}
