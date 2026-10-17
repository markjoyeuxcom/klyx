import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { attributed, producerText } from "./producer";
import { ProducerLine } from "./GitOps";

// Coverage S1c §2: the producer in words, with a health only when its
// identity is established.
describe("an artifact's producer", () => {
  it("words each state, attributing only verified, identity-found and named producers", () => {
    const at = { kind: "ArtifactGenerator", namespace: "flux-system", name: "apps" };
    expect(producerText({ state: "verified", ...at })).toBe("produced by ArtifactGenerator flux-system/apps");
    expect(producerText({ state: "named", ...at })).toContain("its identity is not recorded");
    expect(producerText({ state: "declared", kind: "Pipeline", namespace: "ci", name: "build" })).toBe("produced by Pipeline ci/build; Klyx does not read that kind's health");
    expect(producerText({ state: "disagree", ...at, recordedNamespace: "flux-system", recordedName: "old" })).toContain("the recorded producer is flux-system/old");
    expect(producerText({ state: "incomplete" })).toContain("capped");
    expect(["verified", "by identity", "named"].every((s) => attributed({ state: s }))).toBe(true);
    expect(["declared", "disagree", "gone", "not found", "incomplete", "not readable", "none"].some((s) => attributed({ state: s }))).toBe(false);
  });
  it("the bound-source line shows a health only for an established producer", () => {
    const { rerender } = render(<ProducerLine producer={{ state: "verified", kind: "ArtifactGenerator", namespace: "flux-system", name: "apps", label: "Failed", role: "fault", failing: true }} lastUpdateUnix={1760000000} />);
    expect(screen.getByTestId("source-producer").textContent).toContain("· Failed");
    expect(screen.getByTestId("source-producer").textContent).toContain("this artifact is its last good one");
    rerender(<ProducerLine producer={{ state: "disagree", kind: "ArtifactGenerator", namespace: "flux-system", name: "apps", label: "Failed", role: "fault", failing: true }} />);
    expect(screen.getByTestId("source-producer").textContent).not.toContain("Failed");
  });
});
