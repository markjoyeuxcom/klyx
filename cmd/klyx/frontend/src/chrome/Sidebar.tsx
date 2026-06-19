import { useEffect, useState } from "react";
import {
  IconLayoutGrid,
  IconArrowsLeftRight, IconLayoutDashboard, IconStack2, IconGitBranch, IconGitMerge,
  IconRoute, IconTerminal2, IconSettings, IconBox, IconCircleDot,
  IconAlertTriangle, IconServer, IconAnchor, IconChevronRight, IconChevronLeft,
  IconComponents, IconCertificate,
} from "@tabler/icons-react";
import { useFleet, SECTION_LABELS } from "../store/fleet";
import type { ClusterDTO, ClusterSection, FleetBoardEntry, MeshGraphDTO, MetricsDTO } from "../store/fleet";
import { openTerminal } from "../bridge/configsvc";
import { getClusterMetrics } from "../bridge/metrics";
import { BUILTIN_CATALOG } from "../cluster/builtins";
import { KlyxMark } from "./KlyxMark";

const COLLAPSED_WIDTH = 46;
const EXPANDED_WIDTH = 190;

// Grouped triage-first order (design principle 2: GitOps in the top five).
// Each inner array is a visual group separated by a thin divider.
const SECTION_GROUPS: { section: ClusterSection; Icon: typeof IconLayoutDashboard }[][] = [
  [
    { section: "overview",  Icon: IconLayoutDashboard },
  ],
  [
    { section: "workloads", Icon: IconBox },
    { section: "pods",      Icon: IconCircleDot },
    { section: "nodes",     Icon: IconServer },
    { section: "events",    Icon: IconAlertTriangle },
  ],
  [
    { section: "gitops",    Icon: IconGitBranch },
    { section: "argo",      Icon: IconGitMerge },
    { section: "helm",      Icon: IconAnchor },
    { section: "certs",     Icon: IconCertificate },
  ],
  [
    { section: "network",   Icon: IconRoute },
  ],
  [
    { section: "resources", Icon: IconStack2 },
    { section: "crds",      Icon: IconComponents },
  ],
];

function readPersistedExpanded(): boolean {
  try { return localStorage.getItem("klyx-sidebar-expanded") === "1"; } catch { return false; }
}

