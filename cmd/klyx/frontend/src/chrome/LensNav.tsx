import { useMemo } from "react";
import { IconLayoutDashboard } from "@tabler/icons-react";
import {
  IconAlertTriangle, IconAnchor, IconArrowsLeftRight, IconBox, IconCertificate, IconCircleDot,
  IconComponents, IconCpu, IconDatabase, IconFolder, IconGitBranch, IconGitMerge, IconHistory,
  IconKey, IconLayoutGrid, IconNetwork, IconPackage, IconRoute, IconScale, IconServer,
  IconSettings, IconShieldLock, IconStack2, IconTerminal2, IconWebhook,
} from "@tabler/icons-react";
import { useFleet, WORKLOAD_KINDS } from "../store/fleet";
import type { ArgoViewMode, ClusterSection, ResourceRef, Route, WorkloadKind } from "../store/fleet";
import { openTerminal } from "../bridge/configsvc";

// LensNav — the mockup's labelled selector rows (Signal Bench): navigation is
// TWO horizontal module strips above the content, not a vertical rail. Row 1
// is the lens groups (capability-gated, current group pressed into a well,
// a fault count where the store already knows one); row 2 is the current
// group's items. The model below is the former Sidebar's, rotated 90°.

type SidebarTarget =
  | { kind: "fleet" }
  | { kind: "forwards" }
  | { kind: "activity" }
  | { kind: "settings" }
  | { kind: "terminal" }
  | { kind: "section"; section: ClusterSection; workloadKinds?: readonly WorkloadKind[]; argoView?: ArgoViewMode }
  | { kind: "resource"; ref: ResourceRef };

type ExpandedItem = {
  label: string;
  Icon: typeof IconLayoutDashboard;
  target: SidebarTarget;
  active?: "target" | "none";
};

type ExpandedGroup = { label: string; items: ExpandedItem[] };

const r = (group: string, version: string, plural: string, kind: string, scope: "Namespaced" | "Cluster"): ResourceRef =>
  ({ group, version, plural, kind, scope });

