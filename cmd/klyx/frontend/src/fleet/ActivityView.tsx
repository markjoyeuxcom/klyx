import { IconHistory } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import { EmptyState } from "../chrome/EmptyState";
import { btn } from "../chrome/primitives";

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

  return (
    <div style={{ padding: "20px 24px", fontFamily: "var(--font-mono)", fontSize: 12, maxWidth: 860 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 4 }}>
        <div style={{ fontSize: 18, fontWeight: 600, fontFamily: "var(--font-sans, sans-serif)" }}>Activity</div>
        {log.length > 0 && (
          <button onClick={() => useFleet.getState().clearActionLog()} style={{ ...btn, marginLeft: "auto" }}>
            clear history
          </button>
        )}
      </div>
      <div style={{ fontSize: 11, color: "var(--color-text-tertiary)", marginBottom: 14, fontFamily: "var(--font-sans, sans-serif)" }}>
        Every action outcome, newest first - kept across relaunches ({log.length} recorded).
      </div>

      {log.length === 0 ? (
        <EmptyState
          icon={<IconHistory size={28} stroke={1.2} />}
          title="No actions recorded yet."
          hint="Day-2 results (reconcile, drain, scale, sync, forwards) land here as they happen."
        />
      ) : (
        <div>
          {log.map((e) => (
            <div
              key={e.id}
              data-testid="activity-row"
              style={{
                display: "flex",
                gap: 10,
                alignItems: "baseline",
                padding: "6px 8px",
                borderBottom: "0.5px solid var(--color-border-tertiary)",
              }}
            >
              <span
                style={{
                  width: 8,
                  height: 8,
                  borderRadius: "50%",
                  flexShrink: 0,
                  alignSelf: "center",
                  background: e.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-success)",
                }}
              />
              <span style={{ color: "var(--color-text-tertiary)", flexShrink: 0, width: 150 }} title={new Date(e.at).toISOString()}>
                {fmtTime(e.at)}
              </span>
              <span style={{ color: e.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-primary)", wordBreak: "break-word" }}>
                {e.message}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
