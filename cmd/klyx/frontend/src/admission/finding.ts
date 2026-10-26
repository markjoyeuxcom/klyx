import type { ActionStatus } from "../store/fleet";

// Coverage S3b §4, §5: a refused request's admission finding, as the
// bridge carries it (classified in Go on the server's own status, the
// subject's protection applied before it crossed), and the one line a
// toast and the Activity log say for it.

export type AdmissionCandidateDTO = { kind: string; config?: string; name: string; mayMatch?: string; attributed?: boolean };
export type AdmissionFindingDTO = {
  envelope: string; name: string; binding?: string; cause?: string; code: number; reason?: string;
  request: { operation: string; kind: string; namespace?: string; name: string };
  what: string; summary: string; message?: string; withheld?: string; nothingWritten: boolean; correlation: string;
  candidates: AdmissionCandidateDTO[]; candidatesOmitted?: number; aliases?: number; maybeUnread?: boolean;
  inventory: string; inventoryAsOf?: number; exempt?: boolean;
};
// refused: the server refused the step, or Klyx did before sending it
// (notSent), so nothing of it was written; a step neither written nor
// refused is unverified (its answer was lost)
export type StepDTO = { verb: string; subject: string; written: boolean; refused?: boolean; notSent?: boolean; error?: string; admission?: AdmissionFindingDTO };
export type ActionResultLike = { ok: boolean; error: string; admission?: AdmissionFindingDTO; steps?: StepDTO[] };

// The Activity log keeps the finding's line and its candidates' names,
// never its message (S3b §2.4).
export type ActivityAdmission = { summary: string; candidates: string[] };

export const CANDIDATES_LABEL = "Candidates: their rules could match this request. Klyx does not evaluate their namespace or object selectors, match conditions or bindings, so a candidate may not have run.";

export function candidateName(c: AdmissionCandidateDTO): string {
  return c.config ? `${c.config}/${c.name}` : c.name;
}

export function activityOf(f: AdmissionFindingDTO): ActivityAdmission {
  return { summary: f.summary, candidates: f.candidates.map(candidateName) };
}

function stepWords(s: StepDTO): string {
  if (s.written) return `${s.subject}: written`;
  if (s.admission) return `${s.subject}: refused - ${s.admission.what}`;
  // Settings F1 review, F4: Klyx stopped it before it was sent
  if (s.notSent) return `${s.subject}: not sent - ${s.error || "stopped"}`;
  if (s.refused) return `${s.subject}: not written - ${s.error || "refused"}`;
  return `${s.subject}: outcome unverified - ${s.error || "no answer"}`;
}

// stepsLine says a composite action's every step (S3b §2.3).
export function stepsLine(steps: StepDTO[] | undefined): string {
  return steps && steps.length > 1 ? steps.map(stepWords).join("; ") : "";
}

function stepFindings(steps: StepDTO[] | undefined): ActivityAdmission[] {
  return (steps ?? []).filter((s) => s.admission).map((s) => activityOf(s.admission!));
}

// findingsOf keeps every step's finding and the result's own, each request
// once (Codex's implementation review, finding 4: a refused source beside
// a refused target)
function findingsOf(r: ActionResultLike): ActivityAdmission[] {
  const all = [...stepFindings(r.steps), ...(r.admission ? [activityOf(r.admission)] : [])];
  return all.filter((a, i) => all.findIndex((b) => b.summary === a.summary) === i);
}

/** failureStatus is a failed result's toast and Activity entry: "<what>
 * refused: <who refused it>" for an attributed refusal, today's text
 * otherwise, and each step of a composite action. `failed` reads like
 * "Scale failed"; prefix is the caller's context (a cluster's name). */
export function failureStatus(r: ActionResultLike, failed: string, prefix = ""): ActionStatus {
  const steps = stepsLine(r.steps);
  const head = r.admission ? `${failed.replace(/ failed$/, "")} refused: ${r.admission.what}` : r.error || failed;
  const admissions = findingsOf(r);
  return { kind: "error", message: `${prefix}${head}${steps ? ` (${steps})` : ""}`, ...(admissions.length ? { admission: admissions } : {}) };
}

/** successStatus: a written result, saying any step that was refused (a
 * composite action's source, refused beside a written target). */
export function successStatus(r: ActionResultLike, ok: string): ActionStatus {
  const notWritten = (r.steps ?? []).filter((s) => !s.written);
  if (notWritten.length === 0) return { kind: "success", message: ok };
  const findings = stepFindings(r.steps);
  // a lost answer is not a refusal: the write may have landed (finding 5)
  const said = notWritten.every((s) => s.refused || s.admission) ? "not every step was written" : "not every step was confirmed";
  return { kind: "info", message: `${ok}; ${said} (${stepsLine(r.steps)})`, ...(findings.length ? { admission: findings } : {}) };
}

/** resultStatus is either. */
export function resultStatus(r: ActionResultLike, ok: string, failed: string, prefix = ""): ActionStatus {
  return r.ok ? successStatus(r, ok) : failureStatus(r, failed, prefix);
}

/** validActivityAdmission keeps a stored entry's findings only when they
 * are well formed: older entries, and malformed ones, read as before. */
export function validActivityAdmission(v: unknown): ActivityAdmission[] | undefined {
  if (!Array.isArray(v)) return undefined;
  const ok = v.every((a) => a && typeof a === "object" && typeof (a as ActivityAdmission).summary === "string" &&
    Array.isArray((a as ActivityAdmission).candidates) && (a as ActivityAdmission).candidates.every((c) => typeof c === "string"));
  return ok ? (v as ActivityAdmission[]) : undefined;
}
