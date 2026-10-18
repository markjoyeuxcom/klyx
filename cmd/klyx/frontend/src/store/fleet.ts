import { create } from "zustand";
import type { HealthLike } from "../health/verdict";

export type ClusterDTO = {
  name: string;
  state: string;
  reason: string;
  nodesReady: number;
  nodesTotal: number;
  pods: number;
  // podsKnown false: no complete pod read yet - unknown, never zero (absent
  // in older fixtures means known). podsStale: pods is the last good count.
  podsKnown?: boolean;
  podsStale?: boolean;
  version: string;
  gitopsTier: string;
  gitopsReason: string;
  fluxPresent?: boolean;
  fluxOperatorPresent?: boolean;
  fluxImagesPresent?: boolean;
  // coverage S1c: source composition and notification kinds are served
  fluxCompositionPresent?: boolean;
  fluxNotificationsPresent?: boolean;
  fluxHealthy?: boolean;
  argoPresent?: boolean;
  networkTier: string;
  networkReason: string;
  gatewayAPIVersion?: string;
  ciliumPresent?: boolean;
  clusterMesh?: boolean;
  hasHubble?: boolean; karpenterPresent?: boolean; karpenterHealthy?: boolean; veleroPresent?: boolean; admissionPresent?: boolean; loadBalancingPresent?: boolean; metallbPresent?: boolean;
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
  // EFFECTIVE protection - exactly what the backend guard enforces (explicit
  // flag or a prd/prod/production token). Typed arming keys off this field.
  protected?: boolean;
  protectedInferred?: boolean;
  ageSeconds: number;
};

export type FluxResourceDTO = {
  rule: RuleDTO; // the relocated lens rule (1c addendum §2)
  uid?: string; // identity (1c slice 3 §2a): a same-name recreation is another object
  gvr?: string;
  kind: string;
  namespace: string;
  name: string;
  ready: string;
  reason: string;
  message: string;
  revision: string;
  readySinceSeconds: number;
  // the Ready transition time itself (0/absent = unknown): shared rows are
  // pushed only when they change, so the lens ticks the age from it
  readySinceUnix?: number;
  suspended: boolean;
  sourceKind: string;
  sourceName: string;
  // resolved namespace of the bound source (sourceRef.namespace, else the
  // object's own) - the authoritative join key with sourceKind + sourceName
  sourceNamespace?: string;
  // stale: spec changed, controller has not observed it - ready reflects the
  // previous spec; render the row as transitioning ("dim = not yet realized").
  stale?: boolean;
  generation?: number;
  observedGeneration?: number;
  // the Ready condition's own observedGeneration (absent when it carries
  // none): the generation the verdict describes (coverage S1b §4.1)
  readyGeneration?: number;
  // who applies the object: the verbs' consequence line (roadmap C4)
  owner?: { kind: string; namespace?: string; name: string };
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
  rule: RuleDTO; // the relocated lens rule (1c addendum §2)
  uid?: string; gvr?: string; generation?: number; // identity (1c slice 3 §2a)
  namespace: string; name: string; generators: string[]; templateName: string;
  project: string; repoURL: string; path: string; targetRevision: string; destNamespace: string;
  destServer?: string; generatedApps: number; generatedNames?: string[]; healthy: boolean; conditions: ArgoAppSetConditionDTO[];
};
export type ArgoAppDTO = {
  rule: RuleDTO; // the relocated lens rule (1c addendum §2)
  uid?: string; gvr?: string; generation?: number; // identity (1c slice 3 §2a)
  namespace: string; name: string; project: string;
  syncStatus: string; healthStatus: string; broken: boolean;
  revision: string; repoURL: string; path: string; chart: string; targetRevision: string;
  extraSources: number; destNamespace: string; applicationSet?: string; autoSync: boolean;
  // which cluster the resources live on: server URL or cluster-secret name
  destServer?: string; destName?: string;
  opPhase: string; opMessage: string;
  conditions: ArgoConditionDTO[]; resources?: ArgoResourceDTO[]; reconciledUnix: number;
};
// error set = the read failed (availability unknown); available=false with no
// error = the kind is not served. Never render a failed read as "not installed".
export type ArgoProjectsResultDTO = { available: boolean; message?: string; error?: string; projects: ArgoProjectDTO[] };

// lastTransition (RFC3339) and observedGeneration as the condition records
// them - set by the generic inspector (The Line slice 15b); absent elsewhere.
export type ConditionDTO = { type: string; status: string; reason: string; message: string; scope?: string; lastTransition?: string; observedGeneration?: number };
export type InventoryEntryDTO = { group: string; version: string; kind: string; namespace: string; name: string };
export type FluxSourceDTO = {
  rule: RuleDTO; // the relocated lens rule (1c addendum §2)
  artifactUpdatedUnix?: number; // the artifact's lastUpdateTime
  uid?: string;
  gvr?: string;
  generation?: number;
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
  // The read object's identity and when it was read (design §5b): a detail
  // is shown only for the selected row's UID. error: the read failed - the
  // DTO then carries only the requested kind/namespace/name, never evidence.
  // sourceWarning: the bound source could not be read (not found/unverified).
  uid?: string;
  generation?: number;
  asOfUnix?: number;
  error?: string;
  sourceWarning?: string;
  // coverage S1c §2: a bound ExternalArtifact's producer, its own line after
  // the artifact's verdict; sourceNote names a feature-gate refusal
  sourceProducer?: import("../bridge/fluxcomposition").SourceProducerDTO;
  sourceNote?: string;
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
  // The cluster the detail was read from: DEV/STG/PRD share kind/ns/name, so
  // a detail is only evidence for the selection when this matches too.
  detailCluster?: string | null;
  // From the health Observer's shared rows (1c slice 3 §5.3): per-kind
  // coverage, the A4 state and the one count selector's per-part counts.
  coverage?: KindCoverageDTO[];
  rowsState?: RowsStateDTO;
  partCounts?: { reconciliations: PartCountsDTO | null; sources: PartCountsDTO | null };
};

// What the Flux rows adapter (health/useFluxRows) writes.
export type GitOpsRowsPayload = Pick<GitOpsSlice, "resources" | "sources" | "coverage" | "watchError" | "loading" | "rowsState" | "partCounts">;

// cert-manager lens (M11). NotAfterUnix/RenewalUnix are 0 when unknown; expiry is
// derived client-side from the absolute timestamps. A failing bound issuer is the
// headline for a stuck cert, exactly as a failing source is for a stuck Kustomization.
// RuleDTO: a relocated lens rule's verdict, computed once in Go (1c addendum
// §2) - the lens's inclusion predicate and presentation role, kept separate.
// Lenses read it; they never recompute it.
export type RuleDTO = { included: boolean; role: string; label: string };
// RuleStepDTO: the verdict a snapshot row takes from atUnix on - a time-only
// boundary evaluated in Go at read time. The lens switches on its clock.
export type RuleStepDTO = { atUnix: number; rule: RuleDTO };

export type CertificateDTO = {
  uid?: string; // the object's identity (slice 3 §2a): a same-name recreation is another object
  gvr?: string; // the served group/version/resource it was read at
  rule: RuleDTO;
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
  uid?: string;
  gvr?: string;
  generation?: number;
  rule: RuleDTO;
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
  // asOfUnix: when the chain was read. error: the read failed (NotFound,
  // forbidden, timeout) - a request outcome keyed by the certificate's
  // namespace/name only, never chain evidence or an identity.
  asOfUnix?: number;
  error?: string;
  certificate: CertificateDTO;
  issuer: IssuerDTO | null;
  requests: ChainNodeDTO[];
  failingLink: ChainNodeDTO | null;
  warnings: string[];
};
// One watched kind's evidence (backend KindCoverageDTO): served=false is a
// definitive absence; synced=false = no evidence yet; error = a CURRENT
// failure (after sync: the rows are last-observed).
export type KindCoverageDTO = {
  kind: string; served: boolean; synced: boolean; error?: string;
  // From the health Observer (slice 3): its five-state coverage and the
  // evidence behind it, carried unreduced - a stale kind's last good time,
  // the failure class, a discovery gap.
  coverage?: "live" | "stale" | "checking" | "unreadable" | "absent";
  class?: string; asOf?: string; lastGoodAt?: string; discovery?: string;
  // A budgeted kind that left objects out (coverage S1a §3): its counts are
  // lower bounds; notRetained counts what was left out, itself a lower bound.
  truncated?: boolean; notRetained?: number;
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
  // the cluster the chain was read from: DEV/STG/PRD share ns/name, so a
  // chain is evidence for the selection only when this matches too
  chainCluster?: string | null;
  coverage?: KindCoverageDTO[]; // per kind: Certificate, Issuer, ClusterIssuer
  // The shared rows' A4 state (slice 3 §4): updating - the summary is ahead,
  // the counts still the rows'; refreshing - past the deadline, the rows are
  // shown as of evaluatedAt while a fenced re-read runs.
  rowsState?: RowsStateDTO;
  // The task's global counts per part from the one count selector (slice 3
  // §4a): the rows' own counts, or the summary's past the A4 deadline. Lens
  // headers and attention chips read these; filtered counts stay row-scoped.
  partCounts?: { certificates: PartCountsDTO | null; issuers: PartCountsDTO | null };
};

// A rows-backed lens's A4 state (slice 3 §4): updating - the summary is
// ahead, inside the deadline; refreshing - past it, the rows are shown as of
// evaluatedAt while a fenced re-read runs (refreshError: its last failure).
export type RowsStateDTO = { updating: boolean; refreshing: boolean; evaluatedAt: string | null; refreshError?: string | null };
// fault: how many counted objects are faults, from the same reading as
// included - a header's mark never mixes the summary's count with the
// rows' roles (slice 11b review).
export type PartCountsDTO = { total: number; included: number; fault: number };

