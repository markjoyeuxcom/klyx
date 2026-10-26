import type { FluxOpOwnerDTO } from "../bridge/fluxop";
import type { GitOpsOwnershipDTO } from "../store/fleet";

// The ownership consequence of a Flux verb (roadmap C4, coverage S1b §5),
// for every Flux kind: an owner's next apply may undo what the verb wrote.
// "suspend" is a spec.suspend write; "annotation" a request annotation.
export function ownershipLine(owner: FluxOpOwnerDTO | undefined, mechanism: "suspend" | "annotation"): string {
  if (!owner) return "";
  const who = `${owner.kind} ${owner.namespace ? `${owner.namespace}/` : ""}${owner.name}`;
  return mechanism === "suspend"
    ? `Owned by ${who}. If Git sets spec.suspend for it, the owner sets it back on its next apply.`
    : `Owned by ${who}: the annotation may be removed when the owner applies.`;
}

// The GitOps consequence of a rollout undo, from the authoritative ownership
// check (Flux inventory, Argo resources), never from labels (A3 §9 stage 1).
// undefined is a read in flight; null a read that failed, which falls back
// to the labels' owner and says so. Ownership is evidence of management,
// not a promise that the rollback is reverted at the next tick.
export function undoOwnershipLine(owner: GitOpsOwnershipDTO | null | undefined, labels?: { kind: string; name: string }): string {
  const fix = " Undo is incident response; make the real fix in Git.";
  if (owner === undefined) return " - checking which GitOps unit manages it…";
  if (owner === null) {
    return labels
      ? ` - Klyx could not read which GitOps unit manages it; its labels name ${labels.kind}/${labels.name}, whose next apply may revert this.${fix}`
      : " - Klyx could not read which GitOps unit manages it.";
  }
  const partial = owner.warnings.length > 0 ? ` Ownership was not fully read: ${owner.warnings[0]}` : "";
  if (!owner.ownerKind) return partial ? ` -${partial}` : "";
  const who = `${owner.ownerKind} ${owner.ownerNamespace ? `${owner.ownerNamespace}/` : ""}${owner.ownerName}`;
  const basis = owner.basis === "flux-inventory" ? " (its Flux inventory)" : owner.basis === "argo-resources" ? " (its Argo resources)" : "";
  const effect = owner.basis === "argo-resources"
    ? "Argo CD may revert this rollback when it syncs."
    : owner.ownerSuspended
      ? "It is suspended, so nothing is reapplied until it is resumed."
      : "While it is not suspended it applies its desired state at each reconcile, which may revert this rollback.";
  return ` - ${who} manages it${basis}. ${effect}${fix}${partial}`;
}
