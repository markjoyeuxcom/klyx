import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { IconChevronDown, IconSearch } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { toneColor } from "./primitives/styles";
import { CAP_STATE_TEXT, NAV_GROUPS, activeLocation, destState, groupState, navigate, type Destination, type NavGroup } from "../nav/registry";
import { useNavCounts, type NavBadge, type NavCounts } from "../nav/useNavCounts";

// NavBand - the one 32px navigation row (redesign spec §4), replacing the two
// stacked LensNav strips. Left: the current group, whose popover is the whole
// capability-gated tree with search. Right: the group's task destinations,
// then a Resources menu of raw GVR views. Tabs that do not fit move into More;
// the active destination is never the one hidden.

export function NavBand() {
  const route = useFleet((s) => s.route);
  if (route.name === "cluster") return <ClusterBand cluster={route.cluster} />;
  return (
    <nav aria-label="Fleet navigation" className="navband">
      {route.name === "fleet"
        ? <button className="l-chip" aria-current="page">Fleet board</button>
        : <span className="navband-title">{APP_TITLE[route.name]}</span>}
    </nav>
  );
}

const APP_TITLE: Record<"forwards" | "activity" | "settings", string> = { forwards: "Port-forwards", activity: "Activity", settings: "Settings" };

function Count({ b }: { b: NavBadge | undefined }) {
  if (!b || b.n <= 0) return null;
  return (
    <span className="navband-count" style={{ color: b.tone ? toneColor[b.tone] : undefined }} title={b.title}>
      {b.tone === "danger" ? "▲ " : ""}{b.n}
    </span>
  );
}

function ClusterBand({ cluster }: { cluster: string }) {
  const route = useFleet((s) => s.route);
  const kindFilter = useFleet((s) => s.workloads.kindFilter);
  const c = useFleet((s) => s.clusters.find((x) => x.name === cluster));
  const counts = useNavCounts(cluster);
  const { group, destination } = activeLocation(route, kindFilter);

  const available = (d: Destination) => !!group && destState(c, group, d) === "present";
  const tasks = group ? group.destinations.filter((d) => d.type === "task" && available(d)) : [];
  const resources = group ? group.destinations.filter((d) => d.type === "resource" && available(d)) : [];
  // A raw resource view that is open stays visible as a tab of its own.
  const tabs = destination && destination.type === "resource" ? [...tasks, destination] : tasks;

  return (
    <nav aria-label="Cluster navigation" className="navband">
      <GroupSelector cluster={c} clusterName={cluster} current={group} counts={counts} />
      <Tabs tabs={tabs} active={destination} counts={counts} resources={resources} cluster={cluster} />
    </nav>
  );
}

// ---- tabs with overflow --------------------------------------------------------

function Tabs({ tabs, active, counts, resources, cluster }: { tabs: Destination[]; active: Destination | undefined; counts: NavCounts; resources: Destination[]; cluster: string }) {
  const rowRef = useRef<HTMLDivElement>(null);
  const measureRef = useRef<HTMLDivElement>(null);
  const [fit, setFit] = useState<number>(tabs.length);
  const key = tabs.map((t) => t.id).join("|");

  // Measure every tab off-screen, then keep as many as the row holds after
  // reserving the Resources and More triggers. The active tab always stays.
  useLayoutEffect(() => {
    const row = rowRef.current;
    const measure = measureRef.current;
    if (!row || !measure) return;
    const compute = () => {
      const widths = Array.from(measure.children).map((el) => (el as HTMLElement).offsetWidth);
      const tabWidths = widths.slice(0, tabs.length);
      const reserve = widths.slice(tabs.length).reduce((a, w) => a + w + 2, 0);
      const room = row.clientWidth - reserve;
      if (room <= 0 || tabWidths.every((w) => w === 0)) { setFit(tabs.length); return; }
      let used = 0;
      let n = 0;
      for (const w of tabWidths) { if (used + w + 2 > room) break; used += w + 2; n++; }
      setFit(n);
    };
    compute();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(compute);
    ro.observe(row);
    return () => ro.disconnect();
  }, [key, tabs.length, resources.length]);

  let shown = tabs.slice(0, fit);
  if (active && tabs.includes(active) && !shown.includes(active)) {
    // Keep the active tab and its priority neighbours: drop from the end.
    shown = [...tabs.slice(0, Math.max(0, fit - 1)), active];
  }
  const overflow = tabs.filter((t) => !shown.includes(t));

  const tab = (d: Destination) => (
    <button key={d.id} className="l-chip navband-tab" aria-current={d === active ? "page" : undefined}
      title={d.type === "resource" ? `${d.label} - raw resource view` : undefined}
      onClick={() => navigate(cluster, d)}>
      {d.label}{d.type === "resource" && <span className="t-secondary">raw</span>}
      <Count b={counts.destination[d.id]} />
    </button>
  );

  return (
    <div ref={rowRef} className="navband-tabs">
      {shown.map(tab)}
      {overflow.length > 0 && (
        <NavMenu label="More" title="More destinations" items={overflow} active={active} counts={counts} cluster={cluster} />
      )}
      {resources.length > 0 && (
        <NavMenu label="Resources" title="Raw resource views" items={resources} active={active} counts={counts} cluster={cluster} raw />
      )}
      <div ref={measureRef} aria-hidden="true" className="navband-measure">
        {tabs.map((d) => (
          <span key={d.id} className="l-chip navband-tab">{d.label}{d.type === "resource" && <span className="t-secondary">raw</span>}<Count b={counts.destination[d.id]} /></span>
        ))}
        <span className="l-chip">More <IconChevronDown size={12} /></span>
        {resources.length > 0 && <span className="l-chip">Resources <IconChevronDown size={12} /></span>}
      </div>
    </div>
  );
}

