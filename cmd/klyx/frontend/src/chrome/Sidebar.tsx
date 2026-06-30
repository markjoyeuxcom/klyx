import { useState } from "react";
import {
  IconLayoutGrid,
  IconArrowsLeftRight, IconLayoutDashboard, IconStack2, IconGitBranch, IconGitMerge,
  IconRoute, IconTerminal2, IconSettings, IconBox, IconCircleDot,
  IconAlertTriangle, IconServer, IconAnchor, IconChevronRight, IconChevronLeft,
  IconComponents, IconCertificate, IconFolder, IconDatabase, IconShieldLock,
  IconNetwork, IconKey, IconScale, IconWebhook, IconCpu, IconPackage,
} from "@tabler/icons-react";
import { useFleet, SECTION_LABELS, WORKLOAD_KINDS } from "../store/fleet";
import type { ArgoViewMode, ClusterSection, ResourceRef, Route, WorkloadKind } from "../store/fleet";
import { openTerminal } from "../bridge/configsvc";
import { KlyxMark } from "./KlyxMark";

const COLLAPSED_WIDTH = 46;
const EXPANDED_WIDTH = 224;
const COLLAPSED_GROUPS_KEY = "klyx-sidebar-collapsed-groups";

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
    { section: "gatewayApi", Icon: IconRoute },
  ],
  [
    { section: "resources", Icon: IconStack2 },
    { section: "crds",      Icon: IconComponents },
  ],
];

type SidebarTarget =
  | { kind: "fleet" }
  | { kind: "forwards" }
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

function readPersistedExpanded(): boolean {
  try { return localStorage.getItem("klyx-sidebar-expanded") === "1"; } catch { return false; }
}

function readPersistedCollapsedGroups(): string[] {
  try {
    const raw = localStorage.getItem(COLLAPSED_GROUPS_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === "string") : [];
  } catch {
    return [];
  }
}

