import { describe, it, expect } from "vitest";
import { describeArg } from "./targets";

// Codex's S1b implementation review, finding 7: every lens inspector that
// offers Describe names its kind's group here; a missing kind silently
// loses its Describe item (found for the S1a and S1b kinds).
describe("Describe targets", () => {
  it("names the Flux Operator and image automation kinds", () => {
    for (const [kind, group] of [
      ["FluxInstance", "fluxcd.controlplane.io"], ["FluxReport", "fluxcd.controlplane.io"], ["ResourceSet", "fluxcd.controlplane.io"],
      ["ResourceSetInputProvider", "fluxcd.controlplane.io"], ["ImageRepository", "image.toolkit.fluxcd.io"],
      ["ImagePolicy", "image.toolkit.fluxcd.io"], ["ImageUpdateAutomation", "image.toolkit.fluxcd.io"],
    ]) {
      expect(describeArg(kind)).toBe(`${kind.toLowerCase()}.${group}`);
    }
  });
});