// What the cert-manager rows adapter (health/useCertManagerRows) writes.
export type CertManagerRowsPayload = Pick<CertManagerSlice, "certificates" | "issuers" | "coverage" | "watchError" | "loading" | "rowsState" | "partCounts">;

// External Secrets Operator lens (M12). Refresh staleness is ESO-native: a
// Synced secret whose last successful sync is more than 2x its refresh
// interval old is Stale - the controller has stopped refreshing. A failing
// bound (Cluster)SecretStore is the headline for a stuck secret, exactly as a
// failing issuer is for a stuck certificate.
export type ExternalSecretDTO = {
  rule: RuleDTO;
  uid?: string; // identity (1c slice 3 §2a): a same-name recreation is another secret
  gvr?: string;
  generation?: number; // metadata.generation at the capture
  namespace: string;
  name: string;
  state: string;
  reason: string;
  message: string;
  storeKind: string;
  storeName: string;
  targetSecret: string;
  refreshIntervalSeconds: number; // 0 = refresh disabled
  refreshPolicy?: string; // Periodic (default) | CreatedOnce | OnChange - only Periodic refreshes on the interval
  lastRefreshAgeSeconds: number; // 0 = never synced OR under a second ago - read `refreshed`
  refreshed?: boolean; // status.refreshTime is set; older payloads omit it (fall back to age > 0)
  // status.refreshTime itself (0/absent = never synced): shared rows are
  // pushed only when they change, so the lens ticks the age from it.
  lastRefreshUnix?: number;
  stale?: boolean;
};
export type ExternalSecretStoreDTO = {
  rule: RuleDTO;
  uid?: string;
  gvr?: string;
  generation?: number;
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
  coverage?: KindCoverageDTO[]; // per kind: ExternalSecret, SecretStore, ClusterSecretStore
  rowsState?: RowsStateDTO;
  partCounts?: { secrets: PartCountsDTO | null; stores: PartCountsDTO | null };
};

// What the External Secrets rows adapter (health/useExternalSecretsRows) writes.
export type ExternalSecretsRowsPayload = Pick<ExternalSecretsSlice, "secrets" | "stores" | "coverage" | "watchError" | "loading" | "rowsState" | "partCounts">;

// "observability" removed 2026-06-10: metrics ship inline in overview/workloads/network lanes
// (design principle 5); a dedicated surface can return when it has real content.
export type ClusterSection = "overview" | "gitops" | "argo" | "helm" | "certs" | "externalsecrets" | "gatewayApi" | "flows" | "resources" | "crds" | "workloads" | "pods" | "events" | "nodes" | "access" | "upgrade" | "connectivity" | "karpenter" | "velero" | "admission" | "loadbalancers";
export type ArgoViewMode = "applications" | "projects" | "applicationsets";
// The Flux lens's two destinations (spec §4): the aggregate KS/HR view and Sources.
export type FluxViewMode = "reconciliation" | "sources" | "operator" | "images" | "composition" | "notifications";

export type OwnerDTO = { kind: string; namespace: string; name: string };
// ageSeconds is a list read's; the rows transport never sends it (addendum §3.1).
export type PodDTO = { name: string; uid?: string; createdUnix?: number; ready: boolean; restarts: number; reason: string; node: string; ageSeconds?: number };
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
  ownerRevisionNote?: string; // why ownerRevision is empty for an owned workload
  error: string;
};
export type WorkloadDTO = { kind: string; namespace: string; name: string; uid?: string; gvr?: string; desired: number; ready: number; available: number; updated: number; restarts: number; reason: string; rank: "unhealthy"|"degraded"|"restarts"|"healthy"; gitops: OwnerDTO | null; pods: PodDTO[]; resources: WorkloadResourcesDTO; stale?: boolean; generation?: number; observedGeneration?: number; reloader?: ReloadTargetsDTO | null; paused?: boolean; health?: HealthLike };

// Rollout history (newest first). Images may be empty when a
// ControllerRevision's patch data carries no template - never fabricated.
export type RolloutRevisionDTO = { revision: number; ageSeconds: number; images: string[]; current: boolean; changeCause?: string };
// uid/asOfUnix: the workload the history was read from, and when (§5b).
export type RolloutHistoryResultDTO = { revisions: RolloutRevisionDTO[]; uid?: string; asOfUnix?: number; error?: string };

// Fleet promotion tracker: one cluster's applied GitOps state.
export type PromotionUnitDTO = { kind: string; namespace: string; name: string; sourceKind: string; sourceName: string; revision: string; ready: string; suspended: boolean; stale: boolean; message: string };
export type PromotionSourceDTO = { kind: string; namespace: string; name: string; revision: string; ready: string; suspended: boolean };
export type PromotionSnapshotDTO = { fluxPresent: boolean; argoPresent: boolean; units: PromotionUnitDTO[]; sources: PromotionSourceDTO[]; error?: string };

// Upgrade-readiness report (per cluster).
export type SkewFindingDTO = { node: string; version: string; delta: number; level: string; detail: string };
export type DeprecatedUseDTO = { group: string; version: string; resource: string; kind: string; removedInMinor: number; replacedBy: string; count: number; countKnown: boolean; countCapped?: boolean };
// warnings name inputs the report could not read (e.g. the node list): a
// missing section must never read as clean.
export type UpgradeReportDTO = { serverVersion: string; serverMinor: number; eolDate: string; skew: SkewFindingDTO[]; deprecated: DeprecatedUseDTO[]; warnings?: string[]; error?: string };

// Connectivity can-i (NetworkPolicy simulation).
export type NetpolRefDTO = { namespace: string; name: string; admits: boolean };
export type DirectionVerdictDTO = { isolated: boolean; allowed: boolean; policies: NetpolRefDTO[]; explain: string };
export type ConnectivityReportDTO = { allowed: boolean; ingress: DirectionVerdictDTO; egress: DirectionVerdictDTO; srcLabels: Record<string, string>; dstLabels: Record<string, string>; ciliumPolicies: number; ciliumUnknown?: string; note?: string; error?: string };

// Survivability headroom simulation.
export type HeadroomPodRefDTO = { namespace: string; name: string; cpuReq: number; memReq: number };
export type PDBBlockerDTO = { namespace: string; name: string; disruptionsAllowed: number };
export type HeadroomResultDTO = { lostNodes: number; survivorNodes: number; displaced: number; rescheduled: number; unplaced: HeadroomPodRefDTO[]; cpuShortfall: number; memShortfall: number };
export type HeadroomReportDTO = { result: HeadroomResultDTO; pdbBlockers: PDBBlockerDTO[]; podListCapped: boolean; error?: string };

// Fleet expiry horizon: one cluster's expiring certificate material.
export type ExpiryItemDTO = { source: string; namespace: string; name: string; detail: string; subject: string; notAfterUnix: number };

// Karpenter lens.
export type KarpenterPoolDTO = { rule: RuleDTO; uid?: string; gvr?: string; generation?: number; name: string; ready: string; reason: string; message: string; weight: number; cpuLimit: number | null; cpuUsed: number | null; memLimit: number | null; memUsed: number | null; nodes: number };
export type KarpenterClaimDTO = { rule: RuleDTO; uid?: string; gvr?: string; generation?: number; createdUnix?: number; name: string; pool: string; nodeName: string; ready: string; failingType: string; failingStatus?: string; failingReason: string; failingMessage: string; drifted: boolean; consolidatable: boolean; capacityType: string; instanceType: string; ageSeconds: number };
export type KarpenterEventDTO = { type: string; reason: string; message: string; kind: string; name: string; lastSeenUnix: number };
// The controller probe and disruption feed, read on demand as of asOfUnix;
// the NodePools and NodeClaims are the health Observer's shared rows (1c
// slice 3 §5.5). controllerVerified: a karpenter Deployment was actually
// read - otherwise controllerHealthy carries no claim and controllerError says
// why. eventsError / eventsTruncated: the feed is partial or unreadable, never quiet.
export type KarpenterSnapshotDTO = { detected: boolean; asOfUnix?: number; controllerVerified?: boolean; controllerHealthy: boolean; controllerError?: string; events: KarpenterEventDTO[]; eventsError?: string; eventsTruncated?: boolean; error?: string };
export type ExpiryScanResultDTO = { items: ExpiryItemDTO[]; warnings: string[]; error?: string };
export type WorkloadUsageDTO = { cpuUsage: number | null; memUsage: number | null };
export type WorkloadMetricsStatusDTO = { available: boolean; message: string; updatedAt: string };
export type WorkloadMetricsResultDTO = { status: WorkloadMetricsStatusDTO; usage: Record<string, WorkloadUsageDTO> };
export const WORKLOAD_KINDS = ["Deployment", "StatefulSet", "DaemonSet"] as const;
export type WorkloadKind = typeof WORKLOAD_KINDS[number];
export type WorkloadsSlice = {
  cluster: string | null;
  // The rows are the Observer's, all namespaces (1c slice 3 §5.8): the
  // namespace is a list filter, and namespaces are the rows' own.
  namespace: string;        // "" = all
  items: WorkloadDTO[];
  namespaces: string[];
  fluxPresent: boolean; // the fact the rows were evaluated with (§2a)
  loading: boolean; // the visit has no rows payload yet
  coverage?: KindCoverageDTO[]; // the counted kinds (Deployment, StatefulSet, DaemonSet)
  dependencies?: KindCoverageDTO[]; // what attribution read (Pod, ReplicaSet), from the same capture
  rowsState?: RowsStateDTO;
  counts?: VerdictPartCounts | null; // the one count selector's (global) verdict counts
  // Live usage by "<kind>/<namespace>/<name>", a separate fetch joined at
  // render: a rows push never erases it.
  usage: Record<string, { cpu: number | null; mem: number | null }>;
  kindFilter: Record<WorkloadKind, boolean>;
  // True when the kinds were chosen - by navigation, history or an edit in
  // the view - rather than hydrated from saved preferences. A chosen "All
  // workloads" is not a default for preferences to fill.
  kindExplicit?: boolean;
  needsAttention: boolean;
  search: string;
  expanded: string[];       // keys "<kind>/<namespace>/<name>"
  metricsAvailable: boolean;
  metricsStatus: WorkloadMetricsStatusDTO | null;
  metricsStale: boolean;
  nearLimitSort: boolean;
  error?: string | null; // the counted kinds' read failure (stale: items are the last good read)
};
// What the Workloads rows adapter (health/useWorkloadRows) writes.
export type WorkloadsRowsPayload = Pick<WorkloadsSlice, "items" | "namespaces" | "fluxPresent" | "loading" | "error" | "coverage" | "dependencies" | "rowsState" | "counts">;

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
export type PodSummaryDTO = { namespace: string; name: string; uid?: string; gvr?: string; generation?: number; createdUnix?: number; ready: boolean; phase: string; reason: string; rank: "unhealthy"|"degraded"|"restarts"|"healthy"; restarts: number; node: string; ip: string; ownerKind: string; ownerName: string; ownerUid?: string; ageSeconds: number; containers: ContainerSummaryDTO[]; health?: HealthLike };
// A pod on the rows transport (1c slice 3 addendum §3.2): bounded and
// time-stable - no relative age (ticked from createdUnix), no legacy
// rank/reason/phase. The verdict message and each container's image, ports,
// resources and last termination are the UID-fenced detail's (PodDetailDTO).
export type PodRowContainerDTO = { name: string; init?: boolean; ready: boolean; restarts: number; state: string };
export type PodRowDTO = {
  namespace: string; name: string; uid: string; gvr?: string; generation?: number;
  ownerKind: string; ownerName: string; ownerUid?: string; node: string; ip: string;
  ready: boolean; restarts: number; createdUnix?: number; health?: HealthLike; containers: PodRowContainerDTO[];
};
// RowsVer is the (incarnation, Ver) of the rows a slice holds - the §6a
// probe anchors a push's timing to the lens commit that shows it.
export type RowsVer = { incarnation: number; ver: number };
export type PodDetailDTO = { summary: PodSummaryDTO; labels: Record<string, string>; annotations?: Record<string, string>; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; qosClass: string; serviceAccount: string; asOfUnix?: number; error?: string };

