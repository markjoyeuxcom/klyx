import { describe, it, expect } from "vitest";
import fixtures from "./fixtures/native-rules.json";
import { fluxNeedsAttention, fluxHealth } from "../cluster/GitOps";
import { argoHealth } from "../cluster/ArgoView";

// Pre-switch parity (1c slice 2b-2): the Flux and Argo lenses' own rules on
// the Go golden DTOs. Inline lens predicates are copied verbatim.
type Case = { case: string; dto: never; want: { included: boolean; role: string; label: string } };
const run = (cases: unknown, rule: (d: never) => { included: boolean; role: string; label: string }) => {
  for (const c of cases as Case[]) expect(rule(c.dto), c.case).toEqual(c.want);
};

describe("Flux and Argo lens rules equal the Go rules (pre-switch)", () => {
  it("flux reconciliations", () => run(fixtures.fluxReconciliations, (d: Parameters<typeof fluxHealth>[0] & Parameters<typeof fluxNeedsAttention>[0]) => {
    const h = fluxHealth(d);
    return { included: fluxNeedsAttention(d), role: h.kind, label: h.label };
  }));
  it("flux sources", () => run(fixtures.fluxSources, (d: { ready: string; suspended: boolean; reason?: string }) => {
    const h = fluxHealth(d);
    return { included: !d.suspended && d.ready !== "Ready", role: h.kind, label: h.label }; // GitOps.tsx sourceNeedsAttention
  }));
  it("argo applications", () => run(fixtures.argoApplications, (d: Parameters<typeof argoHealth>[0]) => {
    const h = argoHealth(d);
    return { included: d.broken, role: h.kind, label: h.label };
  }));
  it("argo application sets", () => run(fixtures.argoApplicationSets, (d: { healthy: boolean }) => {
    const h = d.healthy ? { kind: "ok", label: "healthy" } : { kind: "degraded", label: "needs attention" }; // ArgoView row lamp
    return { included: !d.healthy, role: h.kind, label: h.label };
  }));
});
