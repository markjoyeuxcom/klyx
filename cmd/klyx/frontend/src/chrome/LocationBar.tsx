import { useState, useRef, useEffect } from "react";
import { useBelow } from "./useBelow";
import { IconArrowLeft, IconArrowRight, IconArrowsLeftRight, IconClock, IconHistory, IconLayoutSidebarLeftCollapse, IconLayoutSidebarLeftExpand, IconMessageChatbot, IconSearch, IconSettings, IconTerminal2 } from "@tabler/icons-react";
import { openChat } from "../ai/chatStore";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { back, backTarget, entryKey, entryLabel, forward, forwardTarget, go, recentPlaces, useHistory, type HistoryEntry } from "../nav/history";
import { clusterDisplayName } from "../fleet/clusterName";
import { stopAllForwards } from "../bridge/forwards";
import { openCommandPalette } from "./CommandPalette";
import { openTerminal } from "../bridge/configsvc";
import { vh } from "../app/zoom";
import { FleetIndicator } from "./FleetIndicator";
import { StationSwitcher } from "./StationSwitcher";
import { StationStrip } from "./StationStrip";
import { useMenuPopover } from "./useMenuPopover";
import { MOD } from "./modKey";
import { drag, noDrag } from "./drag";
import { LENS_INDEX_ID, LENS_INDEX_TOGGLE_ID, toggleLensIndex, useLensIndexOpen } from "../nav/lensIndexOpen";
import { INSPECTOR_IDENTITY_ID, useInspectorPin } from "../nav/inspectorPin";


// LocationBar (The Line slice 4 §2): history, the mark, the crumbs
// Fleet / Station / Lens, the fleet indicator, the ⌘K well and the
// utilities. Every function of the retired mast and spine has a home here
// or in Settings (the parity table). Crumbs outrank utilities: the well's
// words give up width first; the protected tag never shrinks.
// The widths below which the bar steps things out (layout.css, data-below).
const BAR_BREAKS = [1680, 1600, 1440, 1280, 1180] as const;

export function LocationBar() {
  const barRef = useRef<HTMLElement>(null);
  const below = useBelow(barRef, BAR_BREAKS);
  const route = useFleet((s) => s.route);
  const kindFilter = useFleet((s) => s.workloads.kindFilter);
  const current = useFleet((s) => (s.route.name === "cluster" ? s.clusters.find((c) => c.name === (s.route as { cluster: string }).cluster) : undefined));
  const where = entryLabel({ route, kindFilter });
  const onBoard = route.name === "fleet" && !route.view;
  // The object crumb (slice 4 §6.4): the open inspector's object, only while
  // it is pinned to the cluster shown. The lens crumb then names the lens
  // alone (an instance route's place carries its object too).
  const held = useInspectorPin((s) => s.held);
  const object = route.name === "cluster" && held?.pin.cluster === route.cluster ? held.pin : null;
  const lens = object ? where.place.split(" › ")[0] : where.place;
  return (
    // data-list-keys="ignore": a focused bar control keeps its keys (useListKeys).
    <header ref={barRef} className="mast locbar" data-below={below || undefined} style={drag} data-list-keys="ignore">
      <div className="locbar-left">
        <IndexToggle />
        <HistoryControls />
        <button type="button" className="locbar-mark" style={noDrag} aria-label="Klyx fleet board" title="Fleet board" onClick={() => useFleet.getState().openFleet()}>
          <LineMark />
          <span className="locbar-wordmark" aria-hidden="true">Klyx</span>
        </button>
        <nav aria-label="Location" className="locbar-crumbs" style={noDrag}>
          <button type="button" className="locbar-crumb locbar-fleet" aria-current={onBoard ? "page" : undefined} onClick={() => useFleet.getState().openFleet()}>Fleet</button>
          {route.name === "cluster" ? (
            <>
              <Sep />
              {current ? <StationSwitcher current={current} /> : <span className="locbar-crumb">{clusterDisplayName(route.cluster)}</span>}
              {current?.protected && <ProtectedTag c={current} />}
              <Sep />
              <span className="locbar-crumb locbar-lens" aria-current="page" title={lens}>{lens}</span>
              {object && (
                <>
                  <Sep />
                  <button type="button" className="locbar-crumb locbar-object" title={`${object.kind} ${object.namespace ? `${object.namespace}/` : ""}${object.name} - go to its details`}
                    aria-label={`${object.kind} ${object.name}: go to its details`} onClick={() => document.getElementById(INSPECTOR_IDENTITY_ID)?.focus()}>
                    <span className="locbar-object-kind">{object.kind}</span> {object.name}
                  </button>
                </>
              )}
            </>
          ) : !onBoard && (
            <>
              <Sep />
              <span className="locbar-crumb locbar-lens" aria-current="page">{where.place}</span>
            </>
          )}
        </nav>
        {route.name === "cluster" && (
          <StationStrip cluster={route.cluster} protection={current?.protected ? (current.protectedInferred ? "protected (inferred)" : "protected") : undefined} />
        )}
      </div>
      <div className="locbar-right" style={noDrag}>
        <FleetIndicator />
        <button type="button" aria-label="Go to, run or ask" data-testid="command-bar" onClick={openCommandPalette}
          title={`Go to, run or ask (${MOD}K)`} className="locbar-well">
          <IconSearch size={14} stroke={1.6} style={{ flexShrink: 0 }} aria-hidden />
          <span className="locbar-well-text">Go to, run or ask</span>
          <kbd>{MOD}K</kbd>
        </button>
        <ForwardsIndicator />
        <AskButton cluster={route.name === "cluster" ? route.cluster : null} />
        <UtilityButton label="Activity" title="Activity - recent action outcomes" optional current={route.name === "activity"} onClick={() => useFleet.getState().openActivity()}>
          <IconHistory size={15} stroke={1.5} />
        </UtilityButton>
        <UtilityButton label="Terminal" title="Open a terminal with this fleet's kubeconfig" optional onClick={() => void openTerminal()}>
          <IconTerminal2 size={15} stroke={1.5} />
        </UtilityButton>
        <SettingsButton current={route.name === "settings"} />
      </div>
    </header>
  );
}

