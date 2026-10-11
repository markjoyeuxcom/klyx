// App zoom (accessibility; redesign spec §6 "usable zoom"). The webview has
// no reflowing zoom of its own - Wails' ZoomIn uses WKWebView magnification,
// which enlarges pixels without reflow - so Klyx scales its own layout with
// CSS `zoom` on the document root: text and controls grow, and every width
// the layout measures (the band, splits, drawers, the fleet grid) shrinks
// accordingly, so narrow-mode rules engage instead of content clipping.

export const ZOOM_STEPS = [0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2] as const;
const KEY = "klyx.layout.v1.zoom";

let current = 1;

function apply(z: number) {
  current = z;
  if (typeof document !== "undefined") {
    (document.documentElement.style as CSSStyleDeclaration & { zoom: string }).zoom = z === 1 ? "" : String(z);
  }
}

function persist(z: number) {
  try { localStorage.setItem(KEY, String(z)); } catch { /* best-effort */ }
}

// The persisted step, validated; anything else is 100%.
export function loadZoom(): number {
  try {
    const v = Number(localStorage.getItem(KEY));
    return (ZOOM_STEPS as readonly number[]).includes(v) ? v : 1;
  } catch {
    return 1;
  }
}

export const getZoom = () => current;

export function initZoom(): void {
  apply(loadZoom());
}

export function setZoom(z: number): void {
  const step = (ZOOM_STEPS as readonly number[]).includes(z) ? z : 1;
  apply(step);
  persist(step);
  window.dispatchEvent(new Event("klyx:zoom"));
}

export function zoomIn(): void {
  const i = ZOOM_STEPS.findIndex((s) => s > current);
  if (i >= 0) setZoom(ZOOM_STEPS[i]);
}

export function zoomOut(): void {
  const lower = ZOOM_STEPS.filter((s) => s < current);
  if (lower.length > 0) setZoom(lower[lower.length - 1]);
}

export function zoomReset(): void {
  setZoom(1);
}

// Pointer movement arrives in screen-scaled CSS px while layout widths are in
// the zoomed coordinate space: drag deltas are divided by the zoom so a
// resize handle tracks the pointer at every zoom.
export const unzoom = (px: number) => px / current;

export const zoomPercent = () => `${Math.round(current * 100)}%`;
