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
  refused?: string;
};

// Applied; refused with the plan it was replaced by; or saved, not active.
export type ApplyResult = { ok: boolean; error?: string; plan?: Plan; savedNotActive?: string };

export function refusedPlan(section: string, kind: Plan["kind"], why: string): Plan {
  return { section, kind, digest: "", fileVersion: "", runtime: {}, changes: [], ends: [], continues: [], writes: false, refused: why };
}
