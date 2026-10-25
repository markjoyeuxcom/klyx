import { useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { ConfirmDialog } from "../chrome/ConfirmDialog";
import type { ApplyResult, Plan, PlanWork } from "../bridge/plan";

// PlanView is one activation plan (Settings slice F §5, §6): what changes in
// what runs, what applying ends and what continues, and the interlock where
// one is needed. The confirmation binds to this plan's digest: the caller
// renders it keyed by digest, so a replacement plan starts unconfirmed.

const KIND_LINE: Record<Plan["kind"], string> = {
  save: "Applying writes these settings to fleet.yaml, then uses them.",
  file: "fleet.yaml already says this: applying uses it and writes nothing.",
  revert: "Reverting writes the settings last in use back to fleet.yaml, then uses them.",
};

const box: React.CSSProperties = {
  border: "0.5px solid var(--color-border-secondary)",
  borderRadius: "var(--border-radius-md)",
  background: "var(--color-background-secondary)",
  padding: "8px 10px",
  margin: "8px 0",
  fontSize: 12,
};
const head: React.CSSProperties = { fontSize: 11, fontWeight: 600, color: "var(--color-text-secondary)", margin: "6px 0 2px" };
const list: React.CSSProperties = { margin: 0, paddingLeft: 16 };

function at(started?: string): string {
  if (!started) return "";
  const d = new Date(started);
  return Number.isNaN(d.getTime()) ? "" : `, started ${d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

function WorkList({ work }: { work: PlanWork[] }) {
  return (
    <ul style={list}>
      {work.map((w) => <li key={w.id}>{w.what}{at(w.started)}</li>)}
    </ul>
  );
}

export function PlanView({ plan, busy, onApply, onCancel, interlockTitle, applyLabel = "Apply" }: {
  plan: Plan;
  busy: boolean;
  // applies this plan's digest, with the typed phrase when it has an
  // interlock; a replacement plan comes back in the result
  onApply: (typed: string) => Promise<ApplyResult>;
  onCancel: () => void;
  interlockTitle: string;
  applyLabel?: string;
}) {
  const [confirming, setConfirming] = useState(false);
  const nothing = plan.changes.length === 0 && plan.ends.length === 0 && !plan.writes;
  return (
    <div data-testid="settings-plan" data-plan-digest={plan.digest} style={box} role="region" aria-label="Review the changes">
      {plan.refused ? (
        <div data-testid="plan-refused" style={{ color: "var(--color-text-danger)" }}>
          ▲ This cannot be applied, and nothing was written: {plan.refused}
        </div>
      ) : (
        <>
          <div style={{ color: "var(--color-text-secondary)" }}>{nothing ? "Nothing changes: these are the settings in use." : KIND_LINE[plan.kind]}</div>
          {plan.changes.length > 0 && (
            <>
              <div style={head}>Changes in what runs</div>
              <ul data-testid="plan-changes" style={list}>
                {plan.changes.map((c) => (
                  <li key={c.id}>
                    {c.label}: <span style={{ fontFamily: "var(--font-mono)" }}>{c.from}</span> → <span style={{ fontFamily: "var(--font-mono)" }}>{c.to}</span>
                  </li>
                ))}
              </ul>
            </>
          )}
          <div style={head}>Ends</div>
          {plan.ends.length > 0
            ? <div data-testid="plan-ends"><WorkList work={plan.ends} /></div>
            : <div data-testid="plan-ends-none" style={{ color: "var(--color-text-tertiary)" }}>Nothing running ends.</div>}
          {plan.continues.length > 0 && (
            <>
              <div style={head}>Continues</div>
              <WorkList work={plan.continues} />
            </>
          )}
          {plan.interlock && (
            <div data-testid="plan-interlock" style={{ marginTop: 6, color: "var(--color-text-warning)", display: "flex", gap: 6, alignItems: "flex-start" }}>
              <IconAlertTriangle size={12} stroke={1.8} style={{ flexShrink: 0, marginTop: 2 }} aria-hidden />
              <span>Needs your typed confirmation: {plan.interlock.why}</span>
            </div>
          )}
        </>
      )}
      <div style={{ display: "flex", gap: 8, marginTop: 8 }}>
        {!plan.refused && !nothing && (
          <button
            className={plan.interlock ? "l-btn l-btn-danger" : "l-btn l-btn-primary"}
            disabled={busy}
            onClick={() => (plan.interlock ? setConfirming(true) : void onApply(""))}
          >
            {applyLabel}
          </button>
        )}
        <button className="l-btn" disabled={busy} onClick={onCancel}>{nothing || plan.refused ? "Close" : "Cancel"}</button>
      </div>
      {confirming && plan.interlock && (
        <ConfirmDialog
          title={interlockTitle}
          detail={plan.interlock.why}
          cluster={plan.interlock.phrase}
          protected
          danger
          lockName="egress lock"
          showScope={false}
          confirmLabel={`Confirm and ${applyLabel.toLowerCase()}`}
          // the typed phrase goes to the backend with this plan's digest:
          // Apply checks it there, and a replacement plan closes this dialog
          arm={async (typed) => {
            const r = await onApply(typed);
            return r.ok || r.plan || r.savedNotActive ? { ok: true, error: "" } : { ok: false, error: r.error || "not applied" };
          }}
          onCancel={() => setConfirming(false)}
          onConfirm={() => setConfirming(false)}
        />
      )}
    </div>
  );
}
