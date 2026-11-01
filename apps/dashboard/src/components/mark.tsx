import { useId } from "react";

import { cn } from "@/lib/utils";

// The openarity mark, drawn inline so it takes the page's accent: the theme is
// a class on <html>, which an <img> of assets/logo.svg cannot see. The numbers
// are assets/mark/geometry.py's, as build.py writes them into logo.svg, and
// mark.test.tsx fails if the two differ. The cut, the visor and the nodes are
// holes in a mask, never shapes in a colour, so the page shows through them.
export function Mark({ label, className }: { label?: string; className?: string }) {
  // useId's punctuation is not safe inside url(#…), and two marks on a page
  // sharing one id would both draw through whichever mask came first.
  const mask = `oa-mark-${useId().replace(/[^a-zA-Z0-9]/g, "")}`;

  return (
    <svg
      viewBox="0 0 64 64"
      className={cn("text-primary", className)}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      {label ? <title>{label}</title> : null}
      <mask id={mask} maskUnits="userSpaceOnUse" x="0" y="0" width="64" height="64">
        <rect x="8" y="8" width="48" height="48" rx="14.9" fill="#fff" />
        <rect x="19" y="23.5" width="26" height="17" rx="3" fill="#000" />
        <path d="M0 0L64 64" stroke="#000" strokeWidth="3" />
        <circle cx="17.7" cy="17.7" r="3.1" fill="#000" />
        <circle cx="46.3" cy="46.3" r="3.1" fill="#000" />
      </mask>
      <rect width="64" height="64" fill="currentColor" mask={`url(#${mask})`} />
    </svg>
  );
}
