import { useEffect, useState } from "react";
import {
  applyNotifications,
  applyNotificationsQuiet,
  getNotificationSettings,
  planNotificationsFromFile,
  type NotificationSettings,
  type NotificationsState,
} from "../bridge/notifications";
import type { ApplyResult, Plan } from "../bridge/plan";
import { PlanView } from "../settings/PlanView";
import { SettingField } from "../settings/SettingField";

// NotificationSettingsPanel is Settings > Notifications (The Line slice 10,
// parent §8.6, §14.9): off until enabled; once enabled, new faults on
// protected clusters, optionally every cluster and recoveries. Faults
// already present when a cluster connects are listed in the app, never
// notified. Enabling asks the OS for permission at that moment.
//
// Settings slice F: what the form shows is what runs. A toggle applies at
// once through a plan that ends nothing, and says so; a hand edit of the
// block is drift, adopted with "Use the file's values".

const hint: React.CSSProperties = { fontSize: 11, color: "var(--color-text-tertiary)" };
const row: React.CSSProperties = { display: "flex", alignItems: "center", gap: 8, padding: "3px 0", fontSize: 12 };
const notice: React.CSSProperties = { fontSize: 11, color: "var(--color-text-warning)", display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" };

// onSettings hands the settings in use to the section (its sentence says them).
export function NotificationSettingsPanel({ onSettings }: { onSettings?: (s: NotificationSettings) => void } = {}) {
  const [st, setSt] = useState<NotificationsState | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ kind: "ok" | "warn" | "error"; text: string } | null>(null);
  const [plan, setPlan] = useState<Plan | null>(null);

  const load = async () => {
    const v = await getNotificationSettings();
    if (v) {
      setSt(v);
      onSettings?.(v.inUse);
    } else setLoadFailed(true);
  };
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // outcome says an Apply's result: a plan that came back is shown for
  // review, never applied by this
  const outcome = (r: ApplyResult, okText: string) => {
    if (r.plan) {
      setPlan(r.plan);
      setMessage(r.error ? { kind: "error", text: `Not applied: ${r.error}` } : null);
    } else if (!r.ok) {
      setMessage({ kind: "error", text: `Not applied: ${r.error ?? "unknown error"}` });
    } else {
      setPlan(null);
      setMessage(r.note ? { kind: "warn", text: r.note } : { kind: "ok", text: okText });
    }
  };

  const save = async (next: NotificationSettings) => {
    if (!st) return;
    const prev = st;
    setSt({ ...st, inUse: next });
    setBusy(true);
    const r = await applyNotificationsQuiet(next);
    setBusy(false);
    outcome(r, "Applied: nothing running ends.");
    if (r.ok) await load();
    else setSt(prev);
  };

  const adoptFile = async () => {
    setBusy(true);
    setMessage(null);
    const p = await planNotificationsFromFile();
    setBusy(false);
    setPlan(p);
  };

  if (loadFailed && !st) return <div style={hint} role="alert">The notification settings could not be read.</div>;
  if (!st) return <div style={hint}>Reading…</div>;
  const s = st.inUse;
  return (
    <div data-testid="notification-settings" style={{ display: "grid", gap: 4 }}>
      {st.fileError && <div data-testid="notifications-file-error" style={notice}>▲ fleet.yaml cannot be read: {st.fileError}</div>}
      {st.diverged && (
        <div data-testid="notifications-diverged" style={notice}>
          <span>▲ fleet.yaml&apos;s notification settings differ from the ones in use: a toggle is refused until you use the file&apos;s values, or undo that edit.</span>
          <button className="l-btn" disabled={busy} onClick={() => void adoptFile()}>Use the file&apos;s values</button>
        </div>
      )}
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
      {plan && (
        <PlanView
          key={plan.digest}
          plan={plan}
          busy={busy}
          interlockTitle="Apply the notification settings?"
          onApply={async () => {
            setBusy(true);
            const r = await applyNotifications(plan.digest);
            setBusy(false);
            outcome(r, "Applied: these are the notification settings in use.");
            if (r.ok) await load();
            return r;
          }}
          onCancel={() => setPlan(null)}
        />
      )}
    </div>
  );
}
