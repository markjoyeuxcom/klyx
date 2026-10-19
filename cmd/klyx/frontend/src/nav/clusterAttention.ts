import type { ClusterDTO } from "../store/fleet";
import { taskCounts, type HealthRef, type HealthState, type HealthSummary, type HealthTask, type KindCoverage, type SelectedCounts } from "../store/health";
import { capState, type CapKey } from "./registry";

// The open cluster's attention from the Observer's publications (health 1c
// slice 4): one area per expected task, one line per part, every number
// through the one count selector (taskCounts). Pure: a function of the
// health store and the cluster's capabilities, so the mast, the tree and
// Overview read one model and agree.

type PartMeta = { part: string; label: string; destinationId?: string };
export type TaskMeta = { task: string; label: string; groupId: string; destinationId: string; cap?: CapKey; parts: PartMeta[] };

// The expected mapping (§2, §6). A part with a destinationId badges exactly
// that destination; the first part is the view its task's lens opens on.
export const TASKS: TaskMeta[] = [
  { task: "workloads", label: "Workloads", groupId: "workloads", destinationId: "workloads", parts: [{ part: "workloads", label: "Workloads", destinationId: "workloads" }] },
  { task: "pods", label: "Pods", groupId: "workloads", destinationId: "pods", parts: [{ part: "pods", label: "Pods", destinationId: "pods" }] },
  { task: "nodes", label: "Nodes", groupId: "cluster", destinationId: "nodes", parts: [{ part: "nodes", label: "Nodes", destinationId: "nodes" }] },
  { task: "events", label: "Events", groupId: "cluster", destinationId: "events", parts: [{ part: "warnings", label: "Warning events", destinationId: "events" }] },
  // coverage S8a: Alertmanager's alerts, counted apart from every object
  { task: "alerts", label: "Alerts", groupId: "cluster", destinationId: "alerts", cap: "alerting", parts: [{ part: "alerts", label: "Alertmanager alerts", destinationId: "alerts" }] },
  { task: "helm", label: "Helm releases", groupId: "helm", destinationId: "helm", parts: [{ part: "releases", label: "Releases", destinationId: "helm" }] },
  { task: "flux", label: "Flux", groupId: "flux", destinationId: "flux", cap: "flux",
    parts: [{ part: "reconciliations", label: "Reconciliations", destinationId: "flux" }, { part: "sources", label: "Sources", destinationId: "flux-sources" },
      { part: "fluxinstance", label: "FluxInstance", destinationId: "flux-operator" }, { part: "resourcesets", label: "ResourceSets", destinationId: "flux-operator" },
      { part: "images", label: "Image automation", destinationId: "flux-images" },
      { part: "composition", label: "Source composition", destinationId: "flux-composition" }, { part: "receivers", label: "Receivers", destinationId: "flux-notifications" }] },
  { task: "argo", label: "Argo CD", groupId: "argo", destinationId: "argo", cap: "argo",
    parts: [{ part: "applications", label: "Applications", destinationId: "argo" }, { part: "applicationsets", label: "ApplicationSets", destinationId: "argo-appsets" }] },
  { task: "certs", label: "Certificates", groupId: "certs", destinationId: "certs", cap: "certManager",
    parts: [{ part: "certificates", label: "Certificates", destinationId: "certs" }, { part: "issuers", label: "Issuers" }] },
  { task: "eso", label: "External secrets", groupId: "eso", destinationId: "eso", cap: "externalSecrets",
    parts: [{ part: "externalsecrets", label: "ExternalSecrets", destinationId: "eso" }, { part: "stores", label: "Stores" }] },
  { task: "gateway", label: "Gateways", groupId: "network", destinationId: "gateway", cap: "gatewayAPI",
    parts: [{ part: "gateways", label: "Gateways", destinationId: "gateway" }, { part: "listenersets", label: "ListenerSets" }, { part: "routes", label: "Routes" }] },
  { task: "lb", label: "Load balancers", groupId: "network", destinationId: "loadbalancers", cap: "loadBalancing",
    parts: [{ part: "services", label: "LoadBalancer Services", destinationId: "loadbalancers" }, { part: "pools", label: "Address pools" }, { part: "metallb", label: "MetalLB configuration" }] },
  // coverage S9: standalone HPAs (KEDA-owned ones under their ScaledObject)
  { task: "autoscaling", label: "HPAs", groupId: "autoscaling", destinationId: "autoscaling",
    parts: [{ part: "hpas", label: "HorizontalPodAutoscalers", destinationId: "autoscaling" }] },
  { task: "keda", label: "KEDA", groupId: "autoscaling", destinationId: "autoscaling", cap: "keda",
    parts: [{ part: "scaledobjects", label: "ScaledObjects", destinationId: "autoscaling" }, { part: "scaledjobs", label: "ScaledJobs" }] },
  { task: "karpenter", label: "Karpenter", groupId: "autoscaling", destinationId: "karpenter", cap: "karpenter",
    parts: [{ part: "nodeclaims", label: "NodeClaims", destinationId: "karpenter" }, { part: "nodepools", label: "NodePools" }] },
  { task: "velero", label: "Velero", groupId: "velero", destinationId: "velero", cap: "velero",
    parts: [{ part: "schedules", label: "Schedules", destinationId: "velero" }, { part: "locations", label: "Storage locations" }, { part: "backups", label: "Unscheduled backups" }] },
  { task: "admission", label: "Admission", groupId: "admission", destinationId: "admission", cap: "admission",
    parts: [{ part: "webhooks", label: "Webhook configurations", destinationId: "admission" }, { part: "policies", label: "Admission policies" }] },
];

