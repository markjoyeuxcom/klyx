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
  argoPresent?: boolean;
  networkTier: string;
  networkReason: string;
  gatewayAPIVersion?: string;
  ciliumPresent?: boolean;
  clusterMesh?: boolean;
  hasHubble?: boolean;
  certManagerTier?: string;
  certManagerReason?: string;
  certManagerPresent?: boolean;
  certManagerHealthy?: boolean;
  externalSecretsTier?: string;
  externalSecretsReason?: string;
  externalSecretsPresent?: boolean;
  externalSecretsHealthy?: boolean;
  reloaderTier?: string;
  reloaderReason?: string;
  reloaderPresent?: boolean;
  reloaderHealthy?: boolean;
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
  readySinceSeconds: number;
  suspended: boolean;
  sourceKind: string;
  sourceName: string;
  // stale: spec changed, controller has not observed it - ready reflects the
  // previous spec; render the row as transitioning ("dim = not yet realized").
  stale?: boolean;
  generation?: number;
  observedGeneration?: number;
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
export type ArgoResourceDTO = { group: string; kind: string; namespace: string; name: string; status: string; health: string };
export type ArgoResourceRuleDTO = { group: string; kind: string };
export type ArgoProjectDTO = {
  namespace: string; name: string; description: string;
  sourceRepos: string[]; destinations: string[]; roles: number; roleNames?: string[]; orphanWarn: boolean;
  clusterResourceWhitelist?: ArgoResourceRuleDTO[]; clusterResourceBlacklist?: ArgoResourceRuleDTO[];
  namespaceResourceWhitelist?: ArgoResourceRuleDTO[]; namespaceResourceBlacklist?: ArgoResourceRuleDTO[];
};
export type ArgoAppSetConditionDTO = { type: string; status: string; reason: string; message: string };
export type ArgoApplicationSetDTO = {
  namespace: string; name: string; generators: string[]; templateName: string;
  project: string; repoURL: string; path: string; targetRevision: string; destNamespace: string;
  destServer?: string; generatedApps: number; generatedNames?: string[]; healthy: boolean; conditions: ArgoAppSetConditionDTO[];
};
export type ArgoAppDTO = {
  namespace: string; name: string; project: string;
  syncStatus: string; healthStatus: string; broken: boolean;
  revision: string; repoURL: string; path: string; chart: string; targetRevision: string;
  extraSources: number; destNamespace: string; applicationSet?: string; autoSync: boolean;
  opPhase: string; opMessage: string;
  conditions: ArgoConditionDTO[]; resources?: ArgoResourceDTO[]; reconciledUnix: number;
};
export type ArgoResultDTO = { available: boolean; message?: string; apps: ArgoAppDTO[] };
export type ArgoProjectsResultDTO = { available: boolean; message?: string; projects: ArgoProjectDTO[] };
export type ArgoApplicationSetsResultDTO = { available: boolean; message?: string; applicationSets: ArgoApplicationSetDTO[] };

export type ConditionDTO = { type: string; status: string; reason: string; message: string; scope?: string };
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
  watchError?: string | null; // watch failing before sync (RBAC, CRD mismatch) - not an empty list
  expandedKey: string | null;
  detail: ResourceDetailDTO | null;
};

// cert-manager lens (M11). NotAfterUnix/RenewalUnix are 0 when unknown; expiry is
// derived client-side from the absolute timestamps. A failing bound issuer is the
// headline for a stuck cert, exactly as a failing source is for a stuck Kustomization.
export type CertificateDTO = {
  namespace: string;
  name: string;
  ready: string;
  reason: string;
  message: string;
  issuerKind: string;
  issuerName: string;
  secretName: string;
  dnsNames: string[];
  notBeforeUnix?: number;
  notAfterUnix: number;
  expiresInSeconds: number;
  renewalUnix: number;
  stale?: boolean;
  generation?: number;
  observedGeneration?: number;
};
export type IssuerDTO = {
  kind: string;
  namespace: string;
  name: string;
  ready: string;
  reason: string;
  message: string;
};
export type ChainNodeDTO = {
  kind: string;
  namespace: string;
  name: string;
  ready: string;
  reason: string;
  message: string;
  children: ChainNodeDTO[];
};
export type CertChainDTO = {
  certificate: CertificateDTO;
  issuer: IssuerDTO | null;
  requests: ChainNodeDTO[];
  failingLink: ChainNodeDTO | null;
  warnings: string[];
};
export type CertManagerSlice = {
  cluster: string | null;
  certificates: CertificateDTO[];
  issuers: IssuerDTO[];
  loading: boolean;
  watchError?: string | null; // watch failing before sync (RBAC, CRD mismatch) - not an empty list
  selectedKey: string | null; // "<namespace>/<name>" of the inspected certificate
  chain: CertChainDTO | null; // resolved on demand for the selected cert
  chainLoading: boolean;
};

// External Secrets Operator lens (M12). Refresh staleness is ESO-native: a
// Synced secret whose last successful sync is more than 2x its refresh
// interval old is Stale - the controller has stopped refreshing. A failing
// bound (Cluster)SecretStore is the headline for a stuck secret, exactly as a
// failing issuer is for a stuck certificate.
export type ExternalSecretDTO = {
  namespace: string;
  name: string;
  state: string;
  reason: string;
  message: string;
  storeKind: string;
  storeName: string;
  targetSecret: string;
  refreshIntervalSeconds: number; // 0 = refresh disabled
  lastRefreshAgeSeconds: number; // 0 = never synced
  stale?: boolean;
};
export type ExternalSecretStoreDTO = {
  kind: string;
  namespace: string;
  name: string;
  state: string;
  reason: string;
  message: string;
  provider: string;
};
export type ExternalSecretsSlice = {
  cluster: string | null;
  secrets: ExternalSecretDTO[];
  stores: ExternalSecretStoreDTO[];
  loading: boolean;
  watchError?: string | null; // watch failing before sync (RBAC, CRD mismatch) - not an empty list
  selectedKey: string | null; // "<namespace>/<name>" of the inspected secret
};

