import { describe, it, expect, afterEach, vi } from "vitest";
import fixtures from "./fixtures/native-rules.json";
import { argoAppRule, argoAppSetRule, certRule, claimRule, fluxRule, fluxSourceRule, issuerRule, poolRule, secretRule, storeRule } from "../test/backendRules";
import type { RuleDTO } from "../store/fleet";

// The 1c addendum §2 golden parity gate. Each case's DTO is what the lens
// receives; "want" is the Go rule's verdict (internal/*/rules.go, recorded by
// internal/appbridge/native_rules_golden_test.go). Before the lenses switched
// to the Go-computed rule, this harness ran their own TypeScript rules and all
// 45 cases matched (1c slice 2b-1, commit afef438); the Flux and Argo lenses
// matched their 43 cases the same way (2b-2, commit ec418da). Those rules now live only
// in test/backendRules.ts, which lens tests use to build fixtures the way the
// backend sends them - and this gate keeps that module equal to Go.

type Case = { case: string; nowUnix: number; dto: unknown; want: RuleDTO };

function check(cases: Case[], rule: (dto: never) => RuleDTO) {
  vi.useFakeTimers();
  for (const c of cases) {
    vi.setSystemTime(c.nowUnix * 1000);
    expect(rule(c.dto as never), c.case).toEqual(c.want);
  }
}

afterEach(() => vi.useRealTimers());

describe("the backend rules used by lens fixtures equal the Go rules (golden fixture)", () => {
  it("certificates", () => check(fixtures.certificates as Case[], certRule));
  it("issuers", () => check(fixtures.issuers as Case[], issuerRule));
  it("external secrets", () => check(fixtures.externalSecrets as Case[], secretRule));
  it("secret stores", () => check(fixtures.stores as Case[], storeRule));
  it("node claims", () => check(fixtures.nodeClaims as Case[], claimRule));
  it("node pools", () => check(fixtures.nodePools as Case[], poolRule));
  it("flux reconciliations", () => check(fixtures.fluxReconciliations as Case[], fluxRule));
  it("flux sources", () => check(fixtures.fluxSources as Case[], fluxSourceRule));
  it("argo applications", () => check(fixtures.argoApplications as Case[], argoAppRule));
  it("argo application sets", () => check(fixtures.argoApplicationSets as Case[], argoAppSetRule));
});
