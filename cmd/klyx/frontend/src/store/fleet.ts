import { create } from "zustand";

export type ClusterDTO = {
  name: string;
  state: string;
  reason: string;
  nodesReady: number;
  nodesTotal: number;
  pods: number;
  version: string;
  gitopsTier: string;
  gitopsReason: string;
  fluxPresent?: boolean;
  fluxHealthy?: boolean;
  networkTier: string;
  networkReason: string;
  gatewayAPIVersion?: string;
  ciliumPresent?: boolean;
  clusterMesh?: boolean;
  certManagerTier?: string;
  certManagerReason?: string;
  certManagerPresent?: boolean;
  certManagerHealthy?: boolean;
  env: string;
  region: string;
  provider: string;
  group: string;
  protected?: boolean;
  ageSeconds: number;
};

export type FluxResourceDTO = {
  kind: string;
  namespace: string;
  name: string;
  ready: string;
  reason: string;
  message: string;
  revision: string;
  lastAppliedAgeSeconds: number;
  suspended: boolean;
  sourceKind: string;
  sourceName: string;
};

export type ForwardDTO = {
  id: string;
  cluster: string;
  namespace: string;
  targetKind: string; // "Pod" | "Service"
  targetName: string;
  localPort: number;
  targetPort: number;
  startedUnix: number;
  status: string; // "active" | "broken"
};

// FleetBoardEntry enriches one fleet card with health-board rows. Fields stay
// nullable so a failed read says unreadable instead of fabricating a zero.
export type FleetBoardEntry = {
  cpuFraction: number | null;
  memFraction: number | null;
  workloadsTotal?: number | null;
  broken: number | null;
  flux: { total: number; notReady: number } | null;
  argo: { total: number; broken: number } | null;
  gateway?: { served: boolean; gateways: number; routes: number | null; brokenRoutes: number | null; unprogrammed: number } | null;
};

// LensCount is one triage-tape lens: a real count, "absent" (the tool is not
// installed - expected, excluded from the tape), or "unreadable" (the fetch
// failed - the tape must say so rather than implying quiet).
export type LensCount = number | "absent" | "unreadable";
export type TapeCounts = {
  workloads: LensCount;
  pods: LensCount;
  events: LensCount;
  nodes: LensCount;
  helm: LensCount;
  flux: LensCount;
  argo: LensCount;
};
type TapeSlice = { cluster: string | null; loading: boolean; counts: TapeCounts };
const emptyTapeCounts: TapeCounts = { workloads: "unreadable", pods: "unreadable", events: "unreadable", nodes: "unreadable", helm: "unreadable", flux: "unreadable", argo: "unreadable" };

export type ArgoConditionDTO = { type: string; message: string };
export type ArgoAppDTO = {
  namespace: string; name: string; project: string;
  syncStatus: string; healthStatus: string; broken: boolean;
  revision: string; repoURL: string; path: string; chart: string; targetRevision: string;
  extraSources: number; destNamespace: string; autoSync: boolean;
  opPhase: string; opMessage: string;
  conditions: ArgoConditionDTO[]; reconciledUnix: number;
};
export type ArgoResultDTO = { available: boolean; message?: string; apps: ArgoAppDTO[] };

export type ConditionDTO = { type: string; status: string; reason: string; message: string };
export type InventoryEntryDTO = { group: string; version: string; kind: string; namespace: string; name: string };
export type FluxSourceDTO = {
  kind: string;
  namespace: string;
  name: string;
  ready: string;
  reason: string;
  message: string;
  revision: string;
  url: string;
  suspended: boolean;
};
export type DependencyRefDTO = { namespace: string; name: string };
export type ResourceDetailDTO = {
  kind: string;
  namespace: string;
  name: string;
  suspended?: boolean;
  reason?: string;
  appliedRevision: string;
  attemptedRevision: string;
  applyFailed: boolean;
  conditions: ConditionDTO[];
  inventory: InventoryEntryDTO[];
  source?: FluxSourceDTO | null;
  dependsOn?: DependencyRefDTO[];
  events?: EventRowDTO[];
};

export type GitOpsSlice = {
  cluster: string | null;
  resources: FluxResourceDTO[];
  sources: FluxSourceDTO[];
  loading: boolean;
  expandedKey: string | null;
  detail: ResourceDetailDTO | null;
};

// "observability" removed 2026-06-10: metrics ship inline in overview/workloads/network lanes
// (design principle 5); a dedicated surface can return when it has real content.
export type ClusterSection = "overview" | "gitops" | "argo" | "helm" | "network" | "resources" | "crds" | "workloads" | "pods" | "events" | "nodes";