// A pod selection names the OBJECT (its UID, design §5b): a detail is shown
// only for that UID, and a verb armed from it writes only that UID.
export type PodRef = { namespace: string; name: string; uid: string };
export type PodsSlice = {
  cluster: string | null;
  // The rows are the Observer's, all namespaces and uncapped (1c slice 3
  // §5.9): the namespace is a list filter, and namespaces are the rows' own.
  namespace: string;
  items: PodRowDTO[];
  namespaces: string[];
  loading: boolean; // the visit has no rows payload yet
  needsAttention: boolean;
  search: string;
  selected: PodRef | null;
  detail: PodDetailDTO | null;
  detailLoading: boolean;
  error?: string | null; // the Pod kind's read failure (stale: items are the last good read)
  coverage?: KindCoverageDTO[]; // the counted kind (Pod)
  dependencies?: KindCoverageDTO[]; // what owner resolution read (ReplicaSet), from the same capture
  rowsState?: RowsStateDTO;
  counts?: VerdictPartCounts | null; // the one count selector's (global) verdict counts
  rowsVer?: RowsVer | null;
  // Palette "jump to namespace" (and a scratch pod launch): consumed by
  // PodsView as its namespace filter.
  nsRequest?: { cluster: string; namespace: string } | null;
};
// What the Pods rows adapter (health/usePodRows) writes.
export type PodsRowsPayload = Pick<PodsSlice, "items" | "namespaces" | "loading" | "error" | "coverage" | "dependencies" | "rowsState" | "counts"> & { rowsVer?: RowsVer | null };

export type EventRowDTO = { type: "Normal" | "Warning"; reason: string; message: string; count: number; namespace: string; kind: string; name: string; lastSeenUnix: number; firstSeenUnix: number };
export type EventsResultDTO = { namespaces: string[]; events: EventRowDTO[]; truncated?: boolean; error?: string };
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
  error?: string | null; // the last read FAILED; items are the last good read (or empty)
  rowsScope?: string | null; // "<cluster>|<namespace>" the current items were READ for
};

export type ResourceRef = { group: string; version: string; plural: string; kind: string; scope: string };
// cells: the server's printer-column values, in the page's columns order
// (The Line slice 15a).
export type InstanceDTO = {
  namespace: string; name: string; created: string; fields?: Record<string, string>; cells?: string[];
  // readAt: when this row's page landed (ms). A date cell is relative to its
  // own read, so an appended page never restamps earlier rows (slice 15,
  // implementation review 1). Frontend-only.
  readAt?: number;
};
// stampRead marks a page's rows with the time it landed, keeping a stamp
// the rows already carry.
export const stampRead = <T extends InstanceDTO>(items: T[], at = Date.now()): T[] => items.map((r) => (r.readAt ? r : { ...r, readAt: at }));
// One of the server's printer columns. priority 0 is kubectl's default view,
// 1 and above is -o wide; conditionType is set only when the JSONPath proves
// the column is a condition's status (design review 1).
export type InstanceColumnDTO = { name: string; type: string; format: string; description: string; jsonPath: string; priority: number; numeric: boolean; conditionType?: string };
// columnsSignature: what makes two pages' cells comparable - cells are
// positional, so a page drawn under another page's columns would mislabel
// its values (design review 1).
export function columnsSignature(cols: InstanceColumnDTO[] | null | undefined): string {
  return cols ? JSON.stringify(cols.map((c) => [c.name, c.type, c.format, c.priority, c.jsonPath])) : "none";
}
// error: the last page read FAILED - rows are then not "no instances".
// cluster + ref identify the list a page belongs to; a late page for any other
// cluster/GVR is dropped (review 2026-10-03 #5).
// columns: the list's printer columns, from its first page; null when it was
// read without a table, or when a later page's columns differed
// (columnsMismatch) - the whole list then draws no columns.
export type InstancesSlice = { cluster: string | null; ref: ResourceRef | null; rows: InstanceDTO[]; nextToken: string; loading: boolean; filter: string; riskOnly: boolean; error?: string | null;
  columns?: InstanceColumnDTO[] | null; columnsMismatch?: boolean };

// expectUid: the object meant, by UID (The Line slice 9 note §4.2) - the
// inspector says replaced, removed or unverified rather than show another.
export type InstanceRef = { namespace: string; name: string; expectUid?: string };
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
// path). On dry-run the namespaceOwner* fields carry the namespace's GitOps
// context: a unit prunes only what it tracks, so the object is at risk only
// when a reaching Argo app tracks it (argoTracking "tracked", or "possible"
// when the controller's tracking method is unknown) or - conditionally - when
// the unit manages and could delete the namespace itself.
export type AdhocResultDTO = {
  kind: string; namespace: string; name: string; ok: boolean; error: string;
  namespaceOwnerKind: string; namespaceOwnerName: string; namespaceOwnerNamespace: string; namespacePrunes: boolean;
  namespaceManaged: boolean;
  argoTracking: "" | "tracked" | "possible"; argoTrackingApp: string; argoTrackingPrunes: boolean;
  argoPruneOption: "" | "false" | "confirm"; argoConfigKnown: boolean;
};

// AdhocObjectDTO is one Klyx-created scratch object (label klyx.dev/adhoc) in
// the cleanup inventory.
// uid pins a delete to the object the inventory showed.
export type AdhocObjectDTO = { kind: string; group: string; version: string; plural: string; scope: string; namespace: string; name: string; uid: string; ageSeconds: number };
// The scratch inventory. unread names the kinds whose list failed (the
// inventory is incomplete); error means the sweep itself failed and objects
// is unreadable, never "no scratch objects".
export type AdhocInventoryDTO = { objects: AdhocObjectDTO[]; unread: string[]; error?: string };

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
// namespace - context only: a unit prunes what it tracks, and namespaceManaged
// marks the one that could delete the namespace itself. Empty namespace-owner
// fields mean "no managing unit DETECTED" - never "safe".
export type GitOpsOwnershipDTO = {
  ownerKind: string; ownerNamespace: string; ownerName: string; ownerPrunes: boolean; ownerSuspended: boolean; basis: string;
  namespaceOwnerKind: string; namespaceOwnerNamespace: string; namespaceOwnerName: string; namespacePrunes: boolean; namespaceManaged: boolean; namespaceBasis: string;
  argoApps: { namespace: string; name: string; prunes: boolean }[];
  warnings: string[];
};

// AI-assist ("Explain this"). Read-only; errors fold into `error` (ok stays false).
// groundedOn names the resolved nodes sent to the model, for traceability.
// redacted: manifest values masked before a fix suggestion was sent.
// backend is the provider id (anthropic, openai, gemini, openaiCompatible, ...);
// model is the model the vendor reports having used; origin is where the
// prompt went.
export type AIResultDTO = { ok: boolean; explanation: string; model: string; backend: string; origin?: string; groundedOn: string; error: string; redacted?: number; scrubbed?: number };
export type AIAvailabilityDTO = { backend: string; ready: boolean; model?: string; origin?: string; problem?: string };
// The Line slice 15c: one CRD read in full. schema is one version's,
// flattened into bounded nodes whose paths are segment lists ("p" a
// property, "i" an array's items, "m" a map's values).
export type SchemaPathSeg = { k: "p" | "i" | "m"; n?: string };
export type SchemaNodeDTO = { path: SchemaPathSeg[]; type?: string; format?: string; required?: boolean; enum?: string[]; enumMore?: number; default?: string; defaultTruncated?: boolean;
  description?: string; descriptionTruncated?: boolean; preserveUnknown?: boolean; intOrString?: boolean; embeddedResource?: boolean; constraints?: string[]; deeper?: boolean };
