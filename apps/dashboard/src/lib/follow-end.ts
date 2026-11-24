import { type RefObject, useEffect, useLayoutEffect, useRef, useState } from "react";

/** How close to the end of what scrolls still counts as reading the end. */
const AT_END_PX = 96;

/**
 * What scrolls the conversation: the nearest element around it marked as
 * scrolling (its own pane, or the sheet on a tablet), else the window, as on a
 * phone.
 */
function scrollerOf(el: HTMLElement | null): HTMLElement | null {
  return el?.closest<HTMLElement>("[data-scroller]") ?? null;
}

const endOf = (el: HTMLElement | null) =>
  el ? el.scrollHeight : document.documentElement.scrollHeight;

function atEndOf(el: HTMLElement | null): boolean {
  return el
    ? el.clientHeight + el.scrollTop >= el.scrollHeight - AT_END_PX
    : window.innerHeight + window.scrollY >= endOf(null) - AT_END_PX;
}

const topOf = (el: HTMLElement | null) => (el ? el.scrollTop : window.scrollY);

function toEnd(el: HTMLElement | null) {
  if (el) el.scrollTop = el.scrollHeight;
  else window.scrollTo({ top: endOf(null) });
}

/**
 * Keeps the end of the conversation in view while the reader is at it: what
 * arrives scrolls into sight, at once, since it arrives many times a minute.
 * A reader who scrolled up to read is left where they are, and told there is
 * more below. Only a move up leaves the end: by the time the page's own
 * scroll to the end is heard, more may have arrived under it. Only what holds the conversation is scrolled, so the run beside
 * it stays where it is.
 *
 * `contentKey` changes with what is shown, and each change follows. `layoutKey`
 * changes with the layout (its own pane, the sheet, the window), and the hook
 * listens to the new scroller.
 */
export function useFollowEnd(
  ref: RefObject<HTMLElement | null>,
  contentKey: unknown,
  layoutKey: unknown,
) {
  const atEnd = useRef(true);
  const lastTop = useRef(0);
  const [behind, setBehind] = useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new layout has a new scroller
  useEffect(() => {
    const el = scrollerOf(ref.current);
    const target: HTMLElement | Window = el ?? window;
    lastTop.current = topOf(el);
    const read = () => {
      const top = topOf(el);
      if (top < lastTop.current - 1) atEnd.current = atEndOf(el);
      else if (atEndOf(el)) atEnd.current = true;
      lastTop.current = top;
      if (atEnd.current) setBehind(false);
    };
    target.addEventListener("scroll", read, { passive: true });
    // What is shown can grow after it arrived (an answer laid out, the
    // buttons under it read), and the pane can shrink (the composer comes in
    // under it): either way a reader at the end stays there.
    const grew =
      typeof ResizeObserver === "function" && ref.current
        ? new ResizeObserver(() => {
            if (atEnd.current) toEnd(el);
          })
        : null;
    if (grew && ref.current) grew.observe(ref.current);
    if (grew && el) grew.observe(el);
    return () => {
      target.removeEventListener("scroll", read);
      grew?.disconnect();
    };
  }, [ref, layoutKey]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs for each change to what is shown
  useLayoutEffect(() => {
    if (atEnd.current) toEnd(scrollerOf(ref.current));
    else setBehind(true);
  }, [contentKey]);
  return {
    behind,
    jump: () => {
      toEnd(scrollerOf(ref.current));
      setBehind(false);
    },
  };
}