export type OwnerDTO = { kind: string; namespace: string; name: string };
export type PodDTO = { name: string; ready: boolean; restarts: number; reason: string; node: string; ageSeconds: number };
export type ResourceCellDTO = { usage: number | null; request: number | null; limit: number | null };
export type WorkloadResourcesDTO = { cpu: ResourceCellDTO; mem: ResourceCellDTO };
export type WorkloadDTO = { kind: string; namespace: string; name: string; desired: number; ready: number; available: number; updated: number; restarts: number; reason: string; rank: "unhealthy"|"degraded"|"restarts"|"healthy"; gitops: OwnerDTO | null; pods: PodDTO[]; resources: WorkloadResourcesDTO };
export type WorkloadUsageDTO = { cpuUsage: number | null; memUsage: number | null };
export type WorkloadMetricsStatusDTO = { available: boolean; message: string; updatedAt: string };
export type WorkloadMetricsResultDTO = { status: WorkloadMetricsStatusDTO; usage: Record<string, WorkloadUsageDTO> };
export type WorkloadsResultDTO = { fluxPresent: boolean; namespaces: string[]; workloads: WorkloadDTO[] };
export type WorkloadKind = "Deployment" | "StatefulSet" | "DaemonSet";
export type WorkloadsSlice = {
  cluster: string | null;
  namespace: string;        // "" = all
  items: WorkloadDTO[];
  namespaces: string[];
  fluxPresent: boolean;
  loading: boolean;
  kindFilter: Record<WorkloadKind, boolean>;
  needsAttention: boolean;
  search: string;
  expanded: string[];       // keys "<kind>/<namespace>/<name>"
  metricsAvailable: boolean;
  metricsStatus: WorkloadMetricsStatusDTO | null;
  metricsStale: boolean;
  nearLimitSort: boolean;
  live: boolean;
};

export type ContainerPortDTO = { name: string; port: number; protocol: string };
// ports is optional in the type (older fixtures omit it); the backend always sends it.
export type ContainerSummaryDTO = { name: string; image: string; ready: boolean; restarts: number; state: string; init: boolean; ports?: ContainerPortDTO[] };
export type PodSummaryDTO = { namespace: string; name: string; ready: boolean; phase: string; reason: string; rank: "unhealthy"|"degraded"|"restarts"|"healthy"; restarts: number; node: string; ip: string; ownerKind: string; ownerName: string; ageSeconds: number; containers: ContainerSummaryDTO[] };
export type PodsResultDTO = { namespaces: string[]; pods: PodSummaryDTO[] };
export type PodDetailDTO = { summary: PodSummaryDTO; labels: Record<string, string>; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; qosClass: string; serviceAccount: string };

export type PodRef = { namespace: string; name: string };
export type PodsSlice = {
  cluster: string | null;
  namespace: string;
  items: PodSummaryDTO[];
  namespaces: string[];
  loading: boolean;
  needsAttention: boolean;
  search: string;
  selected: PodRef | null;
  detail: PodDetailDTO | null;
  detailLoading: boolean;
  live: boolean;
};

export type EventRowDTO = { type: "Normal" | "Warning"; reason: string; message: string; count: number; namespace: string; kind: string; name: string; lastSeenUnix: number; firstSeenUnix: number };
export type EventsResultDTO = { namespaces: string[]; events: EventRowDTO[] };
export type EventsSlice = {
  cluster: string | null;
  namespace: string;
  items: EventRowDTO[];
  namespaces: string[];
  loading: boolean;
  warningsOnly: boolean;
  search: string;
  live: boolean;
};

export type ResourceRef = { group: string; version: string; plural: string; kind: string; scope: string };
export type InstanceDTO = { namespace: string; name: string; created: string; fields?: Record<string, string> };
export type InstancesSlice = { ref: ResourceRef | null; rows: InstanceDTO[]; nextToken: string; loading: boolean; filter: string; riskOnly: boolean };

export type InstanceRef = { namespace: string; name: string };
export type EventDTO = { type: string; reason: string; message: string; count: number; lastSeen: string };
export type SecretKeyDTO = { key: string; bytes: number };
export type ServicePortDTO = { name: string; port: number; protocol: string };
export type EndpointAddrDTO = { ip: string; ready: boolean; targetKind: string; targetName: string };
export type ServiceBackingDTO = { ports: ServicePortDTO[]; ready: number; notReady: number; addresses: EndpointAddrDTO[]; selector: Record<string, string> };
export type HPAMetricDTO = { name: string; type: string; target: string; current: string };
export type HPAScalingDTO = { minReplicas: number; maxReplicas: number; currentReplicas: number; desiredReplicas: number; targetKind: string; targetName: string; lastScaleUnix: number; metrics: HPAMetricDTO[] };
export type RelatedRefDTO = { kind: string; namespace: string; name: string; group: string; version: string; plural: string; scope: string; relation: string };
export type InstanceDetailDTO = { kind: string; namespace: string; name: string; created: string; labels: Record<string, string>; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; secretKeys?: SecretKeyDTO[]; serviceBacking?: ServiceBackingDTO | null; hpaScaling?: HPAScalingDTO | null; related?: RelatedRefDTO[] };
export type InstanceDetailSlice = { ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean };

export type Route =
  | { name: "fleet" }
  | { name: "forwards" }
  | { name: "settings" }
  | { name: "cluster"; cluster: string; section: ClusterSection; resource?: ResourceRef; instance?: InstanceRef; gateway?: GatewayRef };

export const SECTION_LABELS: Record<ClusterSection, string> = {
  overview: "Overview",
  // "Flux" not "GitOps": design principle 8 (speak the tool's vocabulary).
  // The view is Flux-specific; an Argo provider would get its own section.
  gitops: "Flux",
  // Argo's own vocabulary throughout (synced/degraded), per design principle 8.
  argo: "Argo CD",
  helm: "Helm",
  network: "Network",
  resources: "Resources",
  crds: "CRDs",
  workloads: "Workloads",
  pods: "Pods",
  events: "Events",
  nodes: "Nodes",
};

