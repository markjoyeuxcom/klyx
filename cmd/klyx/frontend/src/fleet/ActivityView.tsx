import { IconHistory } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { EmptyState } from "../chrome/EmptyState";
import { LensLayout } from "../layout/LensLayout";

// ActivityView is the durable answer to "what did I do and did it succeed" -
// the toast lives six seconds, this list survives relaunches (localStorage,
// newest first, capped). Every day-2 result funnels through setActionStatus,
// so the log needs no extra wiring at the call sites.

function fmtTime(at: number): string {
  const d = new Date(at);
  const today = new Date();
  const sameDay = d.getFullYear() === today.getFullYear() && d.getMonth() === today.getMonth() && d.getDate() === today.getDate();
  const hms = d.toTimeString().slice(0, 8);
  if (sameDay) return hms;
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")} ${hms}`;
}

export function ActivityView() {
  const log = useFleet((s) => s.actionLog);
  const failed = log.filter((e) => e.kind === "error").length;

  // Step 7d: an app route on the workspace template. A success is a fact,
  // not a lamp; only failures are marked.
  return (
    <LensLayout
      label="Activity"
      title="Activity"
      summary={log.length === 0 ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{log.length}</span>
          <span className="t-secondary">recorded · newest first · kept across relaunches</span>
          {failed > 0 && <span className="t-status" style={{ color: "var(--color-text-danger)" }}>▲ {failed} failed</span>}
        </>
      )}
      headerEnd={log.length > 0 ? <button className="l-btn" onClick={() => useFleet.getState().clearActionLog()}>Clear history</button> : undefined}
      state={log.length === 0 ? "empty" : "ready"}
      stateView={
        <EmptyState
          icon={<IconHistory size={28} stroke={1.2} />}
          title="No actions recorded yet."
          hint="Action results (reconcile, drain, scale, sync, forwards) land here as they happen."
        />
      }
      body={
        <div className="ov-page" style={{ fontFamily: "var(--font-mono)", fontSize: 12, gap: 0 }}>
          {log.map((e) => (
            <div key={e.id} data-testid="activity-row" style={{ display: "flex", gap: 10, alignItems: "baseline", padding: "6px 8px", borderBottom: "1px solid var(--color-border-tertiary)" }}>
              <span aria-label={e.kind === "error" ? "failed" : e.kind === "info" ? "noted" : "succeeded"} style={{ width: 12, flexShrink: 0, color: e.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-tertiary)" }}>
                {e.kind === "error" ? "▲" : e.kind === "info" ? "•" : "✓"}
              </span>
              <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0, width: 150 }} title={new Date(e.at).toISOString()}>
                {fmtTime(e.at)}
              </span>
              <span style={{ color: e.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-primary)", wordBreak: "break-word" }}>
                {e.message}
              </span>
            </div>
          ))}
        </div>
      }
    />
  );
}