export type PrinterColumnDTO = { name: string; type: string; format?: string; jsonPath: string; description?: string; priority: number };
export type CRDVersionDTO = { name: string; served: boolean; storage: boolean; deprecated: boolean; deprecationWarning?: string; columns: PrinterColumnDTO[] };
export type CRDConversionDTO = { strategy: string; url?: string; serviceNamespace?: string; serviceName?: string; servicePort?: number; servicePath?: string; reviewVersions?: string[] };
export type CRDDefinitionDTO = { name: string; group: string; kind: string; plural: string; singular: string; listKind: string; scope: string; shortNames: string[]; categories: string[];
  operator: string; uid: string; created: string; versions: CRDVersionDTO[]; storedVersions: string[]; conversion: CRDConversionDTO; conditions: ConditionDTO[];
  // schemaRecorded: the version records a schema; schema[0] is then its root
  // (an empty path) and the fields follow.
  schemaVersion: string; schemaRecorded?: boolean; schema: SchemaNodeDTO[]; schemaTruncated?: boolean; error?: string; notFound?: boolean };

// The Line slice 15b: summary is the object's own table row (every
// priority), read in the same response as the object; observedGeneration is
// -1 when the object records none; fieldsJSON is the sanitised object
// without apiVersion, kind and metadata ("" with fieldsOmitted saying why).
export type SummaryCellDTO = InstanceColumnDTO & { value: string };
export type FieldManagerDTO = { manager: string; operation: string; subresource?: string; time?: string };
export type OwnerRefDTO = { apiVersion: string; kind: string; name: string; controller?: boolean };
export type InstanceDetailDTO = { kind: string; namespace: string; name: string; uid?: string; generation?: number; created: string; labels: Record<string, string>; annotations?: Record<string, string>; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; secretKeys?: SecretKeyDTO[]; serviceBacking?: ServiceBackingDTO | null; hpaScaling?: HPAScalingDTO | null; related?: RelatedRefDTO[]; facts?: FactDTO[]; error?: string; notFound?: boolean;
  summary?: SummaryCellDTO[]; observedGeneration?: number; deleting?: string; finalizers?: string[]; owners?: OwnerRefDTO[]; managers?: FieldManagerDTO[]; fieldsJSON?: string; fieldsOmitted?: string;
  // coverage S0b: false when Klyx shows this object sanitised - Edit is
  // hidden, and the backend refuses an apply whatever the frontend does.
  editEligible?: boolean; describeMode?: "withheld" | "url-scrubbed" | string };
// cluster + resource + ref identify the object a detail belongs to: DEV/STG/PRD
// share namespace/name, so ns/name alone let a late DEV detail render under the
// PRD header (review 2026-10-03 #5).
export type InstanceDetailSlice = { cluster: string | null; resource: ResourceRef | null; ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean };

// Fleet-level views (redesign step 7d): the board, the promotion tracker and
// the expiry horizon share the Fleet band.
export type FleetView = "promotion" | "expiry" | "backups";
export type Route =
  | { name: "fleet"; view?: FleetView }
  | { name: "forwards" }
  | { name: "settings" }
  | { name: "activity" }
  | { name: "cluster"; cluster: string; section: ClusterSection; resource?: ResourceRef; instance?: InstanceRef; gateway?: GatewayRef; argoView?: ArgoViewMode; fluxView?: FluxViewMode };

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
  flows: "Hubble flows",
  resources: "Resources",
  crds: "CRDs",
  workloads: "Workloads",
  pods: "Pods",
  events: "Events",
  nodes: "Nodes",
  access: "Access",
  upgrade: "Upgrade",
  connectivity: "Connectivity",
  karpenter: "Karpenter",
  velero: "Velero",
  admission: "Admission",
  loadbalancers: "Load balancers",
};

export type CRDKindDTO = { kind: string; plural: string; scope: string; version: string; operator: string; shortNames: string[] };
export type CRDGroupDTO = { group: string; category: string; kinds: CRDKindDTO[] };
// error: the count FAILED (e.g. forbidden) - count is then meaningless, never zero.
export type CRDCountDTO = { count: number; capped: boolean; error?: string };
export type CRDGroupBy = "group" | "operator" | "scope" | "alphabetical";

export type CRDSlice = {
  cluster: string | null;
  groups: CRDGroupDTO[];
  loading: boolean;
  error?: string | null; // the CRD list read FAILED: empty groups are not "no CRDs"
  expanded: string[];
  counts: Record<string, CRDCountDTO>;
  groupBy: CRDGroupBy;
  search: string;
  builtinCategory: string | null;
  // fallback: the CRD list failed with this error, and groups come from
  // discovery (The Line slice 15c); selected: the kind whose definition the
  // inspector shows. fallbackMissing names the groups discovery could not
  // read ("group: reason") - a partial fallback says so.
  fallback?: string | null;
  fallbackMissing?: string[];
  selected?: { group: string; plural: string; kind: string; version: string; scope: string } | null;
};

// Counts are keyed by CLUSTER as well as GVR: a count still in flight when the
// user switches cluster lands in its own cluster's slot, never under the new
// cluster's header (DEV/STG/PRD share kinds - review 2026-10-03 #5).
// sameResource compares two GVRs (version included: a different served version
// is a different list).
export const sameResource = (a: ResourceRef | null | undefined, b: ResourceRef | null | undefined) =>
  !!a && !!b && a.group === b.group && a.version === b.version && a.plural === b.plural;

export const crdCountKey = (cluster: string, group: string, version: string, plural: string) => `${cluster}|${group}/${version}/${plural}`;

export type GatewayAddressDTO = { type: string; value: string };
// CondDTO: one status condition as observed. status "" = absent (no
// controller wrote it) - distinct from "False" (rejected) and "Unknown".
export type CondDTO = { status: string; reason: string; message: string; observedGeneration: number };
export type GatewayRefDTO = { rule: RuleDTO; uid?: string; gvr?: string; namespace: string; name: string; className: string; addresses?: GatewayAddressDTO[]; listeners?: ListenerDTO[]; accepted: boolean; programmed: boolean; stale?: boolean; generation?: number; observedGeneration?: number; acceptedCond?: CondDTO; programmedCond?: CondDTO };
// One HTTPRoute of the Gateways task's routes part (1c slice 3 §4a): counted
// once by UID with its worst attachment's captured role/label/parent, and
// every attachment. The topology stays the route's data path.
export type GatewayRouteAttachmentDTO = { parent: string; rule: RuleDTO };
export type GatewayRouteRowDTO = { uid?: string; gvr?: string; generation?: number; namespace: string; name: string; parent: string; rule: RuleDTO; attachments: GatewayRouteAttachmentDTO[] };
export type PolicyDetailDTO = { key: string; value: string };
export type PolicyRefDTO = { kind: string; namespace: string; name: string; targetKind: string; targetNamespace: string; targetName: string; targetSectionName: string; summary: string; details: PolicyDetailDTO[]; inferred: boolean; match: string };
export type CertificateRefDTO = { group: string; kind: string; namespace: string; name: string };
export type ListenerDTO = { name: string; protocol: string; hostname: string; port: number; certificateRefs?: CertificateRefDTO[] };
export type MatchDTO = { pathType: string; pathValue: string; method: string; rule?: number };
// One spec.rules entry with its verdict and each backend's own (The Line
// slice 8d): weight is the effective configured weight (1 when unset).
export type RouteRuleBackendDTO = { kind: string; namespace: string; name: string; port: number; weight: number; weightSet: boolean; rule: RuleDTO };
export type RouteRuleDTO = { index: number; fingerprint?: string; rule: RuleDTO; matches: MatchDTO[]; backends: RouteRuleBackendDTO[] };
// Endpoints, not pods: nonPod counts endpoints without a pod TargetRef.
export type EndpointCountDTO = { ready: number; total: number; nonPod: number; error?: string };
// weightSet false: the backendRef wrote no weight (the Gateway API default
// of 1); an explicit weight 0 sends the backend no traffic. rule is the
// spec.rules index listing it - weights split traffic within one rule.
export type BackendDTO = { kind: string; name: string; namespace: string; port: number; weight: number; weightSet?: boolean; rule?: number; state?: "resolved" | "not-found" | "unreadable" | "unsupported"; error?: string; endpoints?: EndpointCountDTO };
export type PodCountDTO = { ready: number; total: number; unknown: boolean };
export type ServiceNodeDTO = { namespace: string; name: string; type: string; port: number; resolved: boolean; global: boolean; meshClusters: string[]; meshUnconfirmed: boolean; policies: PolicyRefDTO[]; cnps: PolicyRefDTO[] };
export type GatewayNodeDTO = { rule: RuleDTO; namespace: string; name: string; className: string; listeners: ListenerDTO[]; addresses?: GatewayAddressDTO[]; accepted: boolean; programmed: boolean; policies: PolicyRefDTO[]; acceptedCond?: CondDTO; programmedCond?: CondDTO; generation?: number };
export type RouteNodeDTO = { uid?: string; rule: RuleDTO; namespace: string; name: string; hostnames: string[]; matches: MatchDTO[]; parentSectionName?: string; accepted: boolean; resolvedRefs: boolean; backends: BackendDTO[]; rules?: RouteRuleDTO[]; services: ServiceNodeDTO[]; pods: PodCountDTO; policies: PolicyRefDTO[]; parentFound?: boolean; acceptedCond?: CondDTO; resolvedRefsCond?: CondDTO; generation?: number };
// error = the topology could not be read at all (never a snapshot);
// routesComplete = the HTTPRoute list succeeded (absence is then proof).
export type TopologyDTO = { gateway: GatewayNodeDTO; routes: RouteNodeDTO[]; clusterPolicies?: PolicyRefDTO[]; warnings?: string[]; error?: string; routesComplete?: boolean; routesError?: string };
export type GatewayRef = { namespace: string; name: string };

