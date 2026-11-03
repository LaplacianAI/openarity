import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "cn";
import { Loader2 } from "lucide-react";
import { Slot } from "radix-ui";
import * as React from "react";

// Four variants and no more: one filled action per view, a quiet outline for
// the everyday second action, a ghost for the frame and in-flow toggles, and
// brick for a destructive confirmation. A press scales to 0.97 so the button
// answers the hand before the network does.
const buttonVariants = cva(
  "relative inline-flex shrink-0 items-center justify-center gap-2 rounded-lg text-sm font-medium whitespace-nowrap transition-[color,background-color,box-shadow,transform] duration-160 ease-out outline-none select-none focus-visible:ring-[3px] focus-visible:ring-ring/50 active:scale-[0.97] disabled:pointer-events-none disabled:opacity-50 aria-busy:opacity-100 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        destructive:
          "bg-destructive text-destructive-foreground hover:bg-destructive/90 focus-visible:ring-destructive/30",
        outline:
          "bg-sheet text-foreground shadow-[inset_0_0_0_1px_var(--color-input)] hover:bg-card",
        ghost: "text-foreground hover:bg-foreground/6",
      },
      size: {
        default: "h-9 px-4 has-[>svg]:px-3",
        sm: "h-8 gap-1.5 px-3 has-[>svg]:px-2.5",
        icon: "size-9",
        "icon-sm": "size-8",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

function Button({
  className,
  variant = "default",
  size = "default",
  asChild = false,
  loading = false,
  disabled,
  children,
  ...props
}: React.ComponentProps<"button"> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean;
    /** Busy with the action it names: keeps its width, shows a spinner, ignores presses. */
    loading?: boolean;
  }) {
  const Comp = asChild ? Slot.Root : "button";

  return (
    <Comp
      data-slot="button"
      data-variant={variant}
      data-size={size}
      className={cn(buttonVariants({ variant, size, className }))}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      {...props}
    >
      {loading && !asChild ? (
        <>
          <span className="inline-flex items-center gap-[inherit] opacity-0">{children}</span>
          <span className="absolute inset-0 flex items-center justify-center">
            <Loader2 aria-hidden className="animate-spin" />
          </span>
        </>
      ) : (
        children
      )}
    </Comp>
  );
}

export { Button, buttonVariants };
