import { describe, it, expect } from "vitest";
import fixtures from "./fixtures/native-rules.json";
import { gatewayHealth, routeHealth, RANK } from "../cluster/gatewayModel";

// Pre-switch parity (1c slice 2b-2): the Gateway lens's own rules on the Go
// golden DTOs. Gateway inclusion is GatewayApiView's attention count
// (role at or above degraded); routes use the same predicate.
type Verdict = { included: boolean; role: string; label: string };
type Case = { case: string; dto: never; want: Verdict };
const run = (cases: unknown, rule: (d: never) => Verdict) => {
  for (const c of cases as Case[]) expect(rule(c.dto), c.case).toEqual(c.want);
};

describe("Gateway lens rules equal the Go rules (pre-switch)", () => {
  it("gateways", () => run(fixtures.gateways, (d: Parameters<typeof gatewayHealth>[0]) => {
    const h = gatewayHealth(d);
    return { included: RANK[h.kind] <= RANK.degraded, role: h.kind, label: h.label };
  }));
  it("route attachments", () => run(fixtures.routes, (d: { route: Parameters<typeof routeHealth>[0]; listeners: Parameters<typeof routeHealth>[1] }) => {
    const h = routeHealth(d.route, d.listeners);
    return { included: RANK[h.kind] <= RANK.degraded, role: h.kind, label: h.label };
  }));
});
