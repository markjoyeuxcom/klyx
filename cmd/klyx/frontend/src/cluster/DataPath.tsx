import { Rail, aspectOfVerdict, type RailStep } from "../chrome/primitives";
import { Sparkline } from "../chrome/Sparkline";
import type { GatewayNodeDTO, ListenerDTO, RouteNodeDTO, RouteRuleDTO } from "../store/fleet";
import type { RuleMetric, RuleMetrics, RulePoint, RuleSeries } from "../bridge/ruleMetrics";
import { fmtRate, fmtShare, latest, onset, onsetText, proximityText, weightShares } from "../line/dataPath";
import { httpMetricsApply, isL4, noHttpMetricsText, routeKindOf, ruleMatchText, targetListeners } from "./gatewayModel";

// The Gateway data path (The Line slice 8d, parent §5.2): the Gateway, the
// route, one branch per rule with a bar per backend for its configured
// weight, and the Service with its ready endpoints. Measured traffic per
// rule sits beside the configured weights, never merged with them, only
// where attribution holds; a missing series is "no samples", never zero.
// Every route kind draws (coverage S7b §9): a rule says what it matches in
// its kind's words, an L4 rule matches no fields, and measured HTTP traffic
// is an HTTPRoute's only.

const DANGER = new Set(["fault", "degraded"]);

export function DataPath({ gateway, route, listeners, metrics }: { gateway: GatewayNodeDTO; route: RouteNodeDTO; listeners: ListenerDTO[]; metrics: RuleMetrics | null }) {
  const kind = routeKindOf(route);
  const viaSet = route.parentRef?.kind === "ListenerSet";
  const scoped = targetListeners(route, listeners);
  const rules = route.rules ?? [];
  const m = httpMetricsApply(route) ? metrics : null;
  const steps: RailStep[] = [
    {
      key: "gateway",
      segment: "chain",
      name: `Gateway ${gateway.namespace}/${gateway.name}`,
      aspect: aspectOfVerdict(gateway.rule.role),
      label: <span><span className="rail-kind">Gateway</span> <span className="rail-name">{gateway.name}</span></span>,
      body: (
        <div>
          {viaSet ? `through ${route.attachmentLabel || "a ListenerSet"}` : scoped.length === 0 ? "no listener matches the route's section" : scoped.map((l) => `${l.protocol}:${l.port}${l.hostname ? ` ${l.hostname}` : ""}`).join(" · ")}
          {gateway.policies.length > 0 && <> · {gateway.policies.length} polic{gateway.policies.length === 1 ? "y" : "ies"}</>}
        </div>
      ),
    },
    {
      key: "route",
      segment: "chain",
      name: `${kind} ${route.namespace}/${route.name}`,
      aspect: aspectOfVerdict(route.rule.role),
      label: <span><span className="rail-kind">{kind}</span> <span className="rail-name">{route.name}</span></span>,
      body: <div>{isL4(kind) ? "every connection" : route.hostnames.length > 0 ? route.hostnames.join(", ") : kind === "TLSRoute" ? "any SNI (from the listener)" : "any host (from the listener)"}{route.policies.length > 0 ? ` · ${route.policies.length} polic${route.policies.length === 1 ? "y" : "ies"}` : ""}</div>,
      branches: rules.map((r) => [ruleStep(route, r, m, scoped)]),
    },
  ];
  return (
    <div data-testid="data-path" style={{ display: "flex", flexDirection: "column", gap: 6 }}>
      <Rail label="Data path" steps={steps} />
      {m && m.status.available && (
        <div className="rail-detail">
          {[...m.notes, m.backendSplit ? `Per-backend split: ${m.backendSplit}.` : "", m.scopeNote ?? ""].filter(Boolean).map((n, i) => <div key={i}>{n}</div>)}
        </div>
      )}
    </div>
  );
}

