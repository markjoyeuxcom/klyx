import { Fragment, useRef, useState, useEffect, useCallback, forwardRef, useImperativeHandle } from "react";

/**
 * VirtualList — lightweight windowed list renderer.
 *
 * Threshold: when items.length < PLAIN_THRESHOLD (100), renders all items
 * directly without virtualization — avoids overhead for short lists and keeps
 * snapshot tests trivially correct.
 *
 * Fixed-height rows constraint: rows must be fixed height (single-line grid
 * rows). Variable-height content (e.g. EventsView expanded message rows)
 * breaks the math. Callers are expected to detect that case and bail to plain
 * rendering — see EventsView for the documented pattern.
 *
 * scrollToIndex: exposed via forwardRef/useImperativeHandle. Plain path (<100
 * items) scrolls the matching row into view via a data attribute lookup on the
 * container element. Virtual path sets scrollTop = idx * rowHeight directly on
 * the container.
 */

const PLAIN_THRESHOLD = 100;
// Height assumed until the container has been measured (and permanently in
// environments without layout, e.g. jsdom, where clientHeight is always 0).
const FALLBACK_HEIGHT = 600;

export type VirtualListHandle = {
  scrollToIndex: (idx: number) => void;
};

interface VirtualListProps<T> {
  items: T[];
  rowHeight: number;
  overscan?: number;
  render: (item: T, index: number) => React.ReactNode;
  /** Optional style applied to the outer container div (height/flex-1 etc.) */
  style?: React.CSSProperties;
  /**
   * Force the plain (non-windowed) path regardless of item count. Callers use
   * this when row heights become variable (e.g. an expanded row), which breaks
   * fixed-row windowing math.
   */
  forcePlain?: boolean;
  /**
   * Stable per-item key. Without it rows are keyed by index, so row-local
   * state (hover, inputs, expansion) misattributes when live updates reorder
   * the list. Callers with reorderable data should always provide it.
   */
  rowKey?: (item: T, index: number) => string;
}

// forwardRef with generics requires the inner function to be typed carefully.
// We export a wrapper that re-asserts the generic so call sites remain typed.
function VirtualListInner<T>(
  { items, rowHeight, overscan = 8, render, style, forcePlain = false, rowKey }: VirtualListProps<T>,
  ref: React.Ref<VirtualListHandle>,
) {
  const [scrollTop, setScrollTop] = useState(0);
  const rafRef = useRef<number | null>(null);
  const containerRef = useRef<HTMLDivElement>(null);

  const plain = forcePlain || items.length < PLAIN_THRESHOLD;

  // Throttle scroll handling to one state update per frame. The frame callback
  // reads the element's CURRENT scrollTop rather than a value captured when
  // the frame was queued — during fast scrolls several scroll events land per
  // frame and the queue-time value is one event stale.
  const onScroll = useCallback(() => {
    if (rafRef.current !== null) return; // frame queued — it reads the latest value
    rafRef.current = requestAnimationFrame(() => {
      rafRef.current = null;
      const el = containerRef.current;
      if (el) setScrollTop(el.scrollTop);
    });
  }, []);

  // Cancel any queued frame on unmount.
  useEffect(() => () => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
  }, []);

  // Track the container's height so enlarging the window extends the rendered
  // window immediately — a DOM read during render never re-fires on resize,
  // which left blank rows until the next scroll. jsdom does not implement
  // ResizeObserver (typeof guard), so tests keep the fallback height.
  const [containerHeight, setContainerHeight] = useState(FALLBACK_HEIGHT);
  useEffect(() => {
    if (plain) return;
    const el = containerRef.current;
    if (!el) return;
    const measure = () => setContainerHeight(el.clientHeight || FALLBACK_HEIGHT);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [plain]);

  useImperativeHandle(ref, () => ({
    scrollToIndex(idx: number) {
      const el = containerRef.current;
      if (!el) return;
      if (forcePlain || items.length < PLAIN_THRESHOLD) {
        // Plain path: rows are direct children; find by data-vl-idx attribute.
        const row = el.querySelector(`[data-vl-idx="${idx}"]`);
        if (row && typeof (row as HTMLElement).scrollIntoView === "function") {
          (row as HTMLElement).scrollIntoView({ block: "nearest" });
        }
      } else {
        // Virtual path: compute offset and set scrollTop imperatively.
        el.scrollTop = idx * rowHeight;
      }
    },
  }), [items.length, rowHeight, forcePlain]);

  // Plain render for short lists or variable-height rows. The container still
  // owns its scrolling (overflowY) — without it, rows overflow the flex box
  // visibly and spill into the nearest ancestor scroll container, which then
  // scrolls the WHOLE view (list + any side panel) together.
  if (plain) {
    return (
      <div ref={containerRef} style={{ overflowY: "auto", ...style }}>
        {items.map((item, i) => (
          <div key={rowKey ? rowKey(item, i) : i} data-vl-idx={i} style={{ display: "contents" }}>
            {render(item, i)}
          </div>
        ))}
      </div>
    );
  }

  const totalHeight = items.length * rowHeight;

  const firstVisible = Math.max(0, Math.floor(scrollTop / rowHeight) - overscan);
  const lastVisible = Math.min(
    items.length - 1,
    Math.ceil((scrollTop + containerHeight) / rowHeight) + overscan,
  );

  const visibleItems = items.slice(firstVisible, lastVisible + 1);
  const offsetY = firstVisible * rowHeight;

  return (
    <div
      ref={containerRef}
      onScroll={onScroll}
      style={{ height: "100%", overflowY: "auto", position: "relative", ...style }}
    >
      {/* Spacer that forces the scrollbar to the correct total height */}
      <div style={{ height: totalHeight, position: "relative" }}>
        {/* Visible window, absolutely positioned to the current scroll position */}
        <div style={{ position: "absolute", top: offsetY, left: 0, right: 0 }}>
          {visibleItems.map((item, i) => {
            const idx = firstVisible + i;
            // With rowKey the wrapper carries the stable key; without it the
            // caller's own key on the rendered element applies (legacy path).
            return rowKey
              ? <Fragment key={rowKey(item, idx)}>{render(item, idx)}</Fragment>
              : render(item, idx);
          })}
        </div>
      </div>
    </div>
  );
}

// Cast to preserve generic typing across the forwardRef boundary.
export const VirtualList = forwardRef(VirtualListInner) as <T>(
  props: VirtualListProps<T> & { ref?: React.Ref<VirtualListHandle> },
) => React.ReactElement;
