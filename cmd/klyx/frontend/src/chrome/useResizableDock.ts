import { useState, useCallback, useEffect, useRef } from "react";
import { layoutViewportHeight, unzoom, ZOOM_EVENT } from "../app/zoom";

const STORAGE_KEY = "klyx-logs-dock-height";
const MIN_HEIGHT = 160;
const DEFAULT_HEIGHT = 320;
const MAX_SHARE = 0.6;      // the dock never takes more than 60% of the workspace
const LIST_RESERVE = 200;   // ...and leaves the list above it this much (header, filters, rows)
const FLOOR = 120;          // a squeezed dock still shows its header and a few lines
const STORED_MAX = 4000;    // sanity bound on the persisted preference

// The space the dock shares with its list, in layout px: the workspace when it
// is mounted, else the window (layout px, so a zoomed window is smaller). Both
// shrink as app zoom grows, which is what keeps list and dock reachable at 200%.
function available(): number {
  const ws = typeof document === "undefined" ? null : document.querySelector<HTMLElement>(".shell-workspace");
  return ws && ws.clientHeight > 0 ? ws.clientHeight : layoutViewportHeight();
}

export function dockMaxHeight(space = available()): number {
  return Math.max(FLOOR, Math.floor(Math.min(space * MAX_SHARE, space - LIST_RESERVE)));
}

// clampDock: the shown height for a preferred one. Below the minimum only when
// the space itself is too small for the minimum.
export function clampDock(value: number, max: number): number {
  return Math.max(Math.min(MIN_HEIGHT, max), Math.min(max, value));
}

// The persisted preference, kept unclamped by the current window so a zoom or
// resize round trip gives the operator's dock size back.
function readStored(): number {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return DEFAULT_HEIGHT;
    const n = parseInt(raw, 10);
    return Number.isNaN(n) ? DEFAULT_HEIGHT : Math.max(MIN_HEIGHT, Math.min(STORED_MAX, n));
  } catch {
    return DEFAULT_HEIGHT;
  }
}

export interface ResizableDockHandle {
  height: number;
  /** Spread {...handleProps} onto the drag handle div at the top edge of the dock. */
  handleProps: {
    onPointerDown: (e: React.PointerEvent<HTMLDivElement>) => void;
    onPointerMove: (e: React.PointerEvent<HTMLDivElement>) => void;
    onPointerUp: (e: React.PointerEvent<HTMLDivElement>) => void;
    style: React.CSSProperties;
  };
}

/**
 * useResizableDock — returns a dock height and drag-handle props.
 *
 * The handle is a 4 px horizontal strip placed on the TOP edge of the dock.
 * Drag up to enlarge, drag down to shrink. The shown height is clamped to the
 * space it shares with the list (dockMaxHeight, layout px) and re-clamped when
 * the window resizes or app zoom changes; the dragged size is persisted to
 * localStorage["klyx-logs-dock-height"].
 */
export function useResizableDock(): ResizableDockHandle {
  const [preferred, setPreferred] = useState<number>(readStored);
  const [max, setMax] = useState<number>(() => dockMaxHeight());
  const height = clampDock(preferred, max);
  const dragging = useRef(false);
  const startY = useRef(0);
  const startHeight = useRef(0);

  useEffect(() => {
    const remeasure = () => setMax(dockMaxHeight());
    remeasure();
    window.addEventListener("resize", remeasure);
    window.addEventListener(ZOOM_EVENT, remeasure);
    return () => {
      window.removeEventListener("resize", remeasure);
      window.removeEventListener(ZOOM_EVENT, remeasure);
    };
  }, []);

  const onPointerDown = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    dragging.current = true;
    startY.current = e.clientY;
    startHeight.current = height;
    (e.currentTarget as HTMLDivElement).setPointerCapture(e.pointerId);
    e.preventDefault();
  }, [height]);

  const onPointerMove = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    // Dragging up (negative delta) enlarges the dock; handle is on the TOP edge.
    const delta = unzoom(startY.current - e.clientY);
    setPreferred(clampDock(startHeight.current + delta, max));
  }, [max]);

  const onPointerUp = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    dragging.current = false;
    const delta = unzoom(startY.current - e.clientY);
    const final = clampDock(startHeight.current + delta, max);
    setPreferred(final);
    try {
      localStorage.setItem(STORAGE_KEY, String(final));
    } catch {
      // localStorage not available in test / sandboxed environments — ignore.
    }
  }, [max]);

  return {
    height,
    handleProps: {
      onPointerDown,
      onPointerMove,
      onPointerUp,
      style: {
        position: "absolute",
        top: 0,
        left: 0,
        right: 0,
        height: 4,
        cursor: "row-resize",
        zIndex: 10,
        background: "transparent",
      } satisfies React.CSSProperties,
    },
  };
}
