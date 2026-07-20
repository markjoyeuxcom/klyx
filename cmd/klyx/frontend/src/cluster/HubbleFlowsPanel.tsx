import { useEffect, useState } from "react";
import { useFleet, HubbleStatsDTO } from "../store/fleet";
import { getHubbleFlowStats } from "../bridge/gateway";
import { MetersStrip, well, btn } from "../chrome/primitives";

// HubbleFlowsPanel is the Cilium flow/drop triage: verdict rates, the drop
// reasons ranked by rate, and DNS/HTTP error rates - from Hubble's Prometheus
// metrics through the existing metrics transport. Renders ONLY when the cluster
// has Hubble (the parent gates on hasHubble); inside, every section renders
// only when its opt-in metric set actually produced series, and the all-empty
// state says HOW to enable them. Signal Bench: zero-drop cells never glow.
export function HubbleFlowsPanel({ cluster }: { cluster: string }) {
  const [stats, setStats] = useState<HubbleStatsDTO | null>(null);
  const [loading, setLoading] = useState(true);

  const fetchStats = async () => {
    setLoading(true);
    try {
      setStats(await getHubbleFlowStats(cluster));
    } catch {
      setStats(null);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void fetchStats(); }, [cluster]); // eslint-disable-line react-hooks/exhaustive-deps

  const fmt = (v: number | null | undefined, unit: string): string =>
    v === null || v === undefined ? "—" : v >= 10 ? `${Math.round(v)}${unit}` : `${v.toFixed(2)}${unit}`;

  const hasAny = stats && (stats.forwardRate !== null || stats.dropRate !== null || stats.dropReasons.length > 0 || stats.dnsTotalRate !== null || stats.httpTotalRate !== null);

  return (
    <div data-testid="hubble-flows" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 8 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>flows (hubble)</div>
        {stats?.status.updatedAt && <span style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>5m rates</span>}
        <div style={{ flex: 1 }} />
        <button onClick={() => void fetchStats()} disabled={loading} style={{ ...btn, fontSize: 10 }}>refresh</button>
      </div>

      {loading && stats === null ? (
        <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>reading hubble metrics…</div>
      ) : stats === null ? (
        <div style={{ fontSize: 11, color: "var(--color-text-danger)" }}>hubble metrics call failed</div>
      ) : !stats.status.available ? (
        // Degraded is degraded - never an empty panel.
        <div style={{ fontSize: 11, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>{stats.status.message}</div>
      ) : !hasAny ? (
        <div style={{ fontSize: 11, color: "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>{stats.status.message}</div>
      ) : (
        <>
          <MetersStrip
            note="drops first"
            meters={[
              stats.dropRate !== null && { label: "dropped/s", value: fmt(stats.dropRate, ""), tone: stats.dropRate > 0 ? ("danger" as const) : undefined },
              stats.forwardRate !== null && { label: "forwarded/s", value: fmt(stats.forwardRate, ""), tone: "success" as const },
              stats.dnsErrorRate !== null && stats.dnsErrorRate > 0 && { label: "dns errors/s", value: fmt(stats.dnsErrorRate, ""), tone: "warning" as const },
              stats.httpErrorRate !== null && stats.httpErrorRate > 0 && { label: "http 5xx/s", value: fmt(stats.httpErrorRate, ""), tone: "danger" as const },
            ]}
          />
          {stats.dropReasons.length > 0 && (
            <div style={{ display: "flex", flexDirection: "column", gap: 3 }}>
              {stats.dropReasons.slice(0, 8).map((r) => (
                <div key={`${r.reason}/${r.protocol}`} style={{ display: "flex", gap: 8, alignItems: "baseline", fontFamily: "var(--font-mono)", fontSize: 11 }}>
                  <span style={{ color: "var(--color-text-danger)", minWidth: 64, textAlign: "right" }}>{fmt(r.rate, "/s")}</span>
                  <span style={{ color: "var(--color-text-primary)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{r.reason}</span>
                  <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{r.protocol}</span>
                </div>
              ))}
              {stats.dropReasons.length > 8 && (
                <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>+{stats.dropReasons.length - 8} more reasons</span>
              )}
            </div>
          )}
        </>
      )}
    </div>
  );
}

// useHasHubble gates the panel on the cluster's detected Hubble capability.
export function useHasHubble(cluster: string): boolean {
  return useFleet((s) => s.clusters.find((c) => c.name === cluster)?.hasHubble ?? false);
}
