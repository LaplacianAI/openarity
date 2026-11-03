import { Children, cloneElement, isValidElement, type ReactElement, type ReactNode } from "react";

/**
 * A label, its control, what the control is for and what is wrong with it.
 *
 * The control is the one child; the field joins the description and the
 * error to it with `aria-describedby` and marks it invalid, so a screen
 * reader hears the same thing a sighted reader sees. A limit sits beside the
 * label, where it is read before typing, not hidden in a tooltip.
 */
export function Field({
  label,
  htmlFor,
  hint,
  error,
  limit,
  children,
}: {
  label: string;
  /** The control's id. */
  htmlFor: string;
  /** What the field is for, or how to fill it. */
  hint?: ReactNode;
  /** What is wrong with what was entered, in a sentence that says how to fix it. */
  error?: string | null;
  /** A limit, such as "12 of 64": shown beside the label. */
  limit?: ReactNode;
  children: ReactNode;
}) {
  const hintId = `${htmlFor}-hint`;
  const errorId = `${htmlFor}-error`;
  const describedBy = [hint ? hintId : null, error ? errorId : null].filter(Boolean).join(" ");
  return (
    <div className="space-y-1.5">
      <div className="flex items-baseline justify-between gap-3">
        <label htmlFor={htmlFor} className="text-sm font-medium">
          {label}
        </label>
        {limit ? <span className="text-xs text-muted-foreground tabular-nums">{limit}</span> : null}
      </div>
      {wire(children, describedBy || undefined, Boolean(error))}
      {hint ? (
        <p id={hintId} className="text-xs text-muted-foreground">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/**
 * Joins the description and error to the field's one control. Anything that
 * is not an element (a comment, a stray space) is left where it is.
 */
function wire(children: ReactNode, describedBy: string | undefined, invalid: boolean): ReactNode {
  const elements = Children.toArray(children).filter(isValidElement);
  if (elements.length !== 1) return children;
  const control = elements[0] as ReactElement<Record<string, unknown>>;
  return cloneElement(control, {
    "aria-describedby":
      [control.props["aria-describedby"], describedBy].filter(Boolean).join(" ") || undefined,
    "aria-invalid": invalid ? true : control.props["aria-invalid"],
  });
}
