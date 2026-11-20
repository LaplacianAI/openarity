/**
 * Choosing one of a few: a select for a list, and a segmented control for
 * two or three that fit on a line. Both are the dashboard's own, so a choice
 * looks the same on every screen rather than as each browser draws it.
 */
import type { LucideIcon } from "lucide-react";
import { RadioGroup } from "radix-ui";

import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

export type ChoiceOption<T extends string> = {
  value: T;
  label: string;
  /** Said under the field once this option is chosen. */
  hint?: string;
};

/** Options under a heading; a group with no label is drawn without one. */
export type ChoiceGroup<T extends string> = { label: string; options: readonly ChoiceOption<T>[] };

/** A list given flat or in groups, as groups. */
function grouped<T extends string>(
  options: readonly ChoiceOption<T>[] | readonly ChoiceGroup<T>[],
): readonly ChoiceGroup<T>[] {
  const first = options[0];
  if (first === undefined) return [];
  return "options" in first
    ? (options as readonly ChoiceGroup<T>[])
    : [{ label: "", options: options as readonly ChoiceOption<T>[] }];
}

/** The option chosen, from a flat or grouped list. */
export function chosen<T extends string>(
  options: readonly ChoiceOption<T>[] | readonly ChoiceGroup<T>[],
  value: T | "",
): ChoiceOption<T> | undefined {
  return grouped(options)
    .flatMap((g) => g.options)
    .find((o) => o.value === value);
}

/**
 * One choice from a list, anchored under its field at the field's width. A
 * Field passes its description and invalid state, which land on the trigger.
 * An empty value is no choice yet, and the placeholder shows.
 */
export function Choice<T extends string>({
  id,
  value,
  options,
  onChange,
  placeholder,
  disabled,
  ...aria
}: {
  id: string;
  value: T | "";
  options: readonly ChoiceOption<T>[] | readonly ChoiceGroup<T>[];
  onChange: (next: T) => void;
  placeholder?: string;
  disabled?: boolean;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  const groups = grouped(options);
  const known = new Set(groups.flatMap((g) => g.options.map((o) => o.value as string)));
  // A value the list does not offer, such as a provider a newer brain saved,
  // is shown as it is rather than as a blank field; it cannot be chosen again.
  const stray = value !== "" && !known.has(value) ? value : null;
  return (
    <Select
      value={value}
      // Only what the list offers is handed on, never a stray string.
      onValueChange={(v) => {
        if (known.has(v)) onChange(v as T);
      }}
      disabled={disabled}
    >
      <SelectTrigger id={id} className="w-full" {...aria}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent position="popper" align="start">
        {stray ? (
          <SelectItem value={stray} disabled>
            {stray}
          </SelectItem>
        ) : null}
        {groups.map((g, i) => {
          const items = g.options.map((o) => (
            <SelectItem key={o.value} value={o.value}>
              {o.label}
            </SelectItem>
          ));
          return g.label ? (
            <SelectGroup key={g.label}>
              <SelectLabel>{g.label}</SelectLabel>
              {items}
            </SelectGroup>
          ) : (
            // biome-ignore lint/suspicious/noArrayIndexKey: an unlabelled group has nothing else to key on, and the list does not reorder
            <SelectGroup key={i}>{items}</SelectGroup>
          );
        })}
      </SelectContent>
    </Select>
  );
}

export type SegmentOption<T extends string> = {
  value: T;
  label: string;
  icon?: LucideIcon;
  /** The icon's colour class, such as a status colour. */
  tone?: string;
};

/**
 * Two or three choices drawn as one segmented control: a radio group, so the
 * arrow keys move between them and a screen reader hears one choice of many.
 */
export function Segments<T extends string>({
  label,
  value,
  options,
  onChange,
  disabled,
}: {
  label: string;
  value: T;
  options: readonly SegmentOption<T>[];
  onChange: (next: T) => void;
  disabled?: boolean;
}) {
  return (
    <RadioGroup.Root
      aria-label={label}
      orientation="horizontal"
      value={value}
      disabled={disabled}
      onValueChange={(v) => {
        const picked = options.find((o) => o.value === v);
        if (picked) onChange(picked.value);
      }}
      className="inline-flex max-w-full flex-wrap gap-0.5 rounded-md border border-border bg-background p-0.5"
    >
      {options.map((o) => {
        const Icon = o.icon;
        return (
          <RadioGroup.Item
            key={o.value}
            value={o.value}
            className="inline-flex h-8 items-center gap-1.5 rounded-md px-3 text-sm text-muted-foreground transition-[color,background-color] duration-(--dur-press) ease-out outline-none hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 active:scale-[0.97] disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-surface data-[state=checked]:font-medium data-[state=checked]:text-foreground data-[state=checked]:shadow-[inset_0_0_0_1px_var(--sheet-edge)] motion-reduce:active:scale-100"
          >
            {Icon ? <Icon aria-hidden className={cn("size-3.5", o.tone)} /> : null}
            {o.label}
          </RadioGroup.Item>
        );
      })}
    </RadioGroup.Root>
  );
}
