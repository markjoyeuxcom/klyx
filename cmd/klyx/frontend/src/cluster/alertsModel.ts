// Coverage S8a: the Alerts lens's rows (the Go AM* DTOs, display values
// only) and the words Klyx says about them. Correlation is by label, never
// a join by UID; nothing here derives a state from a time.

export type AMPairDTO = { name: string; value: string };
export type AMSubjectDTO = {
  kind?: string; gvr?: string; namespace?: string; name?: string; container?: string; basis?: string[]; alsoNames?: AMSubjectDTO[];
  // exact: namespace and name are shown as written (neither redacted nor
  // clipped) - only then is a name comparison safe
  exact?: boolean;
  state?: string; uid?: string; createdAt?: string; startedBeforeCreated?: boolean;
};
export type AMMuteDTO = { kind: string; mutedFor?: string[]; activeFor?: string[]; expected?: string[]; shown?: string[] };
export type AMAlertDTO = {
  // key: the alert's opaque key (its source and fingerprint, hashed) - every
  // selection and reveal uses it; stateClass and severityClass are Klyx's
  // classes from the raw alert, state and severity the display values
  key: string; fingerprint: string; name: string; severity?: string; severityClass: "critical" | "warning" | "other";
  counted: boolean; state: string; stateClass: "active" | "suppressed" | "unprocessed" | "other";
  summary?: string; description?: string; runbookURL?: string; generatorURL?: string;
  labels: AMPairDTO[]; labelsMore?: number; annotations: AMPairDTO[]; annotationsMore?: number;
  receivers?: string[]; silencedBy?: string[]; inhibitedBy?: string[]; mutedBy?: string[]; mute?: AMMuteDTO; disagreement?: string;
  subject: AMSubjectDTO; startsAt?: string; updatedAt?: string; endsAt?: string;
};

// The lens's state groups, in order (S8a §7): active, unprocessed, suppressed,
// then a state Klyx does not know, kept as written.
export type AlertGroup = "active" | "unprocessed" | "suppressed" | "other";
export const groupOf = (a: AMAlertDTO): AlertGroup => a.stateClass;
const GROUP_ORDER: Record<AlertGroup, number> = { active: 0, unprocessed: 1, suppressed: 2, other: 3 };
export const sevClass = (a: AMAlertDTO): "critical" | "warning" | "other" => a.severityClass;
/** stateWord: Klyx's word for a known state; an unknown one as written. */
export const stateWord = (a: AMAlertDTO): string => (a.stateClass === "other" ? a.state : a.stateClass);
/** sevWord: Klyx's word for a counted severity; any other as written. */
export const sevWord = (a: AMAlertDTO): string => (a.severityClass === "other" ? a.severity || "none" : a.severityClass);
const SEV_ORDER = { critical: 0, warning: 1, other: 2 };
const startMs = (a: AMAlertDTO) => (a.startsAt ? Date.parse(a.startsAt) : Number.MAX_SAFE_INTEGER);

/** sortAlerts: by state group, then severity, then the longest firing. */
export function sortAlerts(xs: AMAlertDTO[]): AMAlertDTO[] {
  return [...xs].sort((a, b) => GROUP_ORDER[groupOf(a)] - GROUP_ORDER[groupOf(b)] || SEV_ORDER[sevClass(a)] - SEV_ORDER[sevClass(b)] || startMs(a) - startMs(b));
}

const short = (id: string) => (id.length > 8 ? `${id.slice(0, 8)}…` : id);

/** causeText: why an alert is not plainly active, in words (S8a §7). */
export function causeText(a: AMAlertDTO, byFingerprint?: Map<string, AMAlertDTO>): string {
  const parts: string[] = [];
  if (a.stateClass === "suppressed") {
    for (const s of a.silencedBy ?? []) parts.push(`silenced (silence ${short(s)})`);
    for (const f of a.inhibitedBy ?? []) {
      const by = byFingerprint?.get(f);
      parts.push(`inhibited by ${by ? `${by.name} (${short(f)})` : short(f)}`);
    }
    if ((a.mutedBy ?? []).length > 0) parts.push(`muted by time interval ${(a.mutedBy ?? []).join(", ")}`);
    if (parts.length === 0) parts.push("suppressed (cause not reported)");
  } else if (a.stateClass === "unprocessed") {
    parts.push("not yet processed by Alertmanager");
  } else if (a.stateClass !== "active") {
    parts.push(`state "${a.state}" (not one Klyx knows)`);
  }
  if (a.stateClass === "active" && a.mute?.kind === "partial") parts.push(`muted for ${(a.mute.mutedFor ?? []).join(", ")}; active for ${(a.mute.activeFor ?? []).join(", ")}`);
  if (a.stateClass === "active" && a.mute?.kind === "incomplete") {
    parts.push(`mute evidence incomplete: expected ${(a.mute.expected ?? []).join(", ") || "no receivers"}; groups show ${(a.mute.shown ?? []).join(", ") || "none"}`);
  }
  if (a.disagreement) parts.push(a.disagreement);
  return parts.join(" · ");
}

