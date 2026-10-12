import type { ClusterDTO } from "../store/fleet";
import { findPart, taskCounts, type HealthRef, type HealthState, type HealthSummary, type HealthTask, type KindCoverage, type SelectedCounts } from "../store/health";
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
  { task: "helm", label: "Helm releases", groupId: "helm", destinationId: "helm", parts: [{ part: "releases", label: "Releases", destinationId: "helm" }] },
  { task: "flux", label: "Flux", groupId: "flux", destinationId: "flux", cap: "flux",
    parts: [{ part: "reconciliations", label: "Reconciliations", destinationId: "flux" }, { part: "sources", label: "Sources", destinationId: "flux-sources" }] },
  { task: "argo", label: "Argo CD", groupId: "argo", destinationId: "argo", cap: "argo",
    parts: [{ part: "applications", label: "Applications", destinationId: "argo" }, { part: "applicationsets", label: "ApplicationSets", destinationId: "argo-appsets" }] },
  { task: "certs", label: "Certificates", groupId: "certs", destinationId: "certs", cap: "certManager",
    parts: [{ part: "certificates", label: "Certificates", destinationId: "certs" }, { part: "issuers", label: "Issuers" }] },
  { task: "eso", label: "External secrets", groupId: "eso", destinationId: "eso", cap: "externalSecrets",
    parts: [{ part: "externalsecrets", label: "ExternalSecrets", destinationId: "eso" }, { part: "stores", label: "Stores" }] },
  { task: "gateway", label: "Gateways", groupId: "network", destinationId: "gateway", cap: "gatewayAPI",
    parts: [{ part: "gateways", label: "Gateways", destinationId: "gateway" }, { part: "routes", label: "Routes" }] },
  { task: "karpenter", label: "Karpenter", groupId: "autoscaling", destinationId: "karpenter", cap: "karpenter",
    parts: [{ part: "nodeclaims", label: "NodeClaims", destinationId: "karpenter" }, { part: "nodepools", label: "NodePools" }] },
];

const CLASSIFIER: Record<string, Line["classifier"]> = { workloads: "verdict", pods: "verdict", nodes: "verdict", events: "evidence" };

// Derived membership (§2.1): an attached route exists only through its
// parent Gateway, so the parent kind is the routes line's evidence too.
const MEMBERSHIP: Record<string, string[]> = { "gateway/routes": ["gateways"] };

const KIND_LABELS: Record<string, string> = {
  pods: "Pods", deployments: "Deployments", statefulsets: "StatefulSets", daemonsets: "DaemonSets", replicasets: "ReplicaSets",
  nodes: "Nodes", events: "Events", helm: "Helm releases", gatewayclasses: "GatewayClasses", gateways: "Gateways", httproutes: "HTTPRoutes",
  services: "Services", endpointslices: "EndpointSlices", kustomizations: "Kustomizations", helmreleases: "HelmReleases",
  gitrepositories: "GitRepositories", ocirepositories: "OCIRepositories", buckets: "Buckets", helmrepositories: "HelmRepositories",
  helmcharts: "HelmCharts", applications: "Applications", applicationsets: "ApplicationSets", certificates: "Certificates",
  issuers: "Issuers", clusterissuers: "ClusterIssuers", externalsecrets: "ExternalSecrets", secretstores: "SecretStores",
  clustersecretstores: "ClusterSecretStores", nodepools: "NodePools", nodeclaims: "NodeClaims",
};
const kindLabel = (k: string) => KIND_LABELS[k] ?? k;

const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

