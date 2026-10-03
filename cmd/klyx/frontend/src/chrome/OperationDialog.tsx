import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { IconAlertTriangle, IconX } from "@tabler/icons-react";
import { armProtected } from "../bridge/configsvc";
import { useRestoreFocus } from "./useRestoreFocus";

// OperationDialog is the operation-COMPOSER chrome shared by every
// multi-option day-2 verb (Argo sync, node drain, helm rollback, flux
// reconcile): overlay, panel, header, scrollable consumer-supplied body, the
// prd-lock typed-confirmation section, and a dispatch footer that registers
// the one-shot backend grant (armProtected) before onConfirm fires.
//
// Confirmation and composition are different jobs: verbs with no options
// (delete, suspend, cordon) stay on ConfirmDialog; verbs that compose an
// operation with real choices live here. Consumers render their options with
// OptionTile / MetaTile below.
export function OperationDialog({
  title,
  ariaLabel,
  subtitle,
  cluster,
  protected: isProtected,
  confirmLabel,
  confirmDisabled = false,
  danger = false,
  onCancel,
  onConfirm,
  children,
}: {
  title: string;
  // ariaLabel overrides the accessible name when the display title carries
  // decoration (quotes etc.) that role queries should not have to match.
  ariaLabel?: string;
  subtitle?: string;
  cluster: string;
  protected: boolean;
  confirmLabel: string;
  // confirmDisabled lets a consumer gate dispatch on its own validity rules
  // (e.g. nothing selected); the prd-lock arming gate is composed on top.
  confirmDisabled?: boolean;
  danger?: boolean;
  onCancel: () => void;
  // onConfirm fires only after the protected grant is registered; consumers
  // capture their composed options in the closure.
  onConfirm: () => void;
  children: ReactNode;
}) {
  const [typed, setTyped] = useState("");
  const [arming, setArming] = useState(false);
  const [armError, setArmError] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const confirmRef = useRef<HTMLButtonElement | null>(null);
  const titleId = useId();
  const armed = !isProtected || typed === cluster;
  const canConfirm = armed && !arming && !confirmDisabled;

  // On a protected cluster, register the typed confirmation with the backend
  // before dispatching: destructive verbs there consume a one-shot grant and
  // refuse without it (defense in depth over the UI-side gate).
  const confirm = async () => {
    if (!canConfirm) return;
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

  // Focus lands inside the dialog: the prd-lock input when protected,
  // otherwise the confirm button - so Escape/Enter work immediately.
  useEffect(() => {
    if (!isProtected) confirmRef.current?.focus();
  }, [isProtected]);

  // Restore focus to whatever invoked the dialog once it closes - without
  // this, focus falls to <body> and a keyboard flow loses its place.
  useRestoreFocus();

  // Modal key handling: swallow keys at the dialog boundary so window-level
  // list navigation (useListKeys) never acts behind the overlay. Escape
  // cancels; Tab wraps between the dialog's first and last focusable elements
  // (a basic focus trap - no dependency needed). Mirrors ConfirmDialog.
  const onDialogKeyDown = (e: React.KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") {
      onCancel();
    } else if (e.key === "Tab") {
      const focusable = dialogRef.current?.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
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
      style={{ position: "fixed", inset: 0, zIndex: 1000, background: "rgba(0,0,0,0.48)", display: "flex", alignItems: "center", justifyContent: "center", padding: 24 }}
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        aria-labelledby={ariaLabel ? undefined : titleId}
        onClick={(e) => e.stopPropagation()}
        onKeyDown={onDialogKeyDown}
        style={{
          width: "min(720px, calc(100vw - 48px))",
          maxHeight: "calc(100vh - 64px)",
          display: "flex",
          flexDirection: "column",
          background: "var(--color-background-primary)",
          border: "0.5px solid var(--color-border-secondary)",
          borderRadius: 6,
          boxShadow: "0 18px 50px rgba(0,0,0,0.35)",
          color: "var(--color-text-primary)",
          fontSize: 13,
        }}
      >
        {/* Hazard stripe (Signal Bench): destructive composers wear the tape.
            Shape + pattern, never hue alone. */}
        {danger && (
          <div aria-hidden style={{ height: 6, flexShrink: 0, opacity: 0.65, borderRadius: "6px 6px 0 0", background: "repeating-linear-gradient(-45deg, var(--color-text-danger) 0 10px, transparent 10px 20px)" }} />
        )}
        <div style={{ padding: "18px 20px 14px", borderBottom: "0.5px solid var(--color-border-tertiary)", flexShrink: 0 }}>
          <div style={{ display: "flex", alignItems: "flex-start", justifyContent: "space-between", gap: 12 }}>
            <div style={{ minWidth: 0 }}>
              <div id={titleId} style={{ fontSize: 17, fontWeight: 600, marginBottom: subtitle ? 5 : 0, display: "flex", alignItems: "center", gap: 7 }}>
                {/* Destructive intent gets a shape, not just a colour - the
                    danger tint alone is invisible to colour-blind operators. */}
                {danger && <IconAlertTriangle size={16} stroke={1.8} style={{ color: "var(--color-text-danger)", flexShrink: 0 }} aria-hidden />}
                {title}
              </div>
              {subtitle && <div style={{ color: "var(--color-text-secondary)", lineHeight: 1.45 }}>{subtitle}</div>}
            </div>
            <button aria-label={`close ${title.toLowerCase()} dialog`} onClick={onCancel} style={{ ...closeBtn, display: "inline-flex", alignItems: "center" }}><IconX size={14} stroke={1.5} /></button>
          </div>
        </div>

        <div style={{ padding: 20, display: "grid", gap: 16, overflow: "auto", minHeight: 0 }}>
          {children}
          {isProtected && (
            <div style={{ display: "grid", gap: 6, padding: "12px 14px", borderRadius: 5, background: "var(--color-background-warning)", boxShadow: "inset 0 0 0 1px var(--color-border-warning)" }}>
              <div style={{ color: "var(--color-text-warning)", fontSize: 12 }}>
                <span style={{ fontFamily: "var(--font-mono)", fontWeight: 700 }}>prd lock</span>
                {" "}active. Type <b style={{ fontFamily: "var(--font-mono)" }}>{cluster}</b> to arm.
              </div>
              <input
                autoFocus
                aria-label="protected cluster confirmation"
                placeholder={cluster}
                value={typed}
                onChange={(e) => setTyped(e.target.value)}
                onKeyDown={(e) => {
                  // Enter dispatches once armed; before that it must do nothing.
                  if (e.key === "Enter" && canConfirm) void confirm();
                }}
                style={{ width: "100%", boxSizing: "border-box", padding: "7px 11px", fontFamily: "var(--font-mono)", fontSize: 12, background: "var(--bench-well-bg, var(--color-background-secondary))", border: "none", boxShadow: "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-tertiary))", borderRadius: 4, color: "var(--color-text-primary)" }}
              />
              {/* LED interlock: one segment per typed-correct character - the
                  arming state is visible progress, not a silent gate. */}
              <div aria-hidden style={{ display: "flex", gap: 10, alignItems: "center", fontFamily: "var(--font-mono)", fontSize: 9.5, color: "var(--color-text-tertiary)" }}>
                <span style={{ display: "flex", gap: 3 }}>
                  {Array.from({ length: cluster.length }, (_, i) => {
                    const lit = i < typed.length && cluster.startsWith(typed.slice(0, i + 1));
                    return <i key={i} style={{ width: 10, height: 4, borderRadius: 1, background: lit ? "var(--color-text-warning)" : "var(--lamp-off, var(--color-border-secondary))", boxShadow: lit ? "var(--lamp-glow-warning, none)" : undefined }} />;
                  })}
                </span>
                {armed ? "armed" : "interlock engaged"}
              </div>
            </div>
          )}
          {armError && <div style={{ color: "var(--color-text-danger)", fontSize: 12 }}>{armError}</div>}
        </div>

        <div style={{ padding: "14px 20px", borderTop: "0.5px solid var(--color-border-tertiary)", display: "flex", justifyContent: "flex-end", gap: 8, background: "var(--color-background-secondary)", flexShrink: 0 }}>
          <button onClick={onCancel} style={footerBtn(false, false, false)}>Cancel</button>
          <button ref={confirmRef} onClick={() => void confirm()} disabled={!canConfirm} style={footerBtn(true, danger, !canConfirm)}>
            {isProtected && !armed ? `${confirmLabel} — locked` : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

// OptionTile is the composer's option vocabulary: a titled toggle with an
// explanation body, so every consequential flag teaches what it does at the
// moment of choice.
export function OptionTile({ title, body, active = false, disabled = false, onClick }: { title: string; body: string; active?: boolean; disabled?: boolean; onClick?: () => void }) {
  return (
    <button
      type="button"
      aria-disabled={disabled}
      aria-pressed={active}
      onClick={disabled ? undefined : onClick}
      style={{
        cursor: disabled ? "default" : "pointer",
        textAlign: "left",
        border: active ? "0.5px solid var(--color-border-info)" : "0.5px solid var(--color-border-tertiary)",
        background: active ? "var(--color-background-info)" : "var(--color-background-secondary)",
        opacity: disabled ? 0.58 : 1,
        color: "var(--color-text-primary)",
        borderRadius: 5,
        padding: "10px 12px",
        minHeight: 72,
      }}
    >
      <div style={{ fontWeight: 600, marginBottom: 5, display: "flex", alignItems: "center", gap: 9 }}>
        <span style={{ flex: 1, minWidth: 0 }}>{title}</span>
        {/* Physical toggle (Signal Bench): the option's state is a switch you
            can read at a glance, not just a tinted border. */}
        <span aria-hidden style={{
          width: 26, height: 14, borderRadius: 7, position: "relative", flexShrink: 0,
          background: active ? "var(--color-background-info)" : "var(--bench-well-bg, var(--color-background-primary))",
          boxShadow: active ? "inset 0 0 0 1px var(--color-border-info)" : "var(--bench-well-shadow, inset 0 0 0 1px var(--color-border-secondary))",
        }}>
          <span style={{
            position: "absolute", top: 2, width: 10, height: 10, borderRadius: "50%",
            left: active ? undefined : 2, right: active ? 2 : undefined,
            background: active ? "var(--color-text-info)" : "var(--color-text-tertiary)",
          }} />
        </span>
      </div>
      <div style={{ color: "var(--color-text-secondary)", fontSize: 12, lineHeight: 1.35 }}>{body}</div>
    </button>
  );
}

// MetaTile shows one piece of operation context (target, source, revision).
export function MetaTile({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div style={{ border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-secondary)", borderRadius: 5, padding: "8px 10px", minWidth: 0 }}>
      <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.4, color: "var(--color-text-tertiary)" }}>{label}</div>
      <div title={title ?? value} style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", fontFamily: "var(--font-mono)", fontSize: 11, marginTop: 4 }}>{value}</div>
    </div>
  );
}

const closeBtn: React.CSSProperties = {
  border: "0.5px solid var(--color-border-tertiary)",
  background: "transparent",
  color: "var(--color-text-secondary)",
  borderRadius: 4,
  width: 24,
  height: 24,
  cursor: "pointer",
  flexShrink: 0,
};

function footerBtn(primary: boolean, danger: boolean, disabled: boolean): React.CSSProperties {
  return {
    padding: "6px 14px",
    fontSize: 12,
    borderRadius: 4,
    cursor: disabled ? "not-allowed" : "pointer",
    opacity: disabled ? 0.5 : 1,
    // Danger uses the badge pairing (danger-toned text on the danger tint):
    // white on --color-text-danger measures ~2.2:1 in every dark theme, where
    // that token is a LIGHT red designed as text, not as a surface.
    border: primary && danger ? "1px solid var(--color-border-danger)" : "0.5px solid var(--color-border-tertiary)",
    background: primary ? (danger ? "var(--color-background-danger)" : "var(--color-background-accent, var(--color-background-secondary))") : "transparent",
    color: primary && danger ? "var(--color-text-danger)" : "var(--color-text-primary)",
    fontWeight: primary && danger ? 600 : undefined,
  };
}
