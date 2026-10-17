import type { FluxOpOwnerDTO } from "../bridge/fluxop";

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
