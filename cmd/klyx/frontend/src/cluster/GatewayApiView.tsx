import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { useFleet } from "../store/fleet";
import type { GatewayRefDTO } from "../store/fleet";
import { listGateways } from "../bridge/gateway";
import { GatewayTopology } from "./GatewayTopology";
import { useListKeys } from "../chrome/useListKeys";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { IconRoute } from "@tabler/icons-react";
import { inputWell, well, ellipsis, MetersStrip, FilterButton } from "../chrome/primitives";
import { SyncingChip, staleStyle } from "./staleness";

const empty: CSSProperties = { padding: 24, color: "var(--color-text-secondary)", fontSize: 13 };

type GatewayMode = "all" | "pending" | "programmed";

function isPending(g: GatewayRefDTO): boolean {
  return !g.accepted || !g.programmed;
}

function gatewayStatus(g: GatewayRefDTO): string {
  if (!g.accepted) return "not accepted";
  if (!g.programmed) return "pending";
  return "programmed";
}

function gatewayAddress(g: GatewayRefDTO): string {
  const values = (g.addresses ?? []).map((a) => a.value).filter(Boolean);
  return values.length > 0 ? values.join(", ") : "—";
}

function gatewayListeners(g: GatewayRefDTO): string {
  const listeners = g.listeners ?? [];
  if (listeners.length === 0) return "—";
  return listeners.map((l) => {
    const proto = l.protocol || "listener";
    const port = l.port > 0 ? `:${l.port}` : "";
    return `${l.name ? `${l.name} ` : ""}${proto}${port}`;
  }).join(", ");
}

