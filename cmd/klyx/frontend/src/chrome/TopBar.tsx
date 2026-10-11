import { useState, useRef, useEffect } from "react";
import { IconArrowLeft, IconArrowRight, IconArrowsLeftRight, IconClock, IconCommand, IconHistory, IconSettings, IconTerminal2 } from "@tabler/icons-react";
import { ThemeToggle } from "./ThemeToggle";
import { KlyxMark } from "./KlyxMark";
import { useFleet } from "../store/fleet";
import { attentionAreas, attentionSummary, openNavTree } from "../nav/attention";
import { back, backTarget, entryKey, entryLabel, forward, forwardTarget, go, recentPlaces, useHistory, type HistoryEntry } from "../nav/history";
import { clusterDisplayName } from "../fleet/ClusterCard";
import { stopAllForwards } from "../bridge/forwards";
import { openCommandPalette } from "./CommandPalette";
import { openTerminal } from "../bridge/configsvc";

// Wails v3 window-drag regions: the bar is draggable, interactive children opt out.
const drag = { "--wails-draggable": "drag" } as React.CSSProperties;
const noDrag = { "--wails-draggable": "no-drag" } as React.CSSProperties;

// TopBar is the mast (spec §1): one 36px band. Left: Back / Forward / Recent,
// then the readable context - cluster and the place in it - and the
// protected-cluster cue, which never clips. Right: the palette and the app
// utilities. Context outranks utility text: the palette label is the first
// thing to give up width. The empty middle drags the window; macOS traffic
// lights sit in the left padding.
export function TopBar() {
  const route = useFleet((s) => s.route);
  const kindFilter = useFleet((s) => s.workloads.kindFilter);
  const current = useFleet((s) => (s.route.name === "cluster" ? s.clusters.find((c) => c.name === (s.route as { cluster: string }).cluster) : undefined));
  const where = entryLabel({ route, kindFilter });
  const clusterName = where.cluster ? clusterDisplayName(where.cluster) : null;
  return (
    // data-list-keys="ignore": a focused mast control keeps its keys (see NavBand).
    <header className="mast" style={drag} data-list-keys="ignore">
      <div className="mast-left">
        <HistoryControls />
        <KlyxMark size={18} />
        <div className="mast-identity" style={noDrag} title={where.cluster ? `${where.cluster} / ${where.place}` : where.place}>
          {clusterName ? (
            <>
              <span className="mast-cluster">{clusterName}</span>
              <span className="mast-sep" aria-hidden="true">/</span>
              <span className="mast-place">{where.place}</span>
            </>
          ) : <span className="mast-cluster">{where.place}</span>}
        </div>
        {route.name === "cluster" && <AttentionButton cluster={route.cluster} />}
        {/* The protected cue never clips: it does not shrink, the place does. */}
        {current?.protected && (
          <span className="mast-protected" style={noDrag} title={current.protectedInferred ? "Protected - inferred from the cluster's environment name; operations need typed confirmation" : "Protected - operations need typed confirmation"}>
            prd lock
          </span>
        )}
      </div>
      <div className="mast-right" style={noDrag}>
        <button
          type="button"
          aria-label="open command palette"
          data-testid="command-bar"
          onClick={openCommandPalette}
          title="open command palette with cmd+k or ctrl+k"
          className="mast-palette"
        >
          <IconCommand size={13} stroke={1.5} style={{ flexShrink: 0 }} />
          <span className="mast-palette-text">jump or run a command</span>
          <kbd>⌘K</kbd>
        </button>
        {/* App utilities (spec §4): icon buttons whose names live in their
            accessible labels and tooltips - context outranks utility text. */}
        <ForwardsIndicator />
        <UtilityButton label="Activity" title="Activity - recent action outcomes" current={route.name === "activity"} onClick={() => useFleet.getState().openActivity()}>
          <IconHistory size={15} stroke={1.5} />
        </UtilityButton>
        <UtilityButton label="Terminal" title="Open a terminal with this fleet's kubeconfig" onClick={() => void openTerminal()}>
          <IconTerminal2 size={15} stroke={1.5} />
        </UtilityButton>
        <SettingsButton current={route.name === "settings"} />
        <ThemeToggle />
      </div>
    </header>
  );
}