/** subjectText: "Pod team/api-1", or "the cluster" for a cluster alert. */
export function subjectText(s: AMSubjectDTO): string {
  if (!s.kind) return "the cluster";
  return `${s.kind} ${s.namespace ? `${s.namespace}/` : ""}${s.name ?? ""}`;
}

/** subjectNote: what Klyx can say about the subject (S8a §4, §5). */
export function subjectNote(s: AMSubjectDTO): string {
  if (!s.kind) return "names no object: a cluster alert";
  const basis = `matched by label ${(s.basis ?? []).join("/")}`;
  switch (s.state) {
    case "found": return s.startedBeforeCreated ? `${basis} · started before this object was created (an earlier object of this name?)` : basis;
    case "not found": return `${basis} · not found in this cluster now`;
    case "not established": return `${basis} · not established (that kind is not read complete right now)`;
    case "not looked up": return `${basis} · named, not looked up (Klyx does not hold this kind)`;
    case "last observed": return `${basis} · the object as last observed (that kind's read is not current)`;
    default: return basis;
  }
}

/** The facts the rows carry: the read's source line (display values) and
 * its source's opaque key. The read's time is the snapshot's freshness (the
 * part's coverage), never a fact: an unchanged read resends nothing. */
export type AlertsFacts = { source: string; sourceKey?: string; match?: Record<string, string>; truncated: boolean };
export function alertsFacts(f: Record<string, unknown> | null): AlertsFacts | null {
  if (!f || typeof f.alertsSource !== "string") return null;
  return {
    source: f.alertsSource, sourceKey: typeof f.alertsSourceKey === "string" ? f.alertsSourceKey : undefined,
    match: f.alertsMatch && typeof f.alertsMatch === "object" ? f.alertsMatch as Record<string, string> : undefined,
    truncated: f.alertsTruncated === true,
  };
}

export type AlertTarget = { uid?: string; kind: string; namespace?: string; name: string; podUIDs?: string[]; podsComplete?: boolean };

// The alerts part's dependency kind for each kind it resolves by UID.
const HELD: Record<string, string> = { Pod: "pods", Deployment: "deployments", StatefulSet: "statefulsets", DaemonSet: "daemonsets", Node: "nodes", Namespace: "namespaces" };
/** byName: an alert whose subject is this object by kind and name only - a
 * kind Klyx does not hold, or a held one it could not establish - compared
 * only when the backend shows the name as written. */
const byName = (s: AMSubjectDTO, t: AlertTarget) => !s.uid && s.exact === true && s.kind === t.kind && s.name === t.name && (s.namespace ?? "") === (t.namespace ?? "")
  && (s.state === "not looked up" || s.state === "not established");

/** inexactFor: an alert whose subject could be this object but whose name
 * is not shown as written (redacted or clipped): Klyx cannot rule it out.
 * A subject's UID resolves that object, never its namespace text, so for a
 * namespace every inexact subject counts, UID or not. */
const inexactFor = (s: AMSubjectDTO, t: AlertTarget) => !!s.kind && s.exact !== true
  && (t.kind === "Namespace" || (!s.uid && (s.kind === t.kind || (!!t.podUIDs && s.kind === "Pod"))));

/** rowsOutdated: why the rows held are not the cluster's newest (a failed or
 * pending re-read after a gap), or null. */
export function rowsOutdated(lr: { refreshError: string | null; counts: { updating: boolean; refreshing: boolean; resync: boolean; refreshError: string | null } }): string | null {
  const err = lr.refreshError ?? lr.counts.refreshError;
  if (err) return `the last re-read of the alerts rows failed: ${err}`;
  if (lr.counts.refreshing || lr.counts.resync || lr.counts.updating) return "the alerts rows are behind the cluster summary, re-reading";
  return null;
}

/** alertsFor: the alerts correlated to an object in its inspector (S8a §5,
 * §7) - by its UID when its subject was found (or last observed); by kind and
 * name for a kind Klyx does not hold or a subject it could not establish
 * (listed in byNameOnly too); a namespace lists every alert in it; a workload
 * also lists alerts naming its current pods. */
