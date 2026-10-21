import type { ClusterDTO } from "../store/fleet";
import type { HealthRef } from "../store/health";
import { lineText, mastReading, TASKS, type Area, type ClusterAttention, type Line } from "../nav/clusterAttention";
import { connectionText, healthReading, unreachable } from "../nav/fleetAttention";
import { envRank, stationLabel } from "../nav/stationOrder";
import { kindOfRef } from "../nav/reveal";

// "Needs you" (The Line slice 5 §4): the fleet's queue, built from each
// cluster's attention model - the one the lens index and the fleet
// indicator read - so they never disagree. Rows are objects (a lit line's
// Top refs, fault and attention roles only), "+N more" past the samples,
// a not-ready controller, and a coverage gap; a cluster with no reading is
// named under the queue, never read as clear. No folding (slice 8).

type Base = { key: string; cluster: string; place: string; rank: number; tone: "danger" | "warning" | undefined; area: string; areaOrder: number; task: string; part: string };
export type QueueRow =
  | (Base & { kind: "object"; ref: HealthRef; objectKind: string; label: string; time: string | null; reason: string | null })
  // the hidden fault and attention objects past the samples, by role
  | (Base & { kind: "more"; faults: number; attention: number; lower: boolean; destinationId: string; lens: string })
  | (Base & { kind: "controller"; text: string; destinationId: string })
  | (Base & { kind: "coverage"; text: string; destinationId: string })
  // coverage S8a: an active Alertmanager alert - counted apart from the
  // objects, never folded into one - and the hidden alerts past the samples
  | (Base & { kind: "alert"; ref: HealthRef; label: string; subject: string | null; time: string | null; reason: string | null })
  | (Base & { kind: "alert-more"; count: number; critical: number; lower: boolean; destinationId: string; lens: string });
export type QueueFilter = "all" | "prd" | "faults";
// faults and attention count the whole queue's model - every lit line's
// role counts, not the samples - and lower says a count is a lower bound.
// alerts is the third count (S8a §6): active critical and warning alerts,
// never added to faults or attention; alertsLower says it is a lower bound.
export type Queue = { rows: QueueRow[]; notRead: { cluster: string; why: string }[]; faults: number; attention: number; lower: boolean; alerts: number; alertsLower: boolean };

// Environment rank, as station order ranks it (one table, pinned to Go's by
// the parity test); prd sorts first, an unrecognised environment last.
export const envRankOf = (env: string) => { const r = envRank((env ?? "").trim().toLowerCase()); return r === 4 ? -1 : r; };
const PRD = 3;

const KINDS: Record<string, string> = {
  pods: "Pod", nodes: "Node", deployments: "Deployment", statefulsets: "StatefulSet", daemonsets: "DaemonSet",
  certificates: "Certificate", externalsecrets: "ExternalSecret", applications: "Application", applicationsets: "ApplicationSet",
  gateways: "Gateway", httproutes: "HTTPRoute", listenersets: "ListenerSet", grpcroutes: "GRPCRoute", nodeclaims: "NodeClaim", nodepools: "NodePool", helmreleases: "HelmRelease",
};
// objectKindOf names a ref's kind from its GVR's resource.
export function objectKindOf(gvr: string): string {
  const known = kindOfRef({ gvr });
  if (known) return known;
  const res = gvr.split("/").pop() ?? "";
  return KINDS[res] ?? (res ? res.replace(/s$/, "").replace(/^./, (c) => c.toUpperCase()) : "Object");
}

