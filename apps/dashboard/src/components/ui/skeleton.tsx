import { cn } from "cn";

// A still tone shaped like what it stands in for. A pulse on every block of a
// loading page is noise; the shape alone says "coming".
function Skeleton({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div data-slot="skeleton" className={cn("rounded-lg bg-foreground/8", className)} {...props} />
  );
}

export { Skeleton };
