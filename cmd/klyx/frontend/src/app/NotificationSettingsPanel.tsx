import { useEffect, useState } from "react";
import { getNotificationSettings, setNotificationSettings, type NotificationSettings } from "../bridge/notifications";
import { SettingField } from "../settings/SettingField";

// NotificationSettingsPanel is Settings > Notifications (The Line slice 10,
// parent §8.6, §14.9): off until enabled; once enabled, new faults on
// protected clusters, optionally every cluster and recoveries. Faults
// already present when a cluster connects are listed in the app, never
// notified. Enabling asks the OS for permission at that moment.

const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };
const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, padding: "3px 0", fontSize: 12 };

// onSettings hands the settings to the section (its sentence says them).
export function NotificationSettingsPanel({ onSettings }: { onSettings?: (s: NotificationSettings) => void } = {}) {
  const [s, setS] = useState<NotificationSettings | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: "ok" | "warn" | "error"; text: string } | null>(null);

  useEffect(() => {
    void getNotificationSettings().then((v) => { if (v) { setS(v); onSettings?.(v); } else setLoadFailed(true); });
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
      onSettings?.(next);
    } else {
      setMessage(null);
      onSettings?.(next);
    }
  };

  if (loadFailed) return <div style={hint} role="alert">The notification settings could not be read.</div>;
  if (!s) return <div style={hint}>Reading…</div>;
  return (
    <div data-testid="notification-settings" style={{ display: "grid", gap: 4 }}>
      <SettingField id="notifications.enabled" source="file">
        {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={s.enabled} disabled={busy} onChange={(e) => void save({ ...s, enabled: e.target.checked })} />}
      </SettingField>
      <SettingField id="notifications.scope" group source={s.scope ? "file" : "default"}>
        {() => (<>
          <label style={row}>
            <input type="radio" name="notify-scope" disabled={!s.enabled || busy} checked={s.scope !== "all"} onChange={() => void save({ ...s, scope: "protected" })} />
            Protected clusters only
          </label>
          <label style={row}>
            <input type="radio" name="notify-scope" disabled={!s.enabled || busy} checked={s.scope === "all"} onChange={() => void save({ ...s, scope: "all" })} />
            Every cluster
          </label>
        </>)}
      </SettingField>
      <SettingField id="notifications.recoveries" source="file">
        {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" disabled={!s.enabled || busy} checked={s.recoveries} onChange={(e) => void save({ ...s, recoveries: e.target.checked })} />}
      </SettingField>
      {message && (
        <div role={message.kind === "error" ? "alert" : "status"} style={{ fontSize: 11, color: message.kind === "error" ? "var(--color-text-danger)" : message.kind === "warn" ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>
          {message.kind !== "ok" ? "▲ " : ""}{message.text}
        </div>
      )}
    </div>
  );
}
