// Settings slice F §5: an activation plan from what runs to a candidate,
// confirmed by its digest. A replacement plan never inherits the
// confirmation: the page keys its confirmation state by digest.

export type PlanChange = { id: string; label: string; from: string; to: string };
// One piece of running work a plan ends or continues, by identity.
export type PlanWork = { id: string; kind: string; cluster: string; started?: string; what: string };
export type Interlock = { phrase: string; why: string };

export type Plan = {
  section: string;
  kind: "save" | "file" | "revert";
  digest: string;
  fileVersion: string;
  runtime: Record<string, string>;
  changes: PlanChange[];
  ends: PlanWork[];
  continues: PlanWork[];
  interlock?: Interlock;
  writes: boolean;
  // the Apply builds and publishes what it plans, even when nothing shown
  // as in use differs (a saved block whose activation failed)
  activates: boolean;
  refused?: string;
};

// Applied; refused with the plan it was replaced by; a quiet Apply's plan,
// returned for review; or saved, not active. note: applied, with something
// to know (the OS has notifications off for Klyx). unknown: the call failed
// before any answer, so whether it applied is not known.
export type ApplyResult = { ok: boolean; error?: string; plan?: Plan; savedNotActive?: string; note?: string; unknown?: boolean };

export function refusedPlan(section: string, kind: Plan["kind"], why: string): Plan {
  return { section, kind, digest: "", fileVersion: "", runtime: {}, changes: [], ends: [], continues: [], writes: false, activates: false, refused: why };
}

const why = (e: unknown) => (e instanceof Error ? e.message : String(e));

// askPlan asks the backend for a plan: a failed call is a refused plan,
// which nothing applies.
export async function askPlan(section: string, kind: Plan["kind"], call: () => Promise<unknown>): Promise<Plan> {
  try {
    return (await call()) as Plan;
  } catch (e) {
    return refusedPlan(section, kind, why(e));
  }
}

// askApply runs an Apply. A refusal is the backend's answer; a call that
// failed before any answer is unknown, never a refusal: the backend may have
// applied it, so the page reads back what runs (F2b review, R3).
export async function askApply(call: () => Promise<unknown>): Promise<ApplyResult> {
  try {
    return (await call()) as ApplyResult;
  } catch (e) {
    return { ok: false, unknown: true, error: why(e) };
  }
}

// lostAnswer says an Apply whose answer was lost.
export function lostAnswer(r: ApplyResult): string {
  return `Klyx lost the answer to this (${r.error ?? "no answer"}), so whether it applied is not known`;
}