export type LineState = "attention" | "unlit" | "evidence" | "quiet" | "checking" | "unreadable";
export type Line = {
  task: string;
  part: string;
  label: string;
  destinationId?: string; // the destination this part badges, if it has one
  classifier: "verdict" | "native" | "evidence";
  state: LineState;
  n: number; // attention (verdict), Included (native), warnings (evidence); 0 without a count
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
export type NotCovered = { task: string; part?: string; label: string; groupId: string; destinationId: string; capability: "present" | "unverified" };
export type ClusterAttention = {
  ended: string | null; // the observation ended at (a Final for the current incarnation)
  areas: Area[];
  notCovered: NotCovered[];
};

function currentSummary(st: HealthState, cluster: string): HealthSummary | undefined {
  const s = st.summaries[cluster];
  return s && s.incarnation >= (st.knownIncarnation[cluster] ?? 0) ? s : undefined;
}

// line reads one part from its selected counts (§2.1, §3).
function line(meta: TaskMeta, pm: PartMeta, sel: SelectedCounts): Line | null {
  const base = {
    task: meta.task, part: pm.part, label: pm.label, destinationId: pm.destinationId,
    source: sel.source, at: sel.at, updating: sel.updating, refreshing: sel.refreshing,
  };
  const qualify: string[] = [];
  if (sel.resync) qualify.push("rows re-reading");
  if (sel.refreshError) qualify.push(`last re-read failed: ${sel.refreshError}`);
  const p = sel.part;
  if (!p) {
    return { ...base, classifier: CLASSIFIER[meta.task] ?? "native", state: "checking", n: 0, bound: "as-read", tone: undefined, text: "checking…", gaps: qualify, asOf: null, top: [] };
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
      ...base, classifier: p.classifier, state: un ? "unreadable" : "checking", n: 0, bound: "as-read", tone: undefined,
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
  let tone: Line["tone"];
  let text: string;
  if (p.classifier === "verdict") {
    const c = p.counts ?? { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
    n = c.fault + c.degraded;
    if (c.unknown > 0) gaps.unshift(`${c.unknown} unverified`);
    state = n > 0 ? "attention" : "quiet";
    tone = n > 0 ? (c.fault > 0 ? "danger" : "warning") : undefined;
    text = n > 0 ? `${n}${plus} need attention${mixed}` : c.unknown > 0 ? `no known attention${mixed}` : `none need attention${mixed}`;
  } else if (p.classifier === "native") {
    const nat = p.native ?? { total: 0, included: 0, word: "need attention", roles: { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 } };
    const lit = nat.roles.fault + nat.roles.degraded;
    if (nat.roles.unknown > 0) gaps.unshift(`${nat.roles.unknown} unknown`);
    n = nat.included;
    if (lit > 0 && n === 0) n = lit; // a fault outside the tool's predicate is still named
    state = lit > 0 ? "attention" : n > 0 ? "unlit" : "quiet";
    tone = lit > 0 ? (nat.roles.fault > 0 ? "danger" : "warning") : undefined;
    text = n > 0 ? `${n}${plus} ${nat.included > 0 ? nat.word : "need attention"}${mixed}` : `none ${nat.word}${mixed}`;
  } else {
    n = p.evidence ?? 0;
    state = n > 0 ? "evidence" : "quiet";
    tone = undefined;
    text = n > 0 ? `${n}${plus} warning event${n === 1 && !plus ? "" : "s"}${mixed}` : `no warning events${mixed}`;
  }
  return { ...base, classifier: p.classifier, state, n, bound, tone, text, gaps: [...gaps, ...qualify], asOf: p.asOf ?? null, top: p.top ?? [] };
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
// else checking - or, when the summary leaves it out, not covered (§1, §6).
export function clusterAttention(st: HealthState, cluster: string, c: ClusterDTO | undefined): ClusterAttention {
  const known = st.knownIncarnation[cluster] ?? 0;
  const ended = st.ended[cluster];
  if (ended && ended.incarnation >= known) return { ended: ended.at, areas: [], notCovered: [] };
  const summary = currentSummary(st, cluster);
  const areas: Area[] = [];
  const notCovered: NotCovered[] = [];
  for (const meta of TASKS) {
    const task = summary?.tasks.find((t) => t.id === meta.task);
    const sels = meta.parts.map((pm) => taskCounts(st, cluster, meta.task, pm.part));
    // the task is carried by the Observer's own evidence - the summary's, or
    // current-incarnation rows that may be newer than it (review 1/3 #1)
    const carried = (!!task && task.parts.some((p) => p.coverage !== "absent")) || sels.some((x) => x.source === "rows" && x.part?.coverage !== "absent");
    const cap = meta.cap ? capState(c, meta.cap) : "present";
    if (meta.cap && cap === "absent" && !carried) continue; // confirmed absent
    const capability = cap === "present" ? "present" as const : "unverified" as const;
    const lines: Line[] = [];
    const left: PartMeta[] = []; // parts the summary leaves out and no rows answer
    for (const [i, pm] of meta.parts.entries()) {
      const sel = sels[i];
      if (sel.source === "none" && summary && !findPart(summary, meta.task, pm.part)) { left.push(pm); continue; }
      const l = line(meta, pm, sel);
      if (l) lines.push(l);
    }
    // fail closed (§6): a task the summary leaves out is one entry; a part
    // left out of a carried task is its own
    if (left.length > 0 && !task && lines.length === 0) {
      notCovered.push({ task: meta.task, label: meta.label, groupId: meta.groupId, destinationId: meta.destinationId, capability });
    } else {
      for (const pm of left) notCovered.push({ task: meta.task, part: pm.part, label: `${meta.label}: ${pm.label}`, groupId: meta.groupId, destinationId: meta.destinationId, capability });
    }
    if (lines.length === 0 && !task?.controller) continue; // every part absent or not covered
    areas.push(areaOf(meta, lines, task));
  }
  return { ended: null, areas, notCovered };
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
  gaps: number; // areas with a gap, lit or not
  notCovered: number;
  read: number; // areas with at least one answered part
};

// mastReading: areas, never objects; gaps and not-covered counted apart
// from the lit areas; never an unqualified all-clear (§5.1).
export function mastReading(a: ClusterAttention): MastReading {
  if (a.ended) return { state: "ended", areas: 0, tone: undefined, gaps: 0, notCovered: 0, read: 0 };
  // read: an area with at least one counted line (unreadable is not read)
  const read = a.areas.filter((x) => x.lines.some((l) => l.state !== "checking" && l.state !== "unreadable")).length;
  const lit = a.areas.filter((x) => x.state === "attention");
  const gaps = a.areas.filter((x) => x.gap).length;
  if (a.areas.length > 0 && a.areas.every((x) => x.state === "checking") && a.notCovered.length === 0) {
    return { state: "checking", areas: 0, tone: undefined, gaps, notCovered: 0, read };
  }
  return {
    state: lit.length > 0 ? "attention" : "quiet",
    areas: lit.length,
    tone: lit.some((x) => x.tone === "danger") ? "danger" : lit.length > 0 ? "warning" : undefined,
    gaps, notCovered: a.notCovered.length, read,
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
