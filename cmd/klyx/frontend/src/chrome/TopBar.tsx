import { useState, useRef, useEffect } from "react";
import { IconCommand } from "@tabler/icons-react";
import { ThemeToggle } from "./ThemeToggle";
import { KlyxMark } from "./KlyxMark";
import { useFleet } from "../store/fleet";
import { stopAllForwards } from "../bridge/forwards";
import { openCommandPalette } from "./CommandPalette";

// Wails v3 window-drag regions: the bar is draggable, interactive children opt out.
const drag = { "--wails-draggable": "drag" } as React.CSSProperties;
const noDrag = { "--wails-draggable": "no-drag" } as React.CSSProperties;

// TopBar is the full-width title bar: macOS traffic lights sit on its left (the
// padding clears them), the forwards indicator + theme toggle on its right, and
// the empty middle drags the window. The breadcrumb lives in the content Header.
export function TopBar() {
  const clusters = useFleet((s) => s.clusters);
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
      <div style={{ display: "flex", alignItems: "center", gap: 8, color: "var(--color-text-primary)", fontWeight: 500, fontSize: 13 }}>
        <KlyxMark size={20} />
        Klyx
      </div>
      <button
        type="button"
        aria-label="open command palette"
        data-testid="command-bar"
        onClick={openCommandPalette}
        title="open command palette with cmd+k or ctrl+k"
        style={{
          ...noDrag,
          justifySelf: "start",
          width: "min(500px, 42vw)",
          height: 26,
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 10,
          padding: "0 9px",
          border: "0.5px solid var(--color-border-tertiary)",
          background: "var(--color-background-primary)",
          color: "var(--color-text-tertiary)",
          fontFamily: "var(--font-mono)",
          fontSize: 11,
          cursor: "pointer",
          textAlign: "left",
        }}
      >
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
          <IconCommand size={13} stroke={1.5} />
          jump to resource, namespace, action
        </span>
        <span>cmd/ctrl+k</span>
      </button>
      <div style={{ ...noDrag, display: "flex", alignItems: "center", gap: 8, justifySelf: "end" }}>
        {/* Reserve the slot even before the fleet seeds - the chip popping in
            after load shifted the whole right-side chrome. */}
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: "var(--color-text-tertiary)", minWidth: 130, textAlign: "right" }}>
          {clusters.length > 0 ? `${liveClusters}/${clusters.length} clusters watched` : " "}
        </span>
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
