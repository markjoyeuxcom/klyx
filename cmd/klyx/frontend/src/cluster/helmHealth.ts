// Helm release health, shared by the Helm lens and the cluster attention
// summary so both say the same thing about the same release. The rule is Go's
// (internal/helmcli rules.go); this module only picks its step.
import type { RuleDTO, RuleStepDTO } from "../store/fleet";

export type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown"; label: string };

// PENDING_DEADLINE_S is the Go rule's deadline (helmcli.PendingDeadline),
// for the inspector's wording only: twice helm's default 5m --timeout. The
// golden harness holds it to Go.
export const PENDING_DEADLINE_S = 600;
// IN_FLIGHT is helm's own status vocabulary for the "pending" filter -
// evidence, not a verdict.
export const IN_FLIGHT = new Set(["pending-install", "pending-upgrade", "pending-rollback", "uninstalling"]);

// releaseRule is the release's Go verdict at nowS: the read-time rule, or
// the step Go computed for when a pending release crosses its deadline. A
// snapshot verdict, as the lens counted it before: the operation may have
// finished since the read, and the wording says so.
export function releaseRule(r: { rule: RuleDTO; then?: RuleStepDTO }, nowS: number): RuleDTO {
  return r.then && nowS >= r.then.atUnix ? r.then.rule : r.rule;
}
