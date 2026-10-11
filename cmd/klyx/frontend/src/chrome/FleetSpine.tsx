import { asOfText } from "../health/verdict";
import { useState } from "react";
import { IconChevronRight, IconChevronLeft } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
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

  // Names that differ only in middle segments (aks-dev-weu-platform-01 vs
  // aks-prd-neu-platform-01) collide on first+last initials; the segments
  // they all share carry no identity, so the initials of what differs do.
  const colliding = dupes(codes);
  if (colliding.length > 0) {
    const split = colliding.map((n) => n.split(/[-_.:/]+/).filter(Boolean));
    const common = split.reduce((acc, segs) => acc.filter((x) => segs.includes(x)), split[0]);
    colliding.forEach((n, i) => {
      const rest = split[i].filter((x) => !common.includes(x));
      if (rest.length > 0) codes[n] = rest.slice(0, 3).map((x) => x[0]).join("");
    });
  }
  for (const n of dupes(codes)) codes[n] = lastSeg(n);
  for (const n of dupes(codes)) codes[n] = n.slice(0, 2);
  return codes;
}

// Environment grouping (redesign step 7b): six similar AKS names read best
// grouped dev → test → stg → prd, each group in fleet order. Unknown
// environments follow alphabetically, then clusters with none. With no
// environment tags, or one environment for all, the spine stays one flat list.
const ENV_RANK: [RegExp, number][] = [
  [/^(dev|develop|development)$/, 0],
  [/^(test|testing|qa|int|integration|uat)$/, 1],
  [/^(stg|stage|staging|preprod|pre-prod)$/, 2],
  [/^(prd|prod|production)$/, 3],
];
const envRank = (env: string) => ENV_RANK.find(([re]) => re.test(env))?.[1] ?? 4;

export type SpineGroup = { env: string | null; clusters: ClusterDTO[] };

export function spineGroups(clusters: ClusterDTO[]): SpineGroup[] {
  if (!clusters.some((c) => c.env)) return [{ env: null, clusters }];
  const by = new Map<string, ClusterDTO[]>();
  for (const c of clusters) {
    const env = (c.env ?? "").trim().toLowerCase();
    by.set(env, [...(by.get(env) ?? []), c]);
  }
  // One environment for the whole fleet is no grouping: no label.
  if (by.size === 1) return [{ env: null, clusters }];
  return [...by.entries()]
    .sort(([a], [b]) => (a === "" ? 1 : b === "" ? -1 : envRank(a) - envRank(b) || a.localeCompare(b)))
    .map(([env, cs]) => ({ env: env || "other", clusters: cs }));
}

// spineOrder is the order the spine shows - the order mod+1..9 follows.
export function spineOrder(clusters: ClusterDTO[]): ClusterDTO[] {
  return spineGroups(clusters).flatMap((g) => g.clusters);
}

