import type { CancellablePromise } from "@wailsio/runtime";
import { GatewayService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";

// Per-rule route metrics (The Line slice 7c, parent §8.5): one Gateway's
// HTTPRoute rules, each from its own Envoy cluster on that Gateway's proxies,
// over the last 30 minutes at a 30s step and a 1m rate. Read by the data path
// (slice 8). Nothing is summed across rules.
//
// Attribution: a point is the rule's only at or after attribution.cutoff
// (the interval start, the proof, plus one rate window); earlier points are
// left out ("unattributable before <cutoff>"). With no proven interval, no
// point is the rule's. Direct and local responses never reach an upstream
// cluster, so they are not in these series (notes says so).

export type RulePoint = { t: number; v: number };
// "" when nothing was read: the rule or the Gateway unattributable, or the read unavailable.
export type RuleAvailability = "available" | "absent" | "failed" | "";
export type RuleSeries = { availability: RuleAvailability; error?: string; points: RulePoint[] };
export type RuleClasses = { availability: RuleAvailability; error?: string; classes: Record<string, RulePoint[]> };
export type RuleAttribution = { proven: boolean; start?: string; cutoff?: string; basis?: "observed" | "unknown" | string };
export type RuleBackendSeries = { namespace: string; name: string; requests: RuleSeries };
export type RuleMetric = {
  namespace: string;
  name: string;
  uid: string;
  rule: number;
  fingerprint: string;
  unattributable?: string;
  attribution: RuleAttribution;
  requests: RuleSeries; // per second
  classes: RuleClasses; // "1xx".."5xx", shares 0..1
  share503: RuleSeries; // share 0..1
  retries: RuleSeries; // per second
  backends: RuleBackendSeries[]; // empty for Envoy Gateway (see backendSplit)
};
// The Flux unit a route's labels say manages it now (slice 8d), with its
// typed applied value: observedAt only when its change was observed.
export type AppliedValue = { kind: "git" | "artifact" | "chart" | string; value: string; observedAt?: string; basis: "observed" | "unknown" | string; liveSince: string };
export type RouteUnit = { kind: string; namespace: string; name: string; uid?: string; applied?: AppliedValue };
export type RuleRoute = { namespace: string; name: string; uid: string; unit?: RouteUnit };

export type RuleMetrics = {
  status: { available: boolean; message: string; updatedAt: string; sentence?: string; lineage?: string };
  noEndpoint?: boolean; // no metrics endpoint configured or discovered
  scope: "owning-gateway" | "pod" | "kubernetes-pod" | "";
  scopeNote?: string;
  unattributable?: string;
  start: string;
  end: string;
  stepSeconds: number;
  rateSeconds: number;
  routes: RuleRoute[];
  rules: RuleMetric[];
  backendSplit: string;
  notes: string[];
  error?: string;
};

// ruleMetrics is returned as generated, never wrapped in an async function:
// its cancel() stops the read in Go, so a single-flight poll can drop it.
export function ruleMetrics(cluster: string, gwNamespace: string, gwName: string): CancellablePromise<RuleMetrics> {
  return GatewayService.GetRuleMetrics(cluster, gwNamespace, gwName) as unknown as CancellablePromise<RuleMetrics>;
}
