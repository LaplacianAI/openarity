/**
 * Characters a reader may copy or compare: a tool's output, a config, a
 * payload. Mono, wrapped rather than scrolled sideways, folded past a dozen
 * lines, with a copy button that takes the whole of it.
 */
import { Check, ChevronDown, Copy } from "lucide-react";
import { useEffect, useState } from "react";

import { pretty } from "@/lib/run-trace";
import { cn } from "@/lib/utils";

const FOLD_LINES = 12;

export function CodeBlock({
  code,
  label = "code",
  className,
}: {
  code: string;
  /** What it is, for the copy button's name: "output", "the request". */
  label?: string;
  className?: string;
}) {
  const lines = code.split("\n").length;
  const long = lines > FOLD_LINES;
  const [open, setOpen] = useState(false);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(t);
  }, [copied]);

  return (
    <div
      data-folded={long && !open ? "" : undefined}
      className={cn(
        "group/code relative rounded-lg bg-sheet shadow-[inset_0_0_0_1px_var(--color-rule)]",
        className,
      )}
    >
      <pre
        className={cn(
          "overflow-x-hidden p-3 pr-10 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap",
          long &&
            !open &&
            "max-h-60 overflow-y-hidden [mask-image:linear-gradient(to_bottom,black_70%,transparent)]",
        )}
      >
        {code}
      </pre>
      <button
        type="button"
        onClick={() => void navigator.clipboard?.writeText(code).then(() => setCopied(true))}
        aria-label={`Copy ${label}`}
        className="absolute top-1.5 right-1.5 flex size-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/6 hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
      >
        {copied ? (
          <Check aria-hidden className="size-3.5 text-succeeded" />
        ) : (
          <Copy aria-hidden className="size-3.5" />
        )}
      </button>
      {copied ? (
        <span role="status" className="sr-only">
          Copied
        </span>
      ) : null}
      {long ? (
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
          className="flex w-full items-center gap-1 border-t border-rule px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
        >
          <ChevronDown
            aria-hidden
            className={cn("size-3.5 transition-transform duration-150", open && "rotate-180")}
          />
          {open ? "Show less" : `Show all ${lines} lines`}
        </button>
      ) : null}
    </div>
  );
}

/** JSON, indented; anything that does not parse is shown as it came. */
export function JsonView({
  value,
  label,
  className,
}: {
  value: unknown;
  label?: string;
  className?: string;
}) {
  const text = typeof value === "string" ? pretty(value) : JSON.stringify(value, null, 2);
  return <CodeBlock code={text ?? String(value)} label={label} className={className} />;
}
