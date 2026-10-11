import { useState, useCallback, useRef } from "react";
import { unzoom } from "../app/zoom";

const STORAGE_KEY = "klyx-logs-dock-height";
const MIN_HEIGHT = 160;
const DEFAULT_HEIGHT = 320;

function getMaxHeight(): number {
  return Math.floor(window.innerHeight * 0.6);
}

function clamp(value: number): number {
  return Math.max(MIN_HEIGHT, Math.min(getMaxHeight(), value));
}

function readStored(): number {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return DEFAULT_HEIGHT;
    const n = parseInt(raw, 10);
    return Number.isNaN(n) ? DEFAULT_HEIGHT : clamp(n);
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
 * Drag up to enlarge, drag down to shrink. Height is clamped to
 * [160, Math.floor(window.innerHeight * 0.6)] and persisted to
 * localStorage["klyx-logs-dock-height"].
 */
export function useResizableDock(): ResizableDockHandle {
  const [height, setHeight] = useState<number>(readStored);
  const dragging = useRef(false);
  const startY = useRef(0);
  const startHeight = useRef(0);

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
    const next = clamp(startHeight.current + delta);
    setHeight(next);
  }, []);

  const onPointerUp = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    dragging.current = false;
    const delta = unzoom(startY.current - e.clientY);
    const final = clamp(startHeight.current + delta);
    setHeight(final);
    try {
      localStorage.setItem(STORAGE_KEY, String(final));
    } catch {
      // localStorage not available in test / sandboxed environments — ignore.
    }
  }, []);

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
