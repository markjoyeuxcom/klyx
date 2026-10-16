import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { unzoom } from "../app/zoom";
import { inEditable } from "../chrome/inEditable";
import { InspectorExpand } from "./inspectorExpand";
import "./layout.css";

// LensLayout owns the workspace geometry for a list + detail lens: header,
// controls, an optional notice, the body well, the footer, and the split with
// the inspector (resizable, persisted, drawer below the breakpoint). Lenses
// fill slots; they never set outer padding, heights or the inspector width.
// Spec: docs/superpowers/specs/2026-10-10-klyx-redesign-layout-nav.md §2,
// revised by The Line slice 16: the split has no fixed ceiling (the
// inspector may take everything but the body's minimum), and the inspector
// can expand over the whole workspace.

export type DatasetState = "loading" | "unreadable" | "empty" | "no-match" | "ready";

// The table row height. LensLayout writes it into --table-row-height on its
// root, so the CSS rows and the virtualiser's windowing share ONE number:
// a mismatch would overlap rows, clip the last one and drift the cursor.
export const TABLE_ROW_HEIGHT = 40; // parent §2.3: 40px rows

export const INSPECTOR_WIDTH_KEY = "klyx.layout.v1.inspectorWidth";
export const INSPECTOR_DEFAULT = 400;
export const INSPECTOR_MIN = 360;
// The drawer overlays a narrow workspace and keeps the redesign's ceiling.
export const DRAWER_MAX = 560;
// A stored request above this is not a width anyone dragged to.
const REQUEST_CEILING = 4096;
const BODY_MIN = 600;        // table-detail
const CANVAS_BODY_MIN = 640; // canvas-detail: a graph needs more room before it yields to the inspector
const DRAWER_BREAKPOINT = 1040; // workspace inner width
const SPLIT_GAP = 8;
const KEY_STEP = 16;

function readRequestedWidth(): number {
  try {
    const n = Number(localStorage.getItem(INSPECTOR_WIDTH_KEY));
    if (Number.isFinite(n) && n >= INSPECTOR_MIN && n <= REQUEST_CEILING) return n;
  } catch { /* storage unavailable: default */ }
  return INSPECTOR_DEFAULT;
}

function writeRequestedWidth(n: number) {
  try { localStorage.setItem(INSPECTOR_WIDTH_KEY, String(n)); } catch { /* best effort */ }
}

/**
 * splitGeometry decides split vs drawer, the effective inspector width and
 * the widest it may be dragged to (max). In the split the body keeps its
 * minimum and the inspector may take the rest (slice 16); the drawer keeps
 * DRAWER_MAX. The requested width is stored apart from the effective one,
 * so a narrower window never destroys the preference.
 */
export function splitGeometry(innerWidth: number, requested: number, bodyMin = BODY_MIN): { mode: "split" | "drawer"; width: number; max: number } {
  const drawer = () => ({ mode: "drawer" as const, width: Math.min(DRAWER_MAX, Math.max(INSPECTOR_MIN, requested), innerWidth), max: Math.min(DRAWER_MAX, innerWidth) });
  if (innerWidth < DRAWER_BREAKPOINT) return drawer();
  const max = innerWidth - bodyMin - SPLIT_GAP;
  if (max < INSPECTOR_MIN) return drawer();
  return { mode: "split", width: Math.max(INSPECTOR_MIN, Math.min(requested, max)), max };
}

// A surface above the inspector (a menu, a confirm, the console, Describe,
// an expanded logs pane - anything marked data-escape-layer) takes Escape
// before the expanded inspector does.
const ABOVE = '[role="dialog"], [role="alertdialog"], [role="menu"], [data-testid="command-palette"], [data-escape-layer]';
function surfaceAbove(aside: HTMLElement | null): boolean {
  return Array.from(document.querySelectorAll<HTMLElement>(ABOVE)).some((el) => el !== aside && !el.contains(aside ?? null));
}

// The expanded inspector owns Escape only when focus is in it, or nowhere:
// focus in the chat, the mast or the nav leaves Escape to them.
function ownsKey(aside: HTMLElement | null, t: EventTarget | null): boolean {
  const el = t as Node | null;
  if (!el || el === document || el === document.body || el === document.documentElement) return true;
  return !!aside?.contains(el);
}

