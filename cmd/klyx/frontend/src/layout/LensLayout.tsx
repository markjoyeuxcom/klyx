import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import "./layout.css";

// LensLayout owns the workspace geometry for a list + detail lens: header,
// controls, an optional notice, the body well, the footer, and the split with
// the inspector (resizable, persisted, drawer below the breakpoint). Lenses
// fill slots; they never set outer padding, heights or the inspector width.
// Spec: docs/superpowers/specs/2026-10-10-klyx-redesign-layout-nav.md §2.

export type DatasetState = "loading" | "unreadable" | "empty" | "no-match" | "ready";

export const INSPECTOR_WIDTH_KEY = "klyx.layout.v1.inspectorWidth";
export const INSPECTOR_DEFAULT = 400;
export const INSPECTOR_MIN = 360;
export const INSPECTOR_MAX = 560;
const BODY_MIN = 600;
const DRAWER_BREAKPOINT = 1040; // workspace inner width
const SPLIT_GAP = 8;
const KEY_STEP = 16;

function readRequestedWidth(): number {
  try {
    const n = Number(localStorage.getItem(INSPECTOR_WIDTH_KEY));
    if (Number.isFinite(n) && n >= INSPECTOR_MIN && n <= INSPECTOR_MAX) return n;
  } catch { /* storage unavailable: default */ }
  return INSPECTOR_DEFAULT;
}

function writeRequestedWidth(n: number) {
  try { localStorage.setItem(INSPECTOR_WIDTH_KEY, String(n)); } catch { /* best effort */ }
}

/** splitGeometry decides split vs drawer and the effective inspector width. */
export function splitGeometry(innerWidth: number, requested: number): { mode: "split" | "drawer"; width: number } {
  if (innerWidth < DRAWER_BREAKPOINT) {
    return { mode: "drawer", width: Math.min(INSPECTOR_MAX, Math.max(INSPECTOR_MIN, requested), innerWidth) };
  }
  const max = Math.min(INSPECTOR_MAX, innerWidth - BODY_MIN - SPLIT_GAP);
  if (max < INSPECTOR_MIN) return { mode: "drawer", width: Math.min(requested, innerWidth) };
  return { mode: "split", width: Math.max(INSPECTOR_MIN, Math.min(requested, max)) };
}

export function LensLayout({
  label,
  title,
  summary,
  headerEnd,
  controls,
  notice,
  state,
  stateView,
  body,
  footer,
  footerEnd,
  inspector,
  inspectorLabel,
  onCloseInspector,
}: {
  label: string;               // landmark name, e.g. "Pods"
  title: ReactNode;
  summary?: ReactNode;         // total + attention, entity-scoped
  headerEnd?: ReactNode;       // freshness, refresh
  controls?: ReactNode;        // scope, status filters, search
  notice?: ReactNode;          // real degraded-state information only
  state: DatasetState;
  stateView?: ReactNode;       // rendered in the body for every non-ready state
  body: ReactNode;
  footer?: ReactNode;
  footerEnd?: ReactNode;
  inspector?: ReactNode;       // present = an object is selected
  inspectorLabel?: string;
  onCloseInspector?: () => void;
}) {
  const rootRef = useRef<HTMLDivElement>(null);
  const [innerWidth, setInnerWidth] = useState(1280);
  const [requested, setRequested] = useState(readRequestedWidth);

  useLayoutEffect(() => {
    const el = rootRef.current;
    if (!el) return;
    setInnerWidth(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => setInnerWidth(entries[0].contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const geo = splitGeometry(innerWidth, requested);
  const commit = useCallback((n: number) => {
    const v = Math.max(INSPECTOR_MIN, Math.min(INSPECTOR_MAX, Math.round(n)));
    setRequested(v);
    writeRequestedWidth(v);
  }, []);

  // pointer resize on the separator (it sits LEFT of the inspector)
  const drag = useRef<{ x: number; w: number } | null>(null);
  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    drag.current = { x: e.clientX, w: geo.width };
    e.currentTarget.setPointerCapture(e.pointerId);
    e.preventDefault();
  };
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    setRequested(Math.max(INSPECTOR_MIN, Math.min(INSPECTOR_MAX, drag.current.w + (drag.current.x - e.clientX))));
  };
  const onPointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    const w = drag.current.w + (drag.current.x - e.clientX);
    drag.current = null;
    commit(w);
  };
  const onSeparatorKey = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "ArrowLeft") { e.preventDefault(); commit(geo.width + KEY_STEP); }
    else if (e.key === "ArrowRight") { e.preventDefault(); commit(geo.width - KEY_STEP); }
    else if (e.key === "Home") { e.preventDefault(); commit(INSPECTOR_MIN); }
    else if (e.key === "End") { e.preventDefault(); commit(INSPECTOR_MAX); }
  };

  // Drawer: Escape closes it (nested surfaces stop propagation first).
  useEffect(() => {
    if (!inspector || geo.mode !== "drawer" || !onCloseInspector) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onCloseInspector(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [inspector, geo.mode, onCloseInspector]);

  const inspectorEl = inspector ? (
    <aside
      aria-label={inspectorLabel ?? `${label} details`}
      data-testid="lens-inspector"
      className={`lens-inspector${geo.mode === "drawer" ? " lens-drawer" : ""}`}
      style={{ width: geo.width }}
    >
      {inspector}
    </aside>
  ) : null;

  return (
    <div ref={rootRef} className="lens" data-mode={geo.mode} data-testid="lens-layout">
      <section aria-label={label} className="lens-main">
        <div className="lens-header">
          <h1 className="t-workspace-title">{title}</h1>
          {summary && <div className="lens-header-summary">{summary}</div>}
          {headerEnd && <div className="lens-header-end">{headerEnd}</div>}
        </div>
        {controls && <div className="lens-controls">{controls}</div>}
        {notice && <div className="lens-notice">{notice}</div>}
        <div className="lens-body">
          {state === "ready" ? body : <div className="lens-body-state" data-state={state}>{stateView}</div>}
          {(footer || footerEnd) && state === "ready" && (
            <div className="lens-footer">
              {footer}
              {footerEnd && <div className="lens-footer-end">{footerEnd}</div>}
            </div>
          )}
        </div>
      </section>
      {inspectorEl && geo.mode === "split" && (
        <>
          <div
            role="separator"
            aria-orientation="vertical"
            aria-label="Resize details panel"
            aria-valuenow={geo.width}
            aria-valuemin={INSPECTOR_MIN}
            aria-valuemax={INSPECTOR_MAX}
            tabIndex={0}
            className="lens-separator"
            onPointerDown={onPointerDown}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onKeyDown={onSeparatorKey}
            onDoubleClick={() => commit(INSPECTOR_DEFAULT)}
          >
            <span aria-hidden="true" />
          </div>
          {inspectorEl}
        </>
      )}
      {inspectorEl && geo.mode === "drawer" && (
        <>
          <div className="lens-scrim" aria-hidden="true" onClick={onCloseInspector} />
          {inspectorEl}
        </>
      )}
    </div>
  );
}