function ruleStep(route: RouteNodeDTO, r: RouteRuleDTO, metrics: RuleMetrics | null, scoped: ListenerDTO[]): RailStep {
  const matches = ruleMatchText(routeKindOf(route), route, r.matches, scoped);
  const ruleName = route.ruleNames?.[r.index];
  const shares = weightShares(r.backends.map((b) => b.weight));
  const ok = r.rule.role === "ok";
  return {
    key: `rule-${r.index}`,
    segment: "chain",
    name: `rule ${r.index + 1}: ${matches}`,
    aspect: aspectOfVerdict(r.rule.role),
    label: <span><span className="rail-kind">rule {r.index + 1}{ruleName ? ` ${ruleName}` : ""}</span> <span className="rail-name">{matches}</span></span>,
    tags: ok ? undefined : [r.rule.label],
    body: (
      <div style={{ display: "grid", gap: 6 }}>
        {r.backends.length > 0 && (
          <div data-configured style={{ display: "grid", gap: 2 }}>
            <span className="t-legend">configured</span>
            {r.backends.map((b, i) => (
              <div key={`${b.namespace}/${b.name}/${i}`} style={{ display: "grid", gridTemplateColumns: "minmax(0, 1fr) 80px 44px", gap: 6, alignItems: "center" }}>
                <span className="t-identifier" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{b.name}</span>
                <span aria-hidden style={{ height: 6, borderRadius: 1, background: "var(--line-sunk, var(--color-background-secondary))", position: "relative" }}>
                  <span style={{ position: "absolute", left: 0, top: 0, bottom: 0, width: `${shares[i] * 100}%`, background: DANGER.has(b.rule.role) ? "var(--line-danger, var(--color-text-danger))" : "var(--line-route, var(--color-text-info))", borderRadius: 1 }} />
                </span>
                <span className="t-secondary">{b.weight}{b.weightSet ? "" : " (unset)"} · {Math.round(shares[i] * 100)}%</span>
              </div>
            ))}
          </div>
        )}
        <Measured route={route} index={r.index} metrics={metrics} />
      </div>
    ),
    branches: r.backends.map((b, i) => {
      const be = route.backends.find((x) => x.rule === r.index && x.namespace === b.namespace && x.name === b.name);
      const eps = be?.endpoints;
      const failing = DANGER.has(b.rule.role);
      return [{
        key: `backend-${r.index}-${i}`,
        segment: failing ? "failed" : "chain",
        segmentWords: failing ? b.rule.label : undefined,
        name: `${b.kind} ${b.namespace}/${b.name}:${b.port}`,
        aspect: aspectOfVerdict(b.rule.role),
        label: <span><span className="rail-kind">{b.kind}</span> <span className="rail-name">{b.namespace}/{b.name}:{b.port}</span></span>,
        body: (
          <div>
            {eps ? (eps.error ? `endpoints unreadable: ${eps.error}` : `${eps.ready}/${eps.total} endpoints ready`) : b.rule.label}
          </div>
        ),
      } satisfies RailStep];
    }),
  };
}

