import { useFleet, SECTION_LABELS } from "../store/fleet";
import { Breadcrumb } from "./Breadcrumb";

// Header is the per-view content header, below the full-width TopBar. The
// breadcrumb sits directly above the title so both share the content column's
// left edge (aligned with the body). The theme toggle lives in the TopBar.
export function Header() {
  const route = useFleet((s) => s.route);
  const clusters = useFleet((s) => s.clusters);
  const regions = new Set(clusters.map((c) => c.region).filter(Boolean));
  const cluster = route.name === "cluster" ? clusters.find((c) => c.name === route.cluster) : null;
  const title = route.name === "fleet" ? "Fleet" : route.name === "forwards" ? "Port-forwards" : route.name === "activity" ? "Activity" : route.name === "settings" ? "Settings" : SECTION_LABELS[route.section];

  return (
    <div style={{ padding: "11px 16px", borderBottom: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)" }}>
      <div style={{ display: "flex", alignItems: "flex-end", justifyContent: "space-between", gap: 16 }}>
        <div style={{ minWidth: 0 }}>
          <Breadcrumb />
          <div style={{ display: "flex", alignItems: "center", gap: 10, marginTop: 5, minWidth: 0 }}>
            <div style={{ fontSize: 18, fontWeight: 500, lineHeight: 1.2 }}>{title}</div>
            {cluster?.protected && (
              <span style={{
                border: "0.5px solid var(--color-border-warning)",
                background: "var(--color-background-warning)",
                color: "var(--color-text-warning)",
                fontFamily: "var(--font-mono)",
                fontSize: 10,
                padding: "2px 6px",
              }}>
                prd lock
              </span>
            )}
          </div>
        </div>
        {route.name === "fleet" && clusters.length > 0 && (
          <span style={{ background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", color: "var(--color-text-secondary)", fontSize: 11, padding: "2px 8px" }}>
            {clusters.length} cluster{clusters.length === 1 ? "" : "s"}
            {regions.size > 0 ? ` · ${regions.size} region${regions.size === 1 ? "" : "s"}` : ""}
          </span>
        )}
        {cluster && (
          <div style={{ display: "flex", alignItems: "center", gap: 12, color: "var(--color-text-tertiary)", fontFamily: "var(--font-mono)", fontSize: 11, flexShrink: 0 }}>
            <span>{cluster.provider || "cluster"}</span>
            {cluster.region && <span>{cluster.region}</span>}
            <span>{cluster.nodesReady}/{cluster.nodesTotal} nodes</span>
            <span>{cluster.pods} pods</span>
          </div>
        )}
      </div>
    </div>
  );
}
