import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { useDescribe } from "../chrome/describe/useDescribe";
import { useCertManagerRows } from "../health/useCertManagerRows";
import { IconAlertTriangle, IconRoute } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { GatewayNodeDTO, GatewayRef, ListenerDTO, PolicyRefDTO, ResourceRef, RouteMetricDTO, RouteNodeDTO, TopologyDTO } from "../store/fleet";
import { abandonRouteMetrics, getGatewayTopology, getRouteMetrics } from "../bridge/gateway";
import { ruleMetrics, type RuleMetrics } from "../bridge/ruleMetrics";
import { DataPath, sameRule } from "./DataPath";
import { fmtShare, latest } from "../line/dataPath";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, absoluteTime, useNow } from "../chrome/Age";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictMark } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import { coverageGaps, kindEvidence, lastGoodAt, type KindEvidence } from "../layout/coverage";
import {
  POLICY_ABBREV, POLICY_REFS, RANK, REFS, ROLE_TEXT, addressText, certBySecret, certStatus, errColor, fmtErr, fmtMs, fmtRps,
  gatewayRole, listenerText, policyKey, primaryBackendText, roleTone, routeHasGlobal, routeHasPolicies, routeRole, routeKey, sortWarnings,
  type CertLookup, type Role,
  attachmentWhere, carriesTraffic, httpMetricsApply, isL4, laneIssues, laneKey, laneMatchText, noHttpMetricsText, resolveLane, routeId, routeKindOf, routeRef, targetListeners,
} from "./gatewayModel";

// GatewayTopology - one Gateway's data path on the canvas-detail template
// (spec §6 step 6). The canvas draws each attachment of a route of any kind
// (coverage S7b §9) as a lane, keyed by the route and its whole parentRef:
// route -> primary backend -> rules; broken lanes first. The selected
// route's diagnosis lives in the inspector. The topology is a read with
// Refresh (route numbers poll every 20s). Evidence rules (Codex review):
// conditions keep their native status (absent / Unknown / False / last
// observed); a failed read never replaces the last good one; route absence
// is claimed only from complete lists of every served route kind; nothing here asserts an
// end-to-end path works - Klyx reads status and endpoints, it does not probe.

type Mode = "all" | "issues" | "global" | "policies";
type OpenObject = (ref: ResourceRef, namespace: string, name: string) => void;
export type MetricsObs = { state: "reading" | "live" | "stale" | "unavailable"; message: string; at: number | null };