export function Sidebar() {
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
  const inCluster = route.name === "cluster";

  const [expanded, setExpanded] = useState<boolean>(readPersistedExpanded);

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
        {expanded ? (
          <ExpandedMenu
            route={route}
            inCluster={inCluster}
            forwardsCount={forwardsCount}
            onFleet={openFleet}
            onForwards={openForwards}
            onSettings={openSettings}
            onSection={setSection}
            onResource={openResource}
            onArgo={openArgo}
            onWorkloads={openWorkloads}
            workloadKindFilter={workloadKindFilter}
          />
        ) : (
          <CollapsedMenu
            route={route}
            inCluster={inCluster}
            forwardsCount={forwardsCount}
            onFleet={openFleet}
            onForwards={openForwards}
            onSection={setSection}
          />
        )}
      </div>

      {!expanded && (
        <>
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
        </>
      )}

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

function CollapsedMenu({
  route,
  inCluster,
  forwardsCount,
  onFleet,
  onForwards,
  onSection,
}: {
  route: Route;
  inCluster: boolean;
  forwardsCount: number;
  onFleet: () => void;
  onForwards: () => void;
  onSection: (section: ClusterSection) => void;
}) {
  return (
    <>
      <RailButton label="Fleet" active={route.name === "fleet"} onClick={onFleet} expanded={false}>
        <IconLayoutGrid size={16} stroke={1.5} />
      </RailButton>
      <RailButton
        label={forwardsCount > 0 ? `Forwards · ${forwardsCount}` : "Forwards"}
        active={route.name === "forwards"}
        onClick={onForwards}
        expanded={false}
      >
        <IconArrowsLeftRight size={16} stroke={1.5} />
      </RailButton>

      {SECTION_GROUPS.map((group, gi) => (
        <div key={gi}>
          {gi > 0 && <SidebarSeparator />}
          {group.map(({ section, Icon }) => (
            <RailButton
              key={section}
              label={SECTION_LABELS[section]}
              disabled={!inCluster}
              active={inCluster && route.name === "cluster" && route.section === section}
              onClick={() => onSection(section)}
              expanded={false}
            >
              <Icon size={16} stroke={1.5} />
            </RailButton>
          ))}
        </div>
      ))}
    </>
  );
}

function ExpandedMenu({
  route,
  inCluster,
  forwardsCount,
  onFleet,
  onForwards,
  onSettings,
  onSection,
  onResource,
  onArgo,
  onWorkloads,
  workloadKindFilter,
}: {
  route: Route;
  inCluster: boolean;
  forwardsCount: number;
  onFleet: () => void;
  onForwards: () => void;
  onSettings: () => void;
  onSection: (section: ClusterSection) => void;
  onResource: (ref: ResourceRef) => void;
  onArgo: (mode?: ArgoViewMode) => void;
  onWorkloads: (kinds?: readonly WorkloadKind[]) => void;
  workloadKindFilter: Record<WorkloadKind, boolean>;
}) {
  const [collapsedGroups, setCollapsedGroups] = useState<Set<string>>(() => new Set(readPersistedCollapsedGroups()));

  const toggleGroup = (label: string) => {
    setCollapsedGroups((prev) => {
      const next = new Set(prev);
      if (next.has(label)) next.delete(label);
      else next.add(label);
      try { localStorage.setItem(COLLAPSED_GROUPS_KEY, JSON.stringify([...next])); } catch { /* noop */ }
      return next;
    });
  };

  const activate = (target: SidebarTarget) => {
    switch (target.kind) {
      case "fleet":
        onFleet();
        break;
      case "forwards":
        onForwards();
        break;
      case "settings":
        onSettings();
        break;
      case "terminal":
        void openTerminal();
        break;
      case "section":
        if (target.section === "workloads" && target.workloadKinds) {
          onWorkloads(target.workloadKinds);
        } else if (target.section === "argo") {
          onArgo(target.argoView ?? "applications");
        } else {
          onSection(target.section);
        }
        break;
      case "resource":
        onResource(target.ref);
        break;
    }
  };

  return (
    <div style={{ width: "100%", paddingBottom: 4 }}>
      {EXPANDED_MENU.map((group, gi) => {
        const collapsed = collapsedGroups.has(group.label);
        const groupActive = group.items.some((item) => item.active !== "none" && isExpandedItemActive(route, item, workloadKindFilter));
        return (
          <div key={group.label}>
            {gi > 0 && <SidebarSeparator />}
            <button
              aria-label={`${collapsed ? "expand" : "collapse"} ${group.label}`}
              aria-expanded={!collapsed}
              onClick={() => toggleGroup(group.label)}
              style={groupLabelStyle(groupActive)}
            >
              <IconChevronRight
                size={12}
                stroke={1.7}
                style={{ transform: collapsed ? "rotate(0deg)" : "rotate(90deg)", transition: "transform 100ms ease" }}
              />
              <span style={{ minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {group.label}
              </span>
            </button>
            {!collapsed && (
              <div style={{ display: "grid", gap: 1 }}>
                {group.items.map((item) => {
                  const label = item.target.kind === "forwards" && forwardsCount > 0
                    ? `${item.label} · ${forwardsCount}`
                    : item.label;
                  const disabled = requiresCluster(item.target) && !inCluster;
                  return (
                    <ExpandedMenuButton
                      key={`${group.label}/${item.label}`}
                      item={item}
                      label={label}
                      active={item.active !== "none" && isExpandedItemActive(route, item, workloadKindFilter)}
                      disabled={disabled}
                      onClick={() => activate(item.target)}
                    />
                  );
                })}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

function ExpandedMenuButton({
  item,
  label,
  active,
  disabled,
  onClick,
}: {
  item: ExpandedItem;
  label: string;
  active: boolean;
  disabled: boolean;
  onClick: () => void;
}) {
  const Icon = item.Icon;
  return (
    <button
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={onClick}
      style={{
        display: "grid",
        gridTemplateColumns: "18px minmax(0, 1fr)",
        alignItems: "center",
        gap: 8,
        height: 25,
        width: "calc(100% - 16px)",
        margin: "0 8px",
        padding: "0 8px",
        borderRadius: 5,
        cursor: disabled ? "default" : "pointer",
        textAlign: "left",
        background: active ? "var(--color-background-primary)" : "transparent",
        border: active ? "0.5px solid var(--color-border-secondary)" : "0.5px solid transparent",
        color: disabled ? "var(--color-text-tertiary)" : active ? "var(--color-text-primary)" : "var(--color-text-secondary)",
        opacity: disabled ? 0.48 : 1,
        fontSize: 12,
        fontWeight: active ? 500 : 400,
      }}
    >
      <Icon size={15} stroke={1.5} />
      <span
        style={{
          minWidth: 0,
          overflow: "hidden",
          textOverflow: "ellipsis",
          whiteSpace: "nowrap",
        }}
      >
        {label}
      </span>
    </button>
  );
}

function SidebarSeparator() {
  return (
    <div
      role="separator"
      style={{
        height: 0,
        borderTop: "0.5px solid var(--color-border-tertiary)",
        margin: "7px 8px 6px",
      }}
    />
  );
}

function requiresCluster(target: SidebarTarget): boolean {
  return target.kind === "section" || target.kind === "resource";
}

function isExpandedItemActive(route: Route, item: ExpandedItem, workloadKindFilter: Record<WorkloadKind, boolean>): boolean {
  const target = item.target;
  if (target.kind === "fleet") return route.name === "fleet";
  if (target.kind === "forwards") return route.name === "forwards";
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

function groupLabelStyle(active: boolean): React.CSSProperties {
  return {
    display: "grid",
    gridTemplateColumns: "12px minmax(0, 1fr)",
    alignItems: "center",
    gap: 4,
    width: "calc(100% - 16px)",
    minHeight: 22,
    margin: "0 8px",
    padding: "1px 6px 3px",
    borderRadius: 4,
    cursor: "pointer",
    background: active ? "var(--color-background-primary)" : "transparent",
    border: "0.5px solid transparent",
    color: active ? "var(--color-text-primary)" : "var(--color-text-tertiary)",
    fontSize: 10,
    fontWeight: active ? 700 : 600,
    textTransform: "uppercase",
    letterSpacing: 0.2,
    textAlign: "left",
  };
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
