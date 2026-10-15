import { useEffect, useState } from "react";
import { getNotificationSettings, setNotificationSettings, type NotificationSettings } from "../bridge/notifications";

// NotificationSettingsPanel is Settings > Notifications (The Line slice 10,
// parent §8.6, §14.9): off until enabled; once enabled, new faults on
// protected clusters, optionally every cluster and recoveries. Faults
// already present when a cluster connects are listed in the app, never
// notified. Enabling asks the OS for permission at that moment.

const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };
const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, padding: "3px 0", fontSize: 12 };

export function NotificationSettingsPanel() {
  const [s, setS] = useState<NotificationSettings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: "ok" | "warn" | "error"; text: string } | null>(null);

  useEffect(() => {
    void getNotificationSettings().then((v) => { if (v) setS(v); else setLoadFailed(true); });
  }, []);

  const save = async (next: NotificationSettings) => {
    const prev = s;
    setS(next);
    setBusy(true);
    const r = await setNotificationSettings(next);
    setBusy(false);
    if (!r.ok) {
      setS(prev);
      setMessage({ kind: "error", text: `Not saved: ${r.error ?? "unknown error"}` });
    } else if (r.error) {
      setMessage({ kind: "warn", text: r.error });
    } else {
      setMessage(null);
    }
  };

  if (loadFailed) return <div style={hint} role="alert">The notification settings could not be read.</div>;
  if (!s) return <div style={hint}>Reading…</div>;
  return (
    <div data-testid="notification-settings" style={{ display: "grid", gap: 4 }}>
      <label style={row}>
        <input type="checkbox" checked={s.enabled} disabled={busy} onChange={(e) => void save({ ...s, enabled: e.target.checked })} />
        Notify me about new faults
      </label>
      <div style={hint}>Off until enabled. Faults already present when a cluster connects are listed once in Activity, never notified; nothing is sent while Klyx shows that cluster.</div>
      <fieldset disabled={!s.enabled || busy} style={{ border: 0, padding: 0, margin: "4px 0 0", display: "grid", gap: 2 }}>
        <legend className="t-legend" style={{ fontSize: 10, padding: 0, marginBottom: 2 }}>Clusters</legend>
        <label style={row}>
          <input type="radio" name="notify-scope" checked={s.scope !== "all"} onChange={() => void save({ ...s, scope: "protected" })} />
          Protected clusters only
        </label>
        <label style={row}>
          <input type="radio" name="notify-scope" checked={s.scope === "all"} onChange={() => void save({ ...s, scope: "all" })} />
          Every cluster
        </label>
        <label style={row}>
          <input type="checkbox" checked={s.recoveries} onChange={(e) => void save({ ...s, recoveries: e.target.checked })} />
          Also notify recoveries
        </label>
        <div style={hint}>A recovery is notified only for a fault you were notified about, and only once it is healthy again.</div>
      </fieldset>
      {message && (
        <div role={message.kind === "error" ? "alert" : "status"} style={{ fontSize: 11, color: message.kind === "error" ? "var(--color-text-danger)" : message.kind === "warn" ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>
          {message.kind !== "ok" ? "▲ " : ""}{message.text}
        </div>
      )}
    </div>
  );
}
