import { describe, it, expect } from "vitest";
import { render, within } from "@testing-library/react";
import { DataPath } from "./DataPath";
import type { GatewayNodeDTO, ListenerDTO, RouteNodeDTO } from "../store/fleet";
import type { RuleMetric, RuleMetrics } from "../bridge/ruleMetrics";

// The Line slice 8d (parent §5.2): the Gateway, the HTTPRoute, a branch per
// rule with configured-weight bars and measured traffic apart, the Service
// with its endpoints; a failing backend's branch in danger; no samples never
// a zero; attribution, onset and revision proximity as qualified words.

const ok = { included: false, role: "ok", label: "resolved" };
const gateway = { namespace: "infra", name: "eg", className: "eg", listeners: [], accepted: true, programmed: true, policies: [], rule: ok } as unknown as GatewayNodeDTO;
const listeners: ListenerDTO[] = [{ name: "http", protocol: "HTTP", port: 80 } as ListenerDTO];
const route = {
  uid: "r1", namespace: "shop", name: "web", hostnames: ["web.example.com"], matches: [], accepted: true, resolvedRefs: true, services: [], pods: { ready: 0, total: 0, unknown: false }, policies: [],
  rule: { included: true, role: "degraded", label: "web: no ready endpoints" },
  backends: [
    { kind: "Service", namespace: "shop", name: "web", port: 80, weight: 90, weightSet: true, rule: 0, state: "resolved", endpoints: { ready: 0, total: 2, nonPod: 0 } },
    { kind: "Service", namespace: "shop", name: "web-canary", port: 80, weight: 10, weightSet: true, rule: 0, state: "resolved", endpoints: { ready: 1, total: 1, nonPod: 0 } },
  ],
  rules: [{
    index: 0, fingerprint: "f", rule: { included: true, role: "degraded", label: "web: no ready endpoints" }, matches: [{ pathType: "PathPrefix", pathValue: "/", method: "", rule: 0 }],
    backends: [
      { kind: "Service", namespace: "shop", name: "web", port: 80, weight: 90, weightSet: true, rule: { included: true, role: "fault", label: "no ready endpoints" } },
      { kind: "Service", namespace: "shop", name: "web-canary", port: 80, weight: 10, weightSet: true, rule: ok },
    ],
  }],
} as unknown as RouteNodeDTO;

const T = 1792080000;
const at = (u: number) => new Date(u * 1000).toISOString();
const series = (...vs: number[]) => vs.map((v, i) => ({ t: T - (vs.length - 1 - i) * 30, v }));
const rule = (over: Partial<RuleMetric> = {}): RuleMetric => ({
  namespace: "shop", name: "web", uid: "r1", rule: 0, fingerprint: "f",
  attribution: { proven: true, start: at(T - 1200), cutoff: at(T - 1140), basis: "observed" },
  requests: { availability: "available", points: series(10, 11, 12) },
  classes: { availability: "available", classes: { "2xx": series(1, 0.98, 0.9), "5xx": series(0, 0.02, 0.1) } },
  share503: { availability: "absent", points: [] },
  retries: { availability: "failed", error: "query timed out", points: [] },
  backends: [],
  ...over,
});
const metrics = (over: Partial<RuleMetrics> = {}): RuleMetrics => ({
  status: { available: true, message: "", updatedAt: at(T) }, scope: "owning-gateway", start: at(T - 1800), end: at(T), stepSeconds: 30, rateSeconds: 60,
  routes: [{ namespace: "shop", name: "web", uid: "r1", unit: { kind: "Kustomization", namespace: "flux-system", name: "apps", uid: "u1",
    applied: { kind: "git", value: "a1b9e3c0d4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9", basis: "observed", observedAt: at(T - 60), liveSince: at(T - 3600) } } }],
  rules: [rule()], backendSplit: "unavailable: a rule's backends share its one Envoy cluster",
  notes: ["A rule's interval starts when Klyx observed it in the API; when Envoy took the change up is not verified."],
  ...over,
});

describe("DataPath's source (Settings S3b §3.4)", () => {
  it("names where the per-rule numbers are read", () => {
    const { getByText } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics({ status: { available: true, message: "", updatedAt: at(T), sentence: "from lgtm-we-metrics, hosted by prd, filtered by cluster=x" } })} />);
    expect(getByText("Read from lgtm-we-metrics, hosted by prd, filtered by cluster=x.")).toBeTruthy();
  });
});