export type CRDKindDTO = { kind: string; plural: string; scope: string; version: string; operator: string; shortNames: string[] };
export type CRDGroupDTO = { group: string; category: string; kinds: CRDKindDTO[] };
export type CRDCountDTO = { count: number; capped: boolean };
export type CRDGroupBy = "group" | "operator" | "scope" | "alphabetical";

export type CRDSlice = {
  cluster: string | null;
  groups: CRDGroupDTO[];
  loading: boolean;
  expanded: string[];
  counts: Record<string, CRDCountDTO>;
  groupBy: CRDGroupBy;
  search: string;
  builtinCategory: string | null;
};

export const crdCountKey = (group: string, version: string, plural: string) => `${group}/${version}/${plural}`;

export type GatewayAddressDTO = { type: string; value: string };
export type GatewayRefDTO = { namespace: string; name: string; className: string; addresses?: GatewayAddressDTO[]; listeners?: ListenerDTO[]; accepted: boolean; programmed: boolean };
export type GatewayListDTO = { gatewayAPIServed: boolean; gateways: GatewayRefDTO[] };
export type PolicyDetailDTO = { key: string; value: string };
export type PolicyRefDTO = { kind: string; namespace: string; name: string; targetKind: string; targetNamespace: string; targetName: string; targetSectionName: string; summary: string; details: PolicyDetailDTO[]; inferred: boolean; match: string };
export type ListenerDTO = { name: string; protocol: string; hostname: string; port: number };
export type MatchDTO = { pathType: string; pathValue: string; method: string };
export type BackendDTO = { kind: string; name: string; namespace: string; port: number; weight: number };
export type PodCountDTO = { ready: number; total: number; unknown: boolean };
export type ServiceNodeDTO = { namespace: string; name: string; type: string; port: number; resolved: boolean; global: boolean; meshClusters: string[]; meshUnconfirmed: boolean; policies: PolicyRefDTO[]; cnps: PolicyRefDTO[] };
export type GatewayNodeDTO = { namespace: string; name: string; className: string; listeners: ListenerDTO[]; addresses?: GatewayAddressDTO[]; accepted: boolean; programmed: boolean; policies: PolicyRefDTO[] };
export type RouteNodeDTO = { namespace: string; name: string; hostnames: string[]; matches: MatchDTO[]; accepted: boolean; resolvedRefs: boolean; backends: BackendDTO[]; services: ServiceNodeDTO[]; pods: PodCountDTO; policies: PolicyRefDTO[] };
export type TopologyDTO = { gateway: GatewayNodeDTO; routes: RouteNodeDTO[]; clusterPolicies?: PolicyRefDTO[]; warnings?: string[]; error?: string };
export type GatewayRef = { namespace: string; name: string };

export type RouteMetricDTO = { rps: number | null; p50: number | null; p99: number | null; errRate: number | null };
export type RouteMetricsStatusDTO = { available: boolean; message: string; updatedAt: string };
export type RouteMetricsResultDTO = { status: RouteMetricsStatusDTO; routes: Record<string, RouteMetricDTO> };

export type NetworkSlice = {
  served: boolean;
  gateways: GatewayRefDTO[];
  listLoading: boolean;
  selected: GatewayRef | null;
  topology: TopologyDTO | null;
  topologyLoading: boolean;
  selectedRoute: string | null; // "<ns>/<name>"
  routeMetrics: Record<string, RouteMetricDTO>;
  routeMetricsStatus: RouteMetricsStatusDTO | null;
  routeMetricsStale: boolean;
};

export type MeshNodeDTO = { cluster: string; name: string; clusterId: number | null; state: string; present: boolean };
export type MeshEdgeDTO = { a: string; b: string; mutual: boolean };
export type MeshGraphDTO = { nodes: MeshNodeDTO[]; edges: MeshEdgeDTO[] };

export type MetricsDTO = { available: boolean; mode: string; source: string; warning: string; reason: string; cpuFraction: number | null; memFraction: number | null };
export type MetricsSlice = { cluster: string | null; dto: MetricsDTO | null; loading: boolean };

// Nodes types
export type NodeSummaryDTO = { name: string; roles: string[]; ready: boolean; unschedulable: boolean; problems: string[]; version: string; os: string; arch: string; taintCount: number; cpuCapacity: number; cpuAllocatable: number; memCapacity: number; memAllocatable: number; podCapacity: number; ageSeconds: number };
export type NodesResultDTO = { nodes: NodeSummaryDTO[] };
export type NodeTaintDTO = { key: string; value: string; effect: string };
export type PodOnNodeDTO = { namespace: string; name: string; phase: string };
export type NodeDetailDTO = { summary: NodeSummaryDTO; labels: Record<string, string>; taints: NodeTaintDTO[]; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; podsOnNode: PodOnNodeDTO[] };

export type NodeRef = { name: string };
export type NodesSlice = {
  cluster: string | null;
  items: NodeSummaryDTO[];
  loading: boolean;
  selected: NodeRef | null;
  detail: NodeDetailDTO | null;
  detailLoading: boolean;
};

// ---- Helm types ---------------------------------------------------------------

