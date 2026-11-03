import { cn } from "cn";
import * as React from "react";

// 16px on a phone so iOS does not zoom on focus, 14px from 768px. No shadow
// at rest; the hairline turns teal on focus with a 3px ring.
const field =
  "w-full min-w-0 rounded-lg border border-input bg-transparent px-3 text-base transition-[border-color,box-shadow] duration-150 outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 aria-invalid:border-destructive aria-invalid:ring-destructive/20 md:text-sm dark:aria-invalid:ring-destructive/40";

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        field,
        "h-9 py-1 file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground",
        className,
      )}
      {...props}
    />
  );
}

function Textarea({ className, ...props }: React.ComponentProps<"textarea">) {
  return (
    <textarea data-slot="textarea" className={cn(field, "min-h-20 py-2", className)} {...props} />
  );
}

export { Input, Textarea };