export function LensLayout({
  variant = "table",
  part,
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
  variant?: "table" | "canvas"; // table-detail (default) or canvas-detail
  part?: string;               // the task this page shows, when its lens has parts (data-part: the smoke's proof)
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

  const geo = splitGeometry(innerWidth, requested, variant === "canvas" ? CANVAS_BODY_MIN : BODY_MIN);
  const clamp = (n: number) => Math.max(INSPECTOR_MIN, Math.min(geo.max, Math.round(n)));
  const commit = (n: number) => {
    const v = clamp(n);
    setRequested(v);
    writeRequestedWidth(v);
  };

  // Expanded: the inspector takes the whole workspace and the list behind it
  // is hidden, not unmounted. It resets when the inspector closes, so the
  // next object opens beside the list; Escape restores it first.
  const open = !!inspector;
  const [expanded, setExpanded] = useState(false);
  useEffect(() => { if (!open) setExpanded(false); }, [open]);
  const wide = open && expanded;
  const expandValue = useMemo(() => ({ expanded: wide, toggle: () => setExpanded((v) => !v) }), [wide]);

  // pointer resize on the separator (it sits LEFT of the inspector)
  const drag = useRef<{ x: number; w: number } | null>(null);
  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    drag.current = { x: e.clientX, w: geo.width };
    e.currentTarget.setPointerCapture(e.pointerId);
    e.preventDefault();
  };
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    setRequested(clamp(drag.current.w + unzoom(drag.current.x - e.clientX)));
  };
  const onPointerUp = (e: React.PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    const w = drag.current.w + unzoom(drag.current.x - e.clientX);
    drag.current = null;
    commit(w);
  };
  const onSeparatorKey = (e: React.KeyboardEvent<HTMLDivElement>) => {
    if (e.key === "ArrowLeft") { e.preventDefault(); commit(geo.width + KEY_STEP); }
    else if (e.key === "ArrowRight") { e.preventDefault(); commit(geo.width - KEY_STEP); }
    else if (e.key === "Home") { e.preventDefault(); commit(INSPECTOR_MIN); }
    else if (e.key === "End") { e.preventDefault(); commit(geo.max); }
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
    // A split that became a drawer already holds focus in its inspector: it
    // keeps that control and the row to return to (slice 16 review 1).
    const already = !!aside && aside.contains(document.activeElement);
    if (!already) returnFocus.current = document.activeElement as HTMLElement | null;
    main?.setAttribute("inert", "");
    // The command console, open over a drawer it opened beside, keeps focus:
    // the drawer takes it only when nothing above it holds it. The console's
    // own close then focuses the inspector (The Line slice 12 smoke).
    if (!already && !document.querySelector('[data-testid="command-palette"]')) (focusables(aside)[0] ?? aside)?.focus();
    const onKey = (e: KeyboardEvent) => {
      // a field being edited keeps its Escape, as it does in the split
      // (useListKeys ignores editable targets) - slice 16 review 1
      if (e.key === "Escape" && !e.defaultPrevented && !inEditable(e.target)) { onCloseInspector?.(); return; }
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
      // Return focus only when the drawer closed. A drawer that became a
      // split keeps its inspector mounted, and focus stays in it (spec §2:
      // crossing the breakpoint preserves the focused control).
      const aside = asideRef.current;
      if (aside && document.contains(aside) && aside.contains(document.activeElement)) return;
      const back = returnFocus.current;
      returnFocus.current = null;
      if (back && document.contains(back)) back.focus();
    };
  }, [drawerOpen, onCloseInspector]);

  // Escape restores an expanded inspector before anything else closes it: a
  // capture listener runs ahead of the lists' and the drawer's, and marks the
  // key handled. A field being edited, or a surface above, keeps its Escape.
  useEffect(() => {
    if (!wide) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented || inEditable(e.target)) return;
      if (!ownsKey(asideRef.current, e.target) || surfaceAbove(asideRef.current)) return;
      e.preventDefault();
      setExpanded(false);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [wide]);

  const inspectorEl = inspector ? (
    <aside
      ref={asideRef}
      aria-label={inspectorLabel ?? `${label} details`}
      data-testid="lens-inspector"
      data-list-keys="ignore"
      role={geo.mode === "drawer" ? "dialog" : undefined}
      aria-modal={geo.mode === "drawer" ? true : undefined}
      tabIndex={-1}
      className={`lens-inspector${geo.mode === "drawer" ? " lens-drawer" : ""}${wide ? " lens-inspector-wide" : ""}`}
      style={wide ? undefined : { width: geo.width }}
    >
      <InspectorExpand.Provider value={expandValue}>{inspector}</InspectorExpand.Provider>
    </aside>
  ) : null;

  return (
    <div ref={rootRef} className="lens" data-mode={geo.mode} data-variant={variant} data-part={part} data-expanded={wide ? "" : undefined} data-testid="lens-layout" style={{ "--table-row-height": `${TABLE_ROW_HEIGHT}px` } as React.CSSProperties}>
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
      {/* The separator, the scrim and the inspector keep fixed places among
          the children, so crossing the breakpoint never remounts the
          inspector: its focus, scroll and tab survive (slice 16 review 1). */}
      {inspectorEl && geo.mode === "split" && !wide ? (
        <div
          role="separator"
          aria-orientation="vertical"
          aria-label="Resize details panel"
          aria-valuenow={geo.width}
          aria-valuemin={INSPECTOR_MIN}
          aria-valuemax={geo.max}
          tabIndex={0}
          title="Drag to resize · double-click to reset"
          className="lens-separator"
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onKeyDown={onSeparatorKey}
          onDoubleClick={() => commit(INSPECTOR_DEFAULT)}
        >
          <span aria-hidden="true" />
        </div>
      ) : null}
      {inspectorEl && geo.mode === "drawer" ? <div className="lens-scrim" aria-hidden="true" onClick={onCloseInspector} /> : null}
      {inspectorEl}
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
