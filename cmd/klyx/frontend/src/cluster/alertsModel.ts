// Coverage S8a: the Alerts lens's rows (the Go AM* DTOs, display values
// only) and the words Klyx says about them. Correlation is by label, never
// a join by UID; nothing here derives a state from a time.

export type AMPairDTO = { name: string; value: string };
export type AMSubjectDTO = {
  kind?: string; gvr?: string; namespace?: string; name?: string; container?: string; basis?: string[]; alsoNames?: AMSubjectDTO[];
  state?: string; uid?: string; createdAt?: string; startedBeforeCreated?: boolean;
};
export type AMMuteDTO = { kind: string; mutedFor?: string[]; activeFor?: string[]; expected?: string[]; shown?: string[] };
export type AMAlertDTO = {
  fingerprint: string; name: string; severity?: string; counted: boolean; state: string;
  summary?: string; description?: string; runbookURL?: string; generatorURL?: string;
  labels: AMPairDTO[]; labelsMore?: number; annotations: AMPairDTO[]; annotationsMore?: number;
  receivers?: string[]; silencedBy?: string[]; inhibitedBy?: string[]; mutedBy?: string[]; mute?: AMMuteDTO; disagreement?: string;
  subject: AMSubjectDTO; startsAt?: string; updatedAt?: string; endsAt?: string;
};

// The lens's state groups, in order (S8a §7): active, unprocessed, suppressed,
// then a state Klyx does not know, kept as written.
export type AlertGroup = "active" | "unprocessed" | "suppressed" | "other";
export const groupOf = (a: AMAlertDTO): AlertGroup =>
  a.state === "active" || a.state === "unprocessed" || a.state === "suppressed" ? a.state : "other";
const GROUP_ORDER: Record<AlertGroup, number> = { active: 0, unprocessed: 1, suppressed: 2, other: 3 };
export const sevClass = (a: AMAlertDTO): "critical" | "warning" | "other" => {
  const s = (a.severity ?? "").toLowerCase();
  return s === "critical" ? "critical" : s === "warning" ? "warning" : "other";
};
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
  if (a.state === "suppressed") {
    for (const s of a.silencedBy ?? []) parts.push(`silenced (silence ${short(s)})`);
    for (const f of a.inhibitedBy ?? []) {
      const by = byFingerprint?.get(f);
      parts.push(`inhibited by ${by ? `${by.name} (${short(f)})` : short(f)}`);
    }
    if ((a.mutedBy ?? []).length > 0) parts.push(`muted by time interval ${(a.mutedBy ?? []).join(", ")}`);
    if (parts.length === 0) parts.push("suppressed (cause not reported)");
  } else if (a.state === "unprocessed") {
    parts.push("not yet processed by Alertmanager");
  } else if (a.state !== "active") {
    parts.push(`state "${a.state}" (not one Klyx knows)`);
  }
  if (a.state === "active" && a.mute?.kind === "partial") parts.push(`muted for ${(a.mute.mutedFor ?? []).join(", ")}; active for ${(a.mute.activeFor ?? []).join(", ")}`);
  if (a.state === "active" && a.mute?.kind === "incomplete") {
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
    default: return basis;
  }
}

/** The facts the rows carry: the read's source line (display values). */
export type AlertsFacts = { source: string; note?: string; match?: Record<string, string>; readAt?: string; truncated: boolean };
export function alertsFacts(f: Record<string, unknown> | null): AlertsFacts | null {
  if (!f || typeof f.alertsSource !== "string") return null;
  return {
    source: f.alertsSource, note: typeof f.alertsNote === "string" ? f.alertsNote : undefined,
    match: f.alertsMatch && typeof f.alertsMatch === "object" ? f.alertsMatch as Record<string, string> : undefined,
    readAt: typeof f.alertsReadAt === "string" ? f.alertsReadAt : undefined, truncated: f.alertsTruncated === true,
  };
}

export type AlertTarget = { uid?: string; kind: string; namespace?: string; name: string; podUIDs?: string[] };

/** alertsFor: the alerts correlated to an object in its inspector (S8a §5,
 * §7) - by its UID when its subject was found; by kind and name for a kind
 * Klyx does not hold; a namespace lists every alert in it; a workload also
 * lists alerts naming its current pods. */
export function alertsFor(xs: AMAlertDTO[], t: AlertTarget): { own: AMAlertDTO[]; onPods: AMAlertDTO[] } {
  const own: AMAlertDTO[] = [];
  const onPods: AMAlertDTO[] = [];
  const pods = new Set(t.podUIDs ?? []);
  for (const a of xs) {
    const s = a.subject;
    if (t.kind === "Namespace") {
      if (s.namespace === t.name || (s.kind === "Namespace" && s.name === t.name)) own.push(a);
      continue;
    }
    if (t.uid && s.uid === t.uid) own.push(a);
    else if (!s.uid && s.kind === t.kind && s.name === t.name && (s.namespace ?? "") === (t.namespace ?? "") && s.state === "not looked up") own.push(a);
    else if (s.uid && pods.has(s.uid)) onPods.push(a);
  }
  return { own: sortAlerts(own), onPods: sortAlerts(onPods) };
}