const EXPANDED_KEY = "klyx.layout.v1.spineExpanded";
const LEGACY_EXPANDED_KEY = "klyx-spine-expanded";

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
    try {
      const v = localStorage.getItem(EXPANDED_KEY) ?? localStorage.getItem(LEGACY_EXPANDED_KEY);
      return v === "1";
    } catch { return false; }
  });
  const toggle = () => {
    const next = !expanded;
    setExpanded(next);
    try { localStorage.setItem(EXPANDED_KEY, next ? "1" : "0"); localStorage.removeItem(LEGACY_EXPANDED_KEY); } catch { /* noop */ }
  };

  if (clusters.length === 0) return null;
  const selected = route.name === "cluster" ? route.cluster : null;
  const codes = spineCodes(clusters.map((c) => c.name));

  const renderCluster = (c: ClusterDTO) => {
    const unreachable = c.state === "Failed" || c.state === "Unconnected";
    // One lamp, two axes kept honest: workload attention lights it
    // (fault blinks, degraded glows amber); otherwise connection speaks
    // (stale/notes amber). Green only with complete workload evidence.
    const w = board[c.name]?.workloads ?? null;
    const attention = w?.counts.attention ?? 0;
    const broken = (w?.counts.fault ?? 0) > 0;
    const isSelected = c.name === selected;

    let title = `${c.name} — ${c.state.toLowerCase()}`;
    if (unreachable) title = `${c.name} — ${c.state.toLowerCase()}${c.reason ? `: ${c.reason}` : ""}`;
    else if (attention > 0) title = `${c.name} — ${attention}${w!.coverage === "partial" ? "+" : ""} workload${attention === 1 ? "" : "s"} need attention (${w!.counts.fault} fault, ${w!.counts.degraded} degraded${w!.coverage === "partial" ? "; capped read" : ""})`;
    else if (w && (w.coverage !== "complete" || w.counts.unknown > 0)) title = `${c.name} — workload health unverified${w.counts.unknown ? ` (${w.counts.unknown} unknown)` : " (capped read)"}`;
    else if (!w && !unreachable) title = `${c.name} — workload health unreadable`;
    else if (c.state === "Stale") title = `${c.name} — connection stale${c.reason ? `: ${c.reason}` : ""}`;

    if (w?.asOf && !unreachable) title += ` · ${asOfText(w)}`;

    // Channel-strip lamp (Signal Bench): healthy lamps sit dim, faults
    // glow, an unreachable cluster is an unpowered socket (dashed ring).
    // This is the ONE place blink is legal - the spine is the peripheral
    // pager; a broken cluster must be findable from across the room.
    const lamp = unreachable ? (
      <span aria-hidden style={{ width: 8, height: 8, borderRadius: "50%", background: "transparent", border: "1px dashed var(--color-text-tertiary)", flexShrink: 0 }} />
    ) : (
      <StatusDot
        size={8}
        tone={broken ? "danger" : attention > 0 ? "warning" : c.state === "Degraded" || c.state === "Stale" ? "warning" : w?.coverage === "complete" && w.counts.unknown === 0 && c.state === "Synced" ? "success" : "muted"}
        blink={broken}
      />
    );
    const codeColor = unreachable ? "var(--color-text-tertiary)" : isSelected ? "var(--color-text-primary)" : "var(--color-text-secondary)";
    // Current channel is pressed into a well.
    const pressed: React.CSSProperties = isSelected
      ? { background: "var(--bench-well-bg, var(--color-background-primary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-secondary))" }
      : { background: "transparent" };

    if (expanded) {
      const brokenCount = w ? attention : null;
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
            fontFamily: "var(--font-mono)", fontSize: 11, lineHeight: "14px",
            fontWeight: isSelected ? 700 : 400,
            color: codeColor, minWidth: 0, flex: 1,
            // Full names, up to two lines (spec §1): six similar names
            // must stay distinguishable without hovering.
            overflowWrap: "anywhere", display: "-webkit-box", WebkitLineClamp: 2, WebkitBoxOrient: "vertical", overflow: "hidden",
          }}>{c.name}</span>
          {brokenCount != null && brokenCount > 0 && (
            <span style={{ fontSize: 9, fontFamily: "var(--font-mono)", color: broken ? "var(--color-text-danger)" : "var(--color-text-warning)", flexShrink: 0 }}>{brokenCount}</span>
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
          width: 36,
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
  };

  return (
    <div
      data-testid="fleet-spine"
      data-list-keys="ignore"
      style={{
        width: expanded ? "var(--spine-expanded-width)" : "var(--spine-width)",
        boxSizing: "border-box",
        flexShrink: 0,
        // Channel-strip module on the desk (Signal Bench).
        border: "var(--border-hairline) solid var(--color-border-tertiary)",
        borderTop: "1px solid var(--bench-edge-hi, var(--color-border-tertiary))",
        borderRadius: 6,
        boxShadow: "var(--bench-module-shadow, none)",
        background: "var(--color-background-primary)",
        display: "flex",
        flexDirection: "column",
        alignItems: expanded ? "stretch" : "center",
        gap: 4,
        padding: expanded ? "4px 6px 8px" : "4px 0 8px",
        overflowY: "auto",
      }}
    >
      {/* The spine header is the Fleet destination (spec §1) - the one place
          Fleet lives now that the nav band carries no duplicate. */}
      <button
        type="button"
        onClick={() => useFleet.getState().openFleet()}
        aria-current={route.name === "fleet" ? "page" : undefined}
        title="Fleet board"
        style={{
          height: 24, flexShrink: 0, alignSelf: "stretch", margin: expanded ? 0 : "0 4px", padding: 0, border: "none", borderRadius: 4, cursor: "pointer",
          fontFamily: "var(--font-sans)", fontSize: 11, fontWeight: 600,
          color: route.name === "fleet" ? "var(--color-text-primary)" : "var(--color-text-secondary)",
          background: route.name === "fleet" ? "var(--bench-well-bg, var(--color-background-secondary))" : "transparent",
          boxShadow: route.name === "fleet" ? "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-secondary))" : undefined,
        }}
      >
        Fleet
      </button>
      {spineGroups(clusters).map((g) => (
        <div key={g.env ?? "all"} role="group" aria-label={g.env ? `${g.env} clusters` : "clusters"} style={{ display: "flex", flexDirection: "column", alignItems: expanded ? "stretch" : "center", gap: 4 }}>
          {g.env && (
            <span aria-hidden="true" className={expanded ? "t-section-label" : "t-legend"} style={{ padding: expanded ? "6px 6px 0" : "6px 0 0", textAlign: expanded ? "left" : "center" }}>
              {expanded ? g.env : g.env.slice(0, 4)}
            </span>
          )}
          {g.clusters.map((c) => renderCluster(c))}
        </div>
      ))}
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
