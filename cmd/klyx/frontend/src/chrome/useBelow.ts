import { useLayoutEffect, useState, type RefObject } from "react";

// belowOf names every threshold a width is below ("1680 1600"), for CSS to
// match with [data-below~="1600"]; an element not laid out yet (width 0) is
// below none.
export function belowOf(width: number, thresholds: readonly number[]): string {
  return width > 0 ? thresholds.filter((t) => width < t).join(" ") : "";
}

// useBelow measures an element's own width, so its breakpoints follow the
// app zoom: CSS zoom on the root shrinks what an element measures, while
// media and container queries still see the whole window (slice 13a review
// 1; app/zoom.ts).
export function useBelow(ref: RefObject<HTMLElement | null>, thresholds: readonly number[]): string {
  const [below, setBelow] = useState("");
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = (w: number) => setBelow(belowOf(w, thresholds));
    measure(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => measure(entries[0].contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
    // thresholds are a module constant at every call site
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ref]);
  return below;
}
