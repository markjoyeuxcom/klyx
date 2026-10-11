import { describe, it, expect, afterEach, vi } from "vitest";
import fixtures from "./fixtures/native-rules.json";
import { certNeedsAttention, certHealth, issuerHealth } from "../cluster/CertsView";
import { secretNeedsAttention, secretHealth, storeHealth } from "../cluster/ExternalSecretsView";
import { claimHealth, poolHealth } from "../cluster/KarpenterView";

// The 1c addendum §2 golden parity gate: each case's DTO is what the lens
// receives; "want" is the Go rule's verdict (internal/*/rules.go, recorded by
// internal/appbridge/native_rules_golden_test.go). The lens's own TypeScript
// rule must reach the same verdict on every case - the relocation is verbatim.

type Case = { case: string; nowUnix: number; dto: unknown; want: { included: boolean; role: string; label: string } };
type Verdict = { included: boolean; role: string; label: string };

function check(cases: Case[], rule: (dto: never) => Verdict) {
  for (const c of cases) {
    vi.setSystemTime(c.nowUnix * 1000);
    expect(rule(c.dto as never), c.case).toEqual(c.want);
  }
}

afterEach(() => vi.useRealTimers());

describe("relocated lens rules equal the Go rules (golden fixture)", () => {
  it("certificates", () => {
    vi.useFakeTimers();
    check(fixtures.certificates as Case[], (d: Parameters<typeof certHealth>[0]) => {
      const h = certHealth(d);
      return { included: certNeedsAttention(d), role: h.kind, label: h.label };
    });
  });
  it("issuers", () => {
    check(fixtures.issuers as Case[], (d: Parameters<typeof issuerHealth>[0]) => {
      const h = issuerHealth(d);
      return { included: d.ready !== "Ready", role: h.kind, label: h.label };
    });
  });
  it("external secrets", () => {
    check(fixtures.externalSecrets as Case[], (d: Parameters<typeof secretHealth>[0]) => {
      const h = secretHealth(d);
      return { included: secretNeedsAttention(d), role: h.kind, label: h.label };
    });
  });
  it("secret stores", () => {
    check(fixtures.stores as Case[], (d: Parameters<typeof storeHealth>[0]) => {
      const h = storeHealth(d);
      return { included: d.state !== "Synced", role: h.kind, label: h.label };
    });
  });
  it("node claims", () => {
    check(fixtures.nodeClaims as Case[], (d: Parameters<typeof claimHealth>[0]) => {
      const h = claimHealth(d);
      return { included: h.kind === "fault" || h.kind === "degraded", role: h.kind, label: h.label };
    });
  });
  it("node pools", () => {
    check(fixtures.nodePools as Case[], (d: Parameters<typeof poolHealth>[0]) => {
      const h = poolHealth(d);
      return { included: h.kind === "fault", role: h.kind, label: h.label };
    });
  });
});
