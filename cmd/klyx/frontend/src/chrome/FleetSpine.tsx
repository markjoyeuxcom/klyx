import { useState } from "react";
import { IconChevronRight, IconChevronLeft } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { StatusDot } from "./primitives";

// spineCodes derives a short (2-char) identifying code per cluster so the
// spine blocks are readable at a glance, not just on hover. First choice is
// the hyphen-segment initials ("homelab-blue" -> "hb", "prd-weu" -> "pw");
// when two clusters collide, those fall back to the first two letters of
// their LAST segment ("bl", "ne"); a final collision falls back to the first
// two letters of the full name. Codes are stable for a given fleet.
export function spineCodes(names: string[]): Record<string, string> {
  const initials = (n: string) => {
    const segs = n.split("-").filter(Boolean);
    if (segs.length === 1) return segs[0].slice(0, 2);
    return (segs[0][0] + segs[segs.length - 1][0]).slice(0, 2);
  };
  const lastSeg = (n: string) => {
    const segs = n.split("-").filter(Boolean);
    return segs[segs.length - 1].slice(0, 2);
  };

  const codes: Record<string, string> = {};
  for (const n of names) codes[n] = initials(n);

  const dupes = (m: Record<string, string>) => {
    const seen = new Map<string, string[]>();
    for (const [n, c] of Object.entries(m)) seen.set(c, [...(seen.get(c) ?? []), n]);
    return [...seen.values()].filter((g) => g.length > 1).flat();
  };

  for (const n of dupes(codes)) codes[n] = lastSeg(n);
  for (const n of dupes(codes)) codes[n] = n.slice(0, 2);
  return codes;
}

// FleetSpine — design principle 1 taken literally: the fleet never leaves the
// screen. A slim strip of one block per cluster, visible from every view, so
// prd-weu going amber is noticed while tailing logs on nelli. Severity comes
// from the conn state that the fleet push loop already streams; when the fleet
// board has been fetched (fleet view visited), a broken-workloads count
// upgrades the block to red. One click switches cluster from anywhere.
export function FleetSpine() {
  const clusters = useFleet((s) => s.clusters);
  const board = useFleet((s) => s.fleetBoard);
  const route = useFleet((s) => s.route);
  const openCluster = useFleet((s) => s.openCluster);
  // Width is the user's call (persisted): slim code blocks, or expanded with
  // full cluster names once the fleet outgrows two-letter codes.
  const [expanded, setExpanded] = useState<boolean>(() => {
    try { return localStorage.getItem("klyx-spine-expanded") === "1"; } catch { return false; }
  });
  const toggle = () => {
    const next = !expanded;
    setExpanded(next);
    try { localStorage.setItem("klyx-spine-expanded", next ? "1" : "0"); } catch { /* noop */ }
  };

  if (clusters.length === 0) return null;
  const selected = route.name === "cluster" ? route.cluster : null;
  const codes = spineCodes(clusters.map((c) => c.name));

  return (
    <div
      data-testid="fleet-spine"
      style={{
        width: expanded ? 118 : 38,
        flexShrink: 0,
        borderRight: "0.5px solid var(--color-border-tertiary)",
        background: "var(--color-background-secondary)",
        display: "flex",
        flexDirection: "column",
        alignItems: expanded ? "stretch" : "center",
        gap: 6,
        padding: expanded ? "10px 6px" : "10px 0",
      }}
    >
      {clusters.map((c) => {
        const unreachable = c.state === "Failed" || c.state === "Unconnected";
        const broken = (board[c.name]?.broken ?? 0) > 0;
        const isSelected = c.name === selected;

        let title = `${c.name} — ${c.state.toLowerCase()}`;
        if (unreachable) title = `${c.name} — ${c.state.toLowerCase()}${c.reason ? `: ${c.reason}` : ""}`;
        else if (broken) title = `${c.name} — ${board[c.name]!.broken} broken workload${board[c.name]!.broken === 1 ? "" : "s"}`;

        // Channel-strip lamp (Signal Bench): healthy lamps sit dim, faults
        // glow, an unreachable cluster is an unpowered socket (dashed ring).
        // This is the ONE place blink is legal - the spine is the peripheral
        // pager; a broken cluster must be findable from across the room.
        const lamp = unreachable ? (
          <span aria-hidden style={{ width: 8, height: 8, borderRadius: "50%", background: "transparent", border: "1px dashed var(--color-text-tertiary)", flexShrink: 0 }} />
        ) : (
          <StatusDot size={8} tone={broken ? "danger" : c.state === "Degraded" ? "warning" : "success"} blink={broken} />
        );
        const codeColor = unreachable ? "var(--color-text-tertiary)" : isSelected ? "var(--color-text-primary)" : "var(--color-text-secondary)";
        // Current channel is pressed into a well.
        const pressed: React.CSSProperties = isSelected
          ? { background: "var(--bench-well-bg, var(--color-background-primary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-secondary))" }
          : { background: "transparent" };

        if (expanded) {
          const brokenCount = board[c.name]?.broken ?? null;
          return (
            <button
              key={c.name}
              onClick={() => openCluster(c.name)}
              aria-label={`cluster ${c.name}`}
              aria-current={isSelected ? "true" : undefined}
              title={title}
              style={{
                display: "flex",
                alignItems: "center",
                gap: 6,
                padding: "3px 6px",
                borderRadius: 4,
                cursor: "pointer",
                border: "none",
                textAlign: "left",
                ...pressed,
              }}
            >
              {lamp}
              <span style={{
                fontFamily: "var(--font-mono)", fontSize: 10, lineHeight: 1,
                fontWeight: isSelected ? 700 : 400,
                color: codeColor,
                overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", flex: 1,
              }}>{c.name}</span>
              {brokenCount != null && brokenCount > 0 && (
                <span style={{ fontSize: 9, fontFamily: "var(--font-mono)", color: "var(--color-text-danger)", flexShrink: 0 }}>{brokenCount}</span>
              )}
            </button>
          );
        }
        return (
          <button
            key={c.name}
            onClick={() => openCluster(c.name)}
            aria-label={`cluster ${c.name}`}
            aria-current={isSelected ? "true" : undefined}
            title={title}
            style={{
              width: 34,
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              gap: 3,
              padding: "4px 0",
              borderRadius: 4,
              border: "none",
              cursor: "pointer",
              fontSize: 9,
              fontFamily: "var(--font-mono)",
              letterSpacing: "0.04em",
              lineHeight: 1,
              color: codeColor,
              fontWeight: isSelected ? 700 : 400,
              ...pressed,
            }}
          >
            {lamp}
            {codes[c.name]}
          </button>
        );
      })}
      <button
        onClick={toggle}
        aria-label={expanded ? "collapse fleet spine" : "expand fleet spine"}
        title={expanded ? "collapse" : "expand"}
        style={{
          marginTop: "auto",
          alignSelf: "center",
          width: 20, height: 20,
          padding: 0, border: "none", background: "transparent",
          cursor: "pointer", color: "var(--color-text-tertiary)",
          display: "flex", alignItems: "center", justifyContent: "center",
        }}
      >
        {expanded ? <IconChevronLeft size={13} stroke={1.5} /> : <IconChevronRight size={13} stroke={1.5} />}
      </button>
    </div>
  );
}
