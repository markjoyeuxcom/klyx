// App zoom (accessibility; redesign spec §6 "usable zoom"). The webview has
// no reflowing zoom of its own - Wails' ZoomIn uses WKWebView magnification,
// which enlarges pixels without reflow - so Klyx scales its own layout with
// CSS `zoom` on the document root: text and controls grow, and every width
// the layout measures (the band, splits, drawers, the fleet grid) shrinks
// accordingly, so narrow-mode rules engage instead of content clipping.

export const ZOOM_STEPS = [0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2] as const;
const KEY = "klyx.layout.v1.zoom";
export const ZOOM_EVENT = "klyx:zoom";

let current = 1;

// Viewport units are NOT compensated by `zoom` in WebKit: at 200% a 100vh box
// renders twice the window's height (probed in WKWebView 2026-10-11). The
// root therefore also carries --app-zoom, and every viewport-relative length
// goes through vh()/vw() (TSX) or calc(Nvh / var(--app-zoom, 1)) (CSS).
function apply(z: number) {
  current = z;
  if (typeof document !== "undefined") {
    const st = document.documentElement.style as CSSStyleDeclaration & { zoom: string };
    st.zoom = z === 1 ? "" : String(z);
    if (z === 1) st.removeProperty("--app-zoom"); else st.setProperty("--app-zoom", String(z));
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

// initZoom applies the persisted step before first paint, for every root (the
// main window and the log pop-out alike), and follows a change made in another
// Klyx window: zoom is one app-wide reading setting.
let following = false;
export function initZoom(): void {
  apply(loadZoom());
  if (following || typeof window === "undefined") return;
  following = true;
  window.addEventListener("storage", (e) => {
    if (e.key !== KEY) return;
    apply(loadZoom());
    window.dispatchEvent(new Event(ZOOM_EVENT));
  });
}

export function setZoom(z: number): void {
  const step = (ZOOM_STEPS as readonly number[]).includes(z) ? z : 1;
  apply(step);
  persist(step);
  window.dispatchEvent(new Event(ZOOM_EVENT));
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

// Viewport-relative lengths in the zoomed coordinate space (see apply()).
export const vh = (n: number) => `calc(${n}vh / var(--app-zoom, 1))`;
export const vw = (n: number) => `calc(${n}vw / var(--app-zoom, 1))`;

// The window's height in layout px - what a layout length must fit inside.
export const layoutViewportHeight = () => (typeof window === "undefined" ? 0 : window.innerHeight / current);

// handleZoomKey: mod+= / mod++ / mod+- / mod+0, for every root. A reading aid
// works anywhere - in a text field too - unless a handler already took the key.
// Returns whether the key was a zoom key.
export function handleZoomKey(e: KeyboardEvent): boolean {
  if (!(e.metaKey || e.ctrlKey) || e.altKey) return false;
  if (e.key !== "=" && e.key !== "+" && e.key !== "-" && e.key !== "0") return false;
  if (e.defaultPrevented) return true;
  e.preventDefault();
  if (e.key === "-") zoomOut(); else if (e.key === "0") zoomReset(); else zoomIn();
  return true;
}