// "observability" removed 2026-06-10: metrics ship inline in overview/workloads/network lanes
// (design principle 5); a dedicated surface can return when it has real content.
export type ClusterSection = "overview" | "gitops" | "argo" | "helm" | "certs" | "externalsecrets" | "gatewayApi" | "resources" | "crds" | "workloads" | "pods" | "events" | "nodes" | "access" | "upgrade" | "connectivity";
export type ArgoViewMode = "applications" | "projects" | "applicationsets";

export type OwnerDTO = { kind: string; namespace: string; name: string };
export type PodDTO = { name: string; ready: boolean; restarts: number; reason: string; node: string; ageSeconds: number };
export type ResourceCellDTO = { usage: number | null; request: number | null; limit: number | null };
export type WorkloadResourcesDTO = { cpu: ResourceCellDTO; mem: ResourceCellDTO };
// stale: spec changed, controller has not observed it - status/rank reflect
// the previous spec; render as transitioning ("dim = not yet realized").
export type ReloadTargetsDTO = { auto: boolean; configMaps: string[]; secrets: string[] };
// Fleet compare: one cluster's drift-relevant view of a workload. found=false is
// an honest "not present"; error is a degraded lookup, never mistaken for absence.
export type CompareContainerDTO = { name: string; image: string; resources: string };
export type WorkloadCompareDTO = {
  found: boolean; kind: string; namespace: string; name: string;
  desiredReplicas: number; readyReplicas: number; containers: CompareContainerDTO[];
  specHash: string; ownerKind: string; ownerNamespace: string; ownerName: string; ownerRevision: string;
  error: string;
};
export type WorkloadDTO = { kind: string; namespace: string; name: string; desired: number; ready: number; available: number; updated: number; restarts: number; reason: string; rank: "unhealthy"|"degraded"|"restarts"|"healthy"; gitops: OwnerDTO | null; pods: PodDTO[]; resources: WorkloadResourcesDTO; stale?: boolean; generation?: number; observedGeneration?: number; reloader?: ReloadTargetsDTO | null; paused?: boolean };

// Rollout history (newest first). Images may be empty when a
// ControllerRevision's patch data carries no template - never fabricated.
export type RolloutRevisionDTO = { revision: number; ageSeconds: number; images: string[]; current: boolean; changeCause?: string };
export type RolloutHistoryResultDTO = { revisions: RolloutRevisionDTO[]; error?: string };

// Fleet promotion tracker: one cluster's applied GitOps state.
export type PromotionUnitDTO = { kind: string; namespace: string; name: string; sourceKind: string; sourceName: string; revision: string; ready: string; suspended: boolean; stale: boolean; message: string };
export type PromotionSourceDTO = { kind: string; namespace: string; name: string; revision: string; ready: string; suspended: boolean };
export type PromotionSnapshotDTO = { fluxPresent: boolean; argoPresent: boolean; units: PromotionUnitDTO[]; sources: PromotionSourceDTO[]; error?: string };

// Upgrade-readiness report (per cluster).
export type SkewFindingDTO = { node: string; version: string; delta: number; level: string; detail: string };
export type DeprecatedUseDTO = { group: string; version: string; resource: string; kind: string; removedInMinor: number; replacedBy: string; count: number; countKnown: boolean };
export type UpgradeReportDTO = { serverVersion: string; serverMinor: number; eolDate: string; skew: SkewFindingDTO[]; deprecated: DeprecatedUseDTO[]; error?: string };

// Connectivity can-i (NetworkPolicy simulation).
export type NetpolRefDTO = { namespace: string; name: string; admits: boolean };
export type DirectionVerdictDTO = { isolated: boolean; allowed: boolean; policies: NetpolRefDTO[]; explain: string };
export type ConnectivityReportDTO = { allowed: boolean; ingress: DirectionVerdictDTO; egress: DirectionVerdictDTO; srcLabels: Record<string, string>; dstLabels: Record<string, string>; ciliumPolicies: number; error?: string };

// Survivability headroom simulation.
export type HeadroomPodRefDTO = { namespace: string; name: string; cpuReq: number; memReq: number };
export type PDBBlockerDTO = { namespace: string; name: string; disruptionsAllowed: number };
export type HeadroomResultDTO = { lostNodes: number; survivorNodes: number; displaced: number; rescheduled: number; unplaced: HeadroomPodRefDTO[]; cpuShortfall: number; memShortfall: number };
export type HeadroomReportDTO = { result: HeadroomResultDTO; pdbBlockers: PDBBlockerDTO[]; podListCapped: boolean; error?: string };

