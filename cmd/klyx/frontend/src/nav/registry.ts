import { useFleet, WORKLOAD_KINDS } from "../store/fleet";
import type { ArgoViewMode, ClusterDTO, ClusterSection, FluxViewMode, ResourceRef, Route, WorkloadKind } from "../store/fleet";

// The navigation registry (redesign spec §4): one descriptor table drives the
// navigation band, the group tree, route activity and (step 7c) the palette.
// A task destination is purpose-built workflow/inspection; a resource
// destination is the generic GVR browser and is always labelled as the raw
// view, so "Certificates" (diagnosis) and "Certificates" (raw objects) are
// never mistaken for each other.

export type CapKey = "flux" | "fluxOperator" | "fluxImages" | "argo" | "certManager" | "externalSecrets" | "gatewayAPI" | "karpenter" | "hubble" | "cilium";

export type NavTarget =
  | { kind: "section"; section: ClusterSection; workloadKinds?: readonly WorkloadKind[]; argoView?: ArgoViewMode; fluxView?: FluxViewMode }
  | { kind: "resource"; ref: ResourceRef };

export type Destination = {
  id: string;
  label: string;
  type: "task" | "resource";
  target: NavTarget;
  cap?: CapKey;
  // Palette aliases; the label is always searchable.
  keywords?: string[];
  // The lens index's label when it differs (The Line slice 4 §4.1); the
  // palette keeps the label and keywords.
  indexLabel?: string;
};

// lens "group": a tool group is one lens in the lens index, labelled with
// the group's name; its other task destinations are its sub-entries.
export type NavGroup = { id: string; label: string; cap?: CapKey; lens?: "group"; destinations: Destination[] };

const r = (group: string, version: string, plural: string, kind: string, scope: "Namespaced" | "Cluster"): ResourceRef =>
  ({ group, version, plural, kind, scope });
const task = (id: string, label: string, target: NavTarget, extra: Partial<Destination> = {}): Destination => ({ id, label, type: "task", target, ...extra });
const section = (s: ClusterSection, extra: Omit<Extract<NavTarget, { kind: "section" }>, "kind" | "section"> = {}): NavTarget => ({ kind: "section", section: s, ...extra });
const res = (label: string, ref: ResourceRef, extra: Partial<Destination> = {}): Destination =>
  ({ id: `res:${ref.group || "core"}/${ref.version}/${ref.plural}`, label, type: "resource", target: { kind: "resource", ref }, ...extra });
// Deployments/StatefulSets/DaemonSets live in the Resources menu but open the
// workloads task with that one kind filter (spec §4 table).
const kindLink = (kind: WorkloadKind, label: string): Destination =>
  ({ id: `workloads:${kind}`, label, type: "resource", target: section("workloads", { workloadKinds: [kind] }) });

