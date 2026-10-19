import { useMemo } from "react";
import { InspectorBlock } from "../layout/InspectorLayout";
import { Age } from "../chrome/Age";
import { navigate, NAV_GROUPS } from "../nav/registry";
import { useFleet } from "../store/fleet";
import { useRows } from "../health/useRows";
import { alertsFacts, alertsFor, causeText, noAlertEstablished, sevClass, sevWord, stateWord, type AMAlertDTO, type AlertTarget } from "./alertsModel";

// The Alerts block an inspector places (coverage S8a §7), like
// useInspectorAI's: the alerts correlated to this object by label - active,
// unprocessed and suppressed - with the read's freshness and basis. It
// renders nothing when the cluster has no Alertmanager. "No alert" is said
// only when the read and the object's subject resolution are live and
// complete; a failed read is said, never hidden behind an empty list.

const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);
const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

function openAlert(cluster: string, a: AMAlertDTO) {
  useFleet.getState().requestNavIntent({ kind: "reveal", cluster, task: "alerts", part: "alerts", ref: { gvr: "", name: a.name, fingerprint: a.key } });
  const d = NAV_GROUPS.flatMap((g) => g.destinations).find((x) => x.id === "alerts");
  if (d) navigate(cluster, d);
}

function AlertLine({ cluster, a, byFp, nameOnly }: { cluster: string; a: AMAlertDTO; byFp: Map<string, AMAlertDTO>; nameOnly: boolean }) {
  const sev = sevClass(a);
  const cause = causeText(a, byFp);
  const tone = a.stateClass !== "active" ? "var(--color-text-tertiary)" : sev === "critical" ? "var(--color-text-danger)" : sev === "warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)";
  return (
    <button type="button" className="l-link insp-alert" data-testid={`insp-alert-${a.name}`} onClick={() => openAlert(cluster, a)}
      style={{ display: "grid", gridTemplateColumns: "auto 1fr auto", gap: 8, textAlign: "left", width: "100%" }}>
      <span style={{ color: tone }}>{sevWord(a)}</span>
      <span style={{ minWidth: 0, overflow: "hidden", textOverflow: "ellipsis" }}>
        {a.name}<span className="t-secondary"> · {stateWord(a)}{cause ? ` · ${cause}` : ""}{nameOnly ? ` · by name only (${a.subject.state})` : ""}</span>
        {a.summary && <span className="t-secondary" style={{ display: "block", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{a.summary}</span>}
      </span>
      <span className="t-secondary">{a.startsAt ? <Age unixSeconds={unix(a.startsAt)} /> : ""}</span>
    </button>
  );
}

/** useInspectorAlerts: the block for one object (null without an Alertmanager). */
export function useInspectorAlerts(cluster: string, target: AlertTarget | null): React.ReactNode {
  // no Alertmanager, no subscription: the capability is the alerts read's own evidence
  const present = useFleet((s) => s.clusters.find((c) => c.name === cluster)?.alertsPresent ?? false);
  const lr = useRows<AMAlertDTO>(target && present ? cluster : null, "alerts", "alerts");
  const part = lr.part ?? lr.counts.part;
  const facts = alertsFacts(lr.facts);
  const rows = lr.rows;
  const { own, onPods, byNameOnly } = useMemo(() => (rows && target ? alertsFor(rows, target) : { own: [], onPods: [], byNameOnly: new Set<string>() }), [rows, target]);
  const byFp = useMemo(() => new Map((rows ?? []).map((a) => [a.fingerprint, a])), [rows]);
  if (!target || !present || !part || part.coverage === "absent") return null;
  const kind = part.kinds.find((k) => k.kind === "alerts");
  const freshness = kind?.coverage === "live" ? "live"
    : kind?.coverage === "stale" ? `last good ${kind.lastGoodAt ? clock(kind.lastGoodAt) : "read"}`
    : kind?.coverage === "unreadable" ? "could not be read" : "not read yet";
  const failure = lr.error ?? (kind?.coverage === "stale" || kind?.coverage === "unreadable" ? kind.error ?? "the read failed" : null);
  const why = noAlertEstablished(rows, part, target, !!facts?.truncated);
  const total = own.length + onPods.length;
  return (
    <InspectorBlock title="Alerts" count={total > 0 ? total : undefined} extra={<span className="t-secondary" style={{ marginLeft: "auto" }}>{freshness}</span>}>
      {failure && <div className="t-secondary" role="status" style={{ color: "var(--color-text-warning)" }} data-testid="insp-alerts-failure">
        Alerts read failed: {failure}{rows !== null && rows.length > 0 ? " - showing the last good read; nothing here is called resolved." : "."}
      </div>}
      {own.map((a) => <AlertLine key={a.key} cluster={cluster} a={a} byFp={byFp} nameOnly={byNameOnly.has(a.key)} />)}
      {onPods.length > 0 && <div className="t-section-label" style={{ marginTop: 6 }}>on its pods</div>}
      {onPods.map((a) => <AlertLine key={a.key} cluster={cluster} a={a} byFp={byFp} nameOnly={false} />)}
      {total === 0 && <div className="t-secondary" data-testid="insp-alerts-none">
        {why === null ? "No alert matches this object." : rows === null ? "Alerts not read yet." : `No alert matched; not established: ${why}.`}
      </div>}
      {total > 0 && <div className="t-secondary">Matched by label from Alertmanager, not by UID.</div>}
    </InspectorBlock>
  );
}