export function alertsFor(xs: AMAlertDTO[], t: AlertTarget): { own: AMAlertDTO[]; onPods: AMAlertDTO[]; byNameOnly: Set<string> } {
  const own: AMAlertDTO[] = [];
  const onPods: AMAlertDTO[] = [];
  const byNameOnly = new Set<string>();
  const pods = new Set(t.podUIDs ?? []);
  for (const a of xs) {
    const s = a.subject;
    if (t.kind === "Namespace") {
      if (s.namespace === t.name || (s.kind === "Namespace" && s.name === t.name)) own.push(a);
      continue;
    }
    if (t.uid && s.uid === t.uid) own.push(a);
    else if (byName(s, t)) { own.push(a); byNameOnly.add(a.key); }
    else if (s.uid && pods.has(s.uid)) onPods.push(a);
  }
  return { own: sortAlerts(own), onPods: sortAlerts(onPods), byNameOnly };
}

/** noAlertEstablished: whether "no alert matches this object" can be said -
 * the alerts read live and complete, the object's own kind (and a workload's
 * pods) resolved live and complete, the workload's pod set complete, and no
 * alert whose subject Klyx could not show. Otherwise why not. */
export function noAlertEstablished(xs: AMAlertDTO[] | null, part: { kinds: { kind: string; coverage: string; truncated?: boolean }[] } | null,
  t: AlertTarget, readTruncated: boolean, outdated: string | null = null): string | null {
  if (xs === null || !part) return "alerts not read yet";
  if (outdated) return outdated;
  const live = (k: string) => { const c = part.kinds.find((x) => x.kind === k); return !!c && c.coverage === "live" && !c.truncated; };
  if (!live("alerts") || readTruncated) return "the alerts read is capped or not current";
  const held = HELD[t.kind];
  if (held && !live(held)) return `${held} are not read live and complete, so an alert's subject may not be established`;
  if (t.podUIDs && (!live("pods") || t.podsComplete === false)) return "its pods are not read live and complete";
  if (xs.some((a) => inexactFor(a.subject, t))) return "an alert names an object Klyx cannot show exactly (redacted or clipped)";
  return null;
}

// v0.15.1, from the owner's first check of v0.15.0: four "Watchdog" rows on
// a shared Alertmanager read as duplicates. Alerts that share a name are told
// apart by the labels whose values differ among them: at most two, the
// labels that usually say where an alert comes from first.
const WHERE_LABELS = ["cluster", "cluster_name", "k8s_cluster", "tenant", "namespace", "job", "instance", "pod", "service", "prometheus"];
const whereRank = (n: string) => {
  const i = WHERE_LABELS.indexOf(n);
  return i < 0 ? WHERE_LABELS.length : i;
};

export function distinguishingLabels(xs: AMAlertDTO[]): Map<string, string> {
  const byName = new Map<string, AMAlertDTO[]>();
  for (const a of xs) byName.set(a.name, [...(byName.get(a.name) ?? []), a]);
  const out = new Map<string, string>();
  for (const group of byName.values()) {
    if (group.length < 2) continue;
    const values = group.map((a) => new Map(a.labels.map((l) => [l.name, l.value])));
    const names = new Set(group.flatMap((a) => a.labels.map((l) => l.name)));
    names.delete("alertname");
    const differ = [...names].filter((n) => new Set(values.map((v) => v.get(n) ?? "")).size > 1)
      .sort((a, b) => whereRank(a) - whereRank(b) || a.localeCompare(b));
    const shown = differ.slice(0, 2);
    // a label Klyx did not keep may be the one that differs
    const cut = group.some((a) => (a.labelsMore ?? 0) > 0);
    group.forEach((a, i) => {
      if (shown.length > 0) out.set(a.key, shown.map((n) => `${n}=${values[i].get(n) ?? "(none)"}`).join(", "));
      else if (cut) out.set(a.key, "differs in a label not shown");
    });
  }
  return out;
}

// With no match, every alert on the Alertmanager is read as this cluster's.
// Alerts that carry several values of a cluster-identifying label suggest it
// serves several clusters (v0.15.1).
const CLUSTER_LABELS = ["cluster", "cluster_name", "k8s_cluster", "tenant"];

export function clusterLabelSpread(xs: AMAlertDTO[], match?: Record<string, string>): { label: string; values: string[] } | null {
  if (match && Object.keys(match).length > 0) return null;
  for (const label of CLUSTER_LABELS) {
    const values = [...new Set(xs.flatMap((a) => a.labels.filter((l) => l.name === label).map((l) => l.value)))].sort();
    if (values.length > 1) return { label, values };
  }
  return null;
}