// Fleet expiry horizon: one cluster's expiring certificate material.
export type ExpiryItemDTO = { source: string; namespace: string; name: string; detail: string; subject: string; notAfterUnix: number };
export type ExpiryScanResultDTO = { items: ExpiryItemDTO[]; warnings: string[]; error?: string };
export type WorkloadUsageDTO = { cpuUsage: number | null; memUsage: number | null };
export type WorkloadMetricsStatusDTO = { available: boolean; message: string; updatedAt: string };
export type WorkloadMetricsResultDTO = { status: WorkloadMetricsStatusDTO; usage: Record<string, WorkloadUsageDTO> };
export type WorkloadsResultDTO = { fluxPresent: boolean; namespaces: string[]; workloads: WorkloadDTO[]; truncated?: boolean };
export const WORKLOAD_KINDS = ["Deployment", "StatefulSet", "DaemonSet"] as const;
export type WorkloadKind = typeof WORKLOAD_KINDS[number];
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
  truncated?: boolean; // a list hit the server cap: rows are incomplete, scope by namespace
};

function workloadKindFilterFor(kinds: readonly WorkloadKind[]): Record<WorkloadKind, boolean> {
  const selected = new Set(kinds.length === 0 ? WORKLOAD_KINDS : kinds);
  return {
    Deployment: selected.has("Deployment"),
    StatefulSet: selected.has("StatefulSet"),
    DaemonSet: selected.has("DaemonSet"),
  };
}

export type ContainerPortDTO = { name: string; port: number; protocol: string };
// ports and the last* trio are optional in the type (older fixtures omit them);
// the backend always sends them - last* are zero values when the container
// never terminated (lastReason "" means "no last state", never fabricate one).
export type ContainerSummaryDTO = { name: string; image: string; ready: boolean; restarts: number; state: string; init: boolean; ports?: ContainerPortDTO[]; lastReason?: string; lastExitCode?: number; lastFinishedUnix?: number; cpuRequest?: number | null; cpuLimit?: number | null; memRequest?: number | null; memLimit?: number | null };

// Node + per-container live usage (pod-attributed; nil = no sample, never zero).
export type NodeUsageDTO = { node: string; cpu: number | null; mem: number | null; sampledPods: number };
export type NodeUtilizationResultDTO = { available: boolean; message?: string; updatedAtUnix: number; items: NodeUsageDTO[] };
export type ContainerUsageDTO = { container: string; cpu: number | null; mem: number | null };
export type PodUsageResultDTO = { available: boolean; message?: string; updatedAtUnix: number; items: ContainerUsageDTO[] };
export type PodSummaryDTO = { namespace: string; name: string; ready: boolean; phase: string; reason: string; rank: "unhealthy"|"degraded"|"restarts"|"healthy"; restarts: number; node: string; ip: string; ownerKind: string; ownerName: string; ageSeconds: number; containers: ContainerSummaryDTO[] };
export type PodsResultDTO = { namespaces: string[]; pods: PodSummaryDTO[]; truncated?: boolean };
export type PodDetailDTO = { summary: PodSummaryDTO; labels: Record<string, string>; annotations?: Record<string, string>; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; qosClass: string; serviceAccount: string };

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
  truncated?: boolean; // the pod list hit the server cap: rows are incomplete, scope by namespace
  // Palette "jump to namespace": consumed by PodsView, which re-opens its live
  // subscription for the namespace. A raced one-shot list would be discarded
  // by the live sub's stale guard - the subscription itself must move.
  nsRequest?: { cluster: string; namespace: string } | null;
};