export const NAV_GROUPS: NavGroup[] = [
  {
    id: "cluster", label: "Cluster", destinations: [
      task("overview", "Overview", section("overview")),
      task("nodes", "Nodes", section("nodes")),
      task("events", "Events", section("events"), { keywords: ["warnings"] }),
      task("upgrade", "Upgrade check", section("upgrade"), { keywords: ["deprecated apis", "version"] }),
      res("Namespaces", r("", "v1", "namespaces", "Namespace", "Cluster")),
      res("API Services", r("apiregistration.k8s.io", "v1", "apiservices", "APIService", "Cluster")),
    ],
  },
  {
    id: "workloads", label: "Workloads", destinations: [
      task("workloads", "All workloads", section("workloads", { workloadKinds: WORKLOAD_KINDS }), { indexLabel: "Workloads" }),
      task("pods", "Pods", section("pods")),
      kindLink("Deployment", "Deployments"),
      kindLink("StatefulSet", "StatefulSets"),
      kindLink("DaemonSet", "DaemonSets"),
      res("ReplicaSets", r("apps", "v1", "replicasets", "ReplicaSet", "Namespaced")),
      res("Jobs", r("batch", "v1", "jobs", "Job", "Namespaced")),
      res("CronJobs", r("batch", "v1", "cronjobs", "CronJob", "Namespaced")),
    ],
  },
  {
    id: "config", label: "Config", destinations: [
      res("ConfigMaps", r("", "v1", "configmaps", "ConfigMap", "Namespaced")),
      res("Secrets", r("", "v1", "secrets", "Secret", "Namespaced"), { keywords: ["masked"] }),
      res("ResourceQuotas", r("", "v1", "resourcequotas", "ResourceQuota", "Namespaced"), { keywords: ["quota"] }),
      res("LimitRanges", r("", "v1", "limitranges", "LimitRange", "Namespaced")),
      res("PriorityClasses", r("scheduling.k8s.io", "v1", "priorityclasses", "PriorityClass", "Cluster")),
      res("RuntimeClasses", r("node.k8s.io", "v1", "runtimeclasses", "RuntimeClass", "Cluster")),
      res("Leases", r("coordination.k8s.io", "v1", "leases", "Lease", "Namespaced")),
    ],
  },
  {
    id: "autoscaling", label: "Autoscaling", destinations: [
      task("karpenter", "Karpenter", section("karpenter"), { cap: "karpenter", keywords: ["nodepools", "provisioning"] }),
      res("HorizontalPodAutoscalers", r("autoscaling", "v2", "horizontalpodautoscalers", "HorizontalPodAutoscaler", "Namespaced"), { keywords: ["hpa"] }),
      res("PodDisruptionBudgets", r("policy", "v1", "poddisruptionbudgets", "PodDisruptionBudget", "Namespaced"), { keywords: ["pdb"] }),
      res("NodePools", r("karpenter.sh", "v1", "nodepools", "NodePool", "Cluster"), { cap: "karpenter" }),
      res("NodeClaims", r("karpenter.sh", "v1", "nodeclaims", "NodeClaim", "Cluster"), { cap: "karpenter" }),
      res("EC2NodeClasses", r("karpenter.k8s.aws", "v1", "ec2nodeclasses", "EC2NodeClass", "Cluster"), { cap: "karpenter" }),
    ],
  },
  {
    id: "network", label: "Network", destinations: [
      task("connectivity", "Connectivity check", section("connectivity"), { keywords: ["can reach", "network policy"] }),
      // The list of Gateways and their routes; "Gateway topology" is the graph
      // a Gateway opens (UI review 2026-10-13).
      task("gateway", "Gateways and routes", section("gatewayApi"), { cap: "gatewayAPI", keywords: ["gateway api", "httproute", "envoy", "topology"] }),
      task("flows", "Hubble flows", section("flows"), { cap: "hubble", keywords: ["cilium", "drops"] }),
      res("Services", r("", "v1", "services", "Service", "Namespaced")),
      res("Ingresses", r("networking.k8s.io", "v1", "ingresses", "Ingress", "Namespaced")),
      res("NetworkPolicies", r("networking.k8s.io", "v1", "networkpolicies", "NetworkPolicy", "Namespaced")),
      res("EndpointSlices", r("discovery.k8s.io", "v1", "endpointslices", "EndpointSlice", "Namespaced")),
      res("IngressClasses", r("networking.k8s.io", "v1", "ingressclasses", "IngressClass", "Cluster")),
      res("Gateways", r("gateway.networking.k8s.io", "v1", "gateways", "Gateway", "Namespaced"), { cap: "gatewayAPI" }),
      res("HTTPRoutes", r("gateway.networking.k8s.io", "v1", "httproutes", "HTTPRoute", "Namespaced"), { cap: "gatewayAPI" }),
      res("GRPCRoutes", r("gateway.networking.k8s.io", "v1", "grpcroutes", "GRPCRoute", "Namespaced"), { cap: "gatewayAPI" }),
      res("GatewayClasses", r("gateway.networking.k8s.io", "v1", "gatewayclasses", "GatewayClass", "Cluster"), { cap: "gatewayAPI" }),
      res("ReferenceGrants", r("gateway.networking.k8s.io", "v1beta1", "referencegrants", "ReferenceGrant", "Namespaced"), { cap: "gatewayAPI" }),
      res("BackendTLSPolicies", r("gateway.networking.k8s.io", "v1alpha3", "backendtlspolicies", "BackendTLSPolicy", "Namespaced"), { cap: "gatewayAPI" }),
      res("ClientTrafficPolicies", r("gateway.envoyproxy.io", "v1alpha1", "clienttrafficpolicies", "ClientTrafficPolicy", "Namespaced"), { cap: "gatewayAPI" }),
      res("BackendTrafficPolicies", r("gateway.envoyproxy.io", "v1alpha1", "backendtrafficpolicies", "BackendTrafficPolicy", "Namespaced"), { cap: "gatewayAPI" }),
      res("SecurityPolicies", r("gateway.envoyproxy.io", "v1alpha1", "securitypolicies", "SecurityPolicy", "Namespaced"), { cap: "gatewayAPI" }),
      res("CiliumNetworkPolicies", r("cilium.io", "v2", "ciliumnetworkpolicies", "CiliumNetworkPolicy", "Namespaced"), { cap: "cilium", keywords: ["cnp"] }),
      res("CiliumClusterwideNetworkPolicies", r("cilium.io", "v2", "ciliumclusterwidenetworkpolicies", "CiliumClusterwideNetworkPolicy", "Cluster"), { cap: "cilium", keywords: ["ccnp"] }),
    ],
  },
  {
    id: "storage", label: "Storage", destinations: [
      res("PersistentVolumeClaims", r("", "v1", "persistentvolumeclaims", "PersistentVolumeClaim", "Namespaced"), { keywords: ["pvc"] }),
      res("PersistentVolumes", r("", "v1", "persistentvolumes", "PersistentVolume", "Cluster"), { keywords: ["pv"] }),
      res("StorageClasses", r("storage.k8s.io", "v1", "storageclasses", "StorageClass", "Cluster")),
      res("CSI Drivers", r("storage.k8s.io", "v1", "csidrivers", "CSIDriver", "Cluster")),
      res("CSI Nodes", r("storage.k8s.io", "v1", "csinodes", "CSINode", "Cluster")),
      res("Volume Attachments", r("storage.k8s.io", "v1", "volumeattachments", "VolumeAttachment", "Cluster")),
    ],
  },
  {
    id: "access", label: "Access", destinations: [
      task("access", "Access check", section("access"), { keywords: ["can-i", "rbac"] }),
      res("Service Accounts", r("", "v1", "serviceaccounts", "ServiceAccount", "Namespaced")),
      res("Cluster Roles", r("rbac.authorization.k8s.io", "v1", "clusterroles", "ClusterRole", "Cluster")),
      res("Roles", r("rbac.authorization.k8s.io", "v1", "roles", "Role", "Namespaced")),
      res("Cluster Role Bindings", r("rbac.authorization.k8s.io", "v1", "clusterrolebindings", "ClusterRoleBinding", "Cluster")),
      res("Role Bindings", r("rbac.authorization.k8s.io", "v1", "rolebindings", "RoleBinding", "Namespaced")),
      res("CSRs", r("certificates.k8s.io", "v1", "certificatesigningrequests", "CertificateSigningRequest", "Cluster"), { keywords: ["certificate signing requests"] }),
    ],
  },
  {
    id: "flux", label: "Flux CD", cap: "flux", lens: "group", destinations: [
      task("flux", "Reconciliation", section("gitops", { fluxView: "reconciliation" }), { keywords: ["flux", "kustomizations", "helmreleases", "gitops"] }),
      task("flux-sources", "Sources", section("gitops", { fluxView: "sources" }), { keywords: ["flux", "gitrepositories", "ocirepositories"] }),
      task("flux-operator", "Operator", section("gitops", { fluxView: "operator" }), { cap: "fluxOperator", keywords: ["flux operator", "fluxinstance", "fluxreport", "resourceset", "resourcesetinputprovider", "input provider"] }),
      task("flux-images", "Images", section("gitops", { fluxView: "images" }), { cap: "fluxImages", keywords: ["image", "image repository", "image policy", "image update", "image automation", "imagerepository", "imagepolicy", "imageupdateautomation"] }),
      res("Kustomizations", r("kustomize.toolkit.fluxcd.io", "v1", "kustomizations", "Kustomization", "Namespaced")),
      res("HelmReleases", r("helm.toolkit.fluxcd.io", "v2", "helmreleases", "HelmRelease", "Namespaced")),
      res("GitRepositories", r("source.toolkit.fluxcd.io", "v1", "gitrepositories", "GitRepository", "Namespaced")),
      res("HelmRepositories", r("source.toolkit.fluxcd.io", "v1", "helmrepositories", "HelmRepository", "Namespaced")),
      res("HelmCharts", r("source.toolkit.fluxcd.io", "v1", "helmcharts", "HelmChart", "Namespaced")),
      res("OCIRepositories", r("source.toolkit.fluxcd.io", "v1beta2", "ocirepositories", "OCIRepository", "Namespaced")),
      res("Buckets", r("source.toolkit.fluxcd.io", "v1", "buckets", "Bucket", "Namespaced")),
      res("Providers", r("notification.toolkit.fluxcd.io", "v1beta3", "providers", "Provider", "Namespaced")),
      res("Alerts", r("notification.toolkit.fluxcd.io", "v1beta3", "alerts", "Alert", "Namespaced")),
      res("Receivers", r("notification.toolkit.fluxcd.io", "v1", "receivers", "Receiver", "Namespaced")),
    ],
  },
  {
    id: "argo", label: "Argo CD", cap: "argo", lens: "group", destinations: [
      task("argo", "Applications", section("argo", { argoView: "applications" }), { keywords: ["argo", "sync"] }),
      task("argo-projects", "Projects", section("argo", { argoView: "projects" }), { keywords: ["argo", "appprojects"] }),
      task("argo-appsets", "ApplicationSets", section("argo", { argoView: "applicationsets" }), { keywords: ["argo"] }),
      res("Applications", r("argoproj.io", "v1alpha1", "applications", "Application", "Namespaced")),
      res("AppProjects", r("argoproj.io", "v1alpha1", "appprojects", "AppProject", "Namespaced")),
      res("ApplicationSets", r("argoproj.io", "v1alpha1", "applicationsets", "ApplicationSet", "Namespaced")),
    ],
  },
  {
    id: "certs", label: "cert-manager", cap: "certManager", lens: "group", destinations: [
      task("certs", "Certificates", section("certs"), { keywords: ["cert-manager", "tls", "issuance"] }),
      res("Certificates", r("cert-manager.io", "v1", "certificates", "Certificate", "Namespaced")),
      res("CertificateRequests", r("cert-manager.io", "v1", "certificaterequests", "CertificateRequest", "Namespaced")),
      res("Orders", r("acme.cert-manager.io", "v1", "orders", "Order", "Namespaced")),
      res("Challenges", r("acme.cert-manager.io", "v1", "challenges", "Challenge", "Namespaced")),
      res("Issuers", r("cert-manager.io", "v1", "issuers", "Issuer", "Namespaced")),
      res("ClusterIssuers", r("cert-manager.io", "v1", "clusterissuers", "ClusterIssuer", "Cluster")),
    ],
  },
  {
    id: "eso", label: "External Secrets", cap: "externalSecrets", lens: "group", destinations: [
      task("eso", "External secrets", section("externalsecrets"), { keywords: ["eso", "secret sync"] }),
      res("ExternalSecrets", r("external-secrets.io", "v1", "externalsecrets", "ExternalSecret", "Namespaced")),
      res("SecretStores", r("external-secrets.io", "v1", "secretstores", "SecretStore", "Namespaced")),
      res("ClusterSecretStores", r("external-secrets.io", "v1", "clustersecretstores", "ClusterSecretStore", "Cluster")),
    ],
  },
  {
    id: "helm", label: "Helm", lens: "group", destinations: [
      task("helm", "Releases", section("helm"), { keywords: ["helm", "charts", "rollback"] }),
    ],
  },
  {
    id: "api", label: "API browser", destinations: [
      task("crds", "API browser", section("crds"), { keywords: ["custom resources", "crd", "crds by api group"], indexLabel: "API browser" }),
      task("builtins", "Resources", section("resources"), { keywords: ["kinds", "gvr", "built-in resources"], indexLabel: "Resources" }),
    ],
  },
];