const Sep = () => <span className="locbar-sep" aria-hidden="true">/</span>;

// LineMark is the redesign's mark (the mockup's): two rails and the one
// route-coloured sleeper - the line, with a train on it.
function LineMark() {
  return (
    <svg width="22" height="22" viewBox="0 0 22 22" aria-hidden="true" focusable="false" style={{ display: "block", flexShrink: 0 }}>
      <path d="M1 8h20M1 14h20" fill="none" stroke="var(--line-ink-3, var(--color-text-tertiary))" strokeWidth="1.6" />
      <rect x="13" y="3" width="6" height="16" rx="1.5" fill="var(--line-route, var(--color-text-info))" />
    </svg>
  );
}

// IndexToggle shows or hides the lens index (mod+\), as a disclosure.
function IndexToggle() {
  const open = useLensIndexOpen((s) => s.open);
  return (
    <button type="button" id={LENS_INDEX_TOGGLE_ID} className="mast-icon" style={noDrag} aria-label="Lens index" aria-expanded={open} aria-controls={open ? LENS_INDEX_ID : undefined}
      title={`${open ? "Hide" : "Show"} the lens index (${MOD}\\)`} onClick={toggleLensIndex}>
      {open ? <IconLayoutSidebarLeftCollapse size={15} stroke={1.6} /> : <IconLayoutSidebarLeftExpand size={15} stroke={1.6} />}
    </button>
  );
}

// The protected cue (parity: the "prd lock" chip) is a persistent tag on
// the station crumb, never clipped; "inferred" when the environment name
// implied it.
function ProtectedTag({ c }: { c: ClusterDTO }) {
  return (
    <span className="locbar-protected" title={c.protectedInferred ? "Protected - inferred from the cluster's environment name; operations need typed confirmation" : "Protected - operations need typed confirmation"}>
      protected{c.protectedInferred && <span className="locbar-protected-why"> (inferred)</span>}
    </span>
  );
}

