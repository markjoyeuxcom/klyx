import { useState } from "react";
import { armProtected } from "../bridge/configsvc";

export function ConfirmDialog({
  title,
  cluster,
  detail,
  protected: isProtected,
  confirmLabel = "Confirm",
  danger = false,
  onConfirm,
  onCancel,
}: {
  title: string;
  cluster: string;
  detail: string;
  protected: boolean;
  confirmLabel?: string;
  danger?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const [typed, setTyped] = useState("");
  const [armError, setArmError] = useState<string | null>(null);
  const [arming, setArming] = useState(false);
  const armed = !isProtected || typed === cluster;

  // On a protected cluster, register the typed confirmation with the backend
  // before dispatching: destructive verbs there consume a one-shot grant and
  // refuse without it (defense in depth over the UI-side gate).
  const confirm = async () => {
    if (isProtected) {
      setArming(true);
      const r = await armProtected(cluster, typed);
      setArming(false);
      if (!r.ok) {
        setArmError(r.error || "could not arm protected action");
        return;
      }
    }
    onConfirm();
  };

  return (
    <div
      onClick={onCancel}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.4)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 1000 }}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{ background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 20, width: 380, fontSize: 13 }}
      >
        <div style={{ fontWeight: 600, marginBottom: 8 }}>{title}</div>
        <div style={{ color: "var(--color-text-secondary)", marginBottom: 6 }}>{detail}</div>
        <div style={{ color: "var(--color-text-tertiary)", fontSize: 12, marginBottom: 14 }}>
          on <span style={{ fontFamily: "var(--font-mono)" }}>{cluster}</span>
        </div>
        {isProtected && (
          <div style={{ marginBottom: 14 }}>
            <div style={{ color: "var(--color-text-warning)", fontSize: 12, marginBottom: 6 }}>
              <span style={{ fontFamily: "var(--font-mono)", fontWeight: 600 }}>prd lock</span>
              {" "}active. Type <b>{cluster}</b> to confirm.
            </div>
            <input
              autoFocus
              placeholder={cluster}
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              style={{ width: "100%", padding: "6px 8px", fontFamily: "var(--font-mono)", fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }}
            />
          </div>
        )}
        {armError && (
          <div style={{ color: "var(--color-text-danger)", fontSize: 12, marginBottom: 10 }}>{armError}</div>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          <button onClick={onCancel} style={btnStyle(false, false)}>Cancel</button>
          <button onClick={() => void confirm()} disabled={!armed || arming} style={btnStyle(true, danger, !armed || arming)}>{confirmLabel}</button>
        </div>
      </div>
    </div>
  );
}

function btnStyle(primary: boolean, danger: boolean, disabled = false): React.CSSProperties {
  return {
    padding: "5px 12px",
    fontSize: 12,
    borderRadius: 4,
    cursor: disabled ? "not-allowed" : "pointer",
    opacity: disabled ? 0.5 : 1,
    border: "0.5px solid var(--color-border-tertiary)",
    background: primary ? (danger ? "var(--color-text-danger)" : "var(--color-background-accent, var(--color-background-secondary))") : "transparent",
    color: primary && danger ? "#fff" : "var(--color-text-primary)",
  };
}