export function Sidebar() {
  const route = useFleet((s) => s.route);
  const openFleet = useFleet((s) => s.openFleet);
  const openForwards = useFleet((s) => s.openForwards);
  const forwardsCount = useFleet((s) => s.forwards.length);
  const openSettings = useFleet((s) => s.openSettings);
  const newContexts = useFleet((s) => s.newContexts);
  const setSection = useFleet((s) => s.setSection);
  const builtinCategory = useFleet((s) => s.crd.builtinCategory);
  const setBuiltinCategory = useFleet((s) => s.setBuiltinCategory);
  const cluster = useFleet((s) => {
    const r = s.route;
    return r.name === "cluster" ? s.clusters.find((c) => c.name === r.cluster) ?? null : null;
  });
  const board = useFleet((s) => {
    const r = s.route;
    return r.name === "cluster" ? s.fleetBoard[r.cluster] : undefined;
  });
  const mesh = useFleet((s) => s.mesh);
  const metrics = useFleet((s) => s.metrics);
  const inCluster = route.name === "cluster";
  const metricsForCluster = cluster && metrics.cluster === cluster.name ? metrics.dto : null;

  const [expanded, setExpanded] = useState<boolean>(readPersistedExpanded);

  useEffect(() => {
    if (!expanded || !cluster) return;
    const metricsAlreadyCurrent = metrics.cluster === cluster.name && (metrics.loading || metrics.dto);
    if (!metricsAlreadyCurrent) void getClusterMetrics(cluster.name, false);
  }, [expanded, cluster, metrics.cluster, metrics.dto, metrics.loading]);

  function toggle() {
    setExpanded((prev) => {
      const next = !prev;
      try { localStorage.setItem("klyx-sidebar-expanded", next ? "1" : "0"); } catch { /* noop */ }
      return next;
    });
  }

  const width = expanded ? EXPANDED_WIDTH : COLLAPSED_WIDTH;

  return (
    <div style={{
      width,
      minWidth: width,
      transition: "width 120ms ease, min-width 120ms ease",
      background: "var(--color-background-secondary)",
      borderRight: "0.5px solid var(--color-border-tertiary)",
      padding: "10px 0",
      display: "flex",
      flexDirection: "column",
      alignItems: expanded ? "stretch" : "center",
      gap: 4,
      overflow: "hidden",
    }}>
      {/* Logo mark */}
      <KlyxMark
        size={28}
        title="Klyx"
        style={{ margin: expanded ? "0 0 6px 9px" : "0 auto 6px auto" }}
      />

      <div
        data-testid="sidebar-nav-scroll"
        style={{
          flex: 1,
          minHeight: 0,
          width: "100%",
          overflowY: "auto",
          overflowX: "hidden",
          display: "flex",
          flexDirection: "column",
          alignItems: expanded ? "stretch" : "center",
          gap: 4,
        }}
      >
        {/* Fleet home */}
        <RailButton
          label="Fleet"
          active={route.name === "fleet"}
          onClick={openFleet}
          expanded={expanded}
        >
          <IconLayoutGrid size={16} stroke={1.5} />
        </RailButton>

        {/* Port-forwards — fleet-level (tunnels span clusters) */}
        <RailButton
          label={forwardsCount > 0 ? `Forwards · ${forwardsCount}` : "Forwards"}
          active={route.name === "forwards"}
          onClick={openForwards}
          expanded={expanded}
        >
          <IconArrowsLeftRight size={16} stroke={1.5} />
        </RailButton>

        {/* Section nav — grouped with semantic dividers */}
        {SECTION_GROUPS.map((group, gi) => (
          <div key={gi}>
            {gi > 0 && (
              <div role="separator" style={{
                height: 0,
                borderTop: "0.5px solid var(--color-border-tertiary)",
                margin: "6px 8px",
              }} />
            )}
            {group.map(({ section, Icon }) => (
              <div key={section}>
                <RailButton
                  label={SECTION_LABELS[section]}
                  disabled={!inCluster}
                  active={inCluster && route.section === section}
                  onClick={() => setSection(section)}
                  expanded={expanded}
                >
                  <Icon size={16} stroke={1.5} />
                </RailButton>
                {expanded && inCluster && route.section === section && section === "resources" && (
                  <div style={{ display: "flex", flexDirection: "column", gap: 1 }}>
                    {BUILTIN_CATALOG.map((cat) => (
                      <button
                        key={cat.label}
                        aria-label={`category ${cat.label}`}
                        onClick={() => { setSection("resources"); setBuiltinCategory(builtinCategory === cat.label ? null : cat.label); }}
                        style={{
                          display: "flex", alignItems: "center",
                          width: "calc(100% - 18px)",
                          height: 24,
                          margin: "0 9px",
                          paddingLeft: 28,
                          borderRadius: 4,
                          cursor: "pointer",
                          background: builtinCategory === cat.label ? "var(--color-background-primary)" : "transparent",
                          border: builtinCategory === cat.label ? "0.5px solid var(--color-border-secondary)" : "0.5px solid transparent",
                          color: builtinCategory === cat.label ? "var(--color-text-primary)" : "var(--color-text-secondary)",
                          fontSize: 11,
                          textAlign: "left",
                          whiteSpace: "nowrap",
                          overflow: "hidden",
                          textOverflow: "ellipsis",
                        }}
                      >
                        {cat.label}
                      </button>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        ))}

        {expanded && inCluster && cluster && (
          <CapabilitiesBlock cluster={cluster} board={board} mesh={mesh} metrics={metricsForCluster} />
        )}
      </div>

      {/* Bottom placeholders */}
      <RailButton label="Terminal" onClick={() => void openTerminal()} expanded={expanded}>
        <IconTerminal2 size={16} stroke={1.5} />
      </RailButton>
      <RailButton
        label={newContexts > 0 ? `Settings · ${newContexts}` : "Settings"}
        active={route.name === "settings"}
        onClick={openSettings}
        expanded={expanded}
      >
        <IconSettings size={16} stroke={1.5} />
      </RailButton>

      {/* Collapse/expand toggle */}
      <button
        aria-label={expanded ? "collapse sidebar" : "expand sidebar"}
        title={expanded ? "collapse sidebar" : "expand sidebar"}
        onClick={toggle}
        style={{
          display: "flex", alignItems: "center",
          justifyContent: expanded ? "flex-start" : "center",
          gap: 8,
          width: expanded ? "calc(100% - 18px)" : 32,
          height: 32,
          margin: expanded ? "0 9px" : "0 auto",
          borderRadius: 6, padding: expanded ? "0 8px" : 0,
          cursor: "pointer",
          background: "transparent",
          border: "0.5px solid transparent",
          color: "var(--color-text-tertiary)",
        }}
      >
        {expanded
          ? <IconChevronLeft size={14} stroke={1.5} />
          : <IconChevronRight size={14} stroke={1.5} />}
        {expanded && <span style={{ fontSize: 11, whiteSpace: "nowrap" }}>collapse sidebar</span>}
      </button>
    </div>
  );
}

type CapabilityTone = "success" | "warning" | "danger" | "info" | "muted";
type CapabilityRow = { label: string; value: string; tone: CapabilityTone; title?: string };

function CapabilitiesBlock({ cluster, board, mesh, metrics }: { cluster: ClusterDTO; board: FleetBoardEntry | undefined; mesh: MeshGraphDTO | null; metrics: MetricsDTO | null }) {
  const rows = capabilityRows(cluster, board, mesh, metrics);
  return (
    <div
      aria-label="cluster capabilities"
      style={{
        margin: "8px 9px 0",
        padding: "10px 0 2px",
        borderTop: "0.5px solid var(--color-border-tertiary)",
        display: "grid",
        gap: 8,
        flexShrink: 0,
      }}
    >
      <div style={{
        fontSize: 10,
        fontFamily: "var(--font-mono)",
        color: "var(--color-text-tertiary)",
        textTransform: "lowercase",
      }}>
        capabilities
      </div>
      <div style={{ display: "grid", gap: 7 }}>
        {rows.map((row) => (
          <div
            key={row.label}
            title={row.title}
            style={{
              display: "grid",
              gridTemplateColumns: "8px minmax(0, 1fr) auto",
              alignItems: "center",
              gap: 8,
              minHeight: 18,
              fontFamily: "var(--font-mono)",
              fontSize: 11,
            }}
          >
            <span style={{
              width: 7,
              height: 7,
              borderRadius: "50%",
              background: toneColor(row.tone),
            }} />
            <span style={{
              minWidth: 0,
              overflow: "hidden",
              textOverflow: "ellipsis",
              whiteSpace: "nowrap",
              color: "var(--color-text-secondary)",
            }}>
              {row.label}
            </span>
            <span style={{
              color: toneColor(row.tone),
              whiteSpace: "nowrap",
              textAlign: "right",
              maxWidth: 72,
              overflow: "hidden",
              textOverflow: "ellipsis",
            }}>
              {row.value}
            </span>
          </div>
        ))}
      </div>
    </div>
  );
}

function capabilityRows(cluster: ClusterDTO, board: FleetBoardEntry | undefined, mesh: MeshGraphDTO | null, metrics: MetricsDTO | null): CapabilityRow[] {
  return [
    fluxCapability(cluster, board),
    ciliumCapability(cluster, mesh),
    gatewayCapability(cluster, board),
    certManagerCapability(cluster),
    prometheusCapability(metrics),
  ];
}

function certManagerCapability(cluster: ClusterDTO): CapabilityRow {
  if (cluster.certManagerPresent === false || cluster.certManagerTier === "Absent") {
    return { label: "cert-manager", value: "absent", tone: "muted", title: cluster.certManagerReason || undefined };
  }
  if (cluster.certManagerPresent && cluster.certManagerHealthy) return { label: "cert-manager", value: "ready", tone: "success" };
  if (cluster.certManagerPresent) return { label: "cert-manager", value: "degraded", tone: "warning", title: cluster.certManagerReason || undefined };
  return { label: "cert-manager", value: "unknown", tone: "muted" };
}

function fluxCapability(cluster: ClusterDTO, board: FleetBoardEntry | undefined): CapabilityRow {
  const flux = board?.flux;
  if (flux) {
    if (flux.notReady > 0) return { label: "flux", value: `${flux.notReady} bad`, tone: "warning" };
    return { label: "flux", value: "ready", tone: "success" };
  }
  if (cluster.fluxPresent === false || cluster.gitopsTier === "Absent") {
    return { label: "flux", value: "absent", tone: "muted", title: cluster.gitopsReason || undefined };
  }
  if (cluster.fluxPresent && cluster.fluxHealthy) return { label: "flux", value: "ready", tone: "success" };
  if (cluster.fluxPresent) return { label: "flux", value: "degraded", tone: "warning", title: cluster.gitopsReason || undefined };
  return { label: "flux", value: "unknown", tone: "muted" };
}

function meshNodeForCluster(mesh: MeshGraphDTO | null, clusterName: string) {
  return mesh?.nodes.find((n) => n.cluster === clusterName || n.name === clusterName);
}

function ciliumCapability(cluster: ClusterDTO, mesh: MeshGraphDTO | null): CapabilityRow {
  const meshNode = meshNodeForCluster(mesh, cluster.name);
  if (meshNode && meshNode.present && meshNode.state !== "unavailable") {
    const meshed = meshNode.state === "peered";
    return { label: "cilium", value: meshed ? "mesh" : "present", tone: meshed ? "success" : "info" };
  }
  if (cluster.ciliumPresent === true) {
    return { label: "cilium", value: cluster.clusterMesh ? "mesh" : "present", tone: cluster.clusterMesh ? "success" : "info" };
  }
  if (cluster.ciliumPresent === false) {
    return { label: "cilium", value: "absent", tone: "muted" };
  }
  return { label: "cilium", value: "unknown", tone: "muted" };
}

function gatewayCapability(cluster: ClusterDTO, board: FleetBoardEntry | undefined): CapabilityRow {
  const g = board?.gateway;
  const issueCount = (g?.unprogrammed ?? 0) + (g?.brokenRoutes ?? 0);
  if (issueCount > 0) return { label: "gateway api", value: `${issueCount} issue${issueCount === 1 ? "" : "s"}`, tone: "warning" };
  if (cluster.networkTier && cluster.networkTier !== "Healthy" && cluster.gatewayAPIVersion) {
    return { label: "gateway api", value: cluster.networkTier.toLowerCase(), tone: "warning", title: cluster.networkReason || undefined };
  }
  if (cluster.gatewayAPIVersion) return { label: "gateway api", value: cluster.gatewayAPIVersion, tone: "success" };
  if (g?.served && g.routes != null) return { label: "gateway api", value: `${g.routes} route${g.routes === 1 ? "" : "s"}`, tone: "success" };
  if (g?.served) return { label: "gateway api", value: "served", tone: "success" };
  if (g?.served === false || (cluster.networkTier === "Absent" && !cluster.gatewayAPIVersion)) {
    return { label: "gateway api", value: "absent", tone: "muted", title: cluster.networkReason || undefined };
  }
  if (cluster.networkTier === "Healthy") return { label: "gateway api", value: "ready", tone: "success" };
  return { label: "gateway api", value: "unknown", tone: "muted", title: cluster.networkReason || undefined };
}

function prometheusCapability(metrics: MetricsDTO | null): CapabilityRow {
  if (!metrics) return { label: "prometheus", value: "unknown", tone: "muted" };
  if (!metrics.available) return { label: "prometheus", value: "unavailable", tone: "warning", title: metrics.reason || metrics.warning || undefined };
  const source = `${metrics.mode} ${metrics.source}`.toLowerCase();
  const value = source.includes("prometheus") || source.includes("grafana") || source.includes("mimir") || source.includes("lgtm")
    ? "lgtm"
    : "ready";
  return { label: "prometheus", value, tone: "info", title: metrics.source || undefined };
}

function toneColor(tone: CapabilityTone): string {
  switch (tone) {
    case "success": return "var(--color-text-success)";
    case "warning": return "var(--color-text-warning)";
    case "danger": return "var(--color-text-danger)";
    case "info": return "var(--color-text-info)";
    case "muted": return "var(--color-text-tertiary)";
  }
}

function RailButton({ label, active, disabled, onClick, children, expanded }: {
  label: string; active?: boolean; disabled?: boolean; onClick?: () => void;
  children: React.ReactNode; expanded: boolean;
}) {
  return (
    <button
      aria-label={label}
      title={expanded ? undefined : label}
      disabled={disabled}
      onClick={onClick}
      style={{
        display: "flex", alignItems: "center", gap: 8,
        justifyContent: expanded ? "flex-start" : "center",
        width: expanded ? "calc(100% - 18px)" : 32,
        height: 32,
        margin: expanded ? "0 9px" : "0 auto",
        borderRadius: 6,
        padding: expanded ? "0 8px" : 0,
        cursor: disabled ? "default" : "pointer",
        background: active ? "var(--color-background-primary)" : "transparent",
        border: active ? "0.5px solid var(--color-border-secondary)" : "0.5px solid transparent",
        color: disabled ? "var(--color-text-tertiary)" : active ? "var(--color-text-primary)" : "var(--color-text-secondary)",
        opacity: disabled ? 0.5 : 1,
        flexShrink: 0,
      }}
    >
      <span style={{ display: "flex", alignItems: "center", flexShrink: 0 }}>{children}</span>
      {expanded && (
        <span style={{
          fontSize: 12,
          fontWeight: active ? 500 : 400,
          whiteSpace: "nowrap",
          overflow: "hidden",
          textOverflow: "ellipsis",
        }}>
          {label}
        </span>
      )}
    </button>
  );
}
