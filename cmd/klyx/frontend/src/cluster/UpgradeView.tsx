import { useEffect, useMemo, useState } from "react";
import type { UpgradeReportDTO } from "../store/fleet";
import { getUpgradeReadiness } from "../bridge/upgrade";
import { btn, frame, well, selectWell, MetersStrip } from "../chrome/primitives";

// UpgradeView is the "can I upgrade this cluster today?" lens: control-plane
// version + upstream EOL runway, kubelet skew findings, and the deprecated
// APIs the cluster still SERVES, framed against a selectable target minor.
//
// Honesty rules: the object count is "objects reachable via this API" (the
// API server converts on read - it cannot distinguish what version a client
// wrote); EOL dates are UPSTREAM - AKS/EKS windows differ and paid LTS
// extends them; no findings means "clean against the curated table", not a
// guarantee.

function daysUntil(dateISO: string): number | null {
  if (!dateISO) return null;
  const t = Date.parse(dateISO + "T00:00:00Z");
  if (Number.isNaN(t)) return null;
  return Math.floor((t - Date.now()) / 86400000);
}

export function UpgradeView({ cluster }: { cluster: string }) {
  const [report, setReport] = useState<UpgradeReportDTO | null>(null);
  const [target, setTarget] = useState<number | null>(null);

  const fetchReport = async () => {
    setReport(null);
    const r = await getUpgradeReadiness(cluster);
    setReport(r);
    if (r.serverMinor > 0) setTarget((t) => t ?? r.serverMinor + 1);
  };
  useEffect(() => {
    setTarget(null);
    void fetchReport();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster]);

  const eolDays = report ? daysUntil(report.eolDate) : null;
  const targetMinor = target ?? (report && report.serverMinor > 0 ? report.serverMinor + 1 : 0);

  const removedAtTarget = useMemo(
    () => (report?.deprecated ?? []).filter((d) => d.removedInMinor > 0 && targetMinor >= d.removedInMinor),
    [report, targetMinor],
  );
  const laterRemovals = useMemo(
    () => (report?.deprecated ?? []).filter((d) => d.removedInMinor === 0 || targetMinor < d.removedInMinor),
    [report, targetMinor],
  );
  const skewDanger = (report?.skew ?? []).filter((s) => s.level === "danger").length;

  return (
    <div style={{ height: "100%", minHeight: 0, overflowY: "auto", boxSizing: "border-box", padding: 16 }}>
      <div style={{ maxWidth: 900, margin: "0 auto", display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ ...frame, padding: 14, display: "flex", flexDirection: "column", gap: 12 }}>
          <div style={{ display: "flex", alignItems: "center", gap: 10, flexWrap: "wrap" }}>
            <span style={{ fontSize: 11, fontWeight: 600, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-secondary)" }}>upgrade check</span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>read-only · deprecated-API findings against a target version</span>
            <div style={{ flex: 1 }} />
            {report && report.serverMinor > 0 && (
              <label style={{ display: "flex", alignItems: "center", gap: 6, fontSize: 10, color: "var(--color-text-tertiary)" }}>
                target
                <select value={targetMinor} onChange={(e) => setTarget(Number(e.target.value))} aria-label="target minor" style={selectWell}>
                  {[1, 2, 3].map((step) => (
                    <option key={step} value={report.serverMinor + step}>1.{report.serverMinor + step}</option>
                  ))}
                </select>
              </label>
            )}
            <button onClick={() => void fetchReport()} style={{ ...btn, fontSize: 10 }}>refresh</button>
          </div>

          {report === null ? (
            <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>reading cluster version, node skew, and served APIs…</div>
          ) : report.error ? (
            <div style={{ fontSize: 11, color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>{report.error}</div>
          ) : (
            <MetersStrip
              note="upstream dates · provider windows differ"
              meters={[
                { label: "control plane", value: report.serverVersion || "?" },
                eolDays !== null && {
                  label: "upstream EOL",
                  value: eolDays < 0 ? `${-eolDays}d past` : `${eolDays}d left`,
                  tone: eolDays < 0 ? ("danger" as const) : eolDays < 60 ? ("warning" as const) : undefined,
                },
                skewDanger > 0 && { label: "skew violations", value: skewDanger, tone: "danger" as const },
                removedAtTarget.length > 0 && { label: `removed at 1.${targetMinor}`, value: removedAtTarget.length, tone: "danger" as const },
              ]}
            />
          )}
        </div>

        {report && !report.error && report.skew.length > 0 && (
          <div data-testid="upgrade-skew" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 6 }}>
            <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>kubelet skew</div>
            {report.skew.map((s) => (
              <div key={s.node} style={{ display: "flex", gap: 8, alignItems: "baseline", fontFamily: "var(--font-mono)", fontSize: 11 }}>
                <span style={{ color: s.level === "danger" ? "var(--color-text-danger)" : "var(--color-text-warning)", minWidth: 140, overflowWrap: "anywhere" }}>{s.node}</span>
                <span style={{ color: "var(--color-text-secondary)", minWidth: 70 }}>{s.version}</span>
                <span style={{ color: "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>{s.detail}</span>
              </div>
            ))}
          </div>
        )}

        {report && !report.error && (
          <div data-testid="upgrade-deprecated" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 6 }}>
            <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" }}>
              deprecated APIs still served · {report.deprecated.length === 0 ? "none" : report.deprecated.length}
            </div>
            {report.deprecated.length === 0 ? (
              <div style={{ fontSize: 11, color: "var(--color-text-success)" }}>
                no deprecated API from the curated table is served — clean for any target
              </div>
            ) : (
              <>
                {[...removedAtTarget, ...laterRemovals].map((d) => {
                  const gone = d.removedInMinor > 0 && targetMinor >= d.removedInMinor;
                  return (
                    <div key={`${d.group}/${d.version}/${d.resource}`} style={{ display: "flex", flexDirection: "column", gap: 1, padding: "4px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
                      <div style={{ display: "flex", gap: 8, alignItems: "baseline", fontFamily: "var(--font-mono)", fontSize: 11 }}>
                        <span style={{ color: gone ? "var(--color-text-danger)" : "var(--color-text-warning)", fontWeight: gone ? 600 : undefined, overflowWrap: "anywhere" }}>
                          {d.group}/{d.version} {d.kind}
                        </span>
                        <span style={{ color: gone ? "var(--color-text-danger)" : "var(--color-text-tertiary)", flexShrink: 0 }}>
                          {d.removedInMinor === 0 ? "removal unscheduled" : gone ? `REMOVED in 1.${d.removedInMinor}` : `removed in 1.${d.removedInMinor}`}
                        </span>
                        {d.countKnown && <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0 }}>{d.count} object{d.count === 1 ? "" : "s"}</span>}
                      </div>
                      {d.replacedBy && <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>migrate to {d.replacedBy}</span>}
                    </div>
                  );
                })}
                <div style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>
                  counts are objects REACHABLE via the deprecated API (the server converts on read) — the operational meaning is that clients and manifests still requesting this version break at the removal minor
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