export type RouteMetricDTO = { rps: number | null; p50: number | null; p99: number | null; errRate: number | null };
export type RouteMetricsStatusDTO = { available: boolean; message: string; updatedAt: string };
export type RouteMetricsResultDTO = { status: RouteMetricsStatusDTO; routes: Record<string, RouteMetricDTO> };

// The Gateways list is the health Observer's shared rows (health/
// useGatewayRows, 1c slice 3 §5.6); this slice holds the topology fetch.
export type NetworkSlice = {
  cluster: string | null;
  selected: GatewayRef | null;
  topology: TopologyDTO | null;
  topologyLoading: boolean;
  topologyError?: string | null; // the last topology read failed (rendered, never blank)
  topologyAt?: number | null;    // when the shown topology was received (ms)
  routesAt?: number | null;      // when the shown routes were last read COMPLETELY (ms)
  routeMetricsAt?: number | null; // when the shown route numbers were received (ms)
  selectedRoute: string | null; // "<ns>/<name>"
  routeMetrics: Record<string, RouteMetricDTO>;
  routeMetricsStatus: RouteMetricsStatusDTO | null;
  routeMetricsStale: boolean;
};


export type MetricsDTO = { available: boolean; mode: string; source: string; warning: string; reason: string; cpuFraction: number | null; memFraction: number | null };
export type MetricsSlice = { cluster: string | null; dto: MetricsDTO | null; loading: boolean };

// Nodes types
export type NodeSummaryDTO = { name: string; uid?: string; gvr?: string; generation?: number; createdUnix?: number; roles: string[]; ready: boolean; unschedulable: boolean; problems: string[]; version: string; os: string; arch: string; taintCount: number; cpuCapacity: number; cpuAllocatable: number; memCapacity: number; memAllocatable: number; podCapacity: number; ageSeconds?: number; pool?: string; zone?: string; health?: HealthLike };
// A node on the rows transport: time-stable, so never a relative age (the
// lens ticks it from createdUnix; addendum §3.1).
export type NodeRowDTO = Omit<NodeSummaryDTO, "ageSeconds">;
export type NodeTaintDTO = { key: string; value: string; effect: string };
export type PodOnNodeDTO = { namespace: string; name: string; phase: string };
export type NodeDetailDTO = { summary: NodeSummaryDTO; labels: Record<string, string>; annotations?: Record<string, string>; taints: NodeTaintDTO[]; conditions: ConditionDTO[]; events: EventDTO[]; yaml: string; podsOnNode: PodOnNodeDTO[]; asOfUnix?: number; error?: string };

// A node selection names the OBJECT (its UID, design §5b): a detail is shown
// only for that UID, and a verb armed from it writes only that UID.
export type NodeRef = { name: string; uid: string };
export type NodesSlice = {
  cluster: string | null;
  items: NodeRowDTO[];
  loading: boolean; // the rows part has not answered yet
  error?: string | null; // the Node kind's read failure (stale: items are the last good read)
  // From the health Observer's shared rows (1c slice 3 §5.7).
  coverage?: KindCoverageDTO[];
  rowsState?: RowsStateDTO;
  counts?: VerdictPartCounts | null; // the one count selector's (global) verdict counts
  selected: NodeRef | null;
  detail: NodeDetailDTO | null;
  detailLoading: boolean;
};
// A verdict part's counts (observe.Counts).
export type VerdictPartCounts = { total: number; fault: number; degraded: number; unknown: number; progressing: number; paused: number; ok: number };
// What the Nodes rows adapter (health/useNodeRows) writes.
export type NodesRowsPayload = Pick<NodesSlice, "items" | "loading" | "error" | "coverage" | "rowsState" | "counts">;

// ---- Helm types ---------------------------------------------------------------

export type HelmReleaseDTO = {
  name: string;
  namespace: string;
  chart: string;
  appVersion: string;
  status: string;
  revision: number;
  updatedUnix: number;
  // The Go rule at read time (internal/helmcli rules.go) and, when a pending
  // release has a deadline ahead, the verdict it takes then with no new read.
  rule: RuleDTO;
  then?: RuleStepDTO;
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
  // Helm is a bounded CLI snapshot, never a live watch. snapshotAt is when the
  // last successful `helm list` arrived; error is the latest list failure -
  // releases then hold the last good snapshot (or nothing, if none was read).
  snapshotAt?: number | null;
  error?: string | null;
  // Detail read failures are evidence, never an empty history or values.
  historyError?: string | null;
  valuesError?: string | null;
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
  // Last read failure per list (null = last read succeeded). A failure keeps
  // the last good rows for the same cluster, so the lens can say "stale".
  error?: string | null;
  projectsError?: string | null;
  applicationSetsError?: string | null;
  expanded: string[]; // "<ns>/<name>" keys
  // From the health Observer's shared rows (1c slice 3 §5.4) - Applications
  // and ApplicationSets; AppProjects stay a fetch.
  coverage?: KindCoverageDTO[];
  rowsState?: RowsStateDTO;
  partCounts?: { applications: PartCountsDTO | null; applicationsets: PartCountsDTO | null };
};

// What the Argo rows adapter (health/useArgoRows) writes.
export type ArgoRowsPayload = Pick<ArgoSlice, "apps" | "available" | "message" | "error" | "loading"
  | "applicationSets" | "applicationSetsAvailable" | "applicationSetsMessage" | "applicationSetsError" | "applicationSetsLoading"
  | "coverage" | "rowsState" | "partCounts">;

const NODES_EMPTY: NodesSlice = { cluster: null, items: [], loading: false, selected: null, detail: null, detailLoading: false };

const ARGO_EMPTY_ROWS = {
  apps: [] as ArgoAppDTO[], projects: [] as ArgoProjectDTO[], applicationSets: [] as ArgoApplicationSetDTO[],
  available: true, projectsAvailable: true, applicationSetsAvailable: true,
  message: "", projectsMessage: "", applicationSetsMessage: "",
  error: null, projectsError: null, applicationSetsError: null,
};

// ---- / Helm types -------------------------------------------------------------

// info: a fact to note, neither an outcome nor a failure (faults listed at
// connect, The Line slice 10).
export type ActionStatus = { kind: "success" | "error" | "info"; message: string };
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
  // select-*: a uid names the object meant (The Line slice 9 note §4.2):
  // the lens pins it, and a replacement or an absence is said, never the
  // current same-name row substituted
  | { kind: "select-pod"; cluster: string; namespace: string; name: string; uid?: string }
  | { kind: "select-workload"; cluster: string; workloadKind: string; namespace: string; name: string; uid?: string }
  | { kind: "select-node"; cluster: string; node: string; uid?: string }
  | { kind: "select-flux"; cluster: string; fluxKind: string; namespace: string; name: string; uid?: string }
  | { kind: "access-check"; cluster: string; namespace: string; name: string }
  // open-scratch opens the Scratch panel (Pods); compare-workload opens a
  // workload's fleet compare. Both only open existing read-first surfaces.
  | { kind: "open-scratch"; cluster: string }
  | { kind: "compare-workload"; cluster: string; workloadKind: WorkloadKind; namespace: string; name: string }
  // reveal: open a part's view and select its object (health 1c slice 4
  // §5.4) - selection only, never an action
  | { kind: "reveal"; cluster: string; task: string; part: string; ref: RevealRef };
// RevealRef names one object a summary part points at (its Top ref).
export type RevealRef = { gvr: string; namespace?: string; name: string; uid?: string; parent?: string };