export function GatewayTopology({ cluster, gateway }: { cluster: string; gateway: GatewayRef }) {
  const net = useFleet((s) => s.network);
  const selectRoute = useFleet((s) => s.selectRoute);
  const closeGateway = useFleet((s) => s.closeGateway);
  const cm = useFleet((s) => s.certmanager);
  // Its own subscription to the certificates (slice 3 §5): the listeners'
  // TLS evidence no longer depends on the Certificates lens being mounted.
  useCertManagerRows(cluster);
  const nowS = Math.floor(useNow() / 1000);
  const [mode, setMode] = useState<Mode>("all");
  const [nsFilter, setNsFilter] = useState("");
  const [kindFilter, setKindFilter] = useState("");
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  const refresh = () => void getGatewayTopology(cluster, gateway).catch(() => { /* rendered from the store */ });
  useEffect(() => {
    refresh();
    return () => { abandonRouteMetrics(); useFleet.getState().clearNetwork(); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, gateway.namespace, gateway.name]);

  const current = net.cluster === cluster && net.selected?.namespace === gateway.namespace && net.selected?.name === gateway.name;
  const t: TopologyDTO | null = current ? net.topology : null;
  const loading = current && net.topologyLoading;
  const readError = current ? net.topologyError ?? null : null;
  const routes = useMemo(() => t?.routes ?? [], [t]);
  const listeners = useMemo(() => t?.gateway.listeners ?? [], [t]);
  // Route coverage of the shown read: complete (absence is proof), carried
  // over from an earlier complete read (last evidence), or never read.
  const routesNow = !!t && t.routesComplete === true;
  const routesCarried = !!t && !routesNow && !!net.routesAt;
  const certEvidence: KindEvidence = cm.cluster === cluster ? kindEvidence(!cm.loading, cm.coverage, "Certificate", cm.watchError) : "pending";
  // the Certificate kind's own last good time and discovery gap (slice 3 §5a)
  const certCoverage = cm.cluster === cluster ? (cm.coverage ?? []).filter((k) => k.kind === "Certificate") : [];
  const certLastGood = lastGoodAt(certCoverage, ["Certificate"]);
  const certGap = coverageGaps(certCoverage);
  const certLookup = useMemo(() => certBySecret(cm.cluster === cluster ? cm.certificates : []), [cm, cluster]);
  const metrics = current ? net.routeMetrics : {};
  const warnings = useMemo(() => sortWarnings(t?.warnings), [t]);

  // Per-rule metrics (The Line slice 8d) poll every 30 seconds (the step)
  // while the topology is open: single-flight - a poll still running is not
  // doubled - and cancelled, with the read in Go, when the view leaves.
  const rmKey = `${cluster}\u0000${gateway.namespace}\u0000${gateway.name}`;
  const [rm, setRm] = useState<{ key: string; m: RuleMetrics } | null>(null);
  useEffect(() => {
    let live = true;
    let inflight: ReturnType<typeof ruleMetrics> | null = null;
    const tick = () => {
      if (inflight) return;
      const p = ruleMetrics(cluster, gateway.namespace, gateway.name);
      inflight = p;
      p.then(
        (m) => { if (live) setRm({ key: rmKey, m }); },
        (e: unknown) => { if (live) setRm({ key: rmKey, m: failedRuleMetrics(e) }); },
      ).finally(() => { if (inflight === p) inflight = null; });
    };
    tick();
    const id = setInterval(tick, 30000);
    return () => { live = false; clearInterval(id); inflight?.cancel(); };
  }, [cluster, gateway.namespace, gateway.name, rmKey]);
  const ruleM = rm && rm.key === rmKey ? rm.m : null;

  const rmStatus = current ? net.routeMetricsStatus : null;
  const metricsObs: MetricsObs = !rmStatus ? { state: "reading", message: "", at: null }
    : rmStatus.available ? { state: "live", message: rmStatus.message, at: net.routeMetricsAt ?? null }
    : net.routeMetricsStale ? { state: "stale", message: rmStatus.message, at: net.routeMetricsAt ?? null }
    : { state: "unavailable", message: rmStatus.message, at: null };

  const healthOf = useMemo(() => new Map(routes.map((r) => [laneKey(r), routeRole(r)])), [routes]);
  const kindOf = (r: RouteNodeDTO) => healthOf.get(laneKey(r))!.kind;
  // lanes are attachments; a route with two parentRefs here draws two
  const routeCount = useMemo(() => new Set(routes.map(routeId)).size, [routes]);
  const kindCounts = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of routes) m.set(routeKindOf(r), (m.get(routeKindOf(r)) ?? 0) + 1);
    return m;
  }, [routes]);
  const routeKinds = [...kindCounts.keys()].sort();
  const nsCounts = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of routes) m.set(r.namespace, (m.get(r.namespace) ?? 0) + 1);
    return m;
  }, [routes]);
  const namespaces = [...nsCounts.keys()].sort();
  const broken = routes.filter((r) => kindOf(r) === "fault").length;
  const degraded = routes.filter((r) => kindOf(r) === "degraded").length;
  const unverified = routes.filter((r) => kindOf(r) === "unknown" || kindOf(r) === "progressing").length;
  const issueCount = broken + degraded;
  const globalCount = routes.filter(routeHasGlobal).length;
  const policyCount = routes.filter(routeHasPolicies).length;

  const q = query.trim().toLowerCase();
  const visible = useMemo(() => routes
    .filter((r) => {
      if (nsFilter && r.namespace !== nsFilter) return false;
      if (kindFilter && routeKindOf(r) !== kindFilter) return false;
      if (mode === "issues" && RANK[healthOf.get(laneKey(r))!.kind] > RANK.degraded) return false;
      if (mode === "global" && !routeHasGlobal(r)) return false;
      if (mode === "policies" && !routeHasPolicies(r)) return false;
      if (!q) return true;
      return [r.name, r.namespace, routeKindOf(r), r.attachmentLabel ?? "", ...r.hostnames, ...r.backends.map((b) => b.name)].some((v) => v.toLowerCase().includes(q));
    })
    // broken lanes first; the API order holds within a tier (stable sort)
    .sort((a, b) => RANK[healthOf.get(laneKey(a))!.kind] - RANK[healthOf.get(laneKey(b))!.kind]),
  [routes, nsFilter, kindFilter, mode, q, healthOf]);

  // A selection is a lane key; a route's identity (from the Routes view or
  // the palette) names its worst lane here and is narrowed to it below.
  const selKey = current ? net.selectedRoute : null;
  const selected = resolveLane(selKey, routes);

  // The route-level numbers (rate, p50/p99, 5xx) are read once for the
  // selected route, for its inspector: the per-rule read replaced the
  // lanes' poll (slice 8d). They are HTTP request metrics: an HTTPRoute's
  // only (S7b §9), joined by its namespace/name.
  const metricsKey = selected && httpMetricsApply(selected) ? routeKey(selected) : null;
  useEffect(() => {
    if (!metricsKey) return;
    void getRouteMetrics(cluster, gateway.namespace, gateway.name, [metricsKey]);
    return () => abandonRouteMetrics();
  }, [cluster, gateway.namespace, gateway.name, metricsKey]);

  // Selections this view made itself; any other key arrived by navigation
  // (the palette) and must be revealed - clear filters once, after the
  // topology holding it has arrived.
  const ownSelect = useRef<string | null>(null);
  const pick = (k: string | null) => { ownSelect.current = k; selectRoute(k); };
  useEffect(() => {
    if (!selKey || selKey === ownSelect.current || !selected) return;
    ownSelect.current = selKey;
    setMode("all"); setNsFilter(""); setKindFilter(""); setQuery("");
  }, [selKey, selected]);
  // a route identity becomes the lane it resolved to, so the selection
  // stays on that attachment
  useEffect(() => {
    if (selKey && selected && laneKey(selected) !== selKey) pick(laneKey(selected));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selKey, selected]);

  // First fresh, COMPLETE read: select the first broken lane once (no focus
  // theft). An incomplete or failed read does not consume the one shot.
  const autoDone = useRef(false);
  useEffect(() => {
    if (autoDone.current || !t || loading || !routesNow) return;
    autoDone.current = true;
    if (selKey) return;
    const first = visible[0];
    if (first && kindOf(first) === "fault") pick(laneKey(first));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [t, loading, routesNow]);

  // Later operator filter edits win: one that hides the selection closes it.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !visible.includes(selected)) pick(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, nsFilter, kindFilter, q]);

  // A route absent from a complete current read is named as no longer
  // attached; absent from anything less proves nothing.
  const lastRoute = useRef<RouteNodeDTO | null>(null);
  if (selected) lastRoute.current = selected;
  if (lastRoute.current && laneKey(lastRoute.current) !== selKey) lastRoute.current = null;
  const missing = !!selKey && !selected && !!t && !loading;
  const inspected = selected ?? (missing ? lastRoute.current : null);

  const selIdx = selected ? visible.indexOf(selected) : -1;
  useListKeys({
    count: visible.length,
    selected: selIdx,
    onSelect: (i) => { const r = visible[i]; if (r) pick(laneKey(r)); },
    onActivate: (i) => { const r = visible[i]; if (r) pick(laneKey(r)); },
    onEscape: () => pick(null),
    searchRef,
  });

  const openObject: OpenObject = (ref, namespace, name) => {
    const nav = useFleet.getState();
    nav.openResource(ref);
    nav.openInstance(ref.scope === "Cluster" ? "" : namespace, name);
  };

  let state: DatasetState = "ready";
  if (!t && readError) state = "unreadable";
  else if (!t) state = "loading";
  else if (routes.length === 0) state = "empty";
  else if (visible.length === 0) state = "no-match";

  const icon = <IconRoute size={28} stroke={1.2} />;
  const clearFilters = () => { setMode("all"); setNsFilter(""); setKindFilter(""); setQuery(""); };
  const gwRole = t ? gatewayRole(t.gateway) : null;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading the Gateway topology" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read this Gateway's topology." hint={readError ?? undefined} action={{ label: "Retry", onClick: refresh }} />
    : state === "empty" ? (
      <div style={{ width: "100%", maxWidth: 720, alignSelf: "stretch" }}>
        {t && <GatewayStrip g={t.gateway} clusterPolicies={t.clusterPolicies ?? []} absentKinds={warnings.absentKinds} />}
        {routesNow
          ? <EmptyState icon={icon} title="No routes are attached to this Gateway." hint="Routes of every served kind attach through spec.parentRefs, directly or through one of its ListenerSets; none references this Gateway." />
          : <EmptyState icon={<IconAlertTriangle size={28} stroke={1.2} />} title="Could not list every route kind." hint={`${t?.routesError ?? "The route lists were not read"} - which routes attach is unknown.`} action={{ label: "Retry", onClick: refresh }} />}
      </div>
    )
    : <EmptyState icon={icon} title="No routes match the current filters." hint={`${routes.length} routes on this Gateway.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  const notices = [
    readError && t ? (
      <div key="stale" role="status" className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
        <span className="t-status">Refresh failed - showing the read from {net.topologyAt ? absoluteTime(net.topologyAt) : "earlier"}.</span> {readError}
      </div>
    ) : null,
    routesCarried ? (
      <div key="routes" role="status" className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
        <span className="t-status">A route list failed - the lanes are from the complete read at {absoluteTime(net.routesAt!)}.</span> {t?.routesError}
      </div>
    ) : null,
    gwRole && RANK[gwRole.kind] <= RANK.degraded ? (
      <div key="gw" role="status" className="t-body" style={{ color: ROLE_TEXT[gwRole.kind], overflowWrap: "anywhere" }}>
        <span className="t-status">{gwRole.kind === "fault" ? "▲ " : ""}Gateway {gwRole.label}.</span> {gwRole.kind === "fault" ? "Its controller rejects it - the lanes below are not served through it." : "Lane evidence below is route status and endpoints, not proof that traffic flows through this Gateway."}
      </div>
    ) : null,
    ...warnings.gaps.filter((w) => w !== t?.routesError).map((w, i) => (
      <div key={`w${i}`} className="t-body" style={{ color: "var(--color-text-warning)", display: "flex", gap: 6, alignItems: "baseline", overflowWrap: "anywhere" }}>
        <IconAlertTriangle size={12} stroke={1.7} style={{ flexShrink: 0, alignSelf: "center" }} /> {w}
      </div>
    )),
  ].filter(Boolean);

  const metricsCaption = metricsObs.state === "reading" ? "route traffic: reading…"
    : metricsObs.state === "stale" ? `route traffic: last good read - the latest failed${metricsObs.message ? ` (${metricsObs.message})` : ""}`
    : metricsObs.state === "unavailable" ? `route traffic unavailable: ${metricsObs.message || "no metrics source"}`
    : metricsObs.message ? `route traffic: ${metricsObs.message}` : "route traffic · 5m rates";
  const coverage = routesNow ? "" : routesCarried ? " (last complete read)" : " (route list unreadable)";

  return (
    <LensLayout
      variant="canvas"
      label="Gateway topology"
      title={
        <>
          <button className="gw-crumb" onClick={closeGateway} aria-label="Back to Gateways">Gateways</button>
          <span className="t-secondary" aria-hidden="true"> / </span>
          <span style={{ fontFamily: "var(--font-mono)" }}><span style={{ color: "var(--color-text-tertiary)" }}>{gateway.namespace}/</span>{gateway.name}</span>
        </>
      }
      summary={!t || (!routesNow && !routesCarried) ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{routeCount}</span>
          <span className="t-secondary">route{routeCount === 1 ? "" : "s"}{routes.length > routeCount ? ` · ${routes.length} attachments` : ""}{coverage}</span>
          {issueCount > 0 && <span className="t-status" style={{ color: broken > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{[broken ? `▲ ${broken} broken` : "", degraded ? `${degraded} degraded` : ""].filter(Boolean).join(" · ")}</span>}
          {unverified > 0 && <span className="t-secondary">{unverified} unverified</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="Read on demand - not a live watch. Per-rule traffic refreshes every 30s; the selected route's numbers are read once when it is selected.">snapshot</span>
          <UpdatedAt at={current && net.topologyAt ? net.topologyAt : null} />
          <button className="l-btn" onClick={refresh} disabled={loading}>{loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={t && routes.length > 0 ? (
        <>
          <div role="group" aria-label="Route filter" className="l-seg">
            <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {routes.length}</button>
            <button className="l-chip" aria-pressed={mode === "issues"} onClick={() => setMode("issues")}>Issues {issueCount > 0 && <span style={{ color: broken > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{issueCount}</span>}</button>
            {globalCount > 0 && <button className="l-chip" aria-pressed={mode === "global"} onClick={() => setMode("global")}>Global {globalCount}</button>}
            {policyCount > 0 && <button className="l-chip" aria-pressed={mode === "policies"} onClick={() => setMode("policies")}>With policies {policyCount}</button>}
          </div>
          {routeKinds.length > 1 && (
            <label style={{ display: "flex" }}>
              <span className="sr-only">Route kind</span>
              <select className="l-input" value={kindFilter} onChange={(e) => setKindFilter(e.target.value)}>
                <option value="">All kinds</option>
                {routeKinds.map((k) => <option key={k} value={k}>{k} ({kindCounts.get(k)})</option>)}
              </select>
            </label>
          )}
          {namespaces.length > 1 && (
            <label style={{ display: "flex" }}>
              <span className="sr-only">Route namespace</span>
              <select className="l-input" value={nsFilter} onChange={(e) => setNsFilter(e.target.value)}>
                <option value="">All namespaces</option>
                {namespaces.map((ns) => <option key={ns} value={ns}>{ns} ({nsCounts.get(ns)})</option>)}
              </select>
            </label>
          )}
          <label className="lens-search">
            <span className="sr-only">Search routes</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
              value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search route, hostname, backend" />
          </label>
        </>
      ) : undefined}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2, padding: "4px 0" }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={t ? (
        <div className="lens-canvas" data-testid="gateway-canvas">
          <GatewayStrip g={t.gateway} clusterPolicies={t.clusterPolicies ?? []} absentKinds={warnings.absentKinds} />
          <div role="list" aria-label="Route lanes" style={{ display: "grid", gap: 8 }}>
            {visible.map((r) => (
              <RouteLane key={laneKey(r)} route={r} role={healthOf.get(laneKey(r))!} ruleMetrics={ruleM}
                selected={laneKey(r) === selKey} onSelect={() => pick(laneKey(r) === selKey ? null : laneKey(r))} />
            ))}
          </div>
        </div>
      ) : null}
      footer={<span className="t-legend">j k select · esc close · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length !== routes.length ? `${visible.length} of ${routes.length} routes · ` : ""}broken first · {metricsCaption}</span>}
      inspector={selKey && inspected && t ? (
        <RouteInspector key={selKey} cluster={cluster} route={inspected} gone={missing && routesNow} unproven={missing && !routesNow} gateway={t.gateway} listeners={listeners} notes={warnings.byRoute.get(routeId(inspected)) ?? []} ruleMetrics={ruleM}
          metric={metricsKey ? metrics[metricsKey] : undefined} metricsObs={metricsObs} certLookup={certLookup} certEvidence={certEvidence} certLastGood={certLastGood} certGap={certGap} nowS={nowS} onOpenObject={openObject} onClose={() => pick(null)} />
      ) : undefined}
      inspectorLabel="Route details"
      onCloseInspector={() => pick(null)}
    />
  );
}

// GatewayStrip: the canvas's first node - the Gateway itself, with the
// listeners every lane enters through and the policies that apply to all.
function GatewayStrip({ g, clusterPolicies, absentKinds }: { g: GatewayNodeDTO; clusterPolicies: PolicyRefDTO[]; absentKinds: string[] }) {
  const role = gatewayRole(g);
  const address = addressText(g.addresses);
  const [showAbsent, setShowAbsent] = useState(false);
  return (
    <div className="gw-node gw-gateway" data-testid="gateway-strip">
      <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
        <VerdictMark health={role} size={8} />
        <span className="t-legend">gateway</span>
        <span className="t-identifier" style={{ fontWeight: 600 }}>{g.name}</span>
        <span className="t-secondary">{g.className}</span>
        <span className="t-status" style={{ color: ROLE_TEXT[role.kind], marginLeft: "auto" }}>{role.label}</span>
      </div>
      <div className="gw-facts">
        <span>address</span><span style={{ color: address ? undefined : "var(--color-text-tertiary)" }}>{address || "none assigned"}</span>
        <span>listeners</span><span>{g.listeners.length === 0 ? <span className="t-secondary">none</span> : g.listeners.map((l) => `${listenerText(l)}${l.hostname ? ` (${l.hostname})` : ""}`).join(" · ")}</span>
        {g.policies.length > 0 && <><span>policies</span><span><PolicyList policies={g.policies} /></span></>}
        {clusterPolicies.length > 0 && <><span>cluster-wide</span><span><PolicyList policies={clusterPolicies} /></span></>}
        {absentKinds.length > 0 && (
          <>
            <span>policy APIs</span>
            <span className="t-secondary" style={{ fontFamily: "var(--font-sans)" }}>
              <button className="gw-crumb" style={{ color: "var(--color-text-secondary)" }} aria-expanded={showAbsent} onClick={() => setShowAbsent(!showAbsent)}>
                {absentKinds.length} optional kind{absentKinds.length === 1 ? "" : "s"} not installed
              </button>
              {showAbsent && <> - {absentKinds.join(", ")}; no such policies can apply here</>}
            </span>
          </>
        )}
      </div>
    </div>
  );
}

function PolicyList({ policies }: { policies: PolicyRefDTO[] }) {
  return (
    <span style={{ display: "inline-flex", flexWrap: "wrap", gap: 4 }}>
      {policies.map((p) => (
        <span key={policyKey(p)} className="gw-policy" title={`${p.kind} ${p.namespace ? `${p.namespace}/` : ""}${p.name}${p.summary ? ` - ${p.summary}` : ""}`}>
          {POLICY_ABBREV[p.kind] ?? p.kind} {p.name}
        </span>
      ))}
    </span>
  );
}


// RuleSummary is a lane's per-rule reading: how many rules, and the rule
// with the highest latest attributed 5xx share - one rule's value, never a
// sum across rules (slice 7c).
function RuleSummary({ route, metrics }: { route: RouteNodeDTO; metrics: RuleMetrics | null }) {
  const n = route.rules?.length ?? 0;
  const count = `${n} rule${n === 1 ? "" : "s"}`;
  if (!httpMetricsApply(route)) return <span className="t-secondary gw-line" title={noHttpMetricsText(route)}>{count} · {noHttpMetricsText(route)}</span>;
  if (!metrics) return <span className="t-secondary">{count} · reading…</span>;
  if (!metrics.status.available) return <span className="t-secondary">{count} · no route metrics</span>;
  let worst: { rule: number; v: number } | null = null;
  const mine = metrics.rules.filter((r) => r.namespace === route.namespace && r.name === route.name);
  // the readings join the shown rules only by route UID and rule fingerprint
  if (mine.some((r) => !sameRule(route, r.rule, r))) return <span className="t-secondary gw-line">{count} · rules changed since the topology was read</span>;
  for (const r of mine) {
    if (r.unattributable || r.classes.availability !== "available") continue;
    const v = latest(r.classes.classes["5xx"] ?? []);
    if (v !== undefined && (!worst || v > worst.v)) worst = { rule: r.rule, v };
  }
  return (
    <span className="t-secondary gw-line">
      {count} · {worst ? <>rule {worst.rule + 1} 5xx <span style={{ color: errColor(worst.v) }}>{fmtShare(worst.v)}</span></> : "no attributed samples"}
    </span>
  );
}

function RouteLane({ route, role, ruleMetrics, selected, onSelect }: { route: RouteNodeDTO; role: Role; ruleMetrics: RuleMetrics | null; selected: boolean; onSelect: () => void }) {
  const b = route.backends[0];
  const svc = b ? route.services.find((s) => s.namespace === b.namespace && s.name === b.name) : undefined;
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (selected) ref.current?.scrollIntoView?.({ block: "nearest" }); }, [selected]);
  return (
    <div role="listitem" className="gw-lane" data-role={role.kind}>
      <button ref={ref} className="gw-node gw-route" aria-pressed={selected} data-selected={selected ? "true" : undefined} onClick={onSelect}
        aria-label={`${routeKindOf(route)} ${route.namespace}/${route.name}${route.attachmentLabel ? ` via ${route.attachmentLabel}` : ""}: ${role.label}`}>
        <span style={{ display: "flex", alignItems: "center", gap: 6, minWidth: 0 }}>
          <VerdictMark health={role} size={8} />
          <span className="t-identifier gw-name"><span style={{ color: "var(--color-text-tertiary)" }}>{route.namespace}/</span>{route.name}</span>
        </span>
        <span className="t-secondary gw-line"><span data-testid="lane-kind">{routeKindOf(route)}</span> · {attachmentWhere(route)} · {laneMatchText(route)}</span>
        <span className="gw-line" style={{ color: ROLE_TEXT[role.kind], fontFamily: "var(--font-sans)" }}>{role.label}</span>
        {route.policies.length > 0 && <PolicyList policies={route.policies} />}
      </button>
      <span className="gw-edge" aria-hidden="true">›</span>
      <div className="gw-node">
        <span className="t-legend">{route.backends.length > 1 ? `primary backend · ${route.backends.length} total` : "backend"}</span>
        <span className="t-identifier gw-name">{b ? b.name : "none"}</span>
        <span className="t-secondary gw-line">{primaryBackendText(route)}</span>
        {svc && (svc.policies.length > 0 || svc.cnps.length > 0) && <PolicyList policies={[...svc.policies, ...svc.cnps]} />}
        {svc?.global && (
          <span className="t-secondary gw-line" title="Global service: also present on these fleet mesh peers. Live dataplane health is not checked.">
            ⇄ global{svc.meshClusters.length > 0 ? ` → ${svc.meshClusters.join(", ")}` : ""}{svc.meshUnconfirmed ? (svc.meshClusters.length === 0 ? " (peers unverified)" : " (+unverified)") : ""}
          </span>
        )}
      </div>
      <span className="gw-edge" aria-hidden="true">›</span>
      <div className="gw-node">
        <span className="t-legend">rules</span>
        <RuleSummary route={route} metrics={ruleMetrics} />
      </div>
    </div>
  );
}

function RouteInspector({ cluster, route, gone, unproven, gateway, listeners, notes, ruleMetrics: ruleM, metric, metricsObs, certLookup, certEvidence, certLastGood, certGap, nowS, onOpenObject, onClose }: {
  cluster: string;
  ruleMetrics: RuleMetrics | null;
  route: RouteNodeDTO;
  gone: boolean;
  unproven: boolean;
  gateway: GatewayNodeDTO;
  listeners: ListenerDTO[];
  notes: string[];
  metric: RouteMetricDTO | undefined;
  metricsObs: MetricsObs;
  certLookup: CertLookup;
  certEvidence: KindEvidence;
  certLastGood: string | null;
  certGap: string | null;
  nowS: number;
  onOpenObject: OpenObject;
  onClose: () => void;
}) {
  const kind = routeKindOf(route);
  const role = routeRole(route);
  // through a ListenerSet, the section names the ListenerSet's listener, not
  // one of the Gateway's: the Gateway's listeners are not the evidence
  const viaSet = route.parentRef?.kind === "ListenerSet";
  const laneListeners = viaSet ? [] : listeners;
  const issues = laneIssues(route, laneListeners);
  const gwRole = gatewayRole(gateway);
  const primary = route.backends[0];
  const carrying = route.backends.filter(carriesTraffic);
  const scoped = targetListeners(route, listeners);
  const policies = [...route.policies, ...route.services.flatMap((s) => s.policies)];
  const cnps = route.services.flatMap((s) => s.cnps);
  const services = route.backends.filter((b) => b.kind === "Service" && b.state !== "not-found");

  const more: MenuItem[] = [];
  if (services.length > 1) more.push({ kind: "choose", label: "Open backend Service", options: services.map((b) => ({ label: `${b.namespace}/${b.name}`, onSelect: () => onOpenObject(REFS.service, b.namespace, b.name) })) });
  const openablePolicies = [...policies, ...cnps].filter((p) => POLICY_REFS[p.kind]);
  if (openablePolicies.length > 0) more.push({ kind: "choose", label: "Open policy", options: openablePolicies.map((p) => ({ label: `${p.kind} ${p.namespace ? `${p.namespace}/` : ""}${p.name}`, onSelect: () => onOpenObject(POLICY_REFS[p.kind], p.namespace, p.name) })) });
  more.push({ label: "Copy route name", onSelect: () => void navigator.clipboard?.writeText(`${route.namespace}/${route.name}`) });
  // Describe (slice 14c, review 1): kubectl's report for this route, by its
  // UID; never for a route the latest read no longer holds, or cannot prove.
  const describe = useDescribe(cluster, !gone && !unproven ? { kind, namespace: route.namespace, name: route.name, uid: route.uid } : null);
  more.push(...describe.menu);

  return (
    <div data-testid="route-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout pin={{ cluster: cluster }} kind={kind} namespace={route.namespace} name={route.name}
        meta={`${attachmentWhere(route)}${route.hostnames.length > 0 ? ` · ${route.hostnames.join(", ")}` : ""}`}
        onClose={onClose}
        verdict={
          <div>
            {gone && <div role="status" className="t-body" style={{ marginBottom: 8 }}><span className="t-status">No longer attached.</span> The latest complete route lists did not include this attachment; this is its last evidence.</div>}
            {unproven && <div role="status" className="t-body" style={{ marginBottom: 8 }}><span className="t-status">Not in the current read.</span> A route list was incomplete, so its absence proves nothing; this is its last evidence.</div>}
            <InspectorStatus tone={roleTone(role.kind)} kind={role.kind} label={role.label} />
            <div className="verdict-evidence" style={{ display: "grid", gap: 4 }}>
              {issues.length > 0
                // the verdict's reasons read in ink-2; its mark carries the tone (slice 13c.2)
                ? issues.map((i) => <div key={i.short} className="t-body" data-severity={i.severity} style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{i.text}</div>)
                : (
                  <div className="t-body" style={{ color: "var(--color-text-secondary)" }}>
                    Accepted {viaSet ? `through ${route.attachmentLabel || "its ListenerSet"}` : "by this Gateway"} with references resolved; {carrying.length > 1 ? "every backend that carries traffic has ready endpoints" : <>the backend {carrying[0]?.name} has {carrying[0]?.endpoints ? `${carrying[0].endpoints.ready}/${carrying[0].endpoints.total} endpoints ready` : "endpoints"}</>}. Klyx reads status and endpoints - it does not test the path end to end.
                  </div>
                )}
              {RANK[gwRole.kind] <= RANK.degraded && <div className="t-body" data-severity={gwRole.kind} style={{ color: "var(--color-text-secondary)" }}>The Gateway itself is {gwRole.label}.</div>}
            </div>
          </div>
        }
        drawing={(
          <InspectorBlock title="Data path" count={route.rules?.length}>
            <DataPath gateway={gateway} route={route} listeners={listeners} metrics={ruleM} />
          </InspectorBlock>
        )}
        primary={gone ? undefined : { label: `Open ${kind}`, onSelect: () => onOpenObject(routeRef(kind, route.apiVersion), route.namespace, route.name) }}
        secondary={!gone && primary?.state === "resolved" ? { label: "Open Service", onSelect: () => onOpenObject(REFS.service, primary.namespace, primary.name) } : undefined}
        more={gone ? undefined : more}>
        {notes.length > 0 && (
          <InspectorBlock title="Read notes" count={notes.length}>
            {notes.map((n, i) => <div key={i} className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{n.charAt(0).toUpperCase() + n.slice(1)}.</div>)}
          </InspectorBlock>
        )}
        <AttachmentBlock route={route} />
        <MatchesBlock route={route} kind={kind} />
        <InspectorBlock title="Backends" count={route.backends.length}>
          <div className="insp-kv">
            {route.backends.map((b, i) => (
              <Fragment key={i}>
                <span>{b.kind}</span>
                <span>
                  {b.namespace}/{b.name}:{b.port}{b.weightSet || b.weight ? ` · weight ${b.weight}${carriesTraffic(b) ? "" : " (no traffic)"}` : ""}{i === 0 ? <span className="t-secondary"> (lane)</span> : null}
                  <span className="t-secondary" style={{ display: "block" }}>{backendEvidence(b)}</span>
                </span>
              </Fragment>
            ))}
          </div>
        </InspectorBlock>
        {isL4(kind) ? null : viaSet ? (
          <InspectorBlock title="Listener TLS">
            <div className="t-secondary">Attached through {route.attachmentLabel || "a ListenerSet"}: its listeners, and their certificates, are the ListenerSet's - see the ListenerSets view.</div>
          </InspectorBlock>
        ) : <InspectorBlock title="Listener TLS">
          {certGap && <div className="t-secondary" data-testid="cert-discovery-gap" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>cert-manager discovery re-check failing - {certGap}. A capability gap only - the Certificate read below keeps its own state.</div>}
          {scoped.length === 0 ? (
            <div className="t-body" style={{ color: "var(--color-text-warning)" }}>No listener on this Gateway matches the route's section.</div>
          ) : scoped.map((l) => {
            const refs = l.certificateRefs ?? [];
            return (
              <div key={l.name || `${l.protocol}:${l.port}`} style={{ display: "grid", gap: 2 }}>
                <span className="t-identifier">{listenerText(l)}{l.hostname ? ` · ${l.hostname}` : ""}</span>
                {refs.length === 0 ? <span className="t-secondary">No certificateRefs.</span> : refs.map((ref) => {
                  const cert = ref.kind === "Secret" ? certLookup.get(`${ref.namespace}/${ref.name}`) : undefined;
                  const st = ref.kind === "Secret" ? certStatus(cert, certEvidence, nowS, certLastGood) : { text: "not a Secret - not evaluated", color: "var(--color-text-tertiary)" };
                  return (
                    <span key={`${ref.kind}/${ref.namespace}/${ref.name}`} className="t-secondary" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap" }}>
                      <span className="t-identifier">{ref.kind} {ref.namespace}/{ref.name}</span>
                      <span style={{ color: st.color }}>{st.text}</span>
                      {ref.kind === "Secret" && <button className="l-btn" style={{ height: 20 }} onClick={() => onOpenObject(REFS.secret, ref.namespace, ref.name)}>Secret</button>}
                      {cert && <button className="l-btn" style={{ height: 20 }} onClick={() => onOpenObject(REFS.certificate, cert.namespace, cert.name)}>Certificate</button>}
                    </span>
                  );
                })}
              </div>
            );
          })}
        </InspectorBlock>}
        <InspectorBlock title="Observed traffic">
          {!httpMetricsApply(route) ? <div className="t-secondary" data-testid="no-http-metrics">{noHttpMetricsText(route)}.</div>
            : metricsObs.state === "unavailable" ? <div className="t-secondary">Unavailable{metricsObs.message ? ` - ${metricsObs.message}` : ""}.</div>
            : !metric ? <div className="t-secondary">{metricsObs.state === "reading" ? "Reading…" : "No traffic series for this route in the window."}</div>
            : (
              <>
                <div className="insp-kv">
                  <span>rate</span><span>{fmtRps(metric.rps)} rps</span>
                  <span>p50 / p99</span><span>{fmtMs(metric.p50)} / {fmtMs(metric.p99)}</span>
                  <span>5xx</span><span style={{ color: errColor(metric.errRate) }}>{fmtErr(metric.errRate)}</span>
                </div>
                <div className="t-secondary">
                  {metricsObs.state === "stale" ? `Last good read${metricsObs.at ? ` from ${absoluteTime(metricsObs.at)}` : ""} - the latest poll failed${metricsObs.message ? ` (${metricsObs.message})` : ""}. ` : metricsObs.at ? `5m rates, read ${absoluteTime(metricsObs.at)}. ` : ""}
                  p50/p99 are the worst rule's values; 5xx is tinted at 1% and 5% (Klyx's thresholds).
                </div>
              </>
            )}
        </InspectorBlock>
        <InspectorBlock title="Attached policies" count={policies.length}>
          {policies.length === 0 ? <div className="t-secondary">None on this route or its Services. Gateway-wide policies are on the canvas's gateway node.</div> : policies.map((p) => <PolicyDetail key={policyKey(p)} p={p} />)}
        </InspectorBlock>
        {cnps.length > 0 && (
          <InspectorBlock title="Inferred network policies" count={cnps.length}>
            {cnps.map((p) => <PolicyDetail key={policyKey(p)} p={p} inferred />)}
            <div className="t-secondary">Inferred from the Service's pod selector - Klyx's inference, not the controller's statement.</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
      {/* the sheet sits beside the layout, so it shows on any tab */}
      {describe.sheet}
    </div>
  );
}

// AttachmentBlock is the lane's attachment (S7b §5, §6): the reference, the
// controller judging it, other controllers' entries (shown, never judged),
// and Klyx's own evidence - which explains the status and never replaces it.
function AttachmentBlock({ route }: { route: RouteNodeDTO }) {
  const p = route.parentRef;
  const ev = route.evidence;
  if (!p && !ev) return null;
  const others = route.others ?? ev?.others ?? [];
  return (
    <InspectorBlock title="Attachment">
      <div className="insp-kv" data-testid="route-attachment-evidence">
        {p && <><span>parent</span><span className="t-identifier">{p.kind} {p.namespace}/{p.name}{p.sectionName ? ` · ${p.sectionName}` : ""}{p.port ? ` :${p.port}` : ""}</span></>}
        <span>controller</span><span>{route.controller ? <span className="t-identifier">{route.controller}</span> : <span style={{ color: "var(--color-text-warning)" }}>not established{route.controllerWhy ? ` - ${route.controllerWhy}` : ""}</span>}</span>
        {others.map((o, i) => <Fragment key={i}><span>{i === 0 ? "other entries" : ""}</span><span className="t-secondary">an entry from another controller ({o.controller || "unnamed"}), not judged: Accepted {o.accepted || "absent"}, ResolvedRefs {o.resolvedRefs || "absent"}</span></Fragment>)}
        {ev?.allowance.text && <><span>allowedRoutes</span><span className="t-secondary" style={{ color: ev.allowance.disagrees ? "var(--color-text-warning)" : undefined }}>{ev.allowance.text}</span></>}
        {(ev?.grants ?? []).map((g, i) => <Fragment key={`g${i}`}><span>{i === 0 ? "grants" : ""}</span><span className="t-secondary">rule {g.rule + 1} {g.backend}: {g.text}</span></Fragment>)}
        {(ev?.features ?? []).map((f, i) => <Fragment key={`f${i}`}><span>{i === 0 ? "features" : ""}</span><span className="t-secondary">{f}</span></Fragment>)}
        {(ev?.disagreements ?? []).map((d, i) => <Fragment key={`d${i}`}><span>{i === 0 ? "disagrees" : ""}</span><span style={{ color: "var(--color-text-warning)" }}>{d}</span></Fragment>)}
      </div>
    </InspectorBlock>
  );
}

// MatchesBlock says what the route matches in its kind's words (S7b §5).
function MatchesBlock({ route, kind }: { route: RouteNodeDTO; kind: string }) {
  if (isL4(kind)) {
    return (
      <InspectorBlock title="Matches">
        <div className="t-secondary">Every connection on the listener: a {kind} matches no fields.</div>
      </InspectorBlock>
    );
  }
  const hosts = route.hostnames.length > 0 ? route.hostnames.join(", ") : kind === "TLSRoute" ? "any SNI (from the listener)" : "any (from the listener)";
  const more = (route.hostnameCount ?? 0) > route.hostnames.length ? ` +${(route.hostnameCount ?? 0) - route.hostnames.length} more` : "";
  if (kind === "TLSRoute") {
    return (
      <InspectorBlock title="Matches">
        <div className="insp-kv"><span>SNI</span><span>{hosts}{more}</span></div>
      </InspectorBlock>
    );
  }
  return (
    <InspectorBlock title="Matches" count={route.matches.length}>
      <div className="insp-kv">
        {kind === "GRPCRoute"
          ? route.matches.map((m, i) => <Fragment key={i}><span>call</span><span>{m.grpcService || "any service"}/{m.grpcMethod || "any method"}</span></Fragment>)
          : route.matches.map((m, i) => <Fragment key={i}><span>{m.method || "any"}</span><span>{m.pathType} {m.pathValue}</span></Fragment>)}
        <span>hostnames</span><span>{hosts}{more}</span>
      </div>
    </InspectorBlock>
  );
}

function backendEvidence(b: RouteNodeDTO["backends"][number]): string {
  if (b.state === "not-found") return "Service not found";
  if (b.state === "no-port") return b.error || `no ${b.transport || "TCP"} port ${b.port}`;
  if (b.state === "unreadable") return `could not be read${b.error ? ` - ${b.error}` : ""}`;
  if (b.state === "unsupported" || b.kind !== "Service") return "not evaluated (Klyx reads Service backends only)";
  const ep = b.endpoints;
  if (!ep) return "endpoints not read";
  if (ep.error) return `endpoints unreadable - ${ep.error}`;
  return `${ep.ready}/${ep.total} endpoints ready${ep.nonPod > 0 ? ` · ${ep.nonPod} not backed by a pod` : ""}`;
}

function PolicyDetail({ p, inferred = false }: { p: PolicyRefDTO; inferred?: boolean }) {
  return (
    <div style={{ display: "grid", gap: 2 }}>
      <span className="t-identifier" style={{ fontWeight: 600, overflowWrap: "anywhere" }}>{p.kind} {p.namespace ? `${p.namespace}/` : ""}{p.name}</span>
      <span className="t-secondary">{inferred ? `Selects the pods behind Service ${p.targetNamespace}/${p.targetName}${p.match ? ` (via ${p.match})` : ""}` : `Target ${p.targetKind} ${p.targetNamespace}/${p.targetName}${p.targetSectionName ? ` section ${p.targetSectionName}` : ""}`}</span>
      {p.summary && <span className="t-secondary">Features: {p.summary}</span>}
      {p.details.map((d, i) => <span key={i} className="t-identifier" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{d.key}: {d.value}</span>)}
    </div>
  );
}

// failedRuleMetrics is a poll that failed: no metrics, the reason said.
function failedRuleMetrics(e: unknown): RuleMetrics {
  const message = e instanceof Error ? e.message : String(e);
  return { status: { available: false, message, updatedAt: "" }, scope: "", start: "", end: "", stepSeconds: 30, rateSeconds: 60, routes: [], rules: [], backendSplit: "", notes: [], error: message };
}