const CLASSIFIER: Record<string, Line["classifier"]> = { workloads: "verdict", pods: "verdict", nodes: "verdict", events: "evidence", alerts: "alerts" };

// Derived membership (§2.1): an attached route exists only through its
// parent Gateway, so the parent kind is the routes line's evidence too.
const MEMBERSHIP: Record<string, string[]> = { "gateway/routes": ["gateways"] };

const KIND_LABELS: Record<string, string> = {
  pods: "Pods", deployments: "Deployments", statefulsets: "StatefulSets", daemonsets: "DaemonSets", replicasets: "ReplicaSets",
  nodes: "Nodes", events: "Events", helm: "Helm releases", gatewayclasses: "GatewayClasses", gateways: "Gateways", httproutes: "HTTPRoutes", listenersets: "ListenerSets",
  grpcroutes: "GRPCRoutes", tlsroutes: "TLSRoutes", tcproutes: "TCPRoutes", udproutes: "UDPRoutes", referencegrants: "ReferenceGrants", namespaces: "Namespaces",
  services: "Services", endpointslices: "EndpointSlices", kustomizations: "Kustomizations", helmreleases: "HelmReleases",
  gitrepositories: "GitRepositories", ocirepositories: "OCIRepositories", buckets: "Buckets", helmrepositories: "HelmRepositories",
  helmcharts: "HelmCharts", applications: "Applications", applicationsets: "ApplicationSets", certificates: "Certificates",
  issuers: "Issuers", clusterissuers: "ClusterIssuers", externalsecrets: "ExternalSecrets", secretstores: "SecretStores",
  clustersecretstores: "ClusterSecretStores", nodepools: "NodePools", nodeclaims: "NodeClaims",
  schedules: "Schedules", backupstoragelocations: "BackupStorageLocations", backups: "Backups",
  ipaddresspools: "IPAddressPools", l2advertisements: "L2Advertisements", bgpadvertisements: "BGPAdvertisements",
  configurationstates: "ConfigurationStates", ciliumloadbalancerippools: "CiliumLoadBalancerIPPools",
  alerts: "Alertmanager alerts",
  horizontalpodautoscalers: "HorizontalPodAutoscalers", scaledobjects: "ScaledObjects", scaledjobs: "ScaledJobs",
  triggerauthentications: "TriggerAuthentications", clustertriggerauthentications: "ClusterTriggerAuthentications",
};
const kindLabel = (k: string) => KIND_LABELS[k] ?? k;

const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

// "alerts": active Alertmanager alerts (coverage S8a §6) - counted apart,
// never "attention": they light no area, lamp or spine.
export type LineState = "attention" | "unlit" | "evidence" | "alerts" | "quiet" | "checking" | "unreadable";
export type Line = {
  task: string;
  part: string;
  label: string;
  destinationId?: string; // the destination this part badges, if it has one
  classifier: "verdict" | "native" | "evidence" | "alerts";
  state: LineState;
  n: number; // attention (verdict), Included (native), warnings (evidence); 0 without a count
  // the role counts behind a lit line: faults and degraded objects (verdict
  // counts, or native roles); 0 elsewhere. The fleet queue counts and
  // completes its samples from these, never from a tool's wider Included.
  fault: number;
  degraded: number;
  // "as-read": the counts as read (a stale read says so in its gaps);
  // "lower": a lower bound on current attention (live kinds, some missing);
  // "mixed": current and last-observed objects (live and stale kinds) -
  // never a lower bound
  bound: "as-read" | "lower" | "mixed";
  tone: "danger" | "warning" | undefined;
  text: string;
  gaps: string[]; // what this line could not establish, each named
  source: SelectedCounts["source"];
  at: string | null; // the selected part's evaluation time
  asOf: string | null; // a snapshot part's read time
  updating: boolean;
  refreshing: boolean;
  top: HealthRef[];
  // an alerts line's critical count (S8a §6: the Faults filter keeps them)
  alertsCritical?: number;
};
export type Area = {
  id: string;
  label: string;
  groupId: string;
  destinationId: string;
  lines: Line[];
  controller: { state: string; reason?: string } | null;
  state: "attention" | "checking" | "gaps" | "quiet";
  tone: "danger" | "warning" | undefined;
  gap: boolean;
};
export type ClusterAttention = {
  ended: string | null; // the observation ended at (a Final for the current incarnation)
  areas: Area[];
};