// ---- capability ------------------------------------------------------------

// present / absent come from detection; "checking" while detection has not
// run (the cluster is still connecting) and "unknown" when the cluster cannot
// be read - neither may be shown as absent.
export type CapState = "present" | "absent" | "checking" | "unknown";

export function capState(c: ClusterDTO | undefined, cap: CapKey | undefined): CapState {
  if (!cap) return "present";
  if (!c) return "unknown";
  if (c.state === "Connecting") return "checking";
  if (c.state !== "Synced" && c.state !== "Degraded" && c.state !== "Stale") return "unknown";
  const present: Record<CapKey, boolean> = {
    flux: c.fluxPresent ?? false,
    fluxOperator: c.fluxOperatorPresent ?? false,
    fluxImages: c.fluxImagesPresent ?? false,
    argo: c.argoPresent ?? false,
    certManager: c.certManagerPresent ?? false,
    externalSecrets: c.externalSecretsPresent ?? false,
    gatewayAPI: Boolean(c.gatewayAPIVersion),
    karpenter: c.karpenterPresent ?? false,
    hubble: c.hasHubble ?? false,
    cilium: c.ciliumPresent ?? false,
  };
  return present[cap] ? "present" : "absent";
}

// A group is offered unless its own capability is definitively absent; a
// destination additionally needs its own capability. Checking/unknown stay
// listed (disabled, with the reason) - never silently missing.
export function groupState(c: ClusterDTO | undefined, g: NavGroup): CapState {
  const own = capState(c, g.cap);
  if (own !== "present") return own;
  const states = g.destinations.map((d) => capState(c, d.cap));
  if (states.includes("present")) return "present";
  if (states.includes("checking")) return "checking";
  if (states.includes("unknown")) return "unknown";
  return "absent";
}

