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
  connect: "fleet.yaml already lists this cluster: applying connects it and writes nothing.",
  remove: "Applying disconnects the cluster and removes its entry from fleet.yaml.",
  retry: "Retrying makes a fresh connection with the saved terms and writes nothing.",
};

// kindLine is what applying the plan does, in words (Settings S2 §4.2).
function kindLine(plan: Plan): string {
  if (plan.kind === "file" && plan.class === "removal") return "fleet.yaml no longer lists this cluster: applying disconnects it and writes nothing.";
  if (plan.kind === "revert" && plan.section === "fleet") return "Reverting writes the terms that last connected back to fleet.yaml, then connects with them.";
  if (plan.kind === "remove" && !plan.writes) return "Applying disconnects the cluster; fleet.yaml has no entry for it to remove.";
  return KIND_LINE[plan.kind];
}

// the class's consequence a plan states beside its changes (S2 §4.2, §4.5)
const CLASS_NOTE: Partial<Record<NonNullable<Plan["class"]>, string>> = {
  reconnect: "The cluster is held until its new connection is usable: nothing new starts on it meanwhile, and its views follow the new connection.",
  protection: "Confirmations, reviews and offers made before this are stale, and the cluster's conversations end.",
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

export function PlanView({ plan, busy, onApply, onCancel, interlockTitle, applyLabel = "Apply", onRecords, title }: {
  plan: Plan;
  busy: boolean;
  // applies this plan's digest, with the typed phrase when it has an
  // interlock; a replacement plan comes back in the result
  onApply: (typed: string) => Promise<ApplyResult>;
  onCancel: () => void;
  interlockTitle: string;
  applyLabel?: string;
  // a removal's keychain choices: each toggle re-plans with the kinds
  // chosen (Settings S2 §4.4)
  onRecords?: (chosen: string[]) => void;
  title?: string;
}) {
  const [confirming, setConfirming] = useState(false);
  // the backend says whether applying does anything: a plan with nothing
  // to show can still start what is saved (F2a review, R5)
  const nothing = !plan.activates && !plan.writes;
  return (
    <div data-testid="settings-plan" data-plan-digest={plan.digest} style={box} role="region" aria-label={title ?? "Review the changes"}>
      {title && <div style={{ fontWeight: 600, marginBottom: 4 }}>{title}</div>}
      {plan.refused ? (
        <div data-testid="plan-refused" style={{ color: "var(--color-text-danger)" }}>
          ▲ This cannot be applied, and nothing was written: {plan.refused}
        </div>
      ) : (
        <>
          <div style={{ color: "var(--color-text-secondary)" }}>{nothing ? "Nothing changes: these are the settings in use." : kindLine(plan)}</div>
          {plan.class && CLASS_NOTE[plan.class] && <div data-testid="plan-class-note" style={{ color: "var(--color-text-secondary)" }}>{CLASS_NOTE[plan.class]}</div>}
          {plan.activates && plan.changes.length === 0 && (
            <div data-testid="plan-activates" style={{ color: "var(--color-text-secondary)" }}>The settings in fleet.yaml are not in use: applying starts them.</div>
          )}
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
          {plan.records && plan.records.length > 0 && (
            <>
              <div style={head}>Keychain credentials</div>
              <div style={{ color: "var(--color-text-tertiary)" }}>Each is kept unless you choose to delete it. A kept one is found again if a cluster is added under this name, for the same endpoint.</div>
              <ul data-testid="plan-records" style={{ ...list, listStyle: "none", paddingLeft: 0 }}>
                {plan.records.map((r) => (
                  <li key={r.kind}>
                    <label style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
                      <input type="checkbox" data-setting-exempt="action" checked={r.chosen} disabled={busy || !onRecords}
                        onChange={(e) => onRecords?.(plan.records!.filter((x) => (x.kind === r.kind ? e.target.checked : x.chosen)).map((x) => x.kind))} />
                      delete the {r.kind} credential
                    </label>
                  </li>
                ))}
              </ul>
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
          lockName={plan.section === "fleet" ? "protection lock" : "egress lock"}
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
