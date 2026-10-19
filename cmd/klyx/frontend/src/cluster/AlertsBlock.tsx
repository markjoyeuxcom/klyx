import { useMemo } from "react";
import { InspectorBlock } from "../layout/InspectorLayout";
import { Age } from "../chrome/Age";
import { navigate, NAV_GROUPS } from "../nav/registry";
import { useFleet } from "../store/fleet";
import { useRows } from "../health/useRows";
import { alertsFacts, alertsFor, causeText, sevClass, type AMAlertDTO, type AlertTarget } from "./alertsModel";

// The Alerts block an inspector places (coverage S8a §7), like
// useInspectorAI's: the alerts correlated to this object by label - active,
// unprocessed and suppressed - with the read's time and basis. It renders
// nothing when the cluster has no Alertmanager. "No alert" is said only
// from a complete, live read.

const unix = (iso?: string) => (iso ? Math.floor(Date.parse(iso) / 1000) : 0);
const clock = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

function openAlert(cluster: string, a: AMAlertDTO) {
  useFleet.getState().requestNavIntent({ kind: "reveal", cluster, task: "alerts", part: "alerts", ref: { gvr: "", name: a.name, fingerprint: a.fingerprint } });
  const d = NAV_GROUPS.flatMap((g) => g.destinations).find((x) => x.id === "alerts");
  if (d) navigate(cluster, d);
}

function AlertLine({ cluster, a, byFp }: { cluster: string; a: AMAlertDTO; byFp: Map<string, AMAlertDTO> }) {
  const sev = sevClass(a);
  const cause = causeText(a, byFp);
  const tone = a.state !== "active" ? "var(--color-text-tertiary)" : sev === "critical" ? "var(--color-text-danger)" : sev === "warning" ? "var(--color-text-warning)" : "var(--color-text-secondary)";
  return (
    <button type="button" className="l-link insp-alert" data-testid={`insp-alert-${a.name}`} onClick={() => openAlert(cluster, a)}
      style={{ display: "grid", gridTemplateColumns: "auto 1fr auto", gap: 8, textAlign: "left", width: "100%" }}>
      <span style={{ color: tone }}>{a.severity || "none"}</span>
      <span style={{ minWidth: 0, overflow: "hidden", textOverflow: "ellipsis" }}>
        {a.name}<span className="t-secondary"> · {a.state}{cause ? ` · ${cause}` : ""}</span>
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
  const { own, onPods } = useMemo(() => (rows && target ? alertsFor(rows, target) : { own: [], onPods: [] }), [rows, target]);
  const byFp = useMemo(() => new Map((rows ?? []).map((a) => [a.fingerprint, a])), [rows]);
  if (!target || !present || !part || part.coverage === "absent") return null;
  const kind = part.kinds.find((k) => k.kind === "alerts");
  const complete = kind?.coverage === "live" && !kind.truncated && !facts?.truncated;
  const asOf = facts?.readAt ? `as of ${clock(facts.readAt)}` : "not read yet";
  const total = own.length + onPods.length;
  return (
    <InspectorBlock title="Alerts" count={total > 0 ? total : undefined} extra={<span className="t-secondary" style={{ marginLeft: "auto" }}>{asOf}</span>}>
      {own.map((a) => <AlertLine key={a.fingerprint} cluster={cluster} a={a} byFp={byFp} />)}
      {onPods.length > 0 && <div className="t-section-label" style={{ marginTop: 6 }}>on its pods</div>}
      {onPods.map((a) => <AlertLine key={a.fingerprint} cluster={cluster} a={a} byFp={byFp} />)}
      {total === 0 && <div className="t-secondary" data-testid="insp-alerts-none">
        {complete ? "No alert matches this object." : rows === null ? "Alerts not read yet." : "No alert among the alerts read; not established (the read is capped or not current)."}
      </div>}
      {total > 0 && <div className="t-secondary">Matched by label from Alertmanager, not by UID.</div>}
    </InspectorBlock>
  );
}