// NavMenu: a trigger plus a menu of destinations (More / Resources). Same
// keyboard model as the inspector's ActionMenu: arrows, Home/End, Escape
// returns focus to the trigger, Tab closes.
function NavMenu({ label, title, items, active, counts, cluster, raw = false }: { label: string; title: string; items: Destination[]; active: Destination | undefined; counts: NavCounts; cluster: string; raw?: boolean }) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const id = useId();
  const holdsActive = !!active && items.includes(active);

  useEffect(() => {
    if (!open) return;
    const items = menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]');
    const current = menuRef.current?.querySelector<HTMLElement>('[aria-current="page"]');
    (current ?? items?.[0])?.focus();
    const onDown = (e: MouseEvent) => { if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setOpen(false); triggerRef.current?.focus(); return; }
      if (e.key === "Tab") { setOpen(false); return; }
      const list = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
      if (list.length === 0) return;
      const i = list.indexOf(document.activeElement as HTMLElement);
      let next = -1;
      if (e.key === "ArrowDown") next = i < 0 ? 0 : (i + 1) % list.length;
      else if (e.key === "ArrowUp") next = i < 0 ? list.length - 1 : (i - 1 + list.length) % list.length;
      else if (e.key === "Home") next = 0;
      else if (e.key === "End") next = list.length - 1;
      if (next >= 0) { e.preventDefault(); e.stopPropagation(); list[next].focus(); }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey, true); };
  }, [open]);

  return (
    <div ref={wrapRef} className="navband-menu-wrap">
      <button ref={triggerRef} className="l-chip" aria-haspopup="menu" aria-expanded={open} aria-controls={id} title={title}
        data-holds-active={holdsActive ? "true" : undefined} onClick={() => setOpen((o) => !o)}>
        {label} <IconChevronDown size={12} stroke={1.7} />
      </button>
      {open && (
        <div ref={menuRef} id={id} role="menu" aria-label={title} className="l-menu navband-menu">
          {raw && <div className="l-menu-label">Raw resource views</div>}
          {items.map((d) => (
            <button key={d.id} role="menuitem" className="l-menu-item" aria-current={d === active ? "page" : undefined}
              onClick={() => { setOpen(false); triggerRef.current?.focus(); navigate(cluster, d); }}>
              <span style={{ flex: 1 }}>{d.label}</span>
              <Count b={counts.destination[d.id]} />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ---- group selector + tree -------------------------------------------------------

type TreeRow =
  | { kind: "group"; g: NavGroup; state: ReturnType<typeof groupState> }
  | { kind: "label"; text: string; key: string }
  | { kind: "dest"; g: NavGroup; d: Destination; disabled: string | null };

// Words of a label: "ResourceQuotas" -> resource, quotas; "EC2NodeClasses" ->
// ec2, node, classes. Search matches word starts, so "source" finds Flux
// Sources and not ResourceQuotas.
const words = (s: string) => s.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);

function matches(q: string, g: NavGroup, d: Destination): boolean {
  if (!q) return true;
  const hay = [d.label, g.label, ...(d.keywords ?? []), d.target.kind === "resource" ? d.target.ref.kind : ""].flatMap(words);
  return q.split(/\s+/).filter(Boolean).every((t) => hay.some((w) => w.startsWith(t)));
}

export function buildTree(c: ClusterDTO | undefined, query: string): TreeRow[] {
  const q = query.trim().toLowerCase();
  const rows: TreeRow[] = [];
  for (const g of NAV_GROUPS) {
    const gs = groupState(c, g);
    if (gs === "absent") continue;
    const dests = g.destinations.filter((d) => destState(c, g, d) !== "absent" && matches(q, g, d));
    if (dests.length === 0) continue;
    rows.push({ kind: "group", g, state: gs });
    const tasks = dests.filter((d) => d.type === "task");
    const res = dests.filter((d) => d.type === "resource");
    const reason = (d: Destination) => { const s = destState(c, g, d); return s === "present" ? null : CAP_STATE_TEXT[s]; };
    for (const d of tasks) rows.push({ kind: "dest", g, d, disabled: reason(d) });
    if (res.length > 0) {
      rows.push({ kind: "label", text: "Resources", key: `${g.id}:res` });
      for (const d of res) rows.push({ kind: "dest", g, d, disabled: reason(d) });
    }
  }
  return rows;
}

function GroupSelector({ cluster, clusterName, current, counts }: { cluster: ClusterDTO | undefined; clusterName: string; current: NavGroup | undefined; counts: NavCounts }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [sel, setSel] = useState(0);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const listId = useId();
  const route = useFleet((s) => s.route);
  const kindFilter = useFleet((s) => s.workloads.kindFilter);
  const { destination: active } = activeLocation(route, kindFilter);

  const rows = useMemo(() => (open ? buildTree(cluster, query) : []), [open, cluster, query]);
  const choices = rows.filter((r): r is Extract<TreeRow, { kind: "dest" }> => r.kind === "dest" && r.disabled === null);

  const close = (refocus: boolean) => { setOpen(false); setQuery(""); if (refocus) triggerRef.current?.focus(); };
  const go = (d: Destination) => { close(true); navigate(clusterName, d); };

  useEffect(() => {
    if (!open) return;
    inputRef.current?.focus();
    const onDown = (e: MouseEvent) => { if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) close(false); };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  // Opening starts on the active destination; typing starts on the first match.
  useEffect(() => {
    if (!open) return;
    const i = query ? 0 : Math.max(0, choices.findIndex((r) => r.d === active));
    setSel(i);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, query]);
  useEffect(() => {
    const el = listRef.current?.querySelector('[data-selected="true"]') as HTMLElement | null;
    if (el && typeof el.scrollIntoView === "function") el.scrollIntoView({ block: "nearest" });
  }, [sel, open]);

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); close(true); return; }
    if (e.key === "Tab") { close(false); return; }
    if (choices.length === 0) return;
    if (e.key === "ArrowDown") { e.preventDefault(); setSel((s) => (s + 1) % choices.length); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setSel((s) => (s - 1 + choices.length) % choices.length); }
    else if (e.key === "Enter") { e.preventDefault(); const r = choices[sel]; if (r) go(r.d); }
  };

  const label = current?.label ?? "Choose a view";
  const badge = current ? counts.group[current.id] : undefined;
  const optionId = (d: Destination) => `${listId}-${d.id}`;
  const selected = choices[sel];

  return (
    <div ref={wrapRef} className="navband-menu-wrap">
      <button ref={triggerRef} className="navband-group" aria-haspopup="dialog" aria-expanded={open}
        aria-label={`${label}${badge && badge.n > 0 ? `, ${badge.title}` : ""} - all views`}
        title={`${label} - all views (search)`} onClick={() => (open ? close(false) : setOpen(true))}>
        <span className="navband-group-label">{label}</span>
        <Count b={badge} />
        <IconChevronDown size={12} stroke={1.7} style={{ flexShrink: 0 }} />
      </button>
      {open && (
        <div role="dialog" aria-label="All views" className="l-menu navtree" onKeyDown={onKey}>
          <label className="navtree-search">
            <IconSearch size={13} stroke={1.6} aria-hidden="true" />
            <span className="sr-only">Find a view</span>
            <input ref={inputRef} className="l-input" value={query} placeholder="Find a view…" spellCheck={false} autoComplete="off"
              role="combobox" aria-expanded={true} aria-controls={listId} aria-autocomplete="list"
              aria-activedescendant={selected ? optionId(selected.d) : undefined}
              onChange={(e) => setQuery(e.target.value)} />
          </label>
          <div ref={listRef} id={listId} role="listbox" aria-label="Views" className="navtree-list">
            {rows.length === 0 && <div className="t-secondary" style={{ padding: "8px 10px" }}>No view matches “{query}”.</div>}
            {rows.map((r) => {
              if (r.kind === "group") {
                const note = r.state === "present" ? null : CAP_STATE_TEXT[r.state];
                return (
                  <div key={`g:${r.g.id}`} role="presentation" className="navtree-group">
                    <span style={{ flex: 1 }}>{r.g.label}</span>
                    {note ? <span className="t-secondary">{note}</span> : <Count b={counts.group[r.g.id]} />}
                  </div>
                );
              }
              if (r.kind === "label") return <div key={r.key} role="presentation" className="navtree-sublabel">{r.text}</div>;
              const isSel = selected?.d === r.d;
              return (
                <div key={`${r.g.id}:${r.d.id}`} id={optionId(r.d)} role="option" aria-selected={isSel} aria-disabled={r.disabled ? true : undefined}
                  aria-current={r.d === active ? "page" : undefined} data-selected={isSel ? "true" : "false"}
                  className="navtree-item" title={r.disabled ?? (r.d.type === "resource" ? "Raw resource view" : undefined)}
                  onMouseMove={() => { if (!r.disabled) setSel(choices.findIndex((x) => x.d === r.d)); }}
                  onMouseDown={(e) => { e.preventDefault(); if (!r.disabled) go(r.d); }}>
                  <span style={{ flex: 1, minWidth: 0, overflowWrap: "anywhere" }}>{r.d.label}</span>
                  {r.disabled ? <span className="t-secondary">{r.disabled}</span> : <Count b={counts.destination[r.d.id]} />}
                </div>
              );
            })}
          </div>
          <div className="navtree-foot t-secondary">↑↓ choose · ↵ open · esc close</div>
        </div>
      )}
    </div>
  );
}
