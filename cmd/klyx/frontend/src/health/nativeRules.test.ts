import { describe, it, expect, afterEach, vi } from "vitest";
import fixtures from "./fixtures/native-rules.json";
import { argoAppRule, argoAppSetRule, certRule, claimRule, fluxRule, fluxSourceRule, gatewayRule, helmRule, helmStep, issuerRule, poolRule, routeRule, secretRule, storeRule } from "../test/backendRules";
import { PENDING_DEADLINE_S, releaseRule } from "../cluster/helmHealth";
import type { RuleDTO } from "../store/fleet";

// The 1c addendum §2 golden parity gate. Each case's DTO is what the lens
// receives; "want" is the Go rule's verdict (internal/*/rules.go, recorded by
// internal/appbridge/native_rules_golden_test.go). Before the lenses switched
// to the Go-computed rule, this harness ran their own TypeScript rules and all
// 45 cases matched (1c slice 2b-1, commit afef438); the Flux and Argo lenses
// matched their 43 cases the same way (2b-2, commit ec418da), the Gateway
// lens its 36 (commit 16466df), and the Helm lens its 11, on both sides of
// each time step (2c, commit e57620c). Those rules now live only
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
  it("gateways", () => check(fixtures.gateways as Case[], gatewayRule));
  it("helm releases: the read-time rule, its step, and the lens's step selection", () => {
    type HelmCase = { case: string; nowUnix: number; dto: Parameters<typeof helmRule>[0] & { rule: RuleDTO; then?: { atUnix: number; rule: RuleDTO } }; want: RuleDTO };
    let steps = 0;
    for (const c of fixtures.helmReleases as unknown as HelmCase[]) {
      expect(helmRule(c.dto, c.nowUnix), c.case).toEqual(c.want);
      expect(helmStep(c.dto, c.nowUnix), `${c.case} (step)`).toEqual(c.dto.then);
      expect(releaseRule(c.dto, c.nowUnix), `${c.case} (lens at read time)`).toEqual(c.want);
      if (c.dto.then) {
        steps++;
        expect(releaseRule(c.dto, c.dto.then.atUnix - 1), `${c.case} (1s before the step)`).toEqual(c.want);
        expect(releaseRule(c.dto, c.dto.then.atUnix), `${c.case} (at the step)`).toEqual(c.dto.then.rule);
        expect(helmRule(c.dto, c.dto.then.atUnix), `${c.case} (Go's step equals the rule then)`).toEqual(c.dto.then.rule);
        // the inspector's wording names Go's deadline
        expect(c.dto.then.atUnix - c.dto.updatedUnix - 1, c.case).toBe(PENDING_DEADLINE_S);
      }
    }
    expect(steps).toBeGreaterThan(0);
  });
  it("route attachments", () => check(fixtures.routes as Case[], (d: { route: Parameters<typeof routeRule>[0]; listeners: Parameters<typeof routeRule>[1] }) => routeRule(d.route, d.listeners)));
});
