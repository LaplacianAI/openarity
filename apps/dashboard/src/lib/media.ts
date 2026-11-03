import { useSyncExternalStore } from "react";

/**
 * Whether a media query matches, kept current as the window changes. Where
 * there is no matchMedia (jsdom, for one) nothing matches, so a layout that
 * asks for a wide screen gets the narrow one.
 */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (changed) => {
      if (typeof window.matchMedia !== "function") return () => {};
      const list = window.matchMedia(query);
      list.addEventListener("change", changed);
      return () => list.removeEventListener("change", changed);
    },
    () => typeof window.matchMedia === "function" && window.matchMedia(query).matches,
    () => false,
  );
}
