import { useState } from "react";
import { IconChevronRight, IconChevronLeft } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { StatusDot } from "./primitives";
import { spineLamp, useFleetAttention } from "../nav/fleetAttention";
import { mastReading } from "../nav/clusterAttention";

// spineCodes derives a short identifying code per cluster so the spine
// blocks are readable at a glance, not just on hover. Codes are unique and
// stable for a given fleet:
//   1. first+last segment initials ("homelab-blue" -> "hb", "prd-weu" -> "pw");
//   2. within a group of names that collide, the initials of the segments
//      they do not share ("aks-dev-weu-platform-01" -> "dw");
//   3. still colliding: two-letter prefixes of those segments ("homelab-blue"
//      vs "homelab-bee" -> "bl" / "be", "aks-dev-weu-…" vs "aks-dev-wus-…" ->
//      "we" / "wu");
//   4. anything left gets a numbered suffix in fleet order ("ak1", "ak2").
export function spineCodes(names: string[]): Record<string, string> {
  const segsOf = (n: string) => n.split(/[-_.:/]+/).filter(Boolean);
  const initials = (n: string) => {
    const segs = segsOf(n);
    if (segs.length <= 1) return (segs[0] ?? n).slice(0, 2);
    return segs[0][0] + segs[segs.length - 1][0];
  };
  const codes: Record<string, string> = {};
  for (const n of names) codes[n] = initials(n);

  const collisions = (): string[][] => {
    const by = new Map<string, string[]>();
    for (const n of names) by.set(codes[n], [...(by.get(codes[n]) ?? []), n]);
    return [...by.values()].filter((g) => g.length > 1);
  };
  const distinguish = (shorten: (rest: string[]) => string) => {
    for (const group of collisions()) {
      const split = group.map(segsOf);
      const common = split.reduce((acc, segs) => acc.filter((x) => segs.includes(x)), split[0]);
      group.forEach((n, i) => {
        const rest = split[i].filter((x) => !common.includes(x));
        if (rest.length > 0) codes[n] = shorten(rest);
      });
    }
  };
  distinguish((rest) => rest.slice(0, 3).map((x) => x[0]).join(""));
  distinguish((rest) => rest.map((x) => x.slice(0, 2)).join("").slice(0, 4));
  for (const group of collisions()) {
    const taken = new Set(Object.values(codes));
    group.forEach((n, i) => {
      if (i === 0) return;
      let k = i + 1;
      while (taken.has(`${codes[n].slice(0, 3)}${k}`)) k++;
      codes[n] = `${codes[n].slice(0, 3)}${k}`;
      taken.add(codes[n]);
    });
  }
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
  const models = useFleetAttention();
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
    // One lamp, two axes kept honest (health 1c slice 5 §2): the first
    // matching rule sets it - unreachable, a danger or warning area, the
    // connection, a transition, a gap - and the title keeps both the
    // connection and the health reading. Never quiet without a full read.
    const m = models[c.name];
    const l = m ? spineLamp(c, m) : { kind: "connecting" as const, tone: "muted" as const, blink: false, title: `${c.name} — ${c.state.toLowerCase()}` };
    const unreachable = l.kind === "unreachable";
    const areas = m && !m.ended ? mastReading(m).areas : 0;
    const isSelected = c.name === selected;
    const title = l.title;

    // Channel-strip lamp (Signal Bench): healthy lamps sit dim, faults
    // glow, an unreachable cluster is an unpowered socket (dashed ring).
    // This is the ONE place blink is legal - the spine is the peripheral
    // pager; a broken cluster must be findable from across the room.
    const lamp = unreachable ? (
      <span aria-hidden style={{ width: 8, height: 8, borderRadius: "50%", background: "transparent", border: "1px dashed var(--color-text-tertiary)", flexShrink: 0 }} />
    ) : (
      <StatusDot size={8} tone={l.tone} blink={l.blink} />
    );
    const codeColor = unreachable ? "var(--color-text-tertiary)" : isSelected ? "var(--color-text-primary)" : "var(--color-text-secondary)";
    // Current channel is pressed into a well.
    const pressed: React.CSSProperties = isSelected
      ? { background: "var(--bench-well-bg, var(--color-background-primary))", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-secondary))" }
      : { background: "transparent" };

    if (expanded) {
      // the number of AREAS needing attention, never objects
      const brokenCount = l.kind === "danger" || l.kind === "warning" ? areas : null;
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
            <span style={{ fontSize: 9, fontFamily: "var(--font-mono)", color: l.kind === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)", flexShrink: 0 }}>{brokenCount}</span>
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