// Ask is a conversation bounded to one cluster: off a cluster it stays
// visible, disabled, with its reason.
function AskButton({ cluster }: { cluster: string | null }) {
  const label = cluster ? `Ask about ${cluster}` : "Ask - open a cluster first";
  return (
    <button type="button" className="locbar-ask" aria-label={label} aria-disabled={cluster ? undefined : true}
      title={cluster ? "Ask about this cluster - read-only AI chat" : "Ask needs an open cluster: a conversation is bounded to one cluster"}
      onClick={() => { if (cluster) openChat(cluster); }}>
      <IconMessageChatbot size={14} stroke={1.5} aria-hidden />
      <span>Ask</span>
    </button>
  );
}

// HistoryControls: Back / Forward (mod+[ / mod+]) and the Recent menu. They
// move between places; nothing they restore runs an action.
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
      <button type="button" className="mast-icon" aria-label={prev ? `Back to ${name(prev)}` : "Back"} title={prev ? `Back to ${name(prev)} (${MOD}[)` : `Back (${MOD}[)`} disabled={!prev} onClick={() => back()}>
        <IconArrowLeft size={15} stroke={1.6} />
      </button>
      <button type="button" className="mast-icon" aria-label={next ? `Forward to ${name(next)}` : "Forward"} title={next ? `Forward to ${name(next)} (${MOD}])` : `Forward (${MOD}])`} disabled={!next} onClick={() => forward()}>
        <IconArrowRight size={15} stroke={1.6} />
      </button>
      <RecentMenu places={recent} name={name} />
    </div>
  );
}

function RecentMenu({ places, name }: { places: HistoryEntry[]; name: (e: HistoryEntry) => string }) {
  const { open, setOpen, close, wrapRef, triggerRef, menuRef } = useMenuPopover();
  return (
    <div ref={wrapRef} className="locbar-pop">
      <button ref={triggerRef} type="button" className="mast-icon" aria-label="Recent places" title="Recent places" aria-haspopup="menu" aria-expanded={open}
        disabled={places.length === 0} onClick={() => setOpen((o) => !o)}>
        <IconClock size={15} stroke={1.6} />
      </button>
      {open && (
        <div ref={menuRef} role="menu" aria-label="Recent places" className="l-menu" style={{ top: "calc(100% + 6px)", left: 0, width: 340, maxHeight: vh(60), overflow: "auto" }}>
          <div className="l-menu-label">Recent places</div>
          {places.map((e) => (
            <button key={entryKey(e)} role="menuitem" className="l-menu-item" onClick={() => { close(); go(e); }} title={name(e)}>
              <span style={{ minWidth: 0, overflowWrap: "anywhere" }}>{name(e)}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

const utilityBtn = (current: boolean): React.CSSProperties => ({
  display: "inline-flex", alignItems: "center", gap: 4, height: 32, minWidth: 32, padding: "0 6px", borderRadius: 6, border: "none", cursor: "pointer",
  justifyContent: "center", fontFamily: "var(--font-mono)", fontSize: 11,
  color: current ? "var(--color-text-primary)" : "var(--color-text-secondary)",
  background: current ? "var(--line-sunk, var(--color-background-secondary))" : "transparent",
  boxShadow: current ? "var(--line-well-ring, inset 0 0 0 1px var(--color-border-tertiary))" : undefined,
});

// optional: a utility the console also reaches, which a narrow bar may step
// out to keep the crumbs and this station's box (slice 13a review 1)
function UtilityButton({ label, title, current = false, optional = false, onClick, children }: { label: string; title: string; current?: boolean; optional?: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" className={optional && !current ? "locbar-optional" : undefined} aria-label={label} title={title} aria-current={current ? "page" : undefined} onClick={onClick} style={utilityBtn(current)}>
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

// ForwardsIndicator is the bar's Port-forwards utility: "⇄" plus the live
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
      <UtilityButton label="Port-forwards" title="Port-forwards - none active" optional current={current} onClick={() => useFleet.getState().openForwards()}>
        <IconArrowsLeftRight size={15} stroke={1.5} />
      </UtilityButton>
    );
  }

  const anyBroken = forwards.some((f) => f.status === "broken");

  return (
    <div ref={ref} className="locbar-pop">
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
            position: "absolute", top: 34, right: 0, zIndex: 50,
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
                title={`${f.cluster} ${f.namespace}/${f.targetName} - manage in Forwards`}
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