export type HelmReleaseDTO = {
  name: string;
  namespace: string;
  chart: string;
  appVersion: string;
  status: string;
  revision: number;
  updatedUnix: number;
};

export type HelmHistoryEntryDTO = {
  revision: number;
  status: string;
  chart: string;
  appVersion: string;
  description: string;
  updatedUnix: number;
};

export type HelmRef = { namespace: string; name: string };

export type HelmSlice = {
  cluster: string | null;
  releases: HelmReleaseDTO[];
  available: boolean;
  message: string;
  loading: boolean;
  selected: HelmRef | null;
  history: HelmHistoryEntryDTO[];
  values: string;
  detailLoading: boolean;
};

type ArgoSlice = {
  cluster: string | null;
  apps: ArgoAppDTO[];
  available: boolean;
  message: string;
  loading: boolean;
  expanded: string[]; // "<ns>/<name>" keys
};

// ---- / Helm types -------------------------------------------------------------

export type ActionStatus = { kind: "success" | "error"; message: string };

// Overview triage summary — fetched once per cluster open, never mutated by the view slices.
export type OverviewSummary = {
  cluster: string | null;
  loading: boolean;
  unhealthyWorkloads: number | null;  // null = fetch failed for that tile
  podsNotReady: number | null;
  warningEvents: number | null;
  nodeProblems: number | null;
  failedReleases: number | null;       // null also means helm unavailable (tile hidden)
  helmAvailable: boolean;
  namespaces: number | null;
  // flux is null when Flux is not installed (tile hidden), or when the fetch
  // failed (tile shows "—" via the null count path).
  flux: { present: boolean; notReady: number; suspended: number } | null;
};

type FleetState = {
  clusters: ClusterDTO[];
  setClusters: (c: ClusterDTO[]) => void;
  route: Route;
  openFleet: () => void;
  openForwards: () => void;
  openSettings: () => void;
  newContexts: number;
  setNewContexts: (n: number) => void;
  fleetBoard: Record<string, FleetBoardEntry>;
  setFleetBoardEntry: (cluster: string, e: FleetBoardEntry) => void;
  tape: TapeSlice;
  setTapeLoading: (cluster: string) => void;
  setTape: (cluster: string, counts: TapeCounts) => void;
  clearTape: () => void;
  openCluster: (name: string) => void;
  setSection: (s: ClusterSection) => void;
  openResource: (ref: ResourceRef) => void;
  closeResource: () => void;
  instances: InstancesSlice;
  setInstancesLoading: (ref: ResourceRef) => void;
  setInstancePage: (items: InstanceDTO[], nextToken: string) => void;
  addInstancePage: (items: InstanceDTO[], nextToken: string) => void;
  setInstanceFilter: (s: string) => void;
  setInstanceRiskOnly: (v: boolean) => void;
  toggleInstanceRiskOnly: () => void;
  clearInstances: () => void;
  openInstance: (namespace: string, name: string) => void;
  closeInstance: () => void;
  instanceDetail: InstanceDetailSlice;
  setInstanceDetailLoading: (ref: InstanceRef) => void;
  setInstanceDetail: (d: InstanceDetailDTO) => void;
  clearInstanceDetail: () => void;
  gitops: GitOpsSlice;
  setGitOps: (cluster: string, resources: FluxResourceDTO[], sources: FluxSourceDTO[]) => void;
  setGitOpsLoading: (cluster: string) => void;
  clearGitOps: () => void;
  expand: (key: string) => void;
  collapse: () => void;
  setDetail: (d: ResourceDetailDTO) => void;
  actionStatus: ActionStatus | null;
  setActionStatus: (s: ActionStatus) => void;
  clearActionStatus: () => void;
  forwards: ForwardDTO[];
  setForwards: (f: ForwardDTO[]) => void;
  crd: CRDSlice;
  setCRDs: (cluster: string, groups: CRDGroupDTO[]) => void;
  setCRDLoading: (cluster: string) => void;
  clearCRDs: () => void;
  toggleCRDGroup: (group: string) => void;
  setCRDCount: (key: string, dto: CRDCountDTO) => void;
  setCRDGroupBy: (g: CRDGroupBy) => void;
  setCRDSearch: (s: string) => void;
  setBuiltinCategory: (c: string | null) => void;
  openGateway: (namespace: string, name: string) => void;
  closeGateway: () => void;
  network: NetworkSlice;
  setGatewaysLoading: () => void;
  setGateways: (l: GatewayListDTO) => void;
  setTopologyLoading: (ref: GatewayRef) => void;
  setTopology: (t: TopologyDTO) => void;
  selectRoute: (key: string | null) => void;
  clearNetwork: () => void;
  setRouteMetrics: (result: RouteMetricsResultDTO) => void;
  mesh: MeshGraphDTO | null;
  setMesh: (m: MeshGraphDTO) => void;
  metrics: MetricsSlice;
  setMetricsLoading: (cluster: string) => void;
  setMetrics: (cluster: string, dto: MetricsDTO) => void;
  clearMetrics: () => void;
  workloads: WorkloadsSlice;
  setWorkloadsLoading: (cluster: string, namespace: string) => void;
  setWorkloads: (cluster: string, namespace: string, result: WorkloadsResultDTO) => void;
  toggleWorkloadKind: (k: WorkloadKind) => void;
  toggleNeedsAttention: () => void;
  setWorkloadsNeedsAttention: (v: boolean) => void;
  setWorkloadsSearch: (s: string) => void;
  toggleWorkloadExpand: (key: string) => void;
  setWorkloadUsage: (cluster: string, namespace: string, result: WorkloadMetricsResultDTO) => void;
  toggleNearLimitSort: () => void;
  setWorkloadsLive: (cluster: string, namespace: string, live: boolean) => void;
  clearWorkloads: () => void;
  pods: PodsSlice;
  setPodsLoading: (cluster: string, namespace: string) => void;
  setPods: (cluster: string, namespace: string, result: PodsResultDTO) => void;
  togglePodsNeedsAttention: () => void;
  setPodsNeedsAttention: (v: boolean) => void;
  setPodsSearch: (s: string) => void;
  selectPod: (ref: PodRef | null) => void;
  setPodDetail: (ref: PodRef, detail: PodDetailDTO) => void;
  setPodsLive: (cluster: string, namespace: string, live: boolean) => void;
  clearPods: () => void;
  events: EventsSlice;
  setEventsLoading: (cluster: string, namespace: string) => void;
  setEvents: (cluster: string, namespace: string, result: EventsResultDTO) => void;
  toggleWarningsOnly: () => void;
  setWarningsOnly: (v: boolean) => void;
  setEventsSearch: (s: string) => void;
  setEventsLive: (cluster: string, namespace: string, live: boolean) => void;
  clearEvents: () => void;
  nodes: NodesSlice;
  setNodesLoading: (cluster: string) => void;
  setNodes: (cluster: string, result: NodesResultDTO) => void;
  selectNode: (ref: NodeRef | null) => void;
  setNodeDetail: (ref: NodeRef, detail: NodeDetailDTO) => void;
  clearNodes: () => void;
  helm: HelmSlice;
  argo: ArgoSlice;
  setArgoLoading: (cluster: string) => void;
  setArgo: (cluster: string, r: ArgoResultDTO) => void;
  toggleArgoExpand: (key: string) => void;
  clearArgo: () => void;
  setHelmLoading: (cluster: string) => void;
  setHelm: (cluster: string, available: boolean, message: string, releases: HelmReleaseDTO[]) => void;
  selectHelmRelease: (ref: HelmRef | null) => void;
  setHelmDetail: (ref: HelmRef, history: HelmHistoryEntryDTO[], values: string) => void;
  clearHelm: () => void;
  overviewSummary: OverviewSummary;
  setOverviewSummaryLoading: (cluster: string) => void;
  setOverviewSummary: (s: OverviewSummary) => void;
  clearOverviewSummary: () => void;
};