const EXPANDED_MENU: ExpandedGroup[] = [
  {
    label: "Fleet",
    items: [
      { label: "Fleet", Icon: IconLayoutGrid, target: { kind: "fleet" } },
      { label: "Port-forwards", Icon: IconArrowsLeftRight, target: { kind: "forwards" } },
      { label: "Activity", Icon: IconHistory, target: { kind: "activity" } },
    ],
  },
  {
    label: "Cluster",
    items: [
      { label: "Overview", Icon: IconLayoutDashboard, target: { kind: "section", section: "overview" } },
      { label: "Nodes", Icon: IconServer, target: { kind: "section", section: "nodes" } },
      { label: "Namespaces", Icon: IconFolder, target: { kind: "resource", ref: r("", "v1", "namespaces", "Namespace", "Cluster") } },
      { label: "API Services", Icon: IconComponents, target: { kind: "resource", ref: r("apiregistration.k8s.io", "v1", "apiservices", "APIService", "Cluster") } },
      { label: "Events", Icon: IconAlertTriangle, target: { kind: "section", section: "events" } },
    ],
  },
  {
    label: "Workloads",
    items: [
      { label: "All workloads", Icon: IconBox, target: { kind: "section", section: "workloads", workloadKinds: WORKLOAD_KINDS } },
      { label: "Pods", Icon: IconCircleDot, target: { kind: "section", section: "pods" } },
      { label: "Deployments", Icon: IconBox, target: { kind: "section", section: "workloads", workloadKinds: ["Deployment"] } },
      { label: "StatefulSets", Icon: IconDatabase, target: { kind: "section", section: "workloads", workloadKinds: ["StatefulSet"] } },
      { label: "DaemonSets", Icon: IconCpu, target: { kind: "section", section: "workloads", workloadKinds: ["DaemonSet"] } },
      { label: "ReplicaSets", Icon: IconStack2, target: { kind: "resource", ref: r("apps", "v1", "replicasets", "ReplicaSet", "Namespaced") } },
      { label: "Jobs", Icon: IconPackage, target: { kind: "resource", ref: r("batch", "v1", "jobs", "Job", "Namespaced") } },
      { label: "CronJobs", Icon: IconPackage, target: { kind: "resource", ref: r("batch", "v1", "cronjobs", "CronJob", "Namespaced") } },
    ],
  },
  {
    label: "Config",
    items: [
      { label: "ConfigMaps", Icon: IconStack2, target: { kind: "resource", ref: r("", "v1", "configmaps", "ConfigMap", "Namespaced") } },
      { label: "Secrets", Icon: IconShieldLock, target: { kind: "resource", ref: r("", "v1", "secrets", "Secret", "Namespaced") } },
      { label: "ResourceQuotas", Icon: IconScale, target: { kind: "resource", ref: r("", "v1", "resourcequotas", "ResourceQuota", "Namespaced") } },
      { label: "LimitRanges", Icon: IconScale, target: { kind: "resource", ref: r("", "v1", "limitranges", "LimitRange", "Namespaced") } },
      { label: "PriorityClasses", Icon: IconCpu, target: { kind: "resource", ref: r("scheduling.k8s.io", "v1", "priorityclasses", "PriorityClass", "Cluster") } },
      { label: "RuntimeClasses", Icon: IconCpu, target: { kind: "resource", ref: r("node.k8s.io", "v1", "runtimeclasses", "RuntimeClass", "Cluster") } },
      { label: "Leases", Icon: IconKey, target: { kind: "resource", ref: r("coordination.k8s.io", "v1", "leases", "Lease", "Namespaced") } },
    ],
  },
  {
    label: "Autoscaling",
    items: [
      { label: "HorizontalPodAutoscalers", Icon: IconScale, target: { kind: "resource", ref: r("autoscaling", "v2", "horizontalpodautoscalers", "HorizontalPodAutoscaler", "Namespaced") } },
      { label: "PodDisruptionBudgets", Icon: IconShieldLock, target: { kind: "resource", ref: r("policy", "v1", "poddisruptionbudgets", "PodDisruptionBudget", "Namespaced") } },
    ],
  },
  {
    label: "Network",
    items: [
      { label: "Services", Icon: IconNetwork, target: { kind: "resource", ref: r("", "v1", "services", "Service", "Namespaced") } },
      { label: "Ingresses", Icon: IconRoute, target: { kind: "resource", ref: r("networking.k8s.io", "v1", "ingresses", "Ingress", "Namespaced") } },
      { label: "NetworkPolicies", Icon: IconShieldLock, target: { kind: "resource", ref: r("networking.k8s.io", "v1", "networkpolicies", "NetworkPolicy", "Namespaced") } },
      { label: "EndpointSlices", Icon: IconNetwork, target: { kind: "resource", ref: r("discovery.k8s.io", "v1", "endpointslices", "EndpointSlice", "Namespaced") } },
      { label: "IngressClasses", Icon: IconRoute, target: { kind: "resource", ref: r("networking.k8s.io", "v1", "ingressclasses", "IngressClass", "Cluster") } },
    ],
  },
  {
    label: "Storage",
    items: [
      { label: "PersistentVolumeClaims", Icon: IconDatabase, target: { kind: "resource", ref: r("", "v1", "persistentvolumeclaims", "PersistentVolumeClaim", "Namespaced") } },
      { label: "PersistentVolumes", Icon: IconDatabase, target: { kind: "resource", ref: r("", "v1", "persistentvolumes", "PersistentVolume", "Cluster") } },
      { label: "StorageClasses", Icon: IconDatabase, target: { kind: "resource", ref: r("storage.k8s.io", "v1", "storageclasses", "StorageClass", "Cluster") } },
      { label: "CSI Drivers", Icon: IconDatabase, target: { kind: "resource", ref: r("storage.k8s.io", "v1", "csidrivers", "CSIDriver", "Cluster") } },
      { label: "CSI Nodes", Icon: IconServer, target: { kind: "resource", ref: r("storage.k8s.io", "v1", "csinodes", "CSINode", "Cluster") } },
      { label: "Volume Attachments", Icon: IconStack2, target: { kind: "resource", ref: r("storage.k8s.io", "v1", "volumeattachments", "VolumeAttachment", "Cluster") } },
    ],
  },
  {
    label: "Access control",
    items: [
      { label: "Service Accounts", Icon: IconKey, target: { kind: "resource", ref: r("", "v1", "serviceaccounts", "ServiceAccount", "Namespaced") } },
      { label: "Cluster Roles", Icon: IconShieldLock, target: { kind: "resource", ref: r("rbac.authorization.k8s.io", "v1", "clusterroles", "ClusterRole", "Cluster") } },
      { label: "Roles", Icon: IconShieldLock, target: { kind: "resource", ref: r("rbac.authorization.k8s.io", "v1", "roles", "Role", "Namespaced") } },
      { label: "Cluster Role Bindings", Icon: IconKey, target: { kind: "resource", ref: r("rbac.authorization.k8s.io", "v1", "clusterrolebindings", "ClusterRoleBinding", "Cluster") } },
      { label: "Role Bindings", Icon: IconKey, target: { kind: "resource", ref: r("rbac.authorization.k8s.io", "v1", "rolebindings", "RoleBinding", "Namespaced") } },
      { label: "CSRs", Icon: IconCertificate, target: { kind: "resource", ref: r("certificates.k8s.io", "v1", "certificatesigningrequests", "CertificateSigningRequest", "Cluster") } },
    ],
  },
  {
    label: "Gateway API",
    items: [
      { label: "Gateway topology", Icon: IconRoute, target: { kind: "section", section: "gatewayApi" } },
      { label: "Gateways", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.networking.k8s.io", "v1", "gateways", "Gateway", "Namespaced") } },
      { label: "HTTPRoutes", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.networking.k8s.io", "v1", "httproutes", "HTTPRoute", "Namespaced") } },
      { label: "GRPCRoutes", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.networking.k8s.io", "v1", "grpcroutes", "GRPCRoute", "Namespaced") } },
      { label: "GatewayClasses", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.networking.k8s.io", "v1", "gatewayclasses", "GatewayClass", "Cluster") } },
      { label: "ReferenceGrants", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.networking.k8s.io", "v1beta1", "referencegrants", "ReferenceGrant", "Namespaced") } },
      { label: "BackendTLSPolicies", Icon: IconCertificate, target: { kind: "resource", ref: r("gateway.networking.k8s.io", "v1alpha3", "backendtlspolicies", "BackendTLSPolicy", "Namespaced") } },
      { label: "ClientTrafficPolicies", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.envoyproxy.io", "v1alpha1", "clienttrafficpolicies", "ClientTrafficPolicy", "Namespaced") } },
      { label: "BackendTrafficPolicies", Icon: IconRoute, target: { kind: "resource", ref: r("gateway.envoyproxy.io", "v1alpha1", "backendtrafficpolicies", "BackendTrafficPolicy", "Namespaced") } },
      { label: "SecurityPolicies", Icon: IconShieldLock, target: { kind: "resource", ref: r("gateway.envoyproxy.io", "v1alpha1", "securitypolicies", "SecurityPolicy", "Namespaced") } },
    ],
  },
  {
    label: "cert-manager",
    items: [
      { label: "Certificates", Icon: IconCertificate, target: { kind: "section", section: "certs" } },
      { label: "Requests", Icon: IconCertificate, target: { kind: "resource", ref: r("cert-manager.io", "v1", "certificaterequests", "CertificateRequest", "Namespaced") } },
      { label: "Orders", Icon: IconCertificate, target: { kind: "resource", ref: r("acme.cert-manager.io", "v1", "orders", "Order", "Namespaced") } },
      { label: "Challenges", Icon: IconCertificate, target: { kind: "resource", ref: r("acme.cert-manager.io", "v1", "challenges", "Challenge", "Namespaced") } },
      { label: "Issuers", Icon: IconCertificate, target: { kind: "resource", ref: r("cert-manager.io", "v1", "issuers", "Issuer", "Namespaced") } },
      { label: "ClusterIssuers", Icon: IconCertificate, target: { kind: "resource", ref: r("cert-manager.io", "v1", "clusterissuers", "ClusterIssuer", "Cluster") } },
    ],
  },
  {
    label: "External Secrets",
    items: [
      { label: "ExternalSecrets", Icon: IconShieldLock, target: { kind: "section", section: "externalsecrets" } },
      { label: "SecretStores", Icon: IconDatabase, target: { kind: "resource", ref: r("external-secrets.io", "v1", "secretstores", "SecretStore", "Namespaced") } },
      { label: "ClusterSecretStores", Icon: IconDatabase, target: { kind: "resource", ref: r("external-secrets.io", "v1", "clustersecretstores", "ClusterSecretStore", "Cluster") } },
    ],
  },
  {
    label: "Argo CD",
    items: [
      { label: "Applications", Icon: IconGitMerge, target: { kind: "section", section: "argo" } },
      { label: "Projects", Icon: IconFolder, target: { kind: "section", section: "argo", argoView: "projects" } },
      { label: "ApplicationSets", Icon: IconStack2, target: { kind: "section", section: "argo", argoView: "applicationsets" } },
    ],
  },
  {
    label: "Flux CD",
    items: [
      { label: "Kustomizations", Icon: IconGitBranch, target: { kind: "section", section: "gitops" } },
      { label: "HelmReleases", Icon: IconPackage, target: { kind: "resource", ref: r("helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmRelease", "Namespaced") } },
      { label: "GitRepositories", Icon: IconGitBranch, target: { kind: "resource", ref: r("source.toolkit.fluxcd.io", "v1", "gitrepositories", "GitRepository", "Namespaced") } },
      { label: "HelmRepositories", Icon: IconPackage, target: { kind: "resource", ref: r("source.toolkit.fluxcd.io", "v1", "helmrepositories", "HelmRepository", "Namespaced") } },
      { label: "OCIRepositories", Icon: IconPackage, target: { kind: "resource", ref: r("source.toolkit.fluxcd.io", "v1beta2", "ocirepositories", "OCIRepository", "Namespaced") } },
      { label: "Buckets", Icon: IconDatabase, target: { kind: "resource", ref: r("source.toolkit.fluxcd.io", "v1", "buckets", "Bucket", "Namespaced") } },
      { label: "Providers", Icon: IconWebhook, target: { kind: "resource", ref: r("notification.toolkit.fluxcd.io", "v1beta3", "providers", "Provider", "Namespaced") } },
      { label: "Alerts", Icon: IconAlertTriangle, target: { kind: "resource", ref: r("notification.toolkit.fluxcd.io", "v1beta3", "alerts", "Alert", "Namespaced") } },
      { label: "Receivers", Icon: IconWebhook, target: { kind: "resource", ref: r("notification.toolkit.fluxcd.io", "v1", "receivers", "Receiver", "Namespaced") } },
    ],
  },
  {
    label: "Helm",
    items: [
      { label: "Releases", Icon: IconAnchor, target: { kind: "section", section: "helm" } },
      { label: "Repositories", Icon: IconPackage, target: { kind: "section", section: "helm" }, active: "none" },
    ],
  },
  {
    label: "Custom resources",
    items: [
      { label: "Browse CRDs", Icon: IconComponents, target: { kind: "section", section: "crds" } },
      { label: "Built-in resources", Icon: IconStack2, target: { kind: "section", section: "resources" } },
    ],
  },
  {
    label: "Tools",
    items: [
      { label: "Terminal", Icon: IconTerminal2, target: { kind: "terminal" }, active: "none" },
      { label: "Settings", Icon: IconSettings, target: { kind: "settings" } },
    ],
  },
];


// Capability gating (design principle 7: render only what's installed).
// Inside a cluster, tool-specific nav is hidden when detection says the tool
// is absent; outside a cluster (fleet/settings) everything shows disabled as
// before. Detection lag just means a group appears when the cluster syncs.
type ClusterCaps = { flux: boolean; argo: boolean; certManager: boolean; externalSecrets: boolean; gatewayAPI: boolean };

const ALL_CAPS: ClusterCaps = { flux: true, argo: true, certManager: true, externalSecrets: true, gatewayAPI: true };

// Groups/sections owned by a single tool; everything else is always shown.
const GROUP_CAPS: Record<string, keyof ClusterCaps> = {
  "Flux CD": "flux",
  "Argo CD": "argo",
  "cert-manager": "certManager",
  "External Secrets": "externalSecrets",
  "Gateway API": "gatewayAPI",
};

function isExpandedItemActive(route: Route, item: ExpandedItem, workloadKindFilter: Record<WorkloadKind, boolean>): boolean {
  const target = item.target;
  if (target.kind === "fleet") return route.name === "fleet";
  if (target.kind === "forwards") return route.name === "forwards";
  if (target.kind === "activity") return route.name === "activity";
  if (target.kind === "settings") return route.name === "settings";
  if (target.kind === "section") {
    if (route.name !== "cluster" || route.section !== target.section || route.resource !== undefined) return false;
    if (target.section === "workloads" && target.workloadKinds) return sameWorkloadKindFilter(workloadKindFilter, target.workloadKinds);
    if (target.section === "argo") return (route.argoView ?? "applications") === (target.argoView ?? "applications");
    return true;
  }
  if (target.kind === "resource") {
    return route.name === "cluster" && route.resource !== undefined && sameRef(route.resource, target.ref);
  }
  return false;
}

function sameWorkloadKindFilter(filter: Record<WorkloadKind, boolean>, kinds: readonly WorkloadKind[]): boolean {
  const selected = new Set(kinds.length === 0 ? WORKLOAD_KINDS : kinds);
  return WORKLOAD_KINDS.every((kind) => filter[kind] === selected.has(kind));
}

function sameRef(a: ResourceRef, b: ResourceRef): boolean {
  return a.group === b.group && a.version === b.version && a.plural === b.plural && a.kind === b.kind && a.scope === b.scope;
}



const navRow: React.CSSProperties = {
  display: "flex",
  gap: 2,
  padding: 4,
  flexShrink: 0,
  overflowX: "auto",
  alignItems: "center",
  background: "var(--color-background-primary)",
  border: "var(--border-hairline) solid var(--color-border-tertiary)",
  borderTop: "1px solid var(--bench-edge-hi, var(--color-border-tertiary))",
  borderRadius: 6,
  boxShadow: "var(--bench-module-shadow, none)",
};

function tabStyle(current: boolean, sub: boolean): React.CSSProperties {
  return {
    display: "flex",
    gap: 7,
    alignItems: "center",
    whiteSpace: "nowrap",
    fontSize: sub ? 10.5 : 11,
    padding: sub ? "4px 9px" : "5px 11px",
    borderRadius: 4,
    border: "none",
    cursor: "pointer",
    background: current ? "var(--bench-well-bg, var(--color-background-secondary))" : "transparent",
    boxShadow: current ? "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-secondary))" : undefined,
    color: current ? "var(--color-text-primary)" : "var(--color-text-secondary)",
    fontWeight: current ? 600 : 400,
  };
}

function TabCount({ n, bad }: { n: number; bad?: boolean }) {
  if (n <= 0) return null;
  return (
    <span style={{ fontFamily: "var(--font-mono)", fontSize: 9, color: bad ? "var(--color-text-danger)" : "var(--color-text-tertiary)", fontWeight: bad ? 700 : 400 }}>{n}</span>
  );
}

// Mockup vocabulary for the strip - shorter than the menu labels.
const DISPLAY_LABEL: Record<string, string> = {
  "External Secrets": "ext-secrets",
  "Custom resources": "crds",
};

export function LensNav() {
  const route = useFleet((s) => s.route);
  const openFleet = useFleet((s) => s.openFleet);
  const openForwards = useFleet((s) => s.openForwards);
  const forwardsCount = useFleet((s) => s.forwards.length);
  const openSettings = useFleet((s) => s.openSettings);
  const newContexts = useFleet((s) => s.newContexts);
  const setSection = useFleet((s) => s.setSection);
  const openResource = useFleet((s) => s.openResource);
  const openArgo = useFleet((s) => s.openArgo);
  const openWorkloads = useFleet((s) => s.openWorkloads);
  const workloadKindFilter = useFleet((s) => s.workloads.kindFilter);
  const board = useFleet((s) => s.fleetBoard);

  const inCluster = route.name === "cluster";
  const current = useFleet((s) => (route.name === "cluster" ? s.clusters.find((c) => c.name === route.cluster) : undefined));
  const caps: ClusterCaps = inCluster && current
    ? {
        flux: current.fluxPresent ?? false,
        argo: current.argoPresent ?? false,
        certManager: current.certManagerPresent ?? false,
        externalSecrets: current.externalSecretsPresent ?? false,
        gatewayAPI: Boolean(current.gatewayAPIVersion),
      }
    : ALL_CAPS;

  // Only cluster lens groups live in the strip; app-level destinations sit at
  // the row's right edge. Tools group is folded into that edge too.
  const groups = useMemo(
    () => EXPANDED_MENU.filter((g) => g.label !== "Fleet" && g.label !== "Tools").filter((g) => {
      const cap = GROUP_CAPS[g.label];
      return !cap || caps[cap];
    }),
    [caps.flux, caps.argo, caps.certManager, caps.externalSecrets, caps.gatewayAPI],
  );

  const activate = (target: SidebarTarget) => {
    switch (target.kind) {
      case "fleet": openFleet(); break;
      case "forwards": openForwards(); break;
      case "activity": useFleet.getState().openActivity(); break;
      case "settings": openSettings(); break;
      case "terminal": void openTerminal(); break;
      case "section":
        if (target.section === "workloads" && target.workloadKinds) openWorkloads(target.workloadKinds);
        else if (target.section === "argo") openArgo(target.argoView ?? "applications");
        else setSection(target.section);
        break;
      case "resource": openResource(target.ref); break;
    }
  };

  const activeGroup = inCluster
    ? groups.find((g) => g.items.some((item) => item.active !== "none" && isExpandedItemActive(route, item, workloadKindFilter)))
    : undefined;

  // Fault counts, from data that is actually loaded - a lens that has not
  // been opened contributes nothing rather than a fake zero. Workloads come
  // from the fleet board (fetched fleet-wide); the rest read their lens
  // stores when they hold the current cluster.
  const clusterName = route.name === "cluster" ? route.cluster : "";
  const brokenWorkloads = inCluster ? (board[clusterName]?.broken ?? 0) : 0;
  const brokenPods = useFleet((s) => (inCluster && s.pods.cluster === clusterName ? s.pods.items.filter((p) => p.rank === "unhealthy").length : 0));
  const brokenFlux = useFleet((s) => (inCluster && s.gitops.cluster === clusterName ? s.gitops.resources.filter((r) => r.ready === "Failed" && !r.suspended).length : 0));
  const brokenArgo = useFleet((s) => (inCluster && s.argo.cluster === clusterName ? s.argo.apps.filter((a) => a.broken).length : 0));
  const groupFault: Record<string, number> = {
    Workloads: Math.max(brokenWorkloads, brokenPods),
    "Flux CD": brokenFlux,
    "Argo CD": brokenArgo,
  };
  const itemFault: Record<string, number> = {
    "All workloads": brokenWorkloads,
    Pods: brokenPods,
    Kustomizations: brokenFlux,
    Applications: brokenArgo,
  };

  return (
    <>
      <nav aria-label="lenses" style={navRow}>
        <button onClick={openFleet} aria-current={route.name === "fleet" ? "true" : undefined} style={tabStyle(route.name === "fleet", false)}>
          fleet
        </button>
        {inCluster && groups.map((g) => {
          const isCur = activeGroup?.label === g.label;
          const label = (DISPLAY_LABEL[g.label] ?? g.label).toLowerCase();
          const bad = groupFault[g.label] ?? 0;
          return (
            <button key={g.label} onClick={() => activate(g.items[0].target)} aria-current={isCur ? "true" : undefined} style={tabStyle(isCur, false)}>
              {bad > 0 && <span aria-hidden style={{ width: 6, height: 6, borderRadius: "50%", background: "var(--color-text-danger)", boxShadow: "var(--lamp-glow-danger, none)", display: "inline-block" }} />}
              {label}
              <TabCount n={bad} bad />
            </button>
          );
        })}
        <span style={{ marginLeft: "auto", display: "flex", gap: 2, alignItems: "center" }}>
          <button onClick={openForwards} aria-current={route.name === "forwards" ? "true" : undefined} style={tabStyle(route.name === "forwards", false)} title="port-forwards">
            <IconArrowsLeftRight size={13} stroke={1.5} /> forwards <TabCount n={forwardsCount} />
          </button>
          <button onClick={() => useFleet.getState().openActivity()} aria-current={route.name === "activity" ? "true" : undefined} style={tabStyle(route.name === "activity", false)} title="activity log">
            <IconHistory size={13} stroke={1.5} /> activity
          </button>
          <button onClick={() => void openTerminal()} style={tabStyle(false, false)} title="terminal">
            <IconTerminal2 size={13} stroke={1.5} />
          </button>
          <button onClick={openSettings} aria-current={route.name === "settings" ? "true" : undefined} style={tabStyle(route.name === "settings", false)} title={newContexts > 0 ? `settings · ${newContexts} new contexts` : "settings"}>
            <IconSettings size={13} stroke={1.5} />{newContexts > 0 ? ` ${newContexts}` : ""}
          </button>
        </span>
      </nav>
      {activeGroup && activeGroup.items.length > 1 && (
        <nav aria-label={`${activeGroup.label.toLowerCase()} lenses`} style={navRow}>
          {activeGroup.items.map((item) => {
            const active = item.active !== "none" && isExpandedItemActive(route, item, workloadKindFilter);
            const bad = itemFault[item.label] ?? 0;
            return (
              <button key={item.label} onClick={() => activate(item.target)} aria-current={active ? "true" : undefined} style={tabStyle(active, true)}>
                {bad > 0 && <span aria-hidden style={{ width: 6, height: 6, borderRadius: "50%", background: "var(--color-text-danger)", boxShadow: "var(--lamp-glow-danger, none)", display: "inline-block" }} />}
                {item.label.toLowerCase()}
                <TabCount n={bad} bad />
              </button>
            );
          })}
        </nav>
      )}
    </>
  );
}
