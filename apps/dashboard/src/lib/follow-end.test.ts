import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { useFollowEnd } from "@/lib/follow-end";

/** A scroller of `client` px showing `height` px, scrolled to `top`; what it holds is returned. */
function pane(client: number, startHeight: number) {
  let height = startHeight;
  const outer = document.createElement("div");
  outer.setAttribute("data-scroller", "");
  const inner = document.createElement("div");
  outer.append(inner);
  document.body.append(outer);
  let top = 0;
  const sets: number[] = [];
  Object.defineProperty(outer, "clientHeight", { get: () => client });
  Object.defineProperty(outer, "scrollHeight", { get: () => height });
  Object.defineProperty(outer, "scrollTop", {
    get: () => top,
    set: (v: number) => {
      top = v;
      sets.push(v);
    },
  });
  return {
    outer,
    inner,
    sets,
    /** More arrives under what is shown. */
    grow(to: number) {
      height = to;
    },
    /** The reader scrolls to `to`. */
    scroll(to: number) {
      top = to;
      outer.dispatchEvent(new Event("scroll"));
    },
  };
}

afterEach(() => {
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

describe("useFollowEnd", () => {
  it("scrolls what holds the content to its end as the content changes", () => {
    const p = pane(500, 2000);
    const ref = { current: p.inner };
    const { rerender } = renderHook(({ key }) => useFollowEnd(ref, key, "pane"), {
      initialProps: { key: 1 },
    });
    p.sets.length = 0;
    rerender({ key: 2 });
    expect(p.sets).toEqual([2000]);
  });

  it("leaves a reader who scrolled up where they are, and says there is more", () => {
    const p = pane(500, 2000);
    const ref = { current: p.inner };
    const { result, rerender } = renderHook(({ key }) => useFollowEnd(ref, key, "pane"), {
      initialProps: { key: 1 },
    });
    act(() => p.scroll(1500));
    act(() => p.scroll(200));
    p.sets.length = 0;
    rerender({ key: 2 });
    expect(p.sets).toEqual([]);
    expect(result.current.behind).toBe(true);

    act(() => result.current.jump());
    expect(p.sets).toEqual([2000]);
    expect(result.current.behind).toBe(false);
  });

  // The page's own scroll to the end is heard after more arrived under it:
  // far from the new end, but the reader did not move up.
  it("stays at the end when more arrives before its own scroll is heard", () => {
    const p = pane(500, 2000);
    const ref = { current: p.inner };
    const { result, rerender } = renderHook(({ key }) => useFollowEnd(ref, key, "pane"), {
      initialProps: { key: 1 },
    });
    expect(p.outer.scrollTop).toBe(2000);
    p.grow(2600);
    act(() => p.scroll(2000));
    p.sets.length = 0;
    rerender({ key: 2 });
    expect(p.sets).toEqual([2600]);
    expect(result.current.behind).toBe(false);
  });

  it("listens to the new scroller when the layout changes", () => {
    const a = pane(500, 2000);
    const b = pane(400, 3000);
    const ref = { current: a.inner };
    const { result, rerender } = renderHook(({ key, layout }) => useFollowEnd(ref, key, layout), {
      initialProps: { key: 1, layout: "a" },
    });
    ref.current = b.inner;
    rerender({ key: 1, layout: "b" });
    // Scrolling the old one up says nothing now; the new one is followed.
    act(() => a.scroll(0));
    b.sets.length = 0;
    rerender({ key: 2, layout: "b" });
    expect(b.sets).toEqual([3000]);
    expect(result.current.behind).toBe(false);
  });
});