describe("DataPath", () => {
  it("draws the Gateway, the route, a branch per rule and each backend's Service", () => {
    const { getByRole } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics()} />);
    const rail = getByRole("list", { name: "Data path" });
    const steps = [...rail.querySelectorAll("li")].map((li) => li.getAttribute("data-step"));
    expect(steps).toEqual(["gateway", "route", "rule-0", "backend-0-0", "backend-0-1"]);
    // the failing backend's branch is danger, its words its verdict
    const bad = rail.querySelector('[data-step="backend-0-0"]') as HTMLElement;
    expect(bad.getAttribute("data-segment")).toBe("failed");
    expect(bad.querySelector("[data-seg-words]")?.textContent).toBe("no ready endpoints: ");
    expect(bad.textContent).toContain("0/2 endpoints ready");
    expect(rail.querySelector('[data-step="backend-0-1"]')?.getAttribute("data-segment")).toBe("chain");
  });

  it("names its steps in the Rail's roles and never sizes its own text (slice 13c review)", () => {
    const { getByTestId } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics()} />);
    const path = getByTestId("data-path");
    expect([...path.querySelectorAll(".rail-kind")].map((k) => k.textContent)).toEqual(expect.arrayContaining(["Gateway", "HTTPRoute"]));
    expect(path.querySelectorAll('[style*="font-size"]')).toHaveLength(0);
  });

  it("shows configured weights apart from what was measured", () => {
    const { getByRole } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics()} />);
    const configured = getByRole("list", { name: "Data path" }).querySelector("[data-configured]") as HTMLElement;
    expect(configured.textContent).toContain("configured");
    expect(configured.textContent).toContain("90 · 90%");
    expect(configured.textContent).toContain("10 · 10%");
  });

  it("reads each metric apart: available, no samples, failed - never a zero for a missing series", () => {
    const { container } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics()} />);
    const m = (label: string) => container.querySelector(`[data-metric="${label}"]`)?.textContent;
    expect(m("requests")).toContain("12.0/s");
    expect(m("5xx share")).toContain("10%");
    expect(m("503 share")).toContain("no samples (no series)");
    expect(m("retries")).toContain("failed: query timed out");
    expect(container.querySelector("[data-measured]")?.textContent).toMatch(/unattributable before \d\d:\d\d/);
    expect(container.querySelectorAll("[data-hatch]").length).toBeGreaterThan(0);
  });

  it("says onset and revision proximity, naming the unit, only beside a witnessed crossing", () => {
    const { container } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics()} />);
    expect(container.querySelector("[data-onset]")?.textContent).toMatch(/^onset: first sampled crossing of 1% at \d\d:\d\d \(1-minute rate, 30-second step\)$/);
    expect(container.querySelector("[data-proximity]")?.textContent).toMatch(/^Kustomization flux-system\/apps \(manages this route now\): applied a1b9e3c, change observed/);
    // already above at the first sample: an onset, but no proximity
    const above = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics({ rules: [rule({ classes: { availability: "available", classes: { "5xx": series(0.2, 0.3) } } })] })} />);
    expect(above.container.querySelector("[data-onset]")?.textContent).toContain("already above 1%");
    expect(above.container.querySelector("[data-proximity]")).toBeNull();
  });

  it("an unattributable rule and a missing metrics endpoint are said, nothing estimated", () => {
    const u = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics({ rules: [rule({ unattributable: "shared or custom cluster names" })] })} />);
    expect(u.container.querySelector("[data-unattributable]")?.textContent).toBe("measured: unavailable (shared or custom cluster names)");
    const none = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics({ noEndpoint: true, status: { available: false, message: "metrics unavailable: no Prometheus Service found", updatedAt: "" } })} />);
    expect(within(none.container).getByText("no route metrics: configure a metrics endpoint")).toBeTruthy();
    expect(none.container.querySelector("[data-metric]")).toBeNull();
    // a configured endpoint that fails keeps its reason (design review of 1078084)
    const denied = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={metrics({ status: { available: false, message: "metrics unavailable: HTTP 403 Forbidden", updatedAt: "" } })} />);
    expect(denied.container.querySelector("[data-no-metrics]")?.textContent).toBe("no route metrics: metrics unavailable: HTTP 403 Forbidden");
  });

  // Design review of 1078084: the topology is a snapshot and the metrics
  // poll apart, so a reading joins a rule only by route UID and fingerprint.
  it("withholds a reading of another rule fingerprint or route UID", () => {
    for (const m of [metrics({ rules: [rule({ fingerprint: "edited" })] }), metrics({ rules: [rule({ uid: "replacement" })], routes: [{ namespace: "shop", name: "web", uid: "replacement" }] })]) {
      const { container, unmount } = render(<DataPath gateway={gateway} route={route} listeners={listeners} metrics={m} />);
      expect(container.querySelector("[data-withheld]")?.textContent).toContain("refresh the topology");
      expect(container.querySelector("[data-metric]")).toBeNull();
      expect(container.querySelector("[data-onset]")).toBeNull();
      expect(container.querySelector("[data-proximity]")).toBeNull();
      unmount();
    }
    // a route without identity cannot be joined at all
    const { container } = render(<DataPath gateway={gateway} route={{ ...route, uid: undefined }} listeners={listeners} metrics={metrics()} />);
    expect(container.querySelector("[data-withheld]")).not.toBeNull();
  });
});