// A destination needs its group's capability and its own.
export function destState(c: ClusterDTO | undefined, g: NavGroup, d: Destination): CapState {
  const own = capState(c, g.cap);
  return own !== "present" ? own : capState(c, d.cap);
}

export const CAP_STATE_TEXT: Record<Exclude<CapState, "present">, string> = {
  absent: "not installed",
  checking: "checking capability…",
  unknown: "cluster unreadable - capability unknown",
};

// ---- route identity ---------------------------------------------------------

export function sameRef(a: ResourceRef, b: ResourceRef): boolean {
  return a.group === b.group && a.version === b.version && a.plural === b.plural && a.kind === b.kind && a.scope === b.scope;
}

function sameKinds(filter: Record<WorkloadKind, boolean>, kinds: readonly WorkloadKind[]): boolean {
  const want = new Set(kinds.length === 0 ? WORKLOAD_KINDS : kinds);
  return WORKLOAD_KINDS.every((k) => filter[k] === want.has(k));
}

// isActive: does the route (plus the workloads kind filter, which is part of
// the All workloads / Deployments destinations' identity) show this
// destination?
export function isActive(route: Route, d: Destination, kindFilter: Record<WorkloadKind, boolean>): boolean {
  if (route.name !== "cluster") return false;
  const t = d.target;
  if (t.kind === "resource") return route.resource !== undefined && sameRef(route.resource, t.ref);
  if (route.section !== t.section || route.resource !== undefined) return false;
  if (t.section === "workloads" && t.workloadKinds) return sameKinds(kindFilter, t.workloadKinds);
  if (t.section === "argo") return (route.argoView ?? "applications") === (t.argoView ?? "applications");
  if (t.section === "gitops") return (route.fluxView ?? "reconciliation") === (t.fluxView ?? "reconciliation");
  return true;
}

