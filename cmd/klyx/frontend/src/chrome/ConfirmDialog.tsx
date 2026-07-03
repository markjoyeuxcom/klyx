import { useEffect, useId, useRef, useState } from "react";
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
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);

  // Initial focus so keys land in the dialog: the protected input autoFocuses
  // itself; without it, focus the confirm button (Enter then activates it).
  useEffect(() => {
    if (!isProtected) confirmRef.current?.focus();
  }, [isProtected]);

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

  // Modal key handling: swallow keys at the dialog boundary so window-level
  // list navigation (useListKeys) never acts behind the overlay. Escape
  // cancels; Tab wraps between the dialog's first and last focusable elements
  // (a basic focus trap - no dependency needed).
  const onDialogKeyDown = (e: React.KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") {
      onCancel();
    } else if (e.key === "Tab") {
      const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (!focusable || focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    }
  };

  return (
    <div
      onClick={onCancel}
      style={{ position: "fixed", inset: 0, background: "rgba(0,0,0,0.4)", display: "flex", alignItems: "center", justifyContent: "center", zIndex: 1000 }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onDialogKeyDown}
        style={{ background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-secondary)", borderRadius: "var(--border-radius-md)", padding: 20, width: 380, fontSize: 13 }}
      >
        <div id={titleId} style={{ fontWeight: 600, marginBottom: 8 }}>{title}</div>
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
              onKeyDown={(e) => {
                // Enter confirms once armed; before that it must do nothing.
                if (e.key === "Enter" && armed && !arming) void confirm();
              }}
              style={{ width: "100%", padding: "6px 8px", fontFamily: "var(--font-mono)", fontSize: 12, background: "var(--color-background-secondary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4, color: "var(--color-text-primary)" }}
            />
          </div>
        )}
        {armError && (
          <div style={{ color: "var(--color-text-danger)", fontSize: 12, marginBottom: 10 }}>{armError}</div>
        )}
        <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
          <button onClick={onCancel} style={btnStyle(false, false)}>Cancel</button>
          <button ref={confirmRef} onClick={() => void confirm()} disabled={!armed || arming} style={btnStyle(true, danger, !armed || arming)}>{confirmLabel}</button>
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
    // Danger uses the badge pairing - see OperationDialog.footerBtn for why
    // white-on-text-danger fails in dark themes.
    border: primary && danger ? "1px solid var(--color-border-danger)" : "0.5px solid var(--color-border-tertiary)",
    background: primary ? (danger ? "var(--color-background-danger)" : "var(--color-background-accent, var(--color-background-secondary))") : "transparent",
    color: primary && danger ? "var(--color-text-danger)" : "var(--color-text-primary)",
    fontWeight: primary && danger ? 600 : undefined,
  };
}