// AttentionButton (spec §4): the cluster's actionable summary in one line -
// how many AREAS need attention, never a sum of objects - and the gaps. It
// opens the same fault-aware navigation tree, not a second ledger.
function AttentionButton({ cluster }: { cluster: string }) {
  const attention = useFleet((s) => s.attention);
  const areas = attentionAreas(attention, cluster);
  const sum = attentionSummary(areas);
  const lines = areas.map((a) => `${a.label}: ${a.text}`).join("\n");
  let text: string;
  let color = "var(--color-text-secondary)";
  if (sum.state === "loading") text = "Checking…";
  else if (sum.state === "attention") {
    text = `${sum.tone === "danger" ? "▲ " : ""}${sum.areas} area${sum.areas === 1 ? "" : "s"} need attention${sum.gaps > 0 ? ` · ${sum.gaps} unverified` : ""}`;
    color = sum.tone === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)";
  } else if (sum.state === "gaps") text = `No attention found · ${sum.gaps} unverified`;
  else text = "No attention found";
  return (
    <button type="button" className="mast-attention" style={{ ...noDrag, color }} onClick={() => openNavTree("attention")}
      aria-label={`Attention: ${text}. Opens the view tree.`} title={`${lines}\n\nOpen the view tree`}>
      {text}
    </button>
  );
}

// HistoryControls: Back / Forward (⌘[ / ⌘]) and the Recent menu. They move
// between places; nothing they restore runs an action.
function HistoryControls() {
  // Re-render on history, route and fleet membership: the buttons name the
  // place they would actually reach, skipping clusters that left the fleet.
  useHistory((h) => h.past);
  useHistory((h) => h.future);
  useHistory((h) => h.recent);
  useFleet((s) => s.route);
  useFleet((s) => s.clusters);
  const prev = backTarget();
  const next = forwardTarget();
  const name = (e: HistoryEntry | undefined) => { if (!e) return ""; const l = entryLabel(e); return l.cluster ? `${clusterDisplayName(l.cluster)} / ${l.place}` : l.place; };
  const recent = recentPlaces();
  return (
    <div className="mast-history" style={noDrag}>
      <button type="button" className="mast-icon" aria-label={prev ? `Back to ${name(prev)}` : "Back"} title={prev ? `Back to ${name(prev)} (⌘[)` : "Back (⌘[)"} disabled={!prev} onClick={() => back()}>
        <IconArrowLeft size={15} stroke={1.6} />
      </button>
      <button type="button" className="mast-icon" aria-label={next ? `Forward to ${name(next)}` : "Forward"} title={next ? `Forward to ${name(next)} (⌘])` : "Forward (⌘])"} disabled={!next} onClick={() => forward()}>
        <IconArrowRight size={15} stroke={1.6} />
      </button>
      <RecentMenu places={recent} name={name} />
    </div>
  );
}