export function GatewayApiView({ cluster }: { cluster: string }) {
  const route = useFleet((s) => s.route);
  const net = useFleet((s) => s.network);
  const openGateway = useFleet((s) => s.openGateway);
  const [mode, setMode] = useState<GatewayMode>("all");
  const [query, setQuery] = useState("");
  const [kbKey, setKbKey] = useState<string | null>(null);
  const searchRef = useRef<HTMLInputElement>(null);

  const gateway = route.name === "cluster" ? route.gateway : undefined;

  useEffect(() => {
    if (!gateway) listGateways(cluster).catch((e) => console.error("listGateways", e));
  }, [cluster, gateway]);

  // Until the slice belongs to this cluster, render the loading state rather
  // than another cluster's gateways (one frame before the effect's fetch lands).
  const sameCluster = net.cluster === cluster;
  const gateways = sameCluster ? net.gateways : [];
  const listLoading = !sameCluster || net.listLoading;
  const served = sameCluster && net.served;

  const orderedGateways = useMemo(
    () => [...gateways].sort((a, b) => Number(isPending(b)) - Number(isPending(a)) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name)),
    [gateways],
  );
  const q = query.trim().toLowerCase();
  const visibleGateways = orderedGateways.filter((g) => {
    if (mode === "pending" && !isPending(g)) return false;
    if (mode === "programmed" && isPending(g)) return false;
    if (!q) return true;
    return [g.namespace, g.name, g.className, gatewayStatus(g), gatewayAddress(g), gatewayListeners(g)].some((v) => v.toLowerCase().includes(q));
  });
  const pending = gateways.filter(isPending).length;
  const programmed = gateways.length - pending;
  const classes = new Set(gateways.map((g) => g.className).filter(Boolean)).size;

  // Keyboard parity with the triage views: j/k moves a key-stable selection,
  // Enter opens the topology, / focuses the filter.
  const kbIdx = kbKey === null ? -1 : visibleGateways.findIndex((g) => `${g.namespace}/${g.name}` === kbKey);
  const handleKbSelect = useCallback((idx: number) => {
    const g = visibleGateways[idx];
    if (g) setKbKey(`${g.namespace}/${g.name}`);
  }, [visibleGateways]);
  const handleKbActivate = useCallback((idx: number) => {
    const g = visibleGateways[idx];
    if (g) openGateway(g.namespace, g.name);
  }, [visibleGateways, openGateway]);
  useListKeys({
    count: gateway ? 0 : visibleGateways.length, // suspend list nav inside the topology
    selected: kbIdx,
    onSelect: handleKbSelect,
    onActivate: handleKbActivate,
    searchRef,
  });

  if (gateway) return <GatewayTopology cluster={cluster} gateway={gateway} />;

  if (listLoading && gateways.length === 0) return <div style={{ padding: "14px 16px" }}><SkeletonRows rows={5} label="loading gateways" /></div>;
  // A failed list on a served API is an error, never "no Gateways".
  if (sameCluster && net.listError) {
    return <div style={{ ...empty, color: "var(--color-text-danger)" }}>Could not list Gateways: {net.listError}</div>;
  }
  if (!served) return <div style={empty}>Gateway API is not installed on this cluster.</div>;
  if (gateways.length === 0) {
    return (
      <EmptyState
        icon={<IconRoute size={28} stroke={1.2} />}
        title="No Gateways on this cluster."
        hint="Gateway API is served but no Gateway objects exist yet."
      />
    );
  }

  return (
    <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box" }}>
      <div style={{ display: "flex", justifyContent: "space-between", gap: 16, alignItems: "flex-start" }}>
        <div>
          <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 3 }}>Gateway topology</div>
          <div style={{ fontSize: 18, fontWeight: 600 }}>Gateway API</div>
        </div>
        <span style={{ fontFamily: "var(--font-mono)", color: pending > 0 ? "var(--color-text-warning)" : "var(--color-text-success)", fontSize: 12 }}>
          {pending > 0 ? `${pending} pending` : "all programmed"}
        </span>
      </div>

      <MetersStrip
        note="broken paths first"
        meters={[
          { label: "gateways", value: gateways.length },
          { label: "programmed", value: programmed, tone: "success" as const },
          pending > 0 && { label: "pending", value: pending, tone: "warning" as const },
          { label: "classes", value: classes },
        ]}
      />

      <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center" }}>
        <FilterButton label="all" count={gateways.length} active={mode === "all"} onClick={() => setMode("all")} />
        <FilterButton label="pending" count={pending} active={mode === "pending"} onClick={() => setMode("pending")} />
        <FilterButton label="programmed" count={programmed} active={mode === "programmed"} onClick={() => setMode("programmed")} />
        <input
          ref={searchRef}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="filter gateways"
          aria-label="filter gateways"
          style={{ ...inputWell, marginLeft: "auto", width: 180 }}
        />
        {visibleGateways.length !== gateways.length && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleGateways.length} of {gateways.length}</span>}
      </div>

      <div style={{ ...well, overflow: "hidden", minHeight: 0, display: "flex", flexDirection: "column", flex: 1 }}>
        <div style={{ display: "grid", gridTemplateColumns: "3px minmax(160px,1fr) 150px minmax(140px,0.8fr) minmax(120px,0.7fr) 100px 100px", gap: 10, alignItems: "center", padding: "5px 12px", borderBottom: "0.5px solid var(--color-border-tertiary)", fontSize: 10, color: "var(--color-text-tertiary)", flexShrink: 0 }}>
          <span />
          <span>gateway</span>
          <span>class</span>
          <span>address</span>
          <span>listeners</span>
          <span>accepted</span>
          <span>programmed</span>
        </div>
        <div data-testid="gateway-list-scroll" style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
          {visibleGateways.length === 0 ? (
            <EmptyState
              title="No Gateways match the current filter."
              action={{ label: "Clear filters", onClick: () => { setMode("all"); setQuery(""); } }}
            />
          ) : visibleGateways.map((g) => {
            const pendingGateway = isPending(g);
            const isKb = kbIdx >= 0 && kbKey === `${g.namespace}/${g.name}`;
            return (
              <button
                key={`${g.namespace}/${g.name}`}
                aria-selected={isKb}
                ref={isKb ? (el) => el?.scrollIntoView?.({ block: "nearest" }) : undefined}
                onClick={() => openGateway(g.namespace, g.name)}
                style={{
                  width: "100%",
                  display: "grid",
                  gridTemplateColumns: "3px minmax(160px,1fr) 150px minmax(140px,0.8fr) minmax(120px,0.7fr) 100px 100px",
                  gap: 10,
                  alignItems: "center",
                  padding: "8px 12px",
                  border: 0,
                  borderTop: "0.5px solid var(--color-border-tertiary)",
                  background: isKb ? "var(--color-background-secondary)" : "transparent",
                  boxShadow: isKb ? "inset 2px 0 0 var(--bench-selection-edge, var(--color-text-info))" : undefined,
                  color: "var(--color-text-primary)",
                  fontSize: 12,
                  cursor: "pointer",
                  textAlign: "left",
                  ...staleStyle(g.stale),
                }}
              >
                <span style={{ alignSelf: "stretch", background: pendingGateway ? "var(--color-text-warning)" : "transparent" }} />
                <span style={{ fontFamily: "var(--font-mono)", ...ellipsis }}>
                  <span style={{ color: "var(--color-text-tertiary)" }}>{g.namespace}/</span>
                  <span>{g.name}</span>
                </span>
                <span style={{ color: "var(--color-text-secondary)", fontSize: 11, ...ellipsis }} title={g.className}>{g.className || "—"}</span>
                <span style={{ color: gatewayAddress(g) === "—" ? "var(--color-text-tertiary)" : "var(--color-text-primary)", fontFamily: "var(--font-mono)", fontSize: 11, ...ellipsis }} title={gatewayAddress(g)}>{gatewayAddress(g)}</span>
                <span style={{ color: "var(--color-text-secondary)", fontFamily: "var(--font-mono)", fontSize: 11, ...ellipsis }} title={gatewayListeners(g)}>{gatewayListeners(g)}</span>
                <span style={{ fontSize: 11, color: g.accepted ? "var(--color-text-success)" : "var(--color-text-danger)" }}>{g.accepted ? "accepted" : "not accepted"}</span>
                {g.stale ? (
                  <SyncingChip generation={g.generation} observedGeneration={g.observedGeneration} />
                ) : (
                  <span style={{ fontSize: 11, color: g.programmed ? "var(--color-text-success)" : "var(--color-text-warning)" }}>{g.programmed ? "programmed" : "pending"}</span>
                )}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}

// SummaryCell + filter pills now come from the shared primitive set.
