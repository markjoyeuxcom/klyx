import { describe, it, expect } from "vitest";
import { undoOwnershipLine } from "./ownership";
import type { GitOpsOwnershipDTO } from "../store/fleet";

const none: GitOpsOwnershipDTO = {
  ownerKind: "", ownerNamespace: "", ownerName: "", ownerPrunes: false, ownerSuspended: false, basis: "",
  namespaceOwnerKind: "", namespaceOwnerNamespace: "", namespaceOwnerName: "", namespacePrunes: false, namespaceManaged: false, namespaceBasis: "",
  argoApps: [], warnings: [],
};

// A3 §9 stage 1: rollout undo's consequence comes from the authoritative
// ownership read, never from labels, and says what that read showed.
describe("undoOwnershipLine", () => {
  it("names a Flux owner from its inventory, suspended or not", () => {
    const ks = { ...none, ownerKind: "Kustomization", ownerNamespace: "flux-system", ownerName: "a1-web", basis: "flux-inventory" };
    expect(undoOwnershipLine(ks)).toMatch(/Kustomization flux-system\/a1-web manages it \(its Flux inventory\)\. While it is not suspended .* may revert this rollback\. Undo is incident response/);
    expect(undoOwnershipLine({ ...ks, ownerSuspended: true })).toMatch(/It is suspended, so nothing is reapplied until it is resumed\./);
  });

  it("names an Argo owner, which the label line missed", () => {
    const app = { ...none, ownerKind: "Application", ownerNamespace: "argocd", ownerName: "shop", basis: "argo-resources" };
    expect(undoOwnershipLine(app, undefined)).toMatch(/Application argocd\/shop manages it \(its Argo resources\)\. Argo CD may revert this rollback when it syncs\./);
  });

  it("is empty for an unmanaged workload, and says when the read was partial", () => {
    expect(undoOwnershipLine(none)).toBe("");
    expect(undoOwnershipLine({ ...none, warnings: ["the inventory was truncated"] })).toBe(" - Ownership was not fully read: the inventory was truncated");
  });

  it("falls back to the labels only when the read failed, and says so", () => {
    expect(undoOwnershipLine(null, { kind: "Kustomization", name: "a1-web" })).toMatch(/could not read which GitOps unit manages it; its labels name Kustomization\/a1-web/);
    expect(undoOwnershipLine(null)).toBe(" - Klyx could not read which GitOps unit manages it.");
  });

  it("shows the read in flight", () => {
    expect(undoOwnershipLine(undefined, { kind: "Kustomization", name: "a1-web" })).toBe(" - checking which GitOps unit manages it…");
  });
});