type FleetState = {
  clusters: ClusterDTO[];
  setClusters: (c: ClusterDTO[]) => void;
  route: Route;
  openFleet: (view?: FleetView) => void;
  openForwards: () => void;
  openSettings: () => void;
  newContexts: number;
  setNewContexts: (n: number) => void;
  openCluster: (name: string) => void;
  setSection: (s: ClusterSection) => void;
  openArgo: (mode?: ArgoViewMode) => void;
  openFlux: (view?: FluxViewMode) => void;
  openResource: (ref: ResourceRef) => void;
  closeResource: () => void;
  instances: InstancesSlice;
  setInstancesLoading: (cluster: string, ref: ResourceRef) => void;
  setInstancePage: (items: InstanceDTO[], nextToken: string, columns?: InstanceColumnDTO[] | null) => void;
  addInstancePage: (items: InstanceDTO[], nextToken: string, columns?: InstanceColumnDTO[] | null) => void;
  setInstanceFilter: (s: string) => void;
  setInstanceRiskOnly: (v: boolean) => void;
  toggleInstanceRiskOnly: () => void;
  clearInstances: () => void;
  openInstance: (namespace: string, name: string, expectUid?: string) => void;
  // instanceOpens counts openings: the instance inspector reads on each one,
  // an identical reopening included (review of efbfb31)
  instanceOpens: number;
  closeInstance: () => void;
  instanceDetail: InstanceDetailSlice;
  setInstanceDetailLoading: (cluster: string, resource: ResourceRef, ref: InstanceRef) => void;
  setInstanceDetail: (d: InstanceDetailDTO) => void;
  clearInstanceDetail: () => void;
  gitops: GitOpsSlice;
  setGitOps: (cluster: string, resources: FluxResourceDTO[], sources: FluxSourceDTO[], watchError?: string) => void;
  setGitOpsLoading: (cluster: string) => void;
  setGitOpsFromRows: (cluster: string, p: GitOpsRowsPayload) => void;
  clearGitOps: () => void;
  expand: (key: string) => void;
  collapse: () => void;
  setDetail: (d: ResourceDetailDTO, cluster?: string) => void;
  certmanager: CertManagerSlice;
  setCertManager: (cluster: string, certificates: CertificateDTO[], issuers: IssuerDTO[], watchError?: string, coverage?: KindCoverageDTO[], rowsState?: CertManagerSlice["rowsState"]) => void;
  setCertManagerLoading: (cluster: string) => void;
  setCertManagerFromRows: (cluster: string, p: CertManagerRowsPayload) => void;
  clearCertManager: () => void;
  selectCertificate: (key: string | null) => void;
  setCertChain: (chain: CertChainDTO, cluster?: string) => void;
  externalsecrets: ExternalSecretsSlice;
  setExternalSecrets: (cluster: string, secrets: ExternalSecretDTO[], stores: ExternalSecretStoreDTO[], watchError?: string, coverage?: KindCoverageDTO[]) => void;
  setExternalSecretsLoading: (cluster: string) => void;
  setExternalSecretsFromRows: (cluster: string, p: ExternalSecretsRowsPayload) => void;
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
  // logActivity records an Activity entry without a toast (per-call AI chat
  // records: metadata only, never question or answer text).
  logActivity: (s: ActionStatus) => void;
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
  setCRDs: (cluster: string, groups: CRDGroupDTO[], error?: string, fallback?: string, fallbackMissing?: string[]) => void;
  selectCRD: (sel: { group: string; plural: string; kind: string; version: string; scope: string } | null) => void;
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
  setTopologyLoading: (cluster: string, ref: GatewayRef) => void;
  setTopology: (t: TopologyDTO) => void;
  setTopologyError: (error: string) => void;
  selectRoute: (key: string | null) => void;
  clearNetwork: () => void;
  setRouteMetrics: (result: RouteMetricsResultDTO) => void;
  metrics: MetricsSlice;
  setMetricsLoading: (cluster: string) => void;
  setMetrics: (cluster: string, dto: MetricsDTO) => void;
  clearMetrics: () => void;
  workloads: WorkloadsSlice;
  setWorkloadsVisit: (cluster: string) => void;
  setWorkloadsFromRows: (cluster: string, p: WorkloadsRowsPayload) => void;
  setWorkloadsNamespace: (namespace: string) => void;
  toggleWorkloadKind: (k: WorkloadKind) => void;
  setWorkloadKindFilter: (kinds: readonly WorkloadKind[]) => void;
  hydrateWorkloadKinds: (kinds: readonly WorkloadKind[]) => void;
  openWorkloads: (kinds?: readonly WorkloadKind[]) => void;
  toggleNeedsAttention: () => void;
  setWorkloadsNeedsAttention: (v: boolean) => void;
  setWorkloadsSearch: (s: string) => void;
  toggleWorkloadExpand: (key: string) => void;
  setWorkloadUsage: (cluster: string, namespace: string, result: WorkloadMetricsResultDTO) => void;
  toggleNearLimitSort: () => void;
  clearWorkloads: () => void;
  pods: PodsSlice;
  setPodsVisit: (cluster: string) => void;
  setPodsFromRows: (cluster: string, p: PodsRowsPayload) => void;
  setPodsNamespace: (namespace: string) => void;
  togglePodsNeedsAttention: () => void;
  setPodsNeedsAttention: (v: boolean) => void;
  setPodsSearch: (s: string) => void;
  selectPod: (ref: PodRef | null) => void;
  setPodDetail: (ref: PodRef, detail: PodDetailDTO) => void;
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
  setNodesVisit: (cluster: string) => void;
  setNodesFromRows: (cluster: string, p: NodesRowsPayload) => void;
  selectNode: (ref: NodeRef | null) => void;
  setNodeDetail: (ref: NodeRef, detail: NodeDetailDTO) => void;
  clearNodes: () => void;
  helm: HelmSlice;
  argo: ArgoSlice;
  setArgoProjectsLoading: (cluster: string) => void;
  setArgoProjects: (cluster: string, r: ArgoProjectsResultDTO) => void;
  setArgoVisit: (cluster: string) => void;
  setArgoFromRows: (cluster: string, p: ArgoRowsPayload) => void;
  toggleArgoExpand: (key: string) => void;
  clearArgo: () => void;
  setHelmLoading: (cluster: string) => void;
  setHelm: (cluster: string, available: boolean, message: string, releases: HelmReleaseDTO[]) => void;
  setHelmListError: (cluster: string, message: string) => void;
  selectHelmRelease: (ref: HelmRef | null) => void;
  setHelmDetail: (ref: HelmRef, history: HelmHistoryEntryDTO[], values: string, historyError?: string | null, valuesError?: string | null) => void;
  clearHelm: () => void;
};