export const useFleet = create<FleetState>((set) => ({
  clusters: [],
  setClusters: (clusters) => set({ clusters }),
  route: { name: "fleet" },
  openFleet: () => set({ route: { name: "fleet" } }),
  openForwards: () => set({ route: { name: "forwards" } }),
  openSettings: () => set({ route: { name: "settings" } }),
  newContexts: 0,
  setNewContexts: (newContexts) => set({ newContexts }),
  fleetBoard: {},
  setFleetBoardEntry: (cluster, e) => set((s) => ({ fleetBoard: { ...s.fleetBoard, [cluster]: e } })),
  tape: { cluster: null, loading: false, counts: emptyTapeCounts },
  // Re-claiming the SAME cluster keeps the existing counts so a re-poll or
  // manual refresh never flickers the tape back to "checking…".
  setTapeLoading: (cluster) =>
    set((s) => ({
      tape: { cluster, loading: true, counts: s.tape.cluster === cluster ? s.tape.counts : emptyTapeCounts },
    })),
  setTape: (cluster, counts) => set({ tape: { cluster, loading: false, counts } }),
  clearTape: () => set({ tape: { cluster: null, loading: false, counts: emptyTapeCounts } }),
  openCluster: (name) => set({ route: { name: "cluster", cluster: name, section: "overview" } }),
  setSection: (section) =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section } } : {})),
  openResource: (resource) =>
    set((s) => {
      if (s.route.name !== "cluster") return {};
      // Preserve the current section if it is a resource-browsing section; otherwise default to "resources".
      const section: ClusterSection = (s.route.section === "crds" || s.route.section === "resources") ? s.route.section : "resources";
      return {
        route: { name: "cluster", cluster: s.route.cluster, section, resource },
        instances: { ref: resource, rows: [], nextToken: "", loading: true, filter: "", riskOnly: false },
      };
    }),
  closeResource: () =>
    set((s) => {
      if (s.route.name !== "cluster") return {};
      const section: ClusterSection = (s.route.section === "crds" || s.route.section === "resources") ? s.route.section : "resources";
      return { route: { name: "cluster", cluster: s.route.cluster, section } };
    }),
  instances: { ref: null, rows: [], nextToken: "", loading: false, filter: "", riskOnly: false },
  setInstancesLoading: (ref) => set((s) => {
    const sameRef = s.instances.ref?.group === ref.group && s.instances.ref?.version === ref.version && s.instances.ref?.plural === ref.plural;
    return { instances: { ref, rows: [], nextToken: "", loading: true, filter: sameRef ? s.instances.filter : "", riskOnly: sameRef ? s.instances.riskOnly : false } };
  }),
  setInstancePage: (items, nextToken) => set((s) => ({ instances: { ...s.instances, rows: items, nextToken, loading: false } })),
  addInstancePage: (items, nextToken) => set((s) => ({ instances: { ...s.instances, rows: [...s.instances.rows, ...items], nextToken, loading: false } })),
  setInstanceFilter: (filter) => set((s) => ({ instances: { ...s.instances, filter } })),
  setInstanceRiskOnly: (riskOnly) => set((s) => ({ instances: { ...s.instances, riskOnly } })),
  toggleInstanceRiskOnly: () => set((s) => ({ instances: { ...s.instances, riskOnly: !s.instances.riskOnly } })),
  clearInstances: () => set({ instances: { ref: null, rows: [], nextToken: "", loading: false, filter: "", riskOnly: false } }),
  openInstance: (namespace, name) =>
    set((s) => {
      if (s.route.name !== "cluster" || !s.route.resource) return {};
      const section: ClusterSection = (s.route.section === "crds" || s.route.section === "resources") ? s.route.section : "resources";
      return {
        route: { name: "cluster", cluster: s.route.cluster, section, resource: s.route.resource, instance: { namespace, name } },
        instanceDetail: { ref: { namespace, name }, detail: null, loading: true },
      };
    }),
  closeInstance: () =>
    set((s) =>
      s.route.name === "cluster"
        ? { route: { name: "cluster", cluster: s.route.cluster, section: s.route.section, resource: s.route.resource } }
        : {}),
  instanceDetail: { ref: null, detail: null, loading: false },
  setInstanceDetailLoading: (ref) => set({ instanceDetail: { ref, detail: null, loading: true } }),
  setInstanceDetail: (detail) => set((s) => ({ instanceDetail: { ...s.instanceDetail, detail, loading: false } })),
  clearInstanceDetail: () => set({ instanceDetail: { ref: null, detail: null, loading: false } }),
  gitops: { cluster: null, resources: [], sources: [], loading: false, expandedKey: null, detail: null },
  setGitOps: (cluster, resources, sources) => set((s) => ({ gitops: { ...s.gitops, cluster, resources, sources, loading: false } })),
  setGitOpsLoading: (cluster) => set((s) => ({ gitops: { ...s.gitops, cluster, resources: [], sources: [], loading: true } })),
  clearGitOps: () => set({ gitops: { cluster: null, resources: [], sources: [], loading: false, expandedKey: null, detail: null } }),
  expand: (key) => set((s) => ({ gitops: { ...s.gitops, expandedKey: key, detail: null } })),
  collapse: () => set((s) => ({ gitops: { ...s.gitops, expandedKey: null, detail: null } })),
  setDetail: (d) => set((s) => ({ gitops: { ...s.gitops, detail: d } })),
  actionStatus: null,
  setActionStatus: (actionStatus) => set({ actionStatus }),
  clearActionStatus: () => set({ actionStatus: null }),
  forwards: [],
  setForwards: (forwards) => set({ forwards }),
  crd: { cluster: null, groups: [], loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null },
  setCRDs: (cluster, groups) => set((s) => ({ crd: { ...s.crd, cluster, groups, loading: false } })),
  setCRDLoading: (cluster) => set((s) => ({ crd: { ...s.crd, cluster, groups: [], loading: true, expanded: [], counts: {} } })),
  clearCRDs: () => set({ crd: { cluster: null, groups: [], loading: false, expanded: [], counts: {}, groupBy: "group", search: "", builtinCategory: null } }),
  toggleCRDGroup: (group) => set((s) => ({
    crd: { ...s.crd, expanded: s.crd.expanded.includes(group) ? s.crd.expanded.filter((g) => g !== group) : [...s.crd.expanded, group] },
  })),
  setCRDCount: (key, dto) => set((s) => ({ crd: { ...s.crd, counts: { ...s.crd.counts, [key]: dto } } })),
  setCRDGroupBy: (groupBy) => set((s) => ({ crd: { ...s.crd, groupBy } })),
  setCRDSearch: (search) => set((s) => ({ crd: { ...s.crd, search } })),
  setBuiltinCategory: (builtinCategory) => set((s) => ({ crd: { ...s.crd, builtinCategory } })),
  openGateway: (namespace, name) =>
    set((s) =>
      s.route.name === "cluster"
        ? {
            route: { name: "cluster", cluster: s.route.cluster, section: "network", gateway: { namespace, name } },
            network: { ...s.network, selected: { namespace, name }, topology: null, topologyLoading: true, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
          }
        : {}),
  closeGateway: () =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section: "network" } } : {})),
  network: { served: false, gateways: [], listLoading: false, selected: null, topology: null, topologyLoading: false, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
  setGatewaysLoading: () => set((s) => ({ network: { ...s.network, listLoading: true } })),
  setGateways: (l) => set((s) => ({ network: { ...s.network, served: l.gatewayAPIServed, gateways: l.gateways ?? [], listLoading: false } })),
  setTopologyLoading: (ref) => set((s) => ({ network: { ...s.network, selected: ref, topology: null, topologyLoading: true, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false } })),
  setTopology: (topology) => set((s) => ({ network: { ...s.network, topology, topologyLoading: false } })),
  selectRoute: (selectedRoute) => set((s) => ({ network: { ...s.network, selectedRoute } })),
  clearNetwork: () => set((s) => ({ network: { ...s.network, selected: null, topology: null, topologyLoading: false, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false } })),
  setRouteMetrics: (result) =>
    set((s) => {
      if (result.status.available) {
        return { network: { ...s.network, routeMetrics: result.routes ?? {}, routeMetricsStatus: result.status, routeMetricsStale: false } };
      }
      // transient failure: keep last-good numbers + last-good updatedAt, mark stale
      const keptUpdatedAt = s.network.routeMetricsStatus?.updatedAt ?? "";
      return {
        network: {
          ...s.network,
          // keep s.network.routeMetrics as-is (do NOT blank)
          routeMetricsStatus: { ...result.status, updatedAt: keptUpdatedAt },
          routeMetricsStale: Object.keys(s.network.routeMetrics).length > 0,
        },
      };
    }),
  mesh: null,
  setMesh: (mesh) => set({ mesh }),
  metrics: { cluster: null, dto: null, loading: false },
  setMetricsLoading: (cluster) => set((s) => ({ metrics: { cluster, dto: s.metrics.cluster === cluster ? s.metrics.dto : null, loading: true } })),
  setMetrics: (cluster, dto) => set({ metrics: { cluster, dto, loading: false } }),
  clearMetrics: () => set({ metrics: { cluster: null, dto: null, loading: false } }),
  workloads: { cluster: null, namespace: "", items: [], namespaces: [], fluxPresent: false, loading: false,
    kindFilter: { Deployment: true, StatefulSet: true, DaemonSet: true }, needsAttention: false, search: "", expanded: [],
    metricsAvailable: false, metricsStatus: null, metricsStale: false, nearLimitSort: false, live: false },
  setWorkloadsLoading: (cluster, namespace) => set((s) => ({ workloads: { ...s.workloads, cluster, namespace, loading: true } })),
  setWorkloads: (cluster, namespace, result) => set((s) => {
    // namespace-list preservation: replace only on all-load; fallback to [namespace] if empty.
    let namespaces = s.workloads.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    return { workloads: { ...s.workloads, cluster, namespace, items: result.workloads ?? [], namespaces, fluxPresent: result.fluxPresent, loading: false } };
  }),
  toggleWorkloadKind: (k) => set((s) => ({ workloads: { ...s.workloads, kindFilter: { ...s.workloads.kindFilter, [k]: !s.workloads.kindFilter[k] } } })),
  toggleNeedsAttention: () => set((s) => ({ workloads: { ...s.workloads, needsAttention: !s.workloads.needsAttention } })),
  setWorkloadsNeedsAttention: (v) => set((s) => ({ workloads: { ...s.workloads, needsAttention: v } })),
  setWorkloadsSearch: (search) => set((s) => ({ workloads: { ...s.workloads, search } })),
  toggleWorkloadExpand: (key) => set((s) => ({ workloads: { ...s.workloads, expanded: s.workloads.expanded.includes(key) ? s.workloads.expanded.filter((k) => k !== key) : [...s.workloads.expanded, key] } })),
  setWorkloadUsage: (cluster, namespace, result) =>
    set((s) => {
      if (s.workloads.cluster !== cluster || s.workloads.namespace !== namespace) return {};
      if (result.status.available) {
        const items = s.workloads.items.map((w) => {
          const u = result.usage[`${w.kind}/${w.namespace}/${w.name}`];
          if (!u) return w;
          return { ...w, resources: {
            cpu: { ...w.resources.cpu, usage: u.cpuUsage },
            mem: { ...w.resources.mem, usage: u.memUsage },
          } };
        });
        return { workloads: { ...s.workloads, items, metricsAvailable: true, metricsStatus: result.status, metricsStale: false } };
      }
      const keptUpdatedAt = s.workloads.metricsStatus?.updatedAt ?? "";
      return { workloads: { ...s.workloads, metricsStatus: { ...result.status, updatedAt: keptUpdatedAt }, metricsStale: s.workloads.metricsAvailable } };
    }),
  toggleNearLimitSort: () => set((s) => ({ workloads: { ...s.workloads, nearLimitSort: !s.workloads.nearLimitSort } })),
  setWorkloadsLive: (cluster, namespace, live) =>
    set((s) => {
      if (s.workloads.cluster !== cluster || s.workloads.namespace !== namespace) return {};
      return { workloads: { ...s.workloads, live } };
    }),
  clearWorkloads: () => set((s) => ({ workloads: { ...s.workloads, cluster: null, items: [], namespaces: [], expanded: [], needsAttention: false, search: "", namespace: "", kindFilter: { Deployment: true, StatefulSet: true, DaemonSet: true }, metricsAvailable: false, metricsStatus: null, metricsStale: false, nearLimitSort: false, live: false } })),
  pods: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false, live: false },
  setPodsLoading: (cluster, namespace) => set((s) => ({ pods: { ...s.pods, cluster, namespace, loading: true } })),
  setPods: (cluster, namespace, result) => set((s) => {
    let namespaces = s.pods.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    return { pods: { ...s.pods, cluster, namespace, items: result.pods ?? [], namespaces, loading: false } };
  }),
  togglePodsNeedsAttention: () => set((s) => ({ pods: { ...s.pods, needsAttention: !s.pods.needsAttention } })),
  setPodsNeedsAttention: (v) => set((s) => ({ pods: { ...s.pods, needsAttention: v } })),
  setPodsSearch: (search) => set((s) => ({ pods: { ...s.pods, search } })),
  selectPod: (ref) => set((s) => ({ pods: { ...s.pods, selected: ref, detail: null, detailLoading: ref !== null } })),
  setPodDetail: (ref, detail) => set((s) => {
    const sel = s.pods.selected;
    if (!sel || sel.namespace !== ref.namespace || sel.name !== ref.name) return {};
    return { pods: { ...s.pods, detail, detailLoading: false } };
  }),
  setPodsLive: (cluster, namespace, live) =>
    set((s) => {
      if (s.pods.cluster !== cluster || s.pods.namespace !== namespace) return {};
      return { pods: { ...s.pods, live } };
    }),
  clearPods: () => set({ pods: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false, live: false } }),
  events: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, warningsOnly: false, search: "", live: false },
  setEventsLoading: (cluster, namespace) => set((s) => ({ events: { ...s.events, cluster, namespace, loading: true } })),
  setEvents: (cluster, namespace, result) => set((s) => {
    let namespaces = s.events.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    return { events: { ...s.events, cluster, namespace, items: result.events ?? [], namespaces, loading: false } };
  }),
  toggleWarningsOnly: () => set((s) => ({ events: { ...s.events, warningsOnly: !s.events.warningsOnly } })),
  setWarningsOnly: (v) => set((s) => ({ events: { ...s.events, warningsOnly: v } })),
  setEventsSearch: (search) => set((s) => ({ events: { ...s.events, search } })),
  setEventsLive: (cluster, namespace, live) =>
    set((s) => {
      if (s.events.cluster !== cluster || s.events.namespace !== namespace) return {};
      return { events: { ...s.events, live } };
    }),
  clearEvents: () => set({ events: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, warningsOnly: false, search: "", live: false } }),
  nodes: { cluster: null, items: [], loading: false, selected: null, detail: null, detailLoading: false },
  setNodesLoading: (cluster) => set((s) => ({ nodes: { ...s.nodes, cluster, loading: true } })),
  setNodes: (cluster, result) => set((s) => ({ nodes: { ...s.nodes, cluster, items: result.nodes ?? [], loading: false } })),
  selectNode: (ref) => set((s) => ({ nodes: { ...s.nodes, selected: ref, detail: null, detailLoading: ref !== null } })),
  setNodeDetail: (ref, detail) => set((s) => {
    const sel = s.nodes.selected;
    if (!sel || sel.name !== ref.name) return {};
    return { nodes: { ...s.nodes, detail, detailLoading: false } };
  }),
  clearNodes: () => set({ nodes: { cluster: null, items: [], loading: false, selected: null, detail: null, detailLoading: false } }),
  helm: { cluster: null, releases: [], available: true, message: "", loading: false, selected: null, history: [], values: "", detailLoading: false },
  argo: { cluster: null, apps: [], available: true, message: "", loading: false, expanded: [] },
  setArgoLoading: (cluster) => set((s) => ({ argo: { ...s.argo, cluster, loading: true } })),
  setArgo: (cluster, r) => set((s) => ({ argo: { ...s.argo, cluster, apps: r.apps ?? [], available: r.available, message: r.message ?? "", loading: false } })),
  toggleArgoExpand: (key) => set((s) => ({ argo: { ...s.argo, expanded: s.argo.expanded.includes(key) ? s.argo.expanded.filter((k) => k !== key) : [...s.argo.expanded, key] } })),
  clearArgo: () => set({ argo: { cluster: null, apps: [], available: true, message: "", loading: false, expanded: [] } }),
  setHelmLoading: (cluster) => set((s) => ({ helm: { ...s.helm, cluster, loading: true } })),
  setHelm: (cluster, available, message, releases) => set((s) => ({ helm: { ...s.helm, cluster, available, message, releases, loading: false } })),
  selectHelmRelease: (ref) => set((s) => ({ helm: { ...s.helm, selected: ref, history: [], values: "", detailLoading: ref !== null } })),
  setHelmDetail: (ref, history, values) => set((s) => {
    const sel = s.helm.selected;
    if (!sel || sel.namespace !== ref.namespace || sel.name !== ref.name) return {};
    return { helm: { ...s.helm, history, values, detailLoading: false } };
  }),
  clearHelm: () => set({ helm: { cluster: null, releases: [], available: true, message: "", loading: false, selected: null, history: [], values: "", detailLoading: false } }),
  overviewSummary: { cluster: null, loading: false, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, failedReleases: null, helmAvailable: false, namespaces: null, flux: null },
  setOverviewSummaryLoading: (cluster) => set({ overviewSummary: { cluster, loading: true, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, failedReleases: null, helmAvailable: false, namespaces: null, flux: null } }),
  setOverviewSummary: (s) => set({ overviewSummary: s }),
  clearOverviewSummary: () => set({ overviewSummary: { cluster: null, loading: false, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, failedReleases: null, helmAvailable: false, namespaces: null, flux: null } }),
}));
