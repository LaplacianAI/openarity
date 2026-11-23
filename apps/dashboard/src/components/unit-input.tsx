import { Input } from "@/components/ui/input";

/** A number with its unit after it, muted, and figures that line up. */
export function Unit({
  id,
  unit,
  value,
  onChange,
  decimal,
  ...aria
}: {
  id: string;
  unit: string;
  value: string;
  onChange: (next: string) => void;
  decimal?: boolean;
  "aria-label"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  return (
    <div className="relative w-full max-w-40">
      <Input
        id={id}
        inputMode={decimal ? "decimal" : "numeric"}
        autoComplete="off"
        className="pr-20 tabular-nums"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        {...aria}
      />
      <span
        aria-hidden="true"
        className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-muted-foreground"
      >
        {unit}
      </span>
    </div>
  );
}