export type EventRowDTO = { type: "Normal" | "Warning"; reason: string; message: string; count: number; namespace: string; kind: string; name: string; lastSeenUnix: number; firstSeenUnix: number };
export type EventsResultDTO = { namespaces: string[]; events: EventRowDTO[]; truncated?: boolean };
export type EventsSlice = {
  cluster: string | null;
  namespace: string;
  items: EventRowDTO[];
  namespaces: string[];
  loading: boolean;
  warningsOnly: boolean;
  search: string;
  live: boolean;
  truncated?: boolean; // the 500-event cap hit: newest warnings may be missing
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
// FactDTO is one salient Summary value (the data path). kind "well" = counter
// well, "row" = labeled row; group "" = headline facts (no section header).
export type FactDTO = { group: string; label: string; value: string; tone: string; kind: string; pct?: number };

// AdhocResultDTO is the per-document outcome of an ad-hoc create (the scratch
// path). On dry-run the namespaceOwner* fields carry the prune-reach verdict.
export type AdhocResultDTO = {
  kind: string; namespace: string; name: string; ok: boolean; error: string;
  namespaceOwnerKind: string; namespaceOwnerName: string; namespaceOwnerNamespace: string; namespacePrunes: boolean;
};

// AdhocObjectDTO is one Klyx-created scratch object (label klyx.dev/adhoc) in
// the cleanup inventory.
export type AdhocObjectDTO = { kind: string; group: string; version: string; plural: string; scope: string; namespace: string; name: string; ageSeconds: number };

// Hubble flow/drop triage readout. Nil/absent numeric fields mean "that metric
// set is not enabled/scraped" - never a fabricated zero.
export type ReasonRateDTO = { reason: string; protocol: string; rate: number };
export type HubbleStatsDTO = {
  forwardRate: number | null; dropRate: number | null; dropReasons: ReasonRateDTO[];
  dnsErrorRate: number | null; dnsTotalRate: number | null;
  httpErrorRate: number | null; httpTotalRate: number | null;
  status: RouteMetricsStatusDTO;
};

// Access check (RBAC lens). The query speaks ResourceAttributes vocabulary;
// an empty saName checks as the kubeconfig identity.
export type AccessQueryDTO = {
  verb: string; group: string; resource: string; subresource: string;
  name: string; namespace: string; saNamespace: string; saName: string;
};
export type AccessVerdictDTO = {
  allowed: boolean; denied: boolean; reason: string; evaluationError: string;
  subject: string; error: string;
};
export type AccessGrantDTO = {
  subjectKind: string; subjectName: string; subjectNamespace: string;
  bindingKind: string; bindingNamespace: string; bindingName: string;
  roleKind: string; roleName: string;
};
export type AccessWhoCanDTO = { grants: AccessGrantDTO[]; clusterWideOnly: boolean; error: string };

// GitOpsOwnershipDTO is the authoritative pre-write verdict: which GitOps unit
// OWNS the object (edits revert on reconcile) and which unit REACHES its
// namespace (out-of-band creates are pruned when namespacePrunes). Empty
// namespace-owner fields mean "no managing unit DETECTED" - never "safe".
export type GitOpsOwnershipDTO = {
  ownerKind: string; ownerNamespace: string; ownerName: string; ownerPrunes: boolean; ownerSuspended: boolean; basis: string;
  namespaceOwnerKind: string; namespaceOwnerNamespace: string; namespaceOwnerName: string; namespacePrunes: boolean; namespaceBasis: string;
  warnings: string[];
};

// AI-assist ("Explain this"). Read-only; errors fold into `error` (ok stays false).
// groundedOn names the resolved nodes sent to the model, for traceability.
export type AIResultDTO = { ok: boolean; explanation: string; model: string; backend: string; groundedOn: string; error: string };
export type AIAvailabilityDTO = { backend: string; ready: boolean };
export type InstanceDetailDTO = { kind: string; namespace: string; name: string; created: string; labels: Record<string, string>; annotations?: Record<string, string>; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; secretKeys?: SecretKeyDTO[]; serviceBacking?: ServiceBackingDTO | null; hpaScaling?: HPAScalingDTO | null; related?: RelatedRefDTO[]; facts?: FactDTO[] };
export type InstanceDetailSlice = { ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean };

export type Route =
  | { name: "fleet" }
  | { name: "forwards" }
  | { name: "settings" }
  | { name: "activity" }
  | { name: "cluster"; cluster: string; section: ClusterSection; resource?: ResourceRef; instance?: InstanceRef; gateway?: GatewayRef; argoView?: ArgoViewMode };

export const SECTION_LABELS: Record<ClusterSection, string> = {
  overview: "Overview",
  // "Flux" not "GitOps": design principle 8 (speak the tool's vocabulary).
  // The view is Flux-specific; an Argo provider would get its own section.
  gitops: "Flux",
  // Argo's own vocabulary throughout (synced/degraded), per design principle 8.
  argo: "Argo CD",
  helm: "Helm",
  certs: "Certificates",
  externalsecrets: "External Secrets",
  gatewayApi: "Gateway API",
  resources: "Resources",
  crds: "CRDs",
  workloads: "Workloads",
  pods: "Pods",
  events: "Events",
  nodes: "Nodes",
  access: "Access",
  upgrade: "Upgrade",
  connectivity: "Connectivity",
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
export type GatewayRefDTO = { namespace: string; name: string; className: string; addresses?: GatewayAddressDTO[]; listeners?: ListenerDTO[]; accepted: boolean; programmed: boolean; stale?: boolean; generation?: number; observedGeneration?: number };
export type GatewayListDTO = { gatewayAPIServed: boolean; gateways: GatewayRefDTO[]; error?: string };
export type PolicyDetailDTO = { key: string; value: string };
export type PolicyRefDTO = { kind: string; namespace: string; name: string; targetKind: string; targetNamespace: string; targetName: string; targetSectionName: string; summary: string; details: PolicyDetailDTO[]; inferred: boolean; match: string };
export type CertificateRefDTO = { group: string; kind: string; namespace: string; name: string };
export type ListenerDTO = { name: string; protocol: string; hostname: string; port: number; certificateRefs?: CertificateRefDTO[] };
export type MatchDTO = { pathType: string; pathValue: string; method: string };
export type BackendDTO = { kind: string; name: string; namespace: string; port: number; weight: number };
export type PodCountDTO = { ready: number; total: number; unknown: boolean };
export type ServiceNodeDTO = { namespace: string; name: string; type: string; port: number; resolved: boolean; global: boolean; meshClusters: string[]; meshUnconfirmed: boolean; policies: PolicyRefDTO[]; cnps: PolicyRefDTO[] };
export type GatewayNodeDTO = { namespace: string; name: string; className: string; listeners: ListenerDTO[]; addresses?: GatewayAddressDTO[]; accepted: boolean; programmed: boolean; policies: PolicyRefDTO[] };
export type RouteNodeDTO = { namespace: string; name: string; hostnames: string[]; matches: MatchDTO[]; parentSectionName?: string; accepted: boolean; resolvedRefs: boolean; backends: BackendDTO[]; services: ServiceNodeDTO[]; pods: PodCountDTO; policies: PolicyRefDTO[] };
export type TopologyDTO = { gateway: GatewayNodeDTO; routes: RouteNodeDTO[]; clusterPolicies?: PolicyRefDTO[]; warnings?: string[]; error?: string };
export type GatewayRef = { namespace: string; name: string };

export type RouteMetricDTO = { rps: number | null; p50: number | null; p99: number | null; errRate: number | null };
export type RouteMetricsStatusDTO = { available: boolean; message: string; updatedAt: string };
export type RouteMetricsResultDTO = { status: RouteMetricsStatusDTO; routes: Record<string, RouteMetricDTO> };

export type NetworkSlice = {
  cluster: string | null;
  served: boolean;
  gateways: GatewayRefDTO[];
  listLoading: boolean;
  listError?: string | null; // list failed on a served API - distinct from "no Gateways"
  selected: GatewayRef | null;
  topology: TopologyDTO | null;
  topologyLoading: boolean;
  selectedRoute: string | null; // "<ns>/<name>"
  routeMetrics: Record<string, RouteMetricDTO>;
  routeMetricsStatus: RouteMetricsStatusDTO | null;
  routeMetricsStale: boolean;
};

export type MeshNodeDTO = { cluster: string; name: string; clusterId: number | null; state: string; present: boolean };
export type MeshEdgeDTO = { a: string; b: string; mutual: boolean; from?: string };
export type MeshGraphDTO = { nodes: MeshNodeDTO[]; edges: MeshEdgeDTO[] };

export type MetricsDTO = { available: boolean; mode: string; source: string; warning: string; reason: string; cpuFraction: number | null; memFraction: number | null };
export type MetricsSlice = { cluster: string | null; dto: MetricsDTO | null; loading: boolean };

// Nodes types
export type NodeSummaryDTO = { name: string; roles: string[]; ready: boolean; unschedulable: boolean; problems: string[]; version: string; os: string; arch: string; taintCount: number; cpuCapacity: number; cpuAllocatable: number; memCapacity: number; memAllocatable: number; podCapacity: number; ageSeconds: number; pool?: string; zone?: string };
export type NodesResultDTO = { nodes: NodeSummaryDTO[] };
export type NodeTaintDTO = { key: string; value: string; effect: string };
export type PodOnNodeDTO = { namespace: string; name: string; phase: string };
export type NodeDetailDTO = { summary: NodeSummaryDTO; labels: Record<string, string>; annotations?: Record<string, string>; taints: NodeTaintDTO[]; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; podsOnNode: PodOnNodeDTO[] };

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
  projects: ArgoProjectDTO[];
  applicationSets: ArgoApplicationSetDTO[];
  available: boolean;
  projectsAvailable: boolean;
  applicationSetsAvailable: boolean;
  message: string;
  projectsMessage: string;
  applicationSetsMessage: string;
  loading: boolean;
  projectsLoading: boolean;
  applicationSetsLoading: boolean;
  expanded: string[]; // "<ns>/<name>" keys
};

// ---- / Helm types -------------------------------------------------------------

export type ActionStatus = { kind: "success" | "error"; message: string };
// One toast on screen / one row in the activity log. `at` is epoch ms.
export type ActionEvent = ActionStatus & { id: number; at: number };

// The activity log survives relaunches: "what did I do and did it succeed"
// must be answerable the morning after, especially on protected clusters.
// Storage helpers live here (not store/persist.ts) to avoid an import cycle.
const ACTION_LOG_KEY = "klyx-action-log";
const ACTION_LOG_MAX = 200;
const MAX_VISIBLE_TOASTS = 4;
let nextActionId = 1;

function loadActionLog(): ActionEvent[] {
  try {
    const raw = localStorage.getItem(ACTION_LOG_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    const entries = parsed.filter(
      (e): e is ActionEvent =>
        e && typeof e.message === "string" && typeof e.at === "number" && typeof e.id === "number" && Number.isFinite(e.id),
    );
    nextActionId = entries.reduce((m, e) => Math.max(m, e.id), 0) + 1;
    return entries.slice(0, ACTION_LOG_MAX);
  } catch {
    return [];
  }
}

function saveActionLog(log: ActionEvent[]): void {
  try {
    localStorage.setItem(ACTION_LOG_KEY, JSON.stringify(log));
  } catch {
    // best-effort - a full or unavailable storage must never block an action
  }
}

// NavIntent is a one-shot cross-view request (the pods nsRequest pattern,
// generalized): the palette or a cross-link navigates to a section and parks
// an intent; the target view consumes it once its data is loaded and opens
// the matching dialog/selection. Verbs only ever OPEN the existing confirm or
// operation composer - the intent channel never fires an API call itself, so
// protected-cluster arming and danger confirms are never bypassed.
export type NavIntent =
  | { kind: "drain-node"; cluster: string; node: string }
  | { kind: "cordon-node"; cluster: string; node: string; cordon: boolean }
  | { kind: "restart-workload"; cluster: string; workloadKind: WorkloadKind; namespace: string; name: string }
  | { kind: "scale-workload"; cluster: string; workloadKind: WorkloadKind; namespace: string; name: string }
  | { kind: "reconcile-flux"; cluster: string; fluxKind: string; namespace: string; name: string }
  | { kind: "suspend-flux"; cluster: string; fluxKind: string; namespace: string; name: string; suspend: boolean }
  | { kind: "sync-argo"; cluster: string; namespace: string; name: string }
  | { kind: "refresh-argo"; cluster: string; namespace: string; name: string }
  | { kind: "logs-pod"; cluster: string; namespace: string; name: string }
  | { kind: "select-pod"; cluster: string; namespace: string; name: string }
  | { kind: "select-workload"; cluster: string; workloadKind: string; namespace: string; name: string }
  | { kind: "select-node"; cluster: string; node: string }
  | { kind: "select-flux"; cluster: string; fluxKind: string; namespace: string; name: string }
  | { kind: "access-check"; cluster: string; namespace: string; name: string };

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
  // fluxError is true when the summary read errored (transient RBAC/network) as
  // opposed to a Flux-free cluster; the tile then shows "—" instead of hiding,
  // so the failure is honest and never reads as "no Flux".
  fluxError: boolean;
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
  openArgo: (mode?: ArgoViewMode) => void;
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
  setGitOps: (cluster: string, resources: FluxResourceDTO[], sources: FluxSourceDTO[], watchError?: string) => void;
  setGitOpsLoading: (cluster: string) => void;
  clearGitOps: () => void;
  expand: (key: string) => void;
  collapse: () => void;
  setDetail: (d: ResourceDetailDTO) => void;
  certmanager: CertManagerSlice;
  setCertManager: (cluster: string, certificates: CertificateDTO[], issuers: IssuerDTO[], watchError?: string) => void;
  setCertManagerLoading: (cluster: string) => void;
  clearCertManager: () => void;
  selectCertificate: (key: string | null) => void;
  setCertChain: (chain: CertChainDTO) => void;
  externalsecrets: ExternalSecretsSlice;
  setExternalSecrets: (cluster: string, secrets: ExternalSecretDTO[], stores: ExternalSecretStoreDTO[], watchError?: string) => void;
  setExternalSecretsLoading: (cluster: string) => void;
  clearExternalSecrets: () => void;
  selectExternalSecret: (key: string | null) => void;
  // actionStatus keeps the latest result (convenient for tests and callers);
  // actionToasts is the on-screen queue - concurrent results stack instead of
  // overwriting each other; actionLog is the persisted history behind the
  // Activity view.
  actionStatus: ActionStatus | null;
  actionToasts: ActionEvent[];
  actionLog: ActionEvent[];
  setActionStatus: (s: ActionStatus) => void;
  dismissActionToast: (id: number) => void;
  clearActionStatus: () => void;
  clearActionLog: () => void;
  openActivity: () => void;
  navIntent: NavIntent | null;
  requestNavIntent: (i: NavIntent) => void;
  clearNavIntent: () => void;
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
  setGatewaysLoading: (cluster: string) => void;
  setGateways: (cluster: string, l: GatewayListDTO) => void;
  setTopologyLoading: (cluster: string, ref: GatewayRef) => void;
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
  setWorkloadKindFilter: (kinds: readonly WorkloadKind[]) => void;
  openWorkloads: (kinds?: readonly WorkloadKind[]) => void;
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
  requestPodsNamespace: (cluster: string, namespace: string) => void;
  clearPodsNsRequest: () => void;
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
  setArgoProjectsLoading: (cluster: string) => void;
  setArgoProjects: (cluster: string, r: ArgoProjectsResultDTO) => void;
  setArgoApplicationSetsLoading: (cluster: string) => void;
  setArgoApplicationSets: (cluster: string, r: ArgoApplicationSetsResultDTO) => void;
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
  openArgo: (mode = "applications") =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section: "argo", argoView: mode } } : {})),
  openWorkloads: (kinds) =>
    set((s) => {
      if (s.route.name !== "cluster") return {};
      return {
        route: { name: "cluster", cluster: s.route.cluster, section: "workloads" },
        workloads: {
          ...s.workloads,
          kindFilter: kinds ? workloadKindFilterFor(kinds) : s.workloads.kindFilter,
        },
      };
    }),
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
  setInstanceDetailLoading: (ref) => set((s) => {
    const current = s.instanceDetail;
    const sameRef = current.ref?.namespace === ref.namespace && current.ref?.name === ref.name;
    return { instanceDetail: { ref, detail: sameRef ? current.detail : null, loading: true } };
  }),
  setInstanceDetail: (detail) => set((s) => ({ instanceDetail: { ...s.instanceDetail, detail, loading: false } })),
  clearInstanceDetail: () => set({ instanceDetail: { ref: null, detail: null, loading: false } }),
  gitops: { cluster: null, resources: [], sources: [], loading: false, watchError: null, expandedKey: null, detail: null },
  setGitOps: (cluster, resources, sources, watchError) => set((s) => ({ gitops: { ...s.gitops, cluster, resources, sources, loading: false, watchError: watchError || null } })),
  setGitOpsLoading: (cluster) => set((s) => ({ gitops: { ...s.gitops, cluster, resources: [], sources: [], loading: true, watchError: null } })),
  clearGitOps: () => set({ gitops: { cluster: null, resources: [], sources: [], loading: false, watchError: null, expandedKey: null, detail: null } }),
  expand: (key) => set((s) => ({ gitops: { ...s.gitops, expandedKey: key, detail: null } })),
  collapse: () => set((s) => ({ gitops: { ...s.gitops, expandedKey: null, detail: null } })),
  setDetail: (d) => set((s) => ({ gitops: { ...s.gitops, detail: d } })),

  certmanager: { cluster: null, certificates: [], issuers: [], loading: false, watchError: null, selectedKey: null, chain: null, chainLoading: false },
  setCertManager: (cluster, certificates, issuers, watchError) => set((s) => ({ certmanager: { ...s.certmanager, cluster, certificates, issuers, loading: false, watchError: watchError || null } })),
  setCertManagerLoading: (cluster) => set((s) => ({ certmanager: { ...s.certmanager, cluster, certificates: [], issuers: [], loading: true, watchError: null, selectedKey: null, chain: null, chainLoading: false } })),
  clearCertManager: () => set({ certmanager: { cluster: null, certificates: [], issuers: [], loading: false, watchError: null, selectedKey: null, chain: null, chainLoading: false } }),
  selectCertificate: (key) => set((s) => ({ certmanager: { ...s.certmanager, selectedKey: key, chain: null, chainLoading: key !== null } })),
  setCertChain: (chain) => set((s) => ({ certmanager: { ...s.certmanager, chain, chainLoading: false } })),

  externalsecrets: { cluster: null, secrets: [], stores: [], loading: false, watchError: null, selectedKey: null },
  setExternalSecrets: (cluster, secrets, stores, watchError) => set((s) => ({ externalsecrets: { ...s.externalsecrets, cluster, secrets, stores, loading: false, watchError: watchError || null } })),
  setExternalSecretsLoading: (cluster) => set((s) => ({ externalsecrets: { ...s.externalsecrets, cluster, secrets: [], stores: [], loading: true, watchError: null, selectedKey: null } })),
  clearExternalSecrets: () => set({ externalsecrets: { cluster: null, secrets: [], stores: [], loading: false, watchError: null, selectedKey: null } }),
  selectExternalSecret: (key) => set((s) => ({ externalsecrets: { ...s.externalsecrets, selectedKey: key } })),
  actionStatus: null,
  actionToasts: [],
  actionLog: loadActionLog(),
  setActionStatus: (actionStatus) =>
    set((s) => {
      const event: ActionEvent = { ...actionStatus, id: nextActionId++, at: Date.now() };
      const actionLog = [event, ...s.actionLog].slice(0, ACTION_LOG_MAX);
      saveActionLog(actionLog);
      return {
        actionStatus,
        actionToasts: [...s.actionToasts, event].slice(-MAX_VISIBLE_TOASTS),
        actionLog,
      };
    }),
  dismissActionToast: (id) => set((s) => ({ actionToasts: s.actionToasts.filter((t) => t.id !== id) })),
  clearActionStatus: () => set({ actionStatus: null, actionToasts: [] }),
  clearActionLog: () => {
    saveActionLog([]);
    set({ actionLog: [] });
  },
  openActivity: () => set({ route: { name: "activity" } }),
  navIntent: null,
  requestNavIntent: (navIntent) => set({ navIntent }),
  clearNavIntent: () => set({ navIntent: null }),
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
    set((s) => {
      if (s.route.name !== "cluster") return {};
      // Crossing clusters (palette jump straight to a topology): drop the
      // previous cluster's gateway list along with stamping the new owner.
      const base = s.network.cluster === s.route.cluster ? s.network : { ...s.network, cluster: s.route.cluster, served: false, gateways: [], listLoading: false, listError: null };
      return {
        route: { name: "cluster", cluster: s.route.cluster, section: "gatewayApi", gateway: { namespace, name } },
        network: { ...base, selected: { namespace, name }, topology: null, topologyLoading: true, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
      };
    }),
  closeGateway: () =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section: "gatewayApi" } } : {})),
  network: { cluster: null, served: false, gateways: [], listLoading: false, listError: null, selected: null, topology: null, topologyLoading: false, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
  setGatewaysLoading: (cluster) => set((s) => {
    if (s.network.cluster === cluster) return { network: { ...s.network, listLoading: true, listError: null } };
    // Cluster switch: drop the previous cluster's gateways instead of rendering
    // them under the new cluster while the fresh list is in flight.
    return { network: { cluster, served: false, gateways: [], listLoading: true, listError: null, selected: null, topology: null, topologyLoading: false, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false } };
  }),
  setGateways: (cluster, l) => set((s) => {
    if (s.network.cluster !== cluster) return {}; // stale in-flight response
    return { network: { ...s.network, served: l.gatewayAPIServed, gateways: l.gateways ?? [], listLoading: false, listError: l.error || null } };
  }),
  setTopologyLoading: (cluster, ref) => set((s) => {
    const base = s.network.cluster === cluster ? s.network : { ...s.network, cluster, served: false, gateways: [], listLoading: false, listError: null };
    return { network: { ...base, selected: ref, topology: null, topologyLoading: true, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false } };
  }),
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
    kindFilter: workloadKindFilterFor(WORKLOAD_KINDS), needsAttention: false, search: "", expanded: [],
    metricsAvailable: false, metricsStatus: null, metricsStale: false, nearLimitSort: false, live: false },
  setWorkloadsLoading: (cluster, namespace) => set((s) => ({ workloads: { ...s.workloads, cluster, namespace, loading: true } })),
  setWorkloads: (cluster, namespace, result) => set((s) => {
    // namespace-list preservation: replace only on all-load; fallback to [namespace] if empty.
    let namespaces = s.workloads.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    return { workloads: { ...s.workloads, cluster, namespace, items: result.workloads ?? [], namespaces, fluxPresent: result.fluxPresent, truncated: result.truncated ?? false, loading: false } };
  }),
  toggleWorkloadKind: (k) => set((s) => ({ workloads: { ...s.workloads, kindFilter: { ...s.workloads.kindFilter, [k]: !s.workloads.kindFilter[k] } } })),
  setWorkloadKindFilter: (kinds) => set((s) => ({ workloads: { ...s.workloads, kindFilter: workloadKindFilterFor(kinds) } })),
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
  clearWorkloads: () => set((s) => ({ workloads: { ...s.workloads, cluster: null, items: [], namespaces: [], expanded: [], kindFilter: workloadKindFilterFor(WORKLOAD_KINDS), needsAttention: false, search: "", namespace: "", metricsAvailable: false, metricsStatus: null, metricsStale: false, nearLimitSort: false, live: false } })),
  pods: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false, live: false },
  setPodsLoading: (cluster, namespace) => set((s) => ({ pods: { ...s.pods, cluster, namespace, loading: true } })),
  setPods: (cluster, namespace, result) => set((s) => {
    let namespaces = s.pods.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    return { pods: { ...s.pods, cluster, namespace, items: result.pods ?? [], namespaces, truncated: result.truncated ?? false, loading: false } };
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
  clearPods: () => set((s) => ({ pods: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false, live: false, nsRequest: s.pods.nsRequest } })),
  requestPodsNamespace: (cluster, namespace) => set((s) => ({ pods: { ...s.pods, nsRequest: { cluster, namespace } } })),
  clearPodsNsRequest: () => set((s) => ({ pods: { ...s.pods, nsRequest: null } })),
  events: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, warningsOnly: false, search: "", live: false, truncated: false },
  setEventsLoading: (cluster, namespace) => set((s) => ({ events: { ...s.events, cluster, namespace, loading: true } })),
  setEvents: (cluster, namespace, result) => set((s) => {
    let namespaces = s.events.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    return { events: { ...s.events, cluster, namespace, items: result.events ?? [], namespaces, loading: false, truncated: result.truncated ?? false } };
  }),
  toggleWarningsOnly: () => set((s) => ({ events: { ...s.events, warningsOnly: !s.events.warningsOnly } })),
  setWarningsOnly: (v) => set((s) => ({ events: { ...s.events, warningsOnly: v } })),
  setEventsSearch: (search) => set((s) => ({ events: { ...s.events, search } })),
  setEventsLive: (cluster, namespace, live) =>
    set((s) => {
      if (s.events.cluster !== cluster || s.events.namespace !== namespace) return {};
      return { events: { ...s.events, live } };
    }),
  clearEvents: () => set({ events: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, warningsOnly: false, search: "", live: false, truncated: false } }),
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
  argo: {
    cluster: null,
    apps: [],
    projects: [],
    applicationSets: [],
    available: true,
    projectsAvailable: true,
    applicationSetsAvailable: true,
    message: "",
    projectsMessage: "",
    applicationSetsMessage: "",
    loading: false,
    projectsLoading: false,
    applicationSetsLoading: false,
    expanded: [],
  },
  setArgoLoading: (cluster) => set((s) => ({ argo: { ...s.argo, cluster, loading: true } })),
  setArgo: (cluster, r) => set((s) => ({ argo: { ...s.argo, cluster, apps: r.apps ?? [], available: r.available, message: r.message ?? "", loading: false } })),
  setArgoProjectsLoading: (cluster) => set((s) => ({ argo: { ...s.argo, cluster, projectsLoading: true } })),
  setArgoProjects: (cluster, r) => set((s) => ({ argo: { ...s.argo, cluster, projects: r.projects ?? [], projectsAvailable: r.available, projectsMessage: r.message ?? "", projectsLoading: false } })),
  setArgoApplicationSetsLoading: (cluster) => set((s) => ({ argo: { ...s.argo, cluster, applicationSetsLoading: true } })),
  setArgoApplicationSets: (cluster, r) => set((s) => ({ argo: { ...s.argo, cluster, applicationSets: r.applicationSets ?? [], applicationSetsAvailable: r.available, applicationSetsMessage: r.message ?? "", applicationSetsLoading: false } })),
  toggleArgoExpand: (key) => set((s) => ({ argo: { ...s.argo, expanded: s.argo.expanded.includes(key) ? s.argo.expanded.filter((k) => k !== key) : [...s.argo.expanded, key] } })),
  clearArgo: () => set({
    argo: {
      cluster: null,
      apps: [],
      projects: [],
      applicationSets: [],
      available: true,
      projectsAvailable: true,
      applicationSetsAvailable: true,
      message: "",
      projectsMessage: "",
      applicationSetsMessage: "",
      loading: false,
      projectsLoading: false,
      applicationSetsLoading: false,
      expanded: [],
    },
  }),
  setHelmLoading: (cluster) => set((s) => ({ helm: { ...s.helm, cluster, loading: true } })),
  setHelm: (cluster, available, message, releases) => set((s) => ({ helm: { ...s.helm, cluster, available, message, releases, loading: false } })),
  selectHelmRelease: (ref) => set((s) => ({ helm: { ...s.helm, selected: ref, history: [], values: "", detailLoading: ref !== null } })),
  setHelmDetail: (ref, history, values) => set((s) => {
    const sel = s.helm.selected;
    if (!sel || sel.namespace !== ref.namespace || sel.name !== ref.name) return {};
    return { helm: { ...s.helm, history, values, detailLoading: false } };
  }),
  clearHelm: () => set({ helm: { cluster: null, releases: [], available: true, message: "", loading: false, selected: null, history: [], values: "", detailLoading: false } }),
  overviewSummary: { cluster: null, loading: false, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, failedReleases: null, helmAvailable: false, namespaces: null, flux: null, fluxError: false },
  setOverviewSummaryLoading: (cluster) => set({ overviewSummary: { cluster, loading: true, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, failedReleases: null, helmAvailable: false, namespaces: null, flux: null, fluxError: false } }),
  setOverviewSummary: (s) => set({ overviewSummary: s }),
  clearOverviewSummary: () => set({ overviewSummary: { cluster: null, loading: false, unhealthyWorkloads: null, podsNotReady: null, warningEvents: null, nodeProblems: null, failedReleases: null, helmAvailable: false, namespaces: null, flux: null, fluxError: false } }),
}));