// CAP_UNKNOWN qualifies a checking part while capability detection has not
// established its tool: nothing is implied, present or absent.
export const CAP_UNKNOWN = "capability not yet known";

// checkingText: an area nothing has answered yet, in one phrase - or null
// when any part is answered or a controller state is known.
export function checkingText(a: Area): string | null {
  if (a.controller || !a.lines.every((l) => l.state === "checking")) return null;
  return a.lines.some((l) => l.gaps.includes(CAP_UNKNOWN)) ? `checking… · ${CAP_UNKNOWN}` : "checking…";
}

function currentSummary(st: HealthState, cluster: string): HealthSummary | undefined {
  const s = st.summaries[cluster];
  return s && s.incarnation >= (st.knownIncarnation[cluster] ?? 0) ? s : undefined;
}

// line reads one part from its selected counts (§2.1, §3). A part nothing
// answers is checking - a gap, never quiet, never a count (slice 6 §4).
function line(meta: TaskMeta, pm: PartMeta, sel: SelectedCounts, capUnknown: boolean): Line | null {
  const base = {
    task: meta.task, part: pm.part, label: pm.label, destinationId: pm.destinationId,
    source: sel.source, at: sel.at, updating: sel.updating, refreshing: sel.refreshing,
  };
  const qualify: string[] = [];
  if (sel.resync) qualify.push("rows re-reading");
  if (sel.refreshError) qualify.push(`last re-read failed: ${sel.refreshError}`);
  const p = sel.part;
  if (!p) {
    return { ...base, classifier: CLASSIFIER[meta.task] ?? "native", state: "checking", n: 0, fault: 0, degraded: 0, bound: "as-read", tone: undefined, text: "checking…", gaps: capUnknown ? [CAP_UNKNOWN, ...qualify] : qualify, asOf: null, top: [] };
  }
  if (p.coverage === "absent") return null;
  const member = MEMBERSHIP[`${meta.task}/${pm.part}`] ?? [];
  const isCounted = (k: KindCoverage) => (k.role ?? "counted") === "counted" || member.includes(k.kind);
  const counted = p.kinds.filter((k) => isCounted(k) && k.coverage !== "absent");
  const deps = p.kinds.filter((k) => !isCounted(k) && k.coverage !== "absent");
  const read = counted.filter((k) => k.coverage === "live" || k.coverage === "stale");
  const missing = counted.filter((k) => k.coverage === "checking" || k.coverage === "unreadable");
  const stale = read.filter((k) => k.coverage === "stale");
  const live = read.filter((k) => k.coverage === "live");
  const gaps: string[] = [];
  const kindGap = (k: KindCoverage, dep: boolean) => {
    const what = k.coverage === "unreadable" ? `could not be read${k.class ? ` (${k.class})` : ""}`
      : k.coverage === "stale" ? `last good ${k.lastGoodAt ? hhmm(k.lastGoodAt) : "unknown"}${k.class ? ` (${k.class})` : ""}` : "not read yet";
    return `${kindLabel(k.kind)} ${what}${dep ? " - a dependency" : ""}`;
  };
  // membership not established (§2.1): an attached route without its parent
  // Gateways read has no count - never a lower bound
  const memberMissing = missing.filter((k) => member.includes(k.kind));
  if (read.length === 0 || memberMissing.length > 0) {
    const un = memberMissing.find((k) => k.coverage === "unreadable") ?? missing.find((k) => k.coverage === "unreadable");
    return {
      ...base, classifier: p.classifier, state: un ? "unreadable" : "checking", n: 0, fault: 0, degraded: 0, bound: "as-read", tone: undefined,
      text: memberMissing.length > 0 ? `attachment membership not established - ${kindLabel(memberMissing[0].kind)} ${un ? "could not be read" : "not read yet"}`
        : un ? `could not be read${un.class ? ` (${un.class})` : ""}` : "checking…",
      gaps: [...missing.filter((k) => k !== un).map((k) => kindGap(k, false)), ...qualify],
      asOf: p.asOf ?? null, top: [],
    };
  }
  for (const k of [...stale, ...missing]) gaps.push(kindGap(k, false));
  for (const d of deps) if (d.coverage !== "live") gaps.push(kindGap(d, true));
  for (const k of p.kinds) if (k.discovery) gaps.push(`discovery re-check failing for ${kindLabel(k.kind)}`);
  const truncated = counted.some((k) => k.truncated);
  // live kinds with some missing: a lower bound on current attention; live
  // and stale kinds: current and last observed; any stale kind: never a
  // lower bound (§2.1)
  // a stale membership kind makes the whole line a stale reading
  const memberStale = stale.some((k) => member.includes(k.kind));
  const bound: Line["bound"] = stale.length > 0 ? (live.length > 0 && !memberStale ? "mixed" : "as-read") : missing.length > 0 || truncated ? "lower" : "as-read";
  const plus = bound === "lower" ? "+" : "";
  const mixed = bound === "mixed" ? " - current and last observed" : "";
  let state: LineState;
  let n: number;
  let fault = 0;
  let degraded = 0;
  let tone: Line["tone"];
  let text: string;
  if (p.classifier === "verdict") {
    const c = p.counts ?? { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
    n = c.fault + c.degraded;
    fault = c.fault;
    degraded = c.degraded;
    if (c.unknown > 0) gaps.unshift(`${c.unknown} unverified`);
    state = n > 0 ? "attention" : "quiet";
    tone = n > 0 ? (c.fault > 0 ? "danger" : "warning") : undefined;
    text = n > 0 ? `${n}${plus} need attention${mixed}` : c.unknown > 0 ? `no known attention${mixed}` : `none need attention${mixed}`;
  } else if (p.classifier === "native") {
    const nat = p.native ?? { total: 0, included: 0, word: "need attention", roles: { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 } };
    const lit = nat.roles.fault + nat.roles.degraded;
    fault = nat.roles.fault;
    degraded = nat.roles.degraded;
    if (nat.roles.unknown > 0) gaps.unshift(`${nat.roles.unknown} unknown`);
    n = nat.included;
    if (lit > 0 && n === 0) n = lit; // a fault outside the tool's predicate is still named
    state = lit > 0 ? "attention" : n > 0 ? "unlit" : "quiet";
    tone = lit > 0 ? (nat.roles.fault > 0 ? "danger" : "warning") : undefined;
    text = n > 0 ? `${n}${plus} ${nat.included > 0 ? nat.word : "need attention"}${mixed}` : `none ${nat.word}${mixed}`;
  } else if (p.classifier === "alerts") {
    // counted apart from the objects: never a fault or degraded role
    const al = p.alerts ?? { counted: 0, critical: 0, warning: 0, uncounted: 0, suppressed: 0, unprocessed: 0, unknown: 0, truncated: false };
    n = al.counted;
    state = n > 0 ? "alerts" : "quiet";
    tone = n > 0 ? (al.critical > 0 ? "danger" : "warning") : undefined;
    text = n > 0 ? `${n}${plus} active alert${n === 1 && !plus ? "" : "s"}${mixed}` : `no active critical or warning alerts${mixed}`;
    return { ...base, classifier: p.classifier, state, n, fault, degraded, bound, tone, text, gaps: [...gaps, ...qualify], asOf: p.asOf ?? null, top: p.top ?? [], alertsCritical: al.critical };
  } else {
    n = p.evidence ?? 0;
    state = n > 0 ? "evidence" : "quiet";
    tone = undefined;
    text = n > 0 ? `${n}${plus} warning event${n === 1 && !plus ? "" : "s"}${mixed}` : `no warning events${mixed}`;
  }
  return { ...base, classifier: p.classifier, state, n, fault, degraded, bound, tone, text, gaps: [...gaps, ...qualify], asOf: p.asOf ?? null, top: p.top ?? [] };
}

function areaOf(meta: TaskMeta, lines: Line[], task: HealthTask | undefined): Area {
  const controller = task?.controller ?? null;
  const notReady = controller?.state === "not ready";
  const lit = lines.filter((l) => l.state === "attention");
  const checking = lines.length > 0 && lines.every((l) => l.state === "checking");
  const gap = lines.some((l) => l.gaps.length > 0 || l.state === "checking" || l.state === "unreadable") || controller?.state === "unknown";
  const state: Area["state"] = lit.length > 0 || notReady ? "attention" : checking && !controller ? "checking" : gap ? "gaps" : "quiet";
  const tone = notReady || lit.some((l) => l.tone === "danger") ? "danger" : lit.length > 0 ? "warning" : undefined;
  return { id: meta.task, label: meta.label, groupId: meta.groupId, destinationId: meta.destinationId, lines, controller, state, tone, gap };
}

// clusterAttention builds the model: the expected tasks (core always;
// capability tasks when present, not yet known, or carried by the summary),
// each part answered by current-incarnation rows or the current summary,
// else checking (§1; slice 6 §4: "not covered" went once the coverage-parity
// gate passed - a part the summary does not carry is checking, a gap).
export function clusterAttention(st: HealthState, cluster: string, c: ClusterDTO | undefined): ClusterAttention {
  const known = st.knownIncarnation[cluster] ?? 0;
  const ended = st.ended[cluster];
  if (ended && ended.incarnation >= known) return { ended: ended.at, areas: [] };
  const summary = currentSummary(st, cluster);
  const areas: Area[] = [];
  for (const meta of TASKS) {
    const task = summary?.tasks.find((t) => t.id === meta.task);
    const sels = meta.parts.map((pm) => taskCounts(st, cluster, meta.task, pm.part));
    // the task is carried by the Observer's own evidence - the summary's, or
    // current-incarnation rows that may be newer than it (review 1/3 #1)
    const carried = (!!task && task.parts.some((p) => p.coverage !== "absent")) || sels.some((x) => x.source === "rows" && x.part?.coverage !== "absent");
    const cap = meta.cap ? capState(c, meta.cap) : "present";
    if (meta.cap && cap === "absent" && !carried) continue; // confirmed absent
    const lines: Line[] = [];
    for (const [i, pm] of meta.parts.entries()) {
      const l = line(meta, pm, sels[i], cap !== "present");
      if (l) lines.push(l);
    }
    if (lines.length === 0 && !task?.controller) continue; // every part absent
    areas.push(areaOf(meta, lines, task));
  }
  return { ended: null, areas };
}

// strongestLine: the line a badge shows - lit lines only, danger before
// warning, then the first in part order; never a sum (§2.2).
export function strongestLine(lines: Line[]): Line | null {
  let best: Line | null = null;
  for (const l of lines) {
    if (l.state !== "attention") continue;
    if (!best || (l.tone === "danger" && best.tone !== "danger")) best = l;
  }
  return best;
}

export type MastReading = {
  state: "ended" | "checking" | "attention" | "quiet";
  areas: number; // areas needing attention (lit lines or a verified not-ready controller)
  tone: "danger" | "warning" | undefined;
  gaps: number; // areas with a gap, lit or not (a checking part is one)
  read: number; // areas with at least one answered part
};

// mastReading: areas, never objects; gaps counted apart
// from the lit areas; never an unqualified all-clear (§5.1).
export function mastReading(a: ClusterAttention): MastReading {
  if (a.ended) return { state: "ended", areas: 0, tone: undefined, gaps: 0, read: 0 };
  // read: an area with at least one counted line (unreadable is not read)
  const read = a.areas.filter((x) => x.lines.some((l) => l.state !== "checking" && l.state !== "unreadable")).length;
  const lit = a.areas.filter((x) => x.state === "attention");
  const gaps = a.areas.filter((x) => x.gap).length;
  if (a.areas.length > 0 && a.areas.every((x) => x.state === "checking")) {
    return { state: "checking", areas: 0, tone: undefined, gaps, read };
  }
  return {
    state: lit.length > 0 ? "attention" : "quiet",
    areas: lit.length,
    tone: lit.some((x) => x.tone === "danger") ? "danger" : lit.length > 0 ? "warning" : undefined,
    gaps, read,
  };
}

const clock = (iso: string | null) => (iso ? new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }) : "");

// provenance names where a line's numbers came from, with that part's own
// time (§3): the lens rows or the cluster summary, never one for the other.
export function provenance(area: { label: string }, l: Line): string {
  const at = clock(l.at);
  const where = l.source === "rows" ? `${area.label} lens rows${at ? ` as of ${at}` : ""}` : l.source === "summary" ? `cluster summary${at ? ` as of ${at}` : ""}` : "not read yet";
  return `${where}${l.updating ? " - updating" : ""}${l.refreshing ? " - refreshing" : ""}`;
}

// lineText is a line's full wording: its text, then its gaps.
export const lineText = (l: Line) => [l.text, ...l.gaps].join(" · ") + (l.asOf ? ` (as of ${hhmm(l.asOf)})` : "");
