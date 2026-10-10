import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import "./layout.css";

// LensLayout owns the workspace geometry for a list + detail lens: header,
// controls, an optional notice, the body well, the footer, and the split with
// the inspector (resizable, persisted, drawer below the breakpoint). Lenses
// fill slots; they never set outer padding, heights or the inspector width.
// Spec: docs/superpowers/specs/2026-10-10-klyx-redesign-layout-nav.md §2.

export type DatasetState = "loading" | "unreadable" | "empty" | "no-match" | "ready";

// The table row height. LensLayout writes it into --table-row-height on its
// root, so the CSS rows and the virtualiser's windowing share ONE number:
// a mismatch would overlap rows, clip the last one and drift the cursor.
export const TABLE_ROW_HEIGHT = 32;

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
    // A width of 0 means "not laid out yet" (hidden, or a test DOM), not
    // "narrow": keep the default until a real measurement arrives.
    if (el.clientWidth > 0) setInnerWidth(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => { const w = entries[0].contentRect.width; if (w > 0) setInnerWidth(w); });
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

  // Drawer = modal dialog: the list behind is inert, focus moves in on open,
  // Tab cycles inside, Escape closes (nested menus/confirms stop it first),
  // and focus returns to the element that opened it.
  const mainRef = useRef<HTMLElement>(null);
  const asideRef = useRef<HTMLElement>(null);
  const returnFocus = useRef<HTMLElement | null>(null);
  const drawerOpen = !!inspector && geo.mode === "drawer";
  useEffect(() => {
    if (!drawerOpen) return;
    const main = mainRef.current;
    const aside = asideRef.current;
    returnFocus.current = document.activeElement as HTMLElement | null;
    main?.setAttribute("inert", "");
    (focusables(aside)[0] ?? aside)?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape" && !e.defaultPrevented) { onCloseInspector?.(); return; }
      if (e.key !== "Tab" || !aside) return;
      const f = focusables(aside);
      if (f.length === 0) { e.preventDefault(); return; }
      const first = f[0], last = f[f.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    };
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
      main?.removeAttribute("inert");
      const back = returnFocus.current;
      if (back && document.contains(back)) back.focus();
    };
  }, [drawerOpen, onCloseInspector]);

  const inspectorEl = inspector ? (
    <aside
      ref={asideRef}
      aria-label={inspectorLabel ?? `${label} details`}
      data-testid="lens-inspector"
      data-list-keys="ignore"
      role={geo.mode === "drawer" ? "dialog" : undefined}
      aria-modal={geo.mode === "drawer" ? true : undefined}
      tabIndex={-1}
      className={`lens-inspector${geo.mode === "drawer" ? " lens-drawer" : ""}`}
      style={{ width: geo.width }}
    >
      {inspector}
    </aside>
  ) : null;

  return (
    <div ref={rootRef} className="lens" data-mode={geo.mode} data-testid="lens-layout" style={{ "--table-row-height": `${TABLE_ROW_HEIGHT}px` } as React.CSSProperties}>
      <section ref={mainRef} aria-label={label} className="lens-main">
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

const FOCUSABLE = 'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

function focusables(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => !el.closest("[inert]"));
}

// LensTable: the one horizontal-scroll surface for a table body. Header and
// rows share it (and one column definition), so they can never scroll apart;
// minWidth is the sum of the column floors at the narrowest fold.
export function LensTable({ minWidth, children, onWidth }: { minWidth: number; children: ReactNode; onWidth?: (w: number) => void }) {
  const ref = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !onWidth) return;
    if (el.clientWidth > 0) onWidth(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => { const w = entries[0].contentRect.width; if (w > 0) onWidth(w); });
    ro.observe(el);
    return () => ro.disconnect();
  }, [onWidth]);
  return (
    <div ref={ref} className="lens-table" role="table">
      <div className="lens-table-inner" style={{ minWidth }}>{children}</div>
    </div>
  );
}