export const useFleet = create<FleetState>((set) => ({
  clusters: [],
  setClusters: (clusters) => set({ clusters }),
  route: { name: "fleet" },
  openFleet: (view) => set({ route: view ? { name: "fleet", view } : { name: "fleet" } }),
  openForwards: () => set({ route: { name: "forwards" } }),
  openSettings: () => set({ route: { name: "settings" } }),
  newContexts: 0,
  setNewContexts: (newContexts) => set({ newContexts }),
  openCluster: (name) => set({ route: { name: "cluster", cluster: name, section: "overview" } }),
  setSection: (section) =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section } } : {})),
  openArgo: (mode = "applications") =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section: "argo", argoView: mode } } : {})),
  openFlux: (view = "reconciliation") =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section: "gitops", fluxView: view } } : {})),
  openWorkloads: (kinds) =>
    set((s) => {
      if (s.route.name !== "cluster") return {};
      return {
        route: { name: "cluster", cluster: s.route.cluster, section: "workloads" },
        workloads: {
          ...s.workloads,
          kindFilter: kinds ? workloadKindFilterFor(kinds) : s.workloads.kindFilter,
          kindExplicit: kinds ? true : s.workloads.kindExplicit,
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
        instances: { cluster: s.route.cluster, ref: resource, rows: [], nextToken: "", loading: true, filter: "", riskOnly: false },
      };
    }),
  closeResource: () =>
    set((s) => {
      if (s.route.name !== "cluster") return {};
      const section: ClusterSection = (s.route.section === "crds" || s.route.section === "resources") ? s.route.section : "resources";
      return { route: { name: "cluster", cluster: s.route.cluster, section } };
    }),
  instances: { cluster: null, ref: null, rows: [], nextToken: "", loading: false, filter: "", riskOnly: false },
  setInstancesLoading: (cluster, ref) => set((s) => {
    const sameRef = s.instances.cluster === cluster && s.instances.ref?.group === ref.group && s.instances.ref?.version === ref.version && s.instances.ref?.plural === ref.plural;
    return { instances: { cluster, ref, rows: [], nextToken: "", loading: true, filter: sameRef ? s.instances.filter : "", riskOnly: sameRef ? s.instances.riskOnly : false } };
  }),
  setInstancePage: (items, nextToken, columns = null) => set((s) => ({ instances: { ...s.instances, rows: stampRead(items), nextToken, loading: false, error: null, columns: columns ?? null, columnsMismatch: false } })),
  addInstancePage: (items, nextToken, columns = null) => set((s) => {
    // A continuation read with other columns (or none) fails closed: the
    // whole list stops drawing columns, never values under the wrong ones.
    const mismatch = !!s.instances.columnsMismatch || columnsSignature(s.instances.columns) !== columnsSignature(columns);
    return { instances: { ...s.instances, rows: [...s.instances.rows, ...stampRead(items)], nextToken, loading: false, columns: mismatch ? null : s.instances.columns, columnsMismatch: mismatch } };
  }),
  setInstanceFilter: (filter) => set((s) => ({ instances: { ...s.instances, filter } })),
  setInstanceRiskOnly: (riskOnly) => set((s) => ({ instances: { ...s.instances, riskOnly } })),
  toggleInstanceRiskOnly: () => set((s) => ({ instances: { ...s.instances, riskOnly: !s.instances.riskOnly } })),
  clearInstances: () => set({ instances: { cluster: null, ref: null, rows: [], nextToken: "", loading: false, filter: "", riskOnly: false } }),
  instanceOpens: 0,
  openInstance: (namespace, name, expectUid) =>
    set((s) => {
      if (s.route.name !== "cluster" || !s.route.resource) return {};
      const section: ClusterSection = (s.route.section === "crds" || s.route.section === "resources") ? s.route.section : "resources";
      return {
        route: { name: "cluster", cluster: s.route.cluster, section, resource: s.route.resource, instance: expectUid ? { namespace, name, expectUid } : { namespace, name } },
        instanceOpens: s.instanceOpens + 1,
        instanceDetail: { cluster: s.route.cluster, resource: s.route.resource, ref: { namespace, name }, detail: null, loading: true },
      };
    }),
  closeInstance: () =>
    set((s) =>
      s.route.name === "cluster"
        ? { route: { name: "cluster", cluster: s.route.cluster, section: s.route.section, resource: s.route.resource } }
        : {}),
  instanceDetail: { cluster: null, resource: null, ref: null, detail: null, loading: false },
  setInstanceDetailLoading: (cluster, resource, ref) => set((s) => {
    const current = s.instanceDetail;
    const sameRef = current.cluster === cluster && sameResource(current.resource, resource) && current.ref?.namespace === ref.namespace && current.ref?.name === ref.name;
    return { instanceDetail: { cluster, resource, ref, detail: sameRef ? current.detail : null, loading: true } };
  }),
  setInstanceDetail: (detail) => set((s) => ({ instanceDetail: { ...s.instanceDetail, detail, loading: false } })),
  clearInstanceDetail: () => set({ instanceDetail: { cluster: null, resource: null, ref: null, detail: null, loading: false } }),
  gitops: { cluster: null, resources: [], sources: [], loading: false, watchError: null, expandedKey: null, detail: null },
  setGitOps: (cluster, resources, sources, watchError) => set((s) => ({ gitops: { ...s.gitops, cluster, resources, sources, loading: false, watchError: watchError || null } })),
  // A different cluster starts from no selection and no detail: a same-key
  // selection must never carry another cluster's evidence over.
  setGitOpsLoading: (cluster) => set((s) => ({ gitops: { ...s.gitops, cluster, resources: [], sources: [], loading: true, watchError: null, coverage: undefined, rowsState: undefined, partCounts: undefined,
    ...(s.gitops.cluster !== cluster ? { expandedKey: null, detail: null, detailCluster: null } : {}) } })),
  setGitOpsFromRows: (cluster, p) => set((s) => ({ gitops: { ...s.gitops, cluster, ...p } })),
  clearGitOps: () => set({ gitops: { cluster: null, resources: [], sources: [], loading: false, watchError: null, expandedKey: null, detail: null } }),
  expand: (key) => set((s) => ({ gitops: { ...s.gitops, expandedKey: key, detail: null } })),
  collapse: () => set((s) => ({ gitops: { ...s.gitops, expandedKey: null, detail: null } })),
  setDetail: (d, cluster) => set((s) => ({ gitops: { ...s.gitops, detail: d, detailCluster: cluster ?? s.gitops.cluster } })),

  certmanager: { cluster: null, certificates: [], issuers: [], loading: false, watchError: null, selectedKey: null, chain: null, chainLoading: false },
  setCertManager: (cluster, certificates, issuers, watchError, coverage, rowsState) => set((s) => ({ certmanager: { ...s.certmanager, cluster, certificates, issuers, loading: false, watchError: watchError || null, coverage, rowsState } })),
  setCertManagerLoading: (cluster) => set((s) => ({ certmanager: { ...s.certmanager, cluster, certificates: [], issuers: [], loading: true, watchError: null, selectedKey: null, chain: null, chainLoading: false, coverage: undefined, rowsState: undefined, partCounts: undefined } })),
  setCertManagerFromRows: (cluster, p) => set((s) => ({ certmanager: { ...s.certmanager, cluster, ...p } })),
  clearCertManager: () => set({ certmanager: { cluster: null, certificates: [], issuers: [], loading: false, watchError: null, selectedKey: null, chain: null, chainLoading: false } }),
  selectCertificate: (key) => set((s) => ({ certmanager: { ...s.certmanager, selectedKey: key, chain: null, chainLoading: key !== null } })),
  setCertChain: (chain, cluster) => set((s) => ({ certmanager: { ...s.certmanager, chain, chainLoading: false, chainCluster: cluster ?? s.certmanager.cluster } })),

  externalsecrets: { cluster: null, secrets: [], stores: [], loading: false, watchError: null, selectedKey: null },
  setExternalSecrets: (cluster, secrets, stores, watchError, coverage) => set((s) => ({ externalsecrets: { ...s.externalsecrets, cluster, secrets, stores, loading: false, watchError: watchError || null, coverage } })),
  setExternalSecretsLoading: (cluster) => set((s) => ({ externalsecrets: { ...s.externalsecrets, cluster, secrets: [], stores: [], loading: true, watchError: null, selectedKey: null, coverage: undefined, rowsState: undefined, partCounts: undefined } })),
  setExternalSecretsFromRows: (cluster, p) => set((s) => ({ externalsecrets: { ...s.externalsecrets, cluster, ...p } })),
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
  logActivity: (entry) =>
    set((s) => {
      const event: ActionEvent = { ...entry, id: nextActionId++, at: Date.now() };
      const actionLog = [event, ...s.actionLog].slice(0, ACTION_LOG_MAX);
      saveActionLog(actionLog);
      return { actionLog };
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
  setCRDs: (cluster, groups, error, fallback, fallbackMissing) => set((s) => ({ crd: { ...s.crd, cluster, groups, loading: false, error: error || null, fallback: fallback || null, fallbackMissing: fallbackMissing ?? [] } })),
  selectCRD: (sel) => set((s) => ({ crd: { ...s.crd, selected: sel } })),
  // a new read owes its own fallback: the last read's never carries over
  setCRDLoading: (cluster) => set((s) => ({ crd: { ...s.crd, cluster, groups: [], loading: true, expanded: [], counts: {}, fallback: null, fallbackMissing: [] } })),
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
      const base = s.network.cluster === s.route.cluster ? s.network : { ...s.network, cluster: s.route.cluster };
      const route = { name: "cluster" as const, cluster: s.route.cluster, section: "gatewayApi" as const, gateway: { namespace, name } };
      // Re-opening the gateway already shown keeps its topology: the view's
      // fetch is keyed on the gateway identity and would not re-run, leaving
      // a blanked topology loading forever.
      if (base === s.network && s.network.selected?.namespace === namespace && s.network.selected?.name === name) {
        return { route, network: { ...s.network, selectedRoute: null } };
      }
      return {
        route,
        network: { ...base, selected: { namespace, name }, topology: null, topologyLoading: true, topologyError: null, topologyAt: null, routesAt: null, routeMetricsAt: null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
      };
    }),
  closeGateway: () =>
    set((s) => (s.route.name === "cluster" ? { route: { name: "cluster", cluster: s.route.cluster, section: "gatewayApi" } } : {})),
  network: { cluster: null, selected: null, topology: null, topologyLoading: false, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false },
  setTopologyLoading: (cluster, ref) => set((s) => {
    // A refresh of the gateway already shown keeps its topology, selection
    // and metrics until the new read lands (or fails, marked as last good).
    if (s.network.cluster === cluster && s.network.selected?.namespace === ref.namespace && s.network.selected?.name === ref.name) {
      return { network: { ...s.network, topologyLoading: true } };
    }
    const base = s.network.cluster === cluster ? s.network : { ...s.network, cluster };
    return { network: { ...base, selected: ref, topology: null, topologyLoading: true, topologyError: null, topologyAt: null, routesAt: null, routeMetricsAt: null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false } };
  }),
  setTopology: (topology) => set((s) => {
    const now = Date.now();
    // An incomplete route read never replaces routes from an earlier
    // complete one: they stay as last evidence (routesAt says when), and
    // nothing is claimed detached.
    const prev = s.network.topology;
    if (topology.routesComplete !== true && prev && s.network.routesAt) {
      return { network: { ...s.network, topology: { ...topology, routes: prev.routes }, topologyLoading: false, topologyError: null, topologyAt: now } };
    }
    return { network: { ...s.network, topology, topologyLoading: false, topologyError: null, topologyAt: now, routesAt: topology.routesComplete === true ? now : null } };
  }),
  setTopologyError: (error) => set((s) => ({ network: { ...s.network, topologyLoading: false, topologyError: error } })),
  selectRoute: (selectedRoute) => set((s) => ({ network: { ...s.network, selectedRoute } })),
  clearNetwork: () => set((s) => ({ network: { ...s.network, selected: null, topology: null, topologyLoading: false, topologyError: null, topologyAt: null, routesAt: null, routeMetricsAt: null, selectedRoute: null, routeMetrics: {}, routeMetricsStatus: null, routeMetricsStale: false } })),
  setRouteMetrics: (result) =>
    set((s) => {
      if (result.status.available) {
        return { network: { ...s.network, routeMetrics: result.routes ?? {}, routeMetricsStatus: result.status, routeMetricsStale: false, routeMetricsAt: Date.now() } };
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
  metrics: { cluster: null, dto: null, loading: false },
  setMetricsLoading: (cluster) => set((s) => ({ metrics: { cluster, dto: s.metrics.cluster === cluster ? s.metrics.dto : null, loading: true } })),
  setMetrics: (cluster, dto) => set({ metrics: { cluster, dto, loading: false } }),
  clearMetrics: () => set({ metrics: { cluster: null, dto: null, loading: false } }),
  workloads: { cluster: null, namespace: "", items: [], namespaces: [], fluxPresent: false, loading: false, usage: {},
    kindFilter: workloadKindFilterFor(WORKLOAD_KINDS), needsAttention: false, search: "", expanded: [],
    metricsAvailable: false, metricsStatus: null, metricsStale: false, nearLimitSort: false },
  // A new cluster's visit starts empty; a revisit keeps its filters.
  setWorkloadsVisit: (cluster) => set((s) => ({ workloads: { ...s.workloads, ...(s.workloads.cluster === cluster ? {} : { items: [], namespaces: [], usage: {}, error: null }), cluster, loading: true, coverage: undefined, dependencies: undefined, rowsState: undefined, counts: undefined } })),
  setWorkloadsFromRows: (cluster, p) => set((s) => ({ workloads: { ...s.workloads, cluster, ...p } })),
  setWorkloadsNamespace: (namespace) => set((s) => ({ workloads: { ...s.workloads, namespace } })),
  toggleWorkloadKind: (k) => set((s) => ({ workloads: { ...s.workloads, kindFilter: { ...s.workloads.kindFilter, [k]: !s.workloads.kindFilter[k] }, kindExplicit: true } })),
  setWorkloadKindFilter: (kinds) => set((s) => ({ workloads: { ...s.workloads, kindFilter: workloadKindFilterFor(kinds), kindExplicit: true } })),
  hydrateWorkloadKinds: (kinds) => set((s) => ({ workloads: { ...s.workloads, kindFilter: workloadKindFilterFor(kinds), kindExplicit: false } })),
  toggleNeedsAttention: () => set((s) => ({ workloads: { ...s.workloads, needsAttention: !s.workloads.needsAttention } })),
  setWorkloadsNeedsAttention: (v) => set((s) => ({ workloads: { ...s.workloads, needsAttention: v } })),
  setWorkloadsSearch: (search) => set((s) => ({ workloads: { ...s.workloads, search } })),
  toggleWorkloadExpand: (key) => set((s) => ({ workloads: { ...s.workloads, expanded: s.workloads.expanded.includes(key) ? s.workloads.expanded.filter((k) => k !== key) : [...s.workloads.expanded, key] } })),
  setWorkloadUsage: (cluster, namespace, result) =>
    set((s) => {
      if (s.workloads.cluster !== cluster || s.workloads.namespace !== namespace) return {};
      if (result.status.available) {
        const usage: WorkloadsSlice["usage"] = {};
        for (const [k, u] of Object.entries(result.usage)) usage[k] = { cpu: u.cpuUsage ?? null, mem: u.memUsage ?? null };
        return { workloads: { ...s.workloads, usage, metricsAvailable: true, metricsStatus: result.status, metricsStale: false } };
      }
      const keptUpdatedAt = s.workloads.metricsStatus?.updatedAt ?? "";
      return { workloads: { ...s.workloads, metricsStatus: { ...result.status, updatedAt: keptUpdatedAt }, metricsStale: s.workloads.metricsAvailable } };
    }),
  toggleNearLimitSort: () => set((s) => ({ workloads: { ...s.workloads, nearLimitSort: !s.workloads.nearLimitSort } })),
  // The view's cleanup. When the route still shows the workloads task (a
  // cluster switch by history or the palette reuses the mounted view), an
  // explicit kind choice belongs to the incoming place and is kept.
  clearWorkloads: () => set((s) => {
    const keepKinds = !!s.workloads.kindExplicit && s.route.name === "cluster" && s.route.section === "workloads" && !s.route.resource;
    return { workloads: { ...s.workloads, cluster: null, items: [], namespaces: [], expanded: [], usage: {}, loading: false,
      kindFilter: keepKinds ? s.workloads.kindFilter : workloadKindFilterFor(WORKLOAD_KINDS), kindExplicit: keepKinds, needsAttention: false, search: "", namespace: "", metricsAvailable: false, metricsStatus: null, metricsStale: false, nearLimitSort: false, error: null,
      coverage: undefined, dependencies: undefined, rowsState: undefined, counts: undefined } };
  }),
  pods: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false },
  // A new cluster's visit starts empty; a revisit keeps its filters.
  setPodsVisit: (cluster) => set((s) => ({ pods: { ...s.pods, ...(s.pods.cluster === cluster ? {} : { items: [], namespaces: [], error: null, selected: null, detail: null, detailLoading: false }), cluster, loading: true, coverage: undefined, dependencies: undefined, rowsState: undefined, counts: undefined } })),
  setPodsFromRows: (cluster, p) => set((s) => ({ pods: { ...s.pods, cluster, ...p } })),
  setPodsNamespace: (namespace) => set((s) => ({ pods: { ...s.pods, namespace } })),
  togglePodsNeedsAttention: () => set((s) => ({ pods: { ...s.pods, needsAttention: !s.pods.needsAttention } })),
  setPodsNeedsAttention: (v) => set((s) => ({ pods: { ...s.pods, needsAttention: v } })),
  setPodsSearch: (search) => set((s) => ({ pods: { ...s.pods, search } })),
  // Re-selecting the same object keeps its detail while it is re-read.
  selectPod: (ref) => set((s) => {
    const cur = s.pods.selected;
    const same = !!ref && !!cur && cur.namespace === ref.namespace && cur.name === ref.name && cur.uid === ref.uid;
    return { pods: { ...s.pods, selected: ref, detail: same ? s.pods.detail : null, detailLoading: ref !== null } };
  }),
  // The single selection-identity guard: a detail lands only on the object
  // (namespace, name and uid) it was read for.
  setPodDetail: (ref, detail) => set((s) => {
    const sel = s.pods.selected;
    if (!sel || sel.namespace !== ref.namespace || sel.name !== ref.name || sel.uid !== ref.uid) return {};
    return { pods: { ...s.pods, detail, detailLoading: false } };
  }),
  clearPods: () => set((s) => ({ pods: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false, nsRequest: s.pods.nsRequest } })),
  requestPodsNamespace: (cluster, namespace) => set((s) => ({ pods: { ...s.pods, nsRequest: { cluster, namespace } } })),
  clearPodsNsRequest: () => set((s) => ({ pods: { ...s.pods, nsRequest: null } })),
  events: { cluster: null, namespace: "", items: [], namespaces: [], loading: false, warningsOnly: false, search: "", live: false, truncated: false },
  setEventsLoading: (cluster, namespace) => set((s) => ({ events: { ...s.events, cluster, namespace, loading: true } })),
  setEvents: (cluster, namespace, result) => set((s) => {
    let namespaces = s.events.namespaces;
    if (namespace === "") namespaces = result.namespaces ?? [];
    if (namespaces.length === 0 && namespace !== "") namespaces = [namespace];
    const scope = `${cluster}|${namespace}`;
    if (result.error) {
      const same = s.events.rowsScope === scope;
      return { events: { ...s.events, cluster, namespace, items: same ? s.events.items : [], rowsScope: same ? scope : null, error: result.error, loading: false } };
    }
    return { events: { ...s.events, cluster, namespace, items: result.events ?? [], namespaces, loading: false, truncated: result.truncated ?? false, rowsScope: scope, error: null } };
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
  nodes: { ...NODES_EMPTY },
  setNodesVisit: (cluster) => set((s) => ({ nodes: { ...(s.nodes.cluster === cluster ? s.nodes : { ...NODES_EMPTY }), cluster, loading: true, coverage: undefined, rowsState: undefined, counts: undefined } })),
  setNodesFromRows: (cluster, p) => set((s) => ({ nodes: { ...s.nodes, cluster, ...p } })),
  // Re-selecting the same object keeps its detail while it is re-read.
  selectNode: (ref) => set((s) => {
    const same = !!ref && s.nodes.selected?.name === ref.name && s.nodes.selected.uid === ref.uid;
    return { nodes: { ...s.nodes, selected: ref, detail: same ? s.nodes.detail : null, detailLoading: ref !== null } };
  }),
  setNodeDetail: (ref, detail) => set((s) => {
    const sel = s.nodes.selected;
    if (!sel || sel.name !== ref.name || sel.uid !== ref.uid) return {};
    return { nodes: { ...s.nodes, detail, detailLoading: false } };
  }),
  clearNodes: () => set({ nodes: { ...NODES_EMPTY } }),
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
  // A different cluster starts empty: B's lens must never show A's rows while
  // B's first read is in flight.
  setArgoProjectsLoading: (cluster) => set((s) => ({ argo: { ...(s.argo.cluster === cluster ? s.argo : { ...s.argo, ...ARGO_EMPTY_ROWS }), cluster, projectsLoading: true } })),
  setArgoProjects: (cluster, r) => set((s) => ({ argo: r.error
    ? (() => { const keep = s.argo.cluster === cluster ? s.argo.projects : [];
        return { ...s.argo, cluster, projects: keep, projectsError: r.error, projectsLoading: false, ...(keep.length === 0 ? { projectsAvailable: false, projectsMessage: r.error } : {}) }; })()
    : { ...s.argo, cluster, projects: r.projects ?? [], projectsAvailable: r.available, projectsMessage: r.message ?? "", projectsError: null, projectsLoading: false } })),
  // A rows visit: a different cluster starts empty; the row lists load.
  setArgoVisit: (cluster) => set((s) => ({ argo: { ...(s.argo.cluster === cluster ? s.argo : { ...s.argo, ...ARGO_EMPTY_ROWS, expanded: [] }), cluster, loading: true, applicationSetsLoading: true, coverage: undefined, rowsState: undefined, partCounts: undefined } })),
  setArgoFromRows: (cluster, p) => set((s) => ({ argo: { ...s.argo, cluster, ...p } })),
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
  // A different cluster never inherits another cluster's snapshot or selection.
  setHelmLoading: (cluster) => set((s) => ({ helm: s.helm.cluster === cluster
    ? { ...s.helm, loading: true }
    : { cluster, releases: [], available: true, message: "", loading: true, selected: null, history: [], values: "", detailLoading: false, snapshotAt: null, error: null, historyError: null, valuesError: null } })),
  setHelm: (cluster, available, message, releases) => set((s) => ({ helm: { ...s.helm, cluster, available, message, releases, loading: false, error: null, snapshotAt: available ? Date.now() : null } })),
  // A list failure keeps the last good snapshot of the SAME cluster. With no
  // prior snapshot it also marks the slice unavailable (the legacy gate).
  setHelmListError: (cluster, message) => set((s) => {
    const prior = s.helm.cluster === cluster && s.helm.snapshotAt != null;
    return { helm: prior
      ? { ...s.helm, loading: false, error: message }
      : { ...s.helm, cluster, releases: [], available: false, message, loading: false, error: message, snapshotAt: null } };
  }),
  selectHelmRelease: (ref) => set((s) => ({ helm: { ...s.helm, selected: ref, history: [], values: "", detailLoading: ref !== null, historyError: null, valuesError: null } })),
  setHelmDetail: (ref, history, values, historyError = null, valuesError = null) => set((s) => {
    const sel = s.helm.selected;
    if (!sel || sel.namespace !== ref.namespace || sel.name !== ref.name) return {};
    return { helm: { ...s.helm, history, values, detailLoading: false, historyError, valuesError } };
  }),
  clearHelm: () => set({ helm: { cluster: null, releases: [], available: true, message: "", loading: false, selected: null, history: [], values: "", detailLoading: false, snapshotAt: null, error: null, historyError: null, valuesError: null } }),
}));

// podsText renders a cluster's pod count honestly: unknown is a dash, never
// a zero; a stale count says it is the last good one.
export function podsText(c: Pick<ClusterDTO, "pods" | "podsKnown" | "podsStale">): string {
  if (c.podsKnown === false) return "—";
  return c.podsStale ? `${c.pods} (last good)` : String(c.pods);
}
