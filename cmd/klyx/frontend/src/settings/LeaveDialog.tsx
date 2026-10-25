import { useEffect, useId, useRef } from "react";
import { resolveLeave, useLeaveRequest } from "./navGuard";
import { useRestoreFocus } from "../chrome/useRestoreFocus";
import { trapTab } from "../chrome/modalFocus";

// LeaveDialog asks before a Settings section with a draft is left (S1 §1):
// save, discard or stay. A typed credential is named on its own line and is
// never saved by Save; leaving discards it either way.
export function LeaveDialog() {
  const request = useLeaveRequest((s) => s.request);
  if (!request) return null;
  return <Open settings={request.settings} credentials={request.credentials} canSave={request.canSave} />;
}

function Open({ settings, credentials, canSave }: { settings: boolean; credentials: string[]; canSave: boolean }) {
  useRestoreFocus();
  const saving = useLeaveRequest((s) => s.saving);
  const ref = useRef<HTMLDivElement | null>(null);
  const stayRef = useRef<HTMLButtonElement | null>(null);
  const titleId = useId();
  useEffect(() => { stayRef.current?.focus(); }, []);
  const what = [settings ? "unsaved settings in this section" : null, ...credentials].filter(Boolean) as string[];
  return (
    <div className="leave-scrim" onClick={() => void resolveLeave("stay")}>
      <div ref={ref} role="dialog" aria-modal="true" aria-labelledby={titleId} className="leave-dialog" data-testid="settings-leave"
        onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); void resolveLeave("stay"); }
          trapTab(e, ref.current);
        }}>
        <h2 id={titleId} className="leave-title">Leave with {what.length === 1 ? what[0] : "unsaved changes"}?</h2>
        {what.length > 1 && <ul className="leave-list">{what.map((w) => <li key={w}>{w}</li>)}</ul>}
        {credentials.length > 0 && (
          <p className="leave-note">{credentials.length === 1 ? "A typed credential is" : "Typed credentials are"} never saved by Save: leaving discards {credentials.length === 1 ? "it" : "them"}. Store {credentials.length === 1 ? "it" : "them"} first to keep {credentials.length === 1 ? "it" : "them"}.</p>
        )}
        <div className="leave-actions">
          <button ref={stayRef} type="button" className="l-btn" disabled={saving} onClick={() => void resolveLeave("stay")}>Stay</button>
          <button type="button" className="l-btn" disabled={saving} onClick={() => void resolveLeave("discard")}>Discard and leave</button>
          {canSave && <button type="button" className="l-btn l-btn-primary" disabled={saving} onClick={() => void resolveLeave("save")}>{saving ? "Saving…" : "Save and leave"}</button>}
        </div>
      </div>
    </div>
  );
}