function RecentMenu({ places, name }: { places: HistoryEntry[]; name: (e: HistoryEntry) => string }) {
  const [open, setOpen] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const onDown = (e: MouseEvent) => { if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setOpen(false); triggerRef.current?.focus(); return; }
      if (e.key === "Tab") { setOpen(false); return; }
      const list = Array.from(menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
      if (list.length === 0) return;
      const i = list.indexOf(document.activeElement as HTMLElement);
      let n = -1;
      if (e.key === "ArrowDown") n = i < 0 ? 0 : (i + 1) % list.length;
      else if (e.key === "ArrowUp") n = i < 0 ? list.length - 1 : (i - 1 + list.length) % list.length;
      else if (e.key === "Home") n = 0;
      else if (e.key === "End") n = list.length - 1;
      if (n >= 0) { e.preventDefault(); e.stopPropagation(); list[n].focus(); }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey, true);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey, true); };
  }, [open]);
  return (
    <div ref={wrapRef} style={{ position: "relative" }}>
      <button ref={triggerRef} type="button" className="mast-icon" aria-label="Recent places" title="Recent places" aria-haspopup="menu" aria-expanded={open}
        disabled={places.length === 0} onClick={() => setOpen((o) => !o)}>
        <IconClock size={15} stroke={1.6} />
      </button>
      {open && (
        <div ref={menuRef} role="menu" aria-label="Recent places" className="l-menu" style={{ top: "calc(100% + 6px)", left: 0, width: 340, maxHeight: "60vh", overflow: "auto" }}>
          <div className="l-menu-label">Recent places</div>
          {places.map((e) => (
            <button key={entryKey(e)} role="menuitem" className="l-menu-item" onClick={() => { setOpen(false); triggerRef.current?.focus(); go(e); }} title={name(e)}>
              <span style={{ minWidth: 0, overflowWrap: "anywhere" }}>{name(e)}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

const utilityBtn = (current: boolean): React.CSSProperties => ({
  display: "inline-flex", alignItems: "center", gap: 4, height: 24, minWidth: 24, padding: "0 6px", borderRadius: 4, border: "none", cursor: "pointer",
  fontFamily: "var(--font-mono)", fontSize: 11,
  color: current ? "var(--color-text-primary)" : "var(--color-text-secondary)",
  background: current ? "var(--bench-well-bg, var(--color-background-secondary))" : "transparent",
  boxShadow: current ? "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))" : undefined,
});

function UtilityButton({ label, title, current = false, onClick, children }: { label: string; title: string; current?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" aria-label={label} title={title} aria-current={current ? "page" : undefined} onClick={onClick} style={utilityBtn(current)}>
      {children}
    </button>
  );
}

function SettingsButton({ current }: { current: boolean }) {
  const newContexts = useFleet((s) => s.newContexts);
  const label = newContexts > 0 ? `Settings, ${newContexts} new kubeconfig contexts` : "Settings";
  return (
    <UtilityButton label={label} title={newContexts > 0 ? `Settings - ${newContexts} new kubeconfig contexts` : "Settings"} current={current} onClick={() => useFleet.getState().openSettings()}>
      <IconSettings size={15} stroke={1.5} />
      {newContexts > 0 && <span style={{ color: "var(--color-text-info)" }}>{newContexts}</span>}
    </UtilityButton>
  );
}

// ForwardsIndicator is the mast's Port-forwards utility: "⇄" plus the live
// count. Clicking it opens a glanceable dropdown - one compact line per
// forward, any line jumps to the Forwards view where the real management
// lives - and "stop all" stays here as the emergency brake. Broken rows render
// warning-coloured. With no forwards it opens the Forwards view directly.
function ForwardsIndicator() {
  const forwards = useFleet((s) => s.forwards);
  const current = useFleet((s) => s.route.name === "forwards");
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  // Close on outside click / Escape.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey); };
  }, [open]);

  if (forwards.length === 0) {
    return (
      <UtilityButton label="Port-forwards" title="Port-forwards - none active" current={current} onClick={() => useFleet.getState().openForwards()}>
        <IconArrowsLeftRight size={15} stroke={1.5} />
      </UtilityButton>
    );
  }

  const anyBroken = forwards.some((f) => f.status === "broken");

  return (
    <div ref={ref} style={{ position: "relative" }}>
      <button
        onClick={() => setOpen((o) => !o)}
        aria-label={`Port-forwards, ${forwards.length} active${anyBroken ? ", some broken" : ""}`}
        data-testid="forwards-chip"
        style={{
          display: "inline-flex", alignItems: "center", gap: 5,
          fontFamily: "var(--font-mono)", fontSize: 11,
          padding: "3px 9px", borderRadius: 11, cursor: "pointer",
          border: `0.5px solid ${anyBroken ? "var(--color-text-warning)" : "var(--color-text-info)"}`,
          background: "transparent",
          color: anyBroken ? "var(--color-text-warning)" : "var(--color-text-info)",
        }}
      >
        <span aria-hidden>⇄</span>
        <span>{forwards.length}</span>
      </button>

      {open && (
        <div
          data-testid="forwards-panel"
          style={{
            position: "absolute", top: 30, right: 0, zIndex: 50,
            width: 280, maxHeight: 320, overflowY: "auto",
            background: "var(--color-background-primary)",
            border: "0.5px solid var(--color-border-tertiary)",
            borderRadius: 4,
            padding: 6,
          }}
        >
          <div style={{ display: "flex", alignItems: "center", padding: "4px 6px 6px", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)", flex: 1 }}>
              port-forwards
            </span>
            <button
              onClick={() => void stopAllForwards()}
              data-testid="forwards-stop-all"
              style={{ fontSize: 10, padding: "2px 8px", borderRadius: 4, cursor: "pointer", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-secondary)" }}
            >stop all</button>
          </div>

          {forwards.map((f) => {
            const broken = f.status === "broken";
            return (
              <button
                key={f.id}
                data-testid={`forward-row-${f.id}`}
                onClick={() => { useFleet.getState().openForwards(); setOpen(false); }}
                title={`${f.cluster} ${f.namespace}/${f.targetName} — manage in Forwards`}
                style={{
                  display: "grid", gridTemplateColumns: "8px 1fr", gap: 8, alignItems: "center",
                  width: "100%", textAlign: "left", cursor: "pointer",
                  padding: "6px 6px", fontFamily: "var(--font-mono)", fontSize: 11,
                  background: "transparent", border: "none",
                  borderBottom: "0.5px solid var(--color-border-tertiary)", borderRadius: 0,
                  color: broken ? "var(--color-text-warning)" : "var(--color-text-primary)",
                }}
              >
                <span style={{ width: 7, height: 7, borderRadius: "50%", background: broken ? "var(--color-text-warning)" : "var(--color-text-success)" }} />
                <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  <span style={{ color: "var(--color-text-tertiary)" }}>{f.cluster}</span>{" "}
                  {f.namespace}/{f.targetName}{" "}
                  <span style={{ color: broken ? "var(--color-text-warning)" : "var(--color-text-info)" }}>
                    :{f.localPort}→:{f.targetPort}
                  </span>
                  {broken && <span style={{ marginLeft: 4, fontSize: 9 }}>broken</span>}
                </span>
              </button>
            );
          })}
          <div style={{ padding: "5px 6px 2px", fontSize: 10, color: "var(--color-text-tertiary)" }}>
            click a forward to manage
          </div>
        </div>
      )}
    </div>
  );
}