function Measured({ route, index, metrics }: { route: RouteNodeDTO; index: number; metrics: RuleMetrics | null }) {
  if (!httpMetricsApply(route)) return <span className="t-secondary" data-no-http-metrics>measured: {noHttpMetricsText(route)}</span>;
  if (!metrics) return <span className="t-secondary">measured: reading…</span>;
  if (!metrics.status.available) {
    return <span className="t-secondary" data-no-metrics>{metrics.noEndpoint ? "no route metrics: configure a metrics endpoint" : `no route metrics: ${metrics.status.message}`}</span>;
  }
  const rm = metrics.rules.find((x) => x.namespace === route.namespace && x.name === route.name && x.rule === index);
  if (!rm) return <span className="t-secondary">measured: this rule was not in the read</span>;
  // The topology is a snapshot and the metrics poll apart: a reading joins
  // this rule only for the same route UID and rule fingerprint.
  if (!sameRule(route, index, rm)) return <span className="t-secondary" data-withheld>measured: withheld - the route or this rule changed since the topology was read; refresh the topology</span>;
  if (rm.unattributable) return <span className="t-secondary" data-unattributable>measured: unavailable ({rm.unattributable})</span>;
  const start = Date.parse(metrics.start) / 1000;
  const end = Date.parse(metrics.end) / 1000;
  const cutoff = rm.attribution.proven && rm.attribution.cutoff ? Date.parse(rm.attribution.cutoff) / 1000 : end;
  const hatch: [number, number] | undefined = cutoff > start ? [start, Math.min(cutoff, end)] : undefined;
  const fiveXX = rm.classes.classes?.["5xx"] ?? [];
  const o = onset(fiveXX, metrics.stepSeconds);
  const unit = metrics.routes?.find((x) => x.uid === route.uid && x.namespace === route.namespace && x.name === route.name)?.unit;
  const proximity = proximityText(unit, o);
  const window = `30 min · ${metrics.rateSeconds / 60}-minute rate · ${metrics.stepSeconds}-second step`;
  return (
    <div data-measured style={{ display: "grid", gap: 3 }}>
      <span className="t-legend">measured · {window}</span>
      {!rm.attribution.proven
        ? <span className="t-secondary">unattributable: no proven interval for this rule</span>
        : hatch && <span className="t-secondary">unattributable before {clock(cutoff)}</span>}
      <Metric label="requests" series={rm.requests} fmt={fmtRate} domain={[start, end]} hatch={hatch} step={metrics.stepSeconds} />
      <Metric label="5xx share" series={{ availability: rm.classes.availability, error: rm.classes.error, points: fiveXX }} fmt={fmtShare} domain={[start, end]} hatch={hatch} step={metrics.stepSeconds} />
      <Metric label="503 share" series={rm.share503} fmt={fmtShare} domain={[start, end]} hatch={hatch} step={metrics.stepSeconds} />
      <Metric label="retries" series={rm.retries} fmt={fmtRate} domain={[start, end]} hatch={hatch} step={metrics.stepSeconds} />
      {rm.attribution.proven && rm.classes.availability === "available" && <span className="t-secondary" data-onset>onset: {onsetText(o, metrics.rateSeconds, metrics.stepSeconds)}</span>}
      {proximity && <span className="t-secondary" data-proximity>{proximity}</span>}
    </div>
  );
}

// sameRule: a reading is this rule's only for the route UID and the rule
// fingerprint the topology shows; an unverifiable join is no join.
export function sameRule(route: RouteNodeDTO, index: number, rm: RuleMetric): boolean {
  const fp = route.rules?.find((r) => r.index === index)?.fingerprint;
  return !!route.uid && !!fp && rm.uid === route.uid && rm.fingerprint === fp;
}

const clock = (unix: number) => new Date(unix * 1000).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });

function Metric({ label, series, fmt, domain, hatch, step }: { label: string; series: RuleSeries; fmt: (v: number | undefined) => string; domain: [number, number]; hatch?: [number, number]; step: number }) {
  const text = series.availability === "failed" ? `failed: ${series.error ?? "the read failed"}`
    : series.availability === "absent" ? "no samples (no series)"
    : fmt(latest(series.points as RulePoint[]));
  return (
    <div data-metric={label} style={{ display: "grid", gridTemplateColumns: "72px minmax(0, 1fr) 120px", gap: 6, alignItems: "center" }}>
      <span className="t-secondary">{label}</span>
      <span style={{ color: series.availability === "failed" ? "var(--line-caution, var(--color-text-warning))" : undefined }}>{text}</span>
      {series.availability === "available" ? <Sparkline points={series.points} domain={domain} hatch={hatch} stepSeconds={step} label={`${label} over 30 minutes`} emptyText="no samples" /> : <span />}
    </div>
  );
}

export type { RuleMetric };
