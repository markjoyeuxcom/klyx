import { createContext, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { IconChevronDown, IconChevronRight, IconX } from "@tabler/icons-react";
import { CopyButton } from "../chrome/CopyButton";
import "./layout.css";

// InspectorLayout owns the detail panel's anatomy (spec §3): sticky identity,
// one verdict, an action band (one primary, at most one secondary, More),
// Overview/Manifest tabs, then a scrolling body of ordered sections. It never
// computes health, permissions or confirmations - callers pass handlers that
// already route through ConfirmDialog / protected-cluster arming.

// InspectorTemplate marks content rendered inside an InspectorLayout, so shared
// section helpers (ResourceInspector's InspectorSection) adopt the template's
// section anatomy instead of their legacy header style.
export const InspectorTemplate = createContext(false);

export type MenuItem =
  | { kind?: "item"; label: string; onSelect: () => void; danger?: boolean; disabled?: boolean; hint?: string }
  | { kind: "choose"; label: string; options: { label: string; onSelect: () => void }[]; danger?: boolean; hint?: string }
  | { kind: "separator" };

export type InspectorAction = { label: string; onSelect: () => void; kbd?: string; options?: { label: string; onSelect: () => void }[] };

export function InspectorLayout({
  kind,
  namespace,
  name,
  meta,
  onClose,
  verdict,
  primary,
  secondary,
  more,
  tab,
  onTab,
  manifest,
  children,
}: {
  kind: string;
  namespace?: string;
  name: string;
  meta?: ReactNode;
  onClose: () => void;
  verdict?: ReactNode;
  primary?: InspectorAction;
  secondary?: InspectorAction;
  more?: MenuItem[];
  // Omit onTab for a lens with no inline manifest: no tabs render, rather
  // than a tab that navigates away and is never selected.
  tab?: "overview" | "manifest";
  onTab?: (t: "overview" | "manifest") => void;
  manifest?: ReactNode;
  children: ReactNode;
}) {
  return (
    <>
      <div className="insp-identity">
        <div className="insp-identity-text">
          <div className="insp-meta">{kind}{namespace ? ` in ${namespace}` : ""}</div>
          <h2 className="t-object-title">{name}</h2>
          {meta && <div className="insp-meta" style={{ marginTop: 2 }}>{meta}</div>}
        </div>
        <CopyButton text={name} label={`copy ${kind.toLowerCase()} name`} />
        <button className="l-btn" style={{ width: 28, padding: 0, justifyContent: "center", background: "transparent", boxShadow: "none", borderTop: 0 }} onClick={onClose} aria-label="close details">
          <IconX size={14} stroke={1.6} />
        </button>
      </div>
      {/* Everything below identity scrolls, so a long diagnosis never pushes
          the actions or sections out of reach; the action band and tabs
          stick once the verdict has scrolled away. */}
      <div className="insp-scroll">
        {verdict && <div className="insp-verdict">{verdict}</div>}
        <div className="insp-sticky">
          {(primary || secondary || (more && more.length > 0)) && (
            <div className="insp-actions" role="group" aria-label="actions">
              {primary && <ActionButton action={primary} primary />}
              {secondary && <ActionButton action={secondary} />}
              {more && more.length > 0 && <ActionMenu label="More" items={more} />}
            </div>
          )}
          {onTab && (
            <div className="insp-tabs" role="tablist" aria-label="detail views">
              <button role="tab" className="insp-tab" aria-selected={tab !== "manifest"} onClick={() => onTab("overview")}>Overview</button>
              <button role="tab" className="insp-tab" aria-selected={tab === "manifest"} onClick={() => onTab("manifest")}>Manifest</button>
            </div>
          )}
        </div>
        <div className="insp-body" role={onTab ? "tabpanel" : undefined}>
          <InspectorTemplate.Provider value={true}>{tab === "manifest" ? manifest : children}</InspectorTemplate.Provider>
        </div>
      </div>
    </>
  );
}

function ActionButton({ action, primary }: { action: InspectorAction; primary?: boolean }) {
  if (action.options && action.options.length > 1) {
    return (
      <ActionMenu
        label={action.label}
        buttonClass={primary ? "l-btn l-btn-primary" : "l-btn"}
        items={[{ kind: "choose", label: `${action.label} in container`, options: action.options }]}
        inlineChoose
      />
    );
  }
  return (
    <button className={primary ? "l-btn l-btn-primary" : "l-btn"} onClick={action.onSelect}>
      {action.label}
      {action.kbd && <kbd className="t-legend" aria-hidden="true" style={{ fontFamily: "var(--font-mono)" }}>{action.kbd.toUpperCase()}</kbd>}
    </button>
  );
}

// ActionMenu: a button that opens a small menu. "choose" items expand into an
// option list (e.g. pick a container) instead of a nested flyout.
export function ActionMenu({ label, items, buttonClass = "l-btn", inlineChoose = false }: {
  label: string;
  items: MenuItem[];
  buttonClass?: string;
  inlineChoose?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [choosing, setChoosing] = useState<number | null>(inlineChoose ? 0 : null);
  const ref = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();

  // Menu keyboard model: focus the first item on open (and when a "choose"
  // list replaces the items), arrows move, Home/End jump, Escape closes this
  // menu only and returns focus to its trigger, Tab closes.
  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
  }, [open, choosing]);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) close(false); };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); return; }
      if (e.key === "Tab") { close(false); return; }
      const items = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([disabled])') ?? []);
      if (items.length === 0) return;
      const i = items.indexOf(document.activeElement as HTMLElement);
      let next = -1;
      if (e.key === "ArrowDown") next = i < 0 ? 0 : (i + 1) % items.length;
      else if (e.key === "ArrowUp") next = i < 0 ? items.length - 1 : (i - 1 + items.length) % items.length;
      else if (e.key === "Home") next = 0;
      else if (e.key === "End") next = items.length - 1;
      if (next >= 0) { e.preventDefault(); e.stopPropagation(); items[next].focus(); }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey, true); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  function close(refocus: boolean) {
    setOpen(false);
    setChoosing(inlineChoose ? 0 : null);
    if (refocus) triggerRef.current?.focus();
  }
  const pick = (fn: () => void) => { close(true); fn(); };
  const chosen = choosing !== null ? items[choosing] : null;

  return (
    <div ref={ref} style={{ position: "relative" }}>
      <button ref={triggerRef} className={buttonClass} aria-haspopup="menu" aria-expanded={open} aria-controls={menuId} onClick={() => (open ? close(false) : setOpen(true))}>
        {label} <IconChevronDown size={12} stroke={1.7} />
      </button>
      {open && (
        <div ref={menuRef} id={menuId} role="menu" aria-label={label} className="l-menu" style={{ top: "calc(100% + 4px)", left: 0 }}>
          {chosen && chosen.kind === "choose" ? (
            <>
              <div className="l-menu-label">{chosen.label}</div>
              {chosen.options.map((o) => (
                <button key={o.label} role="menuitem" className="l-menu-item t-identifier" onClick={() => pick(o.onSelect)}>{o.label}</button>
              ))}
            </>
          ) : (
            items.map((it, i) => {
              if (it.kind === "separator") return <div key={`sep${i}`} className="l-menu-sep" role="separator" />;
              if (it.kind === "choose") {
                return (
                  <button key={it.label} role="menuitem" className="l-menu-item" data-danger={it.danger ? "true" : undefined} title={it.hint}
                    onClick={() => (it.options.length === 1 ? pick(it.options[0].onSelect) : setChoosing(i))}>
                    {it.label}{it.options.length > 1 ? " …" : ""}
                  </button>
                );
              }
              return (
                <button key={it.label} role="menuitem" className="l-menu-item" data-danger={it.danger ? "true" : undefined} disabled={it.disabled} title={it.hint}
                  onClick={() => pick(it.onSelect)}>
                  {it.label}
                </button>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}

// InspectorBlock: one ordered section. Failure, dependencies, containers and
// events are expanded by default; metadata and successful conditions are not.
export function InspectorBlock({ title, count, extra, defaultOpen = true, children }: {
  title: string;
  count?: number;
  extra?: ReactNode;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <section className="insp-block">
      <button className="insp-block-head" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? <IconChevronDown size={12} stroke={1.8} color="var(--color-text-tertiary)" /> : <IconChevronRight size={12} stroke={1.8} color="var(--color-text-tertiary)" />}
        <h3 className="t-section-label">{title}{count != null ? ` · ${count}` : ""}</h3>
        {extra}
      </button>
      {open && children}
    </section>
  );
}
