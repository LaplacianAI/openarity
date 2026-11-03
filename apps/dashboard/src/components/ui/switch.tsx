import { cn } from "cn";
import { Check } from "lucide-react";
import { Checkbox as CheckboxPrimitive, Switch as SwitchPrimitive } from "radix-ui";
import { type ReactNode, useId } from "react";

// On is teal: a switch's state is a selection, the one other place teal goes.
function Switch({ className, ...props }: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        "peer inline-flex h-5 w-9 shrink-0 items-center rounded-full p-0.5 transition-colors duration-150 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 data-[state=checked]:bg-primary data-[state=unchecked]:bg-foreground/50",
        className,
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb className="pointer-events-none block size-4 rounded-full bg-sheet transition-transform duration-150 ease-out data-[state=checked]:translate-x-4" />
    </SwitchPrimitive.Root>
  );
}

/**
 * A setting that is on or off, with what it does said under its name. The
 * whole row is the label, so a thumb can hit it anywhere.
 */
function SwitchField({
  label,
  description,
  ...props
}: React.ComponentProps<typeof SwitchPrimitive.Root> & {
  label: string;
  description?: ReactNode;
}) {
  const id = useId();
  return (
    <div className="flex items-start justify-between gap-4">
      <label htmlFor={id} className="min-w-0 flex-1 cursor-pointer">
        <span id={`${id}-l`} className="block text-sm font-medium">
          {label}
        </span>
        {description ? (
          <span id={`${id}-d`} className="mt-0.5 block text-xs text-muted-foreground">
            {description}
          </span>
        ) : null}
      </label>
      <Switch
        id={id}
        aria-labelledby={`${id}-l`}
        aria-describedby={description ? `${id}-d` : undefined}
        {...props}
      />
    </div>
  );
}

function Checkbox({ className, ...props }: React.ComponentProps<typeof CheckboxPrimitive.Root>) {
  return (
    <CheckboxPrimitive.Root
      data-slot="checkbox"
      className={cn(
        "peer flex size-4 shrink-0 items-center justify-center rounded-sm border border-foreground/50 transition-colors duration-150 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive data-[state=checked]:border-primary data-[state=checked]:bg-primary data-[state=checked]:text-primary-foreground",
        className,
      )}
      {...props}
    >
      <CheckboxPrimitive.Indicator>
        <Check aria-hidden className="size-3" strokeWidth={3} />
      </CheckboxPrimitive.Indicator>
    </CheckboxPrimitive.Root>
  );
}

export { Checkbox, Switch, SwitchField };
