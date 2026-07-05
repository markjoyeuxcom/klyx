import { useState, useRef, useEffect } from "react";
import { IconCommand } from "@tabler/icons-react";
import { ThemeToggle } from "./ThemeToggle";
import { KlyxMark } from "./KlyxMark";
import { useFleet, SECTION_LABELS } from "../store/fleet";
import type { Route } from "../store/fleet";
import { StatusDot } from "./primitives";
import { clusterDisplayName } from "../fleet/ClusterCard";
import { stopAllForwards } from "../bridge/forwards";
import { openCommandPalette } from "./CommandPalette";

// Wails v3 window-drag regions: the bar is draggable, interactive children opt out.
const drag = { "--wails-draggable": "drag" } as React.CSSProperties;
const noDrag = { "--wails-draggable": "no-drag" } as React.CSSProperties;

// TopBar is the full-width title bar: macOS traffic lights sit on its left (the
// padding clears them), the forwards indicator + theme toggle on its right, and
// the empty middle drags the window. The breadcrumb lives in the content Header.
// mastTitleFor renders the route as the mast readout - same shape as the OS
// window title ("cluster / lens").
function mastTitleFor(route: Route): { head: string; path: string } {
  if (route.name === "cluster") {
    const lens = route.resource ? route.resource.plural : SECTION_LABELS[route.section]?.toLowerCase() ?? route.section;
    return { head: clusterDisplayName(route.cluster), path: lens };
  }
  if (route.name === "fleet") return { head: "fleet", path: "" };
  return { head: route.name, path: "" };
}

export function TopBar() {
  const clusters = useFleet((s) => s.clusters);
  const route = useFleet((s) => s.route);
  const mastTitle = mastTitleFor(route);
  const currentProtected = route.name === "cluster" && (clusters.find((c) => c.name === route.cluster)?.protected ?? false);
  const liveClusters = clusters.filter((c) => c.state !== "Failed" && c.state !== "Unconnected").length;
  return (
    <div
      style={{
        ...drag,
        display: "grid",
        gridTemplateColumns: "180px minmax(220px, 1fr) auto",
        alignItems: "center",
        height: 40,
        flexShrink: 0,
        paddingLeft: 84,
        paddingRight: 12,
        // Mast module on the desk (Signal Bench) - machined, not joined.
        background: "var(--color-background-primary)",
        border: "var(--border-hairline) solid var(--color-border-tertiary)",
        borderTop: "1px solid var(--bench-edge-hi, var(--color-border-tertiary))",
        borderRadius: 6,
        boxShadow: "var(--bench-module-shadow, none)",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 10, minWidth: 0 }}>
        <KlyxMark size={20} />
        {/* Mast title (Signal Bench): mono "context / lens" readout. */}
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 13, fontWeight: 700, ...noDrag, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {mastTitle.head}
          {mastTitle.path && <span style={{ color: "var(--color-text-tertiary)", fontWeight: 400 }}> / {mastTitle.path}</span>}
        </span>
        {/* The prd-lock cue moved here from the retired Header - the mast is
            the one always-visible place. */}
        {currentProtected && (
          <span style={{ ...noDrag, fontFamily: "var(--font-mono)", fontSize: 9, padding: "2px 7px", borderRadius: 3, whiteSpace: "nowrap", color: "var(--color-text-warning)", background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
            prd lock
          </span>
        )}
      </div>
      <div style={{ display: "flex", alignItems: "center", gap: 7, justifySelf: "start", fontSize: 9, letterSpacing: "0.12em", textTransform: "uppercase", color: "var(--color-text-tertiary)" }}>
        {clusters.length > 0 && (
          <>
            <StatusDot size={8} tone={liveClusters === clusters.length ? "success" : liveClusters === 0 ? "danger" : "warning"} />
            live · {liveClusters}/{clusters.length} watching
          </>
        )}
      </div>
      <div style={{ ...noDrag, display: "flex", alignItems: "center", gap: 8, justifySelf: "end" }}>
        <button
          type="button"
          aria-label="open command palette"
          data-testid="command-bar"
          onClick={openCommandPalette}
          title="open command palette with cmd+k or ctrl+k"
          style={{
            display: "flex",
            alignItems: "center",
            gap: 10,
            minWidth: 230,
            padding: "5px 12px",
            fontSize: 11,
            color: "var(--color-text-secondary)",
            cursor: "pointer",
            textAlign: "left",
            border: "none",
            borderRadius: "var(--radius-sm)",
            background: "var(--bench-well-bg, var(--color-background-secondary))",
            boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))",
          }}
        >
          <span style={{ display: "inline-flex", alignItems: "center", gap: 6, flex: 1 }}>
            <IconCommand size={13} stroke={1.5} />
            jump or run a command
          </span>
          <kbd style={{ fontFamily: "var(--font-mono)", fontSize: 10, color: "var(--color-text-tertiary)" }}>⌘K</kbd>
        </button>
        <ForwardsIndicator />
        <ThemeToggle />
      </div>
    </div>
  );
}

// ForwardsIndicator shows a compact "⇄ N" chip when any port-forwards exist.
// Clicking it opens a glanceable dropdown: one compact line per forward, click
// any line to jump to the Forwards section where the real management lives.
// "stop all" stays here as the emergency brake. Broken rows render
// warning-coloured.
function ForwardsIndicator() {
  const forwards = useFleet((s) => s.forwards);
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

  if (forwards.length === 0) return null;

  const anyBroken = forwards.some((f) => f.status === "broken");

  return (
    <div ref={ref} style={{ position: "relative" }}>
      <button
        onClick={() => setOpen((o) => !o)}
        aria-label="active port-forwards"
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