const hhmm = (iso: string) => { const d = new Date(iso); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

// refTime: a rule-backed start, else the deciding condition as evidence
// (never the role's start), else nothing - per-object observation wording
// arrives with the console join (slice 9).
export function refTime(r: HealthRef): string | null {
  if (r.since) return `since ${hhmm(r.since)}`;
  if (r.condition?.since) return `${r.condition.type}=${r.condition.status} since ${hhmm(r.condition.since)} (condition time)`;
  return null;
}

const ROLE_TONE: Record<string, "danger" | "warning"> = { fault: "danger", degraded: "warning" };
const lineName = (a: Area, l: Line) => `${a.label}${a.lines.length > 1 ? ` · ${l.label}` : ""}`;
const covered = (l: Line) => l.state === "unreadable" || l.state === "checking" || l.gaps.some((g) => /could not be read|last good|not read yet/.test(g));
const taskOrder = (id: string) => { const i = TASKS.findIndex((t) => t.task === id); return i < 0 ? TASKS.length : i; };

export function fleetQueue(clusters: ClusterDTO[], models: Record<string, ClusterAttention>, filter: QueueFilter = "all"): Queue {
  const rows: QueueRow[] = [];
  const notRead: Queue["notRead"] = [];
  let faults = 0, attention = 0, lower = false, alerts = 0, alertsLower = false;
  for (const c of clusters) {
    const { place } = stationLabel(c);
    const rank = envRankOf(c.env);
    const m = models[c.name];
    if (unreachable(c)) { notRead.push({ cluster: c.name, why: connectionText(c) }); continue; }
    // connecting or reconnecting: a summary held from before is not this
    // connection's reading (Codex review of 087e5e2)
    if (c.state !== "Synced" && c.state !== "Degraded" && c.state !== "Stale") { notRead.push({ cluster: c.name, why: String(c.state).toLowerCase() || "connecting" }); continue; }
    if (!m) { notRead.push({ cluster: c.name, why: "health checking…" }); continue; }
    if (m.ended) { notRead.push({ cluster: c.name, why: healthReading(m)[0] }); continue; }
    const r = mastReading(m);
    if (r.state === "checking") { notRead.push({ cluster: c.name, why: "health checking…" }); continue; }
    for (const a of m.areas) {
      const base = (part: string, tone: Base["tone"], k: string): Base => ({ key: `${c.name}/${k}`, cluster: c.name, place, rank, tone, area: a.label, areaOrder: taskOrder(a.id), task: a.id, part });
      if (a.controller?.state === "not ready") {
        faults++;
        rows.push({ ...base("", "danger", `${a.id}/controller`), kind: "controller", text: `${a.label}: controller not ready${a.controller.reason ? ` - ${a.controller.reason}` : ""}`, destinationId: a.destinationId });
      }
      for (const l of a.lines) {
        if (l.state === "attention") {
          faults += l.fault;
          attention += l.degraded;
          if (l.bound === "lower") lower = true;
          let shownF = 0, shownD = 0;
          for (const ref of l.top) {
            const tone = ROLE_TONE[ref.role];
            if (!tone) continue; // paused, progressing, unknown or ok: never a row
            if (tone === "danger") shownF++; else shownD++;
            rows.push({ ...base(l.part, tone, `${l.task}/${l.part}/${ref.uid || `${ref.namespace ?? ""}/${ref.name}`}`), kind: "object", ref,
              objectKind: objectKindOf(ref.gvr), label: ref.label, time: refTime(ref), reason: ref.reason || null });
          }
          // past the samples: the hidden faults and attention objects only -
          // never a tool's wider Included (a syncing object is not a row)
          const hf = Math.max(0, l.fault - shownF), hd = Math.max(0, l.degraded - shownD);
          if (hf + hd > 0) rows.push({ ...base(l.part, hf > 0 ? "danger" : "warning", `${l.task}/${l.part}/more`), kind: "more", faults: hf, attention: hd, lower: l.bound === "lower", destinationId: l.destinationId ?? a.destinationId, lens: lineName(a, l) });
        }
        if (l.state === "alerts") {
          alerts += l.n;
          if (l.bound === "lower") alertsLower = true;
          let shown = 0, shownCritical = 0;
          for (const ref of l.top) {
            const tone = ref.role === "critical" ? "danger" : ref.role === "warning" ? "warning" : undefined;
            if (!tone) continue;
            shown++;
            if (tone === "danger") shownCritical++;
            rows.push({ ...base(l.part, tone, `${l.task}/${l.part}/${ref.fingerprint ?? `${ref.label}/${ref.namespace ?? ""}/${ref.name}`}`), kind: "alert", ref, label: ref.label,
              subject: ref.name ? `${objectKindOf(ref.gvr)} ${ref.namespace ? `${ref.namespace}/` : ""}${ref.name}` : null,
              time: ref.since ? `started ${hhmm(ref.since)}` : null, reason: ref.reason || null });
          }
          const hidden = Math.max(0, l.n - shown);
          if (hidden > 0) {
            const critical = Math.max(0, (l.alertsCritical ?? 0) - shownCritical);
            rows.push({ ...base(l.part, critical > 0 ? "danger" : "warning", `${l.task}/${l.part}/more`), kind: "alert-more", count: hidden, critical, lower: l.bound === "lower",
              destinationId: l.destinationId ?? a.destinationId, lens: lineName(a, l) });
          }
        }
        if (covered(l)) {
          rows.push({ ...base(l.part, undefined, `${l.task}/${l.part}/coverage`), kind: "coverage", text: `${lineName(a, l)}: ${l.state === "unreadable" || l.state === "checking" ? lineText(l) : l.gaps.filter((g) => /could not be read|last good|not read yet/.test(g)).join(" · ")}`, destinationId: l.destinationId ?? a.destinationId });
        }
      }
    }
  }
  const toneOrder = (t: Base["tone"]) => (t === "danger" ? 0 : t === "warning" ? 1 : 2);
  // the objects, then the alert samples, then their "+N more"
  const isAlert = (r: QueueRow) => (r.kind === "alert" ? 1 : r.kind === "alert-more" ? 2 : 0);
  // prd first; then the objects before the alerts (counted apart); then
  // role; then area in task order; refs keep their order
  const ordered = rows.map((row, i) => ({ row, i })).sort((x, y) =>
    (y.row.rank - x.row.rank) || (isAlert(x.row) - isAlert(y.row)) || (toneOrder(x.row.tone) - toneOrder(y.row.tone)) || (x.row.areaOrder - y.row.areaOrder) || (x.i - y.i)).map((x) => x.row);
  const filtered = ordered
    .filter((row) => filter === "all" || (filter === "prd" ? row.rank === PRD : row.tone === "danger"))
    // under Faults a "+N more" row keeps only its hidden faults, and an
    // alerts "+N more" only its hidden critical alerts
    .map((row) => (filter === "faults" && row.kind === "more" ? { ...row, attention: 0 }
      : filter === "faults" && row.kind === "alert-more" ? { ...row, count: row.critical } : row));
  return {
    rows: filtered,
    notRead: filter === "prd" ? notRead.filter((n) => envRankOf(clusters.find((c) => c.name === n.cluster)?.env ?? "") === PRD) : notRead,
    faults, attention, lower, alerts, alertsLower,
  };
}