// activeLocation: the destination the route shows and the group that owns it.
// A route no destination matches exactly (a custom workloads kind mix, an
// instance opened from the CRD browser) still resolves its owning group, so
// the band never loses its place.
export function activeLocation(route: Route, kindFilter: Record<WorkloadKind, boolean>): { group: NavGroup | undefined; destination: Destination | undefined } {
  if (route.name !== "cluster") return { group: undefined, destination: undefined };
  for (const g of NAV_GROUPS) {
    const d = g.destinations.find((x) => isActive(route, x, kindFilter));
    if (d) return { group: g, destination: d };
  }
  const owner = NAV_GROUPS.find((g) => g.destinations.some((d) => d.target.kind === "section" && d.target.section === route.section && (route.resource === undefined || route.section === "crds" || route.section === "resources")));
  return { group: owner, destination: undefined };
}

// ---- navigation --------------------------------------------------------------

type Store = ReturnType<typeof useFleet.getState>;
export type NavActions = Pick<Store, "route" | "openCluster" | "openResource" | "openWorkloads" | "openArgo" | "openFlux" | "setSection">;

// navigate opens a destination on a cluster. Switching cluster is explicit:
// the caller names the cluster; the current one is never assumed for another.
// The store is injectable so the palette's command index stays testable.
export function navigate(cluster: string, d: Destination, nav: NavActions = useFleet.getState()): void {
  if (nav.route.name !== "cluster" || nav.route.cluster !== cluster) nav.openCluster(cluster);
  const t = d.target;
  if (t.kind === "resource") { nav.openResource(t.ref); return; }
  if (t.section === "workloads" && t.workloadKinds) nav.openWorkloads(t.workloadKinds);
  else if (t.section === "argo") nav.openArgo(t.argoView ?? "applications");
  else if (t.section === "gitops") nav.openFlux(t.fluxView ?? "reconciliation");
  else nav.setSection(t.section);
}

// A cluster can be navigated to when it is connected (Stale keeps its
// last-good capability detection).
export const connected = (c: ClusterDTO) => c.state === "Synced" || c.state === "Degraded" || c.state === "Stale";

// The first destination a group selection opens: its first available task,
// else its first available resource (Config and Storage have no tasks).
export function entryOf(c: ClusterDTO | undefined, g: NavGroup): Destination | undefined {
  const ok = g.destinations.filter((d) => destState(c, g, d) === "present");
  return ok.find((d) => d.type === "task") ?? ok[0];
}
