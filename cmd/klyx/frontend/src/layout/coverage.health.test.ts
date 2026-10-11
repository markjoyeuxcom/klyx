import { describe, it, expect } from "vitest";
import { coverageError, coverageFromHealth, kindEvidence } from "./coverage";

const names = { certificates: "Certificate", issuers: "Issuer", clusterissuers: "ClusterIssuer" };

describe("coverageFromHealth (slice 3 §5a)", () => {
  it("carries all five states unreduced and maps each to its lens evidence", () => {
    const cov = coverageFromHealth([
      { kind: "certificates", coverage: "live" },
      { kind: "issuers", coverage: "stale", class: "forbidden", error: "rbac", lastGoodAt: "2026-10-11T12:00:00Z" },
      { kind: "clusterissuers", coverage: "unreadable", class: "transport", error: "refused" },
      { kind: "secrets", coverage: "live" }, // not one of this lens's kinds
    ], names);
    expect(cov.map((k) => k.kind)).toEqual(["Certificate", "Issuer", "ClusterIssuer"]);
    expect(cov[1]).toMatchObject({ coverage: "stale", class: "forbidden", lastGoodAt: "2026-10-11T12:00:00Z", synced: true, error: "forbidden: rbac" });
    expect(kindEvidence(true, cov, "Certificate", null)).toBe("complete");
    expect(kindEvidence(true, cov, "Issuer", null)).toBe("stale");
    expect(kindEvidence(true, cov, "ClusterIssuer", null)).toBe("failed");
    expect(coverageError(cov)).toBe("Issuer: forbidden: rbac; ClusterIssuer: transport: refused");
  });

  it("checking is pending and absent is definitively none", () => {
    const cov = coverageFromHealth([{ kind: "certificates", coverage: "checking" }, { kind: "issuers", coverage: "absent" }], names);
    expect(kindEvidence(true, cov, "Certificate", null)).toBe("pending");
    expect(kindEvidence(true, cov, "Issuer", null)).toBe("complete");
    expect(cov[1].served).toBe(false);
    expect(coverageError(cov)).toBeNull();
  });

  it("a discovery gap on a live kind travels along without degrading the evidence", () => {
    const [k] = coverageFromHealth([{ kind: "certificates", coverage: "live", discovery: "discovery failing" }], names);
    expect(k).toMatchObject({ discovery: "discovery failing", coverage: "live" });
    expect(kindEvidence(true, [k], "Certificate", null)).toBe("complete");
  });
});

// Codex review of 442d42a #1: a part's dependency kinds are never its own
// evidence; they are read separately, for joins.
describe("coverageFromHealth roles", () => {
  const kinds = [
    { kind: "nodepools", role: "counted", coverage: "live" as const },
    { kind: "nodeclaims", role: "dependency", coverage: "stale" as const, error: "dial tcp" },
  ];
  const names = { nodepools: "NodePool", nodeclaims: "NodeClaim" };
  it("keeps counted kinds by default and dependencies on request", () => {
    expect(coverageFromHealth(kinds, names).map((k) => k.kind)).toEqual(["NodePool"]);
    expect(coverageFromHealth(kinds, names, "dependency").map((k) => [k.kind, k.coverage])).toEqual([["NodeClaim", "stale"]]);
  });
  it("treats a kind without a role as counted (older payloads)", () => {
    expect(coverageFromHealth([{ kind: "nodepools", coverage: "live" }], names).map((k) => k.kind)).toEqual(["NodePool"]);
  });
});
