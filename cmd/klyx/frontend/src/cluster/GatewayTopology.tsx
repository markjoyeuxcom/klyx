import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { IconAlertTriangle, IconRoute } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { GatewayNodeDTO, GatewayRef, ListenerDTO, PolicyRefDTO, ResourceRef, RouteMetricDTO, RouteNodeDTO, TopologyDTO } from "../store/fleet";
import { getGatewayTopology, getRouteMetrics } from "../bridge/gateway";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { UpdatedAt, absoluteTime } from "../chrome/Age";
import { useListKeys } from "../chrome/useListKeys";
import { VerdictLamp } from "../health/VerdictView";
import { InspectorStatus } from "./ResourceInspector";
import { LensLayout, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout, type MenuItem } from "../layout/InspectorLayout";
import {
  POLICY_ABBREV, POLICY_REFS, RANK, REFS, ROLE_TEXT, addressText, certBySecret, certStatus, errColor, fmtErr, fmtMs, fmtRps,
  gatewayHealth, listenerText, policyKey, roleTone, routeHasGlobal, routeHasPolicies, routeHealth, routeIssues, routeKey, sortWarnings,
  type CertLookup, type Role,
} from "./gatewayModel";

// GatewayTopology - one Gateway's data path on the canvas-detail template
// (spec §6 step 6). The canvas draws each HTTPRoute as a lane: route ->
// primary backend Service (+ its pods) -> live traffic; broken lanes first.
// The selected route's diagnosis lives in the inspector. The topology is a
// read with Refresh (route numbers poll every 20s); a failed refresh keeps
// the last good read, marked. Selection is the store's selectedRoute so the
// palette can deep-link a route.

type Mode = "all" | "issues" | "global" | "policies";
type OpenObject = (ref: ResourceRef, namespace: string, name: string) => void;

export function GatewayTopology({ cluster, gateway }: { cluster: string; gateway: GatewayRef }) {
  const net = useFleet((s) => s.network);
  const selectRoute = useFleet((s) => s.selectRoute);
  const closeGateway = useFleet((s) => s.closeGateway);
  const cm = useFleet((s) => s.certmanager);
  const [mode, setMode] = useState<Mode>("all");
  const [nsFilter, setNsFilter] = useState("");
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement>(null);

  const refresh = () => void getGatewayTopology(cluster, gateway).catch(() => { /* rendered from the store */ });
  useEffect(() => {
    refresh();
    return () => useFleet.getState().clearNetwork();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cluster, gateway.namespace, gateway.name]);

  const current = net.cluster === cluster && net.selected?.namespace === gateway.namespace && net.selected?.name === gateway.name;
  const t: TopologyDTO | null = current ? net.topology : null;
  const loading = current && net.topologyLoading;
  const readError = current ? net.topologyError ?? null : null;
  const routes = useMemo(() => t?.routes ?? [], [t]);
  const listeners = useMemo(() => t?.gateway.listeners ?? [], [t]);
  const certLookup = useMemo(() => certBySecret(cm.cluster === cluster ? cm.certificates : []), [cm, cluster]);
  const metrics = current ? net.routeMetrics : {};
  const warnings = useMemo(() => sortWarnings(t?.warnings), [t]);

  // route numbers poll while the topology is open; the structure does not
  const routeKeysJoined = routes.map(routeKey).join(",");
  useEffect(() => {
    if (!routeKeysJoined) return;
    const keys = routeKeysJoined.split(",");
    const tick = () => void getRouteMetrics(cluster, gateway.namespace, gateway.name, keys).catch(() => { /* status carries it */ });
    tick();
    const id = setInterval(tick, 20000);
    return () => clearInterval(id);
  }, [cluster, gateway.namespace, gateway.name, routeKeysJoined]);

  const healthOf = useMemo(() => new Map(routes.map((r) => [routeKey(r), routeHealth(r, listeners)])), [routes, listeners]);
  const nsCounts = useMemo(() => {
    const m = new Map<string, number>();
    for (const r of routes) m.set(r.namespace, (m.get(r.namespace) ?? 0) + 1);
    return m;
  }, [routes]);
  const namespaces = [...nsCounts.keys()].sort();
  const issueCount = routes.filter((r) => RANK[healthOf.get(routeKey(r))!.kind] <= RANK.degraded).length;
  const brokenCount = routes.filter((r) => healthOf.get(routeKey(r))!.kind === "fault").length;
  const globalCount = routes.filter(routeHasGlobal).length;
  const policyCount = routes.filter(routeHasPolicies).length;

  const q = query.trim().toLowerCase();
  const visible = useMemo(() => routes
    .filter((r) => {
      if (nsFilter && r.namespace !== nsFilter) return false;
      if (mode === "issues" && RANK[healthOf.get(routeKey(r))!.kind] > RANK.degraded) return false;
      if (mode === "global" && !routeHasGlobal(r)) return false;
      if (mode === "policies" && !routeHasPolicies(r)) return false;
      if (!q) return true;
      return [r.name, r.namespace, ...r.hostnames, ...r.services.map((s) => s.name)].some((v) => v.toLowerCase().includes(q));
    })
    // broken lanes first; the API order holds within a tier (stable sort)
    .sort((a, b) => RANK[healthOf.get(routeKey(a))!.kind] - RANK[healthOf.get(routeKey(b))!.kind]),
  [routes, nsFilter, mode, q, healthOf]);

  const selKey = current ? net.selectedRoute : null;
  const selected = selKey ? routes.find((r) => routeKey(r) === selKey) : undefined;

  // Selections this view made itself; any other key arrived by navigation
  // (the palette) and must be revealed - clear filters once, after the
  // topology holding it has arrived.
  const ownSelect = useRef<string | null>(null);
  const pick = (k: string | null) => { ownSelect.current = k; selectRoute(k); };
  useEffect(() => {
    if (!selKey || selKey === ownSelect.current || !selected) return;
    ownSelect.current = selKey;
    setMode("all"); setNsFilter(""); setQuery("");
  }, [selKey, selected]);

  // First fresh read: select the first broken lane once (no focus theft).
  const autoDone = useRef(false);
  useEffect(() => {
    if (autoDone.current || !t || loading) return;
    autoDone.current = true;
    if (selKey) return;
    const first = visible[0];
    if (first && healthOf.get(routeKey(first))!.kind === "fault") pick(routeKey(first));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [t, loading]);

  // Later operator filter edits win: one that hides the selection closes it.
  const filterEdits = useRef(0);
  useEffect(() => {
    if (filterEdits.current++ === 0) return;
    if (selected && !visible.includes(selected)) pick(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, nsFilter, q]);

  // A route that left a complete read is named as gone, with its last
  // evidence; a partial read (t.error) proves nothing about absence.
  const lastRoute = useRef<RouteNodeDTO | null>(null);
  if (selected) lastRoute.current = selected;
  if (lastRoute.current && routeKey(lastRoute.current) !== selKey) lastRoute.current = null;
  const missing = !!selKey && !selected && !!t && !loading;
  const inspected = selected ?? (missing ? lastRoute.current : null);

  const selIdx = selected ? visible.indexOf(selected) : -1;
  useListKeys({
    count: visible.length,
    selected: selIdx,
    onSelect: (i) => { const r = visible[i]; if (r) pick(routeKey(r)); },
    onActivate: (i) => { const r = visible[i]; if (r) pick(routeKey(r)); },
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
  const clearFilters = () => { setMode("all"); setNsFilter(""); setQuery(""); };
  const gwRole = t ? gatewayHealth(t.gateway) : null;
  const stateView = state === "loading" ? <SkeletonRows rows={5} label="reading the Gateway topology" />
    : state === "unreadable" ? <EmptyState icon={icon} title="Could not read this Gateway's topology." hint={readError ?? undefined} action={{ label: "Retry", onClick: refresh }} />
    : state === "empty" ? (
      <div style={{ width: "100%", maxWidth: 720, alignSelf: "stretch" }}>
        {t && <GatewayStrip g={t.gateway} clusterPolicies={t.clusterPolicies ?? []} absentKinds={warnings.absentKinds} />}
        <EmptyState icon={icon} title="No HTTPRoutes are attached to this Gateway." hint={t?.error ? "The read was partial - see the notice above." : "Routes attach through spec.parentRefs; nothing references this Gateway."} />
      </div>
    )
    : <EmptyState icon={icon} title="No routes match the current filters." hint={`${routes.length} routes on this Gateway.`} action={{ label: "Clear filters", onClick: clearFilters }} />;

  const notices = [
    readError && t ? (
      <div key="stale" role="status" className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>
        <span className="t-status">Refresh failed - showing the read from {net.topologyAt ? absoluteTime(net.topologyAt) : "earlier"}.</span> {readError}
      </div>
    ) : null,
    t?.error ? (
      <div key="partial" role="status" className="t-body" style={{ color: "var(--color-text-danger)", overflowWrap: "anywhere" }}>
        <span className="t-status">▲ Partial read.</span> {t.error}
      </div>
    ) : null,
    gwRole && RANK[gwRole.kind] <= RANK.degraded ? (
      <div key="gw" role="status" className="t-body" style={{ color: ROLE_TEXT[gwRole.kind], overflowWrap: "anywhere" }}>
        <span className="t-status">{gwRole.kind === "fault" ? "▲ " : ""}Gateway {gwRole.label}.</span> {gwRole.kind === "fault" ? "Its controller has not accepted it - every lane below is unserved until it does." : "No data path is programmed yet - the lanes below describe intent, not live traffic."}
      </div>
    ) : null,
    ...warnings.gaps.map((w, i) => (
      <div key={`w${i}`} className="t-body" style={{ color: "var(--color-text-warning)", display: "flex", gap: 6, alignItems: "baseline", overflowWrap: "anywhere" }}>
        <IconAlertTriangle size={12} stroke={1.7} style={{ flexShrink: 0, alignSelf: "center" }} /> {w}
      </div>
    )),
  ].filter(Boolean);

  const rmStatus = current ? net.routeMetricsStatus : null;
  const metricsCaption = !rmStatus ? "route traffic: reading…"
    : !rmStatus.available && net.routeMetricsStale ? `route traffic: last good read - the latest failed${rmStatus.message ? ` (${rmStatus.message})` : ""}`
    : !rmStatus.available ? `route traffic unavailable: ${rmStatus.message || "no metrics source"}`
    : rmStatus.message ? `route traffic: ${rmStatus.message}`
    : "route traffic · 5m rates";

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
      summary={!t ? null : (
        <>
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{routes.length}</span>
          <span className="t-secondary">HTTPRoute{routes.length === 1 ? "" : "s"}{t.error ? " (partial read)" : ""}</span>
          {issueCount > 0
            ? <span className="t-status" style={{ color: brokenCount > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{brokenCount > 0 ? `▲ ${brokenCount} broken` : ""}{brokenCount > 0 && issueCount > brokenCount ? " · " : ""}{issueCount > brokenCount ? `${issueCount - brokenCount} degraded` : ""}</span>
            : routes.length > 0 && <span className="t-secondary">every path usable</span>}
        </>
      )}
      headerEnd={
        <>
          <span className="t-secondary" title="Read on demand - not a live watch. Route traffic refreshes every 20s.">snapshot</span>
          <UpdatedAt at={current && net.topologyAt ? net.topologyAt : null} />
          <button className="l-btn" onClick={refresh} disabled={loading}>{loading ? "Reading…" : "Refresh"}</button>
        </>
      }
      controls={t && routes.length > 0 ? (
        <>
          <div role="group" aria-label="Route filter" style={{ display: "inline-flex", gap: 4 }}>
            <button className="l-chip" aria-pressed={mode === "all"} onClick={() => setMode("all")}>All {routes.length}</button>
            <button className="l-chip" aria-pressed={mode === "issues"} onClick={() => setMode("issues")}>Issues {issueCount > 0 && <span style={{ color: brokenCount > 0 ? "var(--color-text-danger)" : "var(--color-text-warning)" }}>{issueCount}</span>}</button>
            {globalCount > 0 && <button className="l-chip" aria-pressed={mode === "global"} onClick={() => setMode("global")}>Global {globalCount}</button>}
            {policyCount > 0 && <button className="l-chip" aria-pressed={mode === "policies"} onClick={() => setMode("policies")}>With policies {policyCount}</button>}
          </div>
          {namespaces.length > 1 && (
            <label style={{ display: "flex" }}>
              <span className="sr-only">Route namespace</span>
              <select className="l-input" value={nsFilter} onChange={(e) => setNsFilter(e.target.value)}>
                <option value="">All namespaces</option>
                {namespaces.map((ns) => <option key={ns} value={ns}>{ns} ({nsCounts.get(ns)})</option>)}
              </select>
            </label>
          )}
          <label style={{ display: "flex", flex: "0 1 260px", minWidth: 140, marginLeft: "auto" }}>
            <span className="sr-only">Search routes</span>
            <input ref={searchRef} className="l-input" style={{ width: "100%" }} type="search" autoCapitalize="off" autoCorrect="off" spellCheck={false}
              value={query} onChange={(e) => setQuery(e.target.value)} placeholder="Search route, hostname, service" />
          </label>
        </>
      ) : undefined}
      notice={notices.length > 0 ? <div style={{ display: "grid", gap: 2, padding: "4px 0" }}>{notices}</div> : undefined}
      state={state}
      stateView={stateView}
      body={t ? (
        <div className="lens-canvas" data-testid="gateway-canvas">
          <GatewayStrip g={t.gateway} clusterPolicies={t.clusterPolicies ?? []} absentKinds={warnings.absentKinds} />
          <div role="list" aria-label="HTTPRoute lanes" style={{ display: "grid", gap: 8 }}>
            {visible.map((r) => (
              <RouteLane key={routeKey(r)} route={r} role={healthOf.get(routeKey(r))!} metric={metrics[routeKey(r)]} metricsOff={!!rmStatus && !rmStatus.available && !net.routeMetricsStale}
                selected={routeKey(r) === selKey} onSelect={() => pick(routeKey(r) === selKey ? null : routeKey(r))} />
            ))}
          </div>
        </div>
      ) : null}
      footer={<span className="t-legend">j k select · esc close · / search</span>}
      footerEnd={<span className="t-secondary">{visible.length !== routes.length ? `${visible.length} of ${routes.length} routes · ` : ""}broken first · {metricsCaption}</span>}
      inspector={selKey && inspected && t ? (
        <RouteInspector key={selKey} route={inspected} gone={missing && !t.error} unproven={missing && !!t.error} listeners={listeners} notes={warnings.byRoute.get(selKey) ?? []}
          metric={metrics[selKey]} certLookup={certLookup} certsLoaded={cm.cluster === cluster} onOpenObject={openObject} onClose={() => pick(null)} />
      ) : undefined}
      inspectorLabel="Route details"
      onCloseInspector={() => pick(null)}
    />
  );
}

// GatewayStrip: the canvas's first node - the Gateway itself, with the
// listeners every lane enters through and the policies that apply to all.
function GatewayStrip({ g, clusterPolicies, absentKinds }: { g: GatewayNodeDTO; clusterPolicies: PolicyRefDTO[]; absentKinds: string[] }) {
  const role = gatewayHealth(g);
  const address = addressText(g.addresses);
  return (
    <div className="gw-node gw-gateway" data-testid="gateway-strip">
      <div style={{ display: "flex", alignItems: "center", gap: 8, minWidth: 0 }}>
        <Lamp role={role} />
        <span className="t-legend">gateway</span>
        <span className="t-identifier" style={{ fontWeight: 600 }}>{g.name}</span>
        <span className="t-secondary">{g.className}</span>
        <span className="t-status" style={{ color: ROLE_TEXT[role.kind], marginLeft: "auto" }}>{role.kind === "fault" ? "▲ " : ""}{role.label}</span>
      </div>
      <div className="gw-facts">
        <span>address</span><span style={{ color: address ? undefined : "var(--color-text-tertiary)" }}>{address || "none assigned"}</span>
        <span>listeners</span><span>{g.listeners.length === 0 ? <span className="t-secondary">none</span> : g.listeners.map((l) => `${listenerText(l)}${l.hostname ? ` (${l.hostname})` : ""}`).join(" · ")}</span>
        {g.policies.length > 0 && <><span>policies</span><span><PolicyList policies={g.policies} /></span></>}
        {clusterPolicies.length > 0 && <><span>cluster-wide</span><span><PolicyList policies={clusterPolicies} /></span></>}
        {absentKinds.length > 0 && <><span>not installed</span><span className="t-secondary" style={{ fontFamily: "var(--font-sans)" }}>{absentKinds.join(", ")} - no such policies can apply here</span></>}
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

function Lamp({ role }: { role: Role }) {
  return <span role="img" aria-label={role.label} style={{ display: "inline-flex", flexShrink: 0 }}><VerdictLamp health={role} size={8} /></span>;
}

function RouteLane({ route, role, metric, metricsOff, selected, onSelect }: { route: RouteNodeDTO; role: Role; metric: RouteMetricDTO | undefined; metricsOff: boolean; selected: boolean; onSelect: () => void }) {
  const svc = route.services[0];
  const ref = useRef<HTMLButtonElement>(null);
  useEffect(() => { if (selected) ref.current?.scrollIntoView?.({ block: "nearest" }); }, [selected]);
  const servesTraffic = role.kind !== "fault" && !!svc?.resolved;
  return (
    <div role="listitem" className="gw-lane" data-role={role.kind}>
      <button ref={ref} className="gw-node gw-route" aria-pressed={selected} data-selected={selected ? "true" : undefined} onClick={onSelect}
        aria-label={`HTTPRoute ${route.namespace}/${route.name}: ${role.label}`}>
        <span style={{ display: "flex", alignItems: "center", gap: 6, minWidth: 0 }}>
          <Lamp role={role} />
          <span className="t-identifier gw-name"><span style={{ color: "var(--color-text-tertiary)" }}>{route.namespace}/</span>{route.name}</span>
        </span>
        <span className="t-secondary gw-line">{route.parentSectionName ? `listener ${route.parentSectionName}` : "all listeners"} · {route.hostnames[0] ?? "any host"}{route.hostnames.length > 1 ? ` +${route.hostnames.length - 1}` : ""} · {route.matches[0]?.pathValue ?? "/"}{route.matches.length > 1 ? ` +${route.matches.length - 1}` : ""}</span>
        <span className="gw-line" style={{ color: ROLE_TEXT[role.kind], fontWeight: role.kind === "fault" ? 600 : 400 }}>{role.kind === "fault" ? "▲ " : ""}{role.label}</span>
        {route.policies.length > 0 && <PolicyList policies={route.policies} />}
      </button>
      <span className="gw-edge" aria-hidden="true">›</span>
      <div className="gw-node">
        <span className="t-legend">service</span>
        <span className="t-identifier gw-name">{svc ? svc.name : "none"}{route.backends.length > 1 ? <span className="t-secondary"> +{route.backends.length - 1}</span> : null}</span>
        <span className="t-secondary gw-line">
          {!svc ? "no backend" : !svc.resolved ? "not found" : `${svc.type} :${svc.port}`}
          {svc?.resolved && <> · pods {route.pods.unknown ? "unknown" : `${route.pods.ready}/${route.pods.total}`}</>}
        </span>
        {svc && (svc.policies.length > 0 || svc.cnps.length > 0) && <PolicyList policies={[...svc.policies, ...svc.cnps]} />}
        {svc?.global && (
          <span className="t-secondary gw-line" title="Global service: also present on these fleet mesh peers. Live dataplane health is not checked.">
            ⇄ global{svc.meshClusters.length > 0 ? ` → ${svc.meshClusters.join(", ")}` : ""}{svc.meshUnconfirmed ? (svc.meshClusters.length === 0 ? " (peers unverified)" : " (+unverified)") : ""}
          </span>
        )}
      </div>
      <span className="gw-edge" aria-hidden="true">›</span>
      <div className="gw-node">
        <span className="t-legend">traffic</span>
        {servesTraffic && metricsOff ? <span className="t-secondary">no metrics source</span> : servesTraffic ? (
          <>
            <span className="t-identifier">{fmtRps(metric?.rps ?? null)} rps · p99 {fmtMs(metric?.p99 ?? null)}</span>
            <span className="t-secondary gw-line">p50 {fmtMs(metric?.p50 ?? null)} · <span style={{ color: errColor(metric?.errRate ?? null) }}>5xx {fmtErr(metric?.errRate ?? null)}</span></span>
          </>
        ) : <span className="t-secondary">no data path</span>}
      </div>
    </div>
  );
}

function RouteInspector({ route, gone, unproven, listeners, notes, metric, certLookup, certsLoaded, onOpenObject, onClose }: {
  route: RouteNodeDTO;
  gone: boolean;
  unproven: boolean;
  listeners: ListenerDTO[];
  notes: string[];
  metric: RouteMetricDTO | undefined;
  certLookup: CertLookup;
  certsLoaded: boolean;
  onOpenObject: OpenObject;
  onClose: () => void;
}) {
  const role = routeHealth(route, listeners);
  const issues = routeIssues(route, listeners);
  const svc = route.services[0];
  const scoped = route.parentSectionName ? listeners.filter((l) => l.name === route.parentSectionName) : listeners;
  const policies = [...route.policies, ...route.services.flatMap((s) => s.policies)];
  const cnps = route.services.flatMap((s) => s.cnps);
  const services = route.backends.filter((b) => b.kind === "Service");

  const more: MenuItem[] = [];
  if (services.length > 1) more.push({ kind: "choose", label: "Open backend Service", options: services.map((b) => ({ label: `${b.namespace}/${b.name}`, onSelect: () => onOpenObject(REFS.service, b.namespace, b.name) })) });
  const openablePolicies = [...policies, ...cnps].filter((p) => POLICY_REFS[p.kind]);
  if (openablePolicies.length > 0) more.push({ kind: "choose", label: "Open policy", options: openablePolicies.map((p) => ({ label: `${p.kind} ${p.namespace ? `${p.namespace}/` : ""}${p.name}`, onSelect: () => onOpenObject(POLICY_REFS[p.kind], p.namespace, p.name) })) });
  more.push({ label: "Copy route name", onSelect: () => void navigator.clipboard?.writeText(`${route.namespace}/${route.name}`) });

  return (
    <div data-testid="route-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout kind="HTTPRoute" namespace={route.namespace} name={route.name}
        meta={`${route.parentSectionName ? `listener ${route.parentSectionName}` : "all listeners"}${route.hostnames.length > 0 ? ` · ${route.hostnames.join(", ")}` : ""}`}
        onClose={onClose}
        verdict={
          <div>
            {gone && <div role="status" className="t-body" style={{ marginBottom: 8 }}><span className="t-status">No longer attached.</span> The last complete read of this Gateway did not include it; this is its last evidence.</div>}
            {unproven && <div role="status" className="t-body" style={{ marginBottom: 8 }}><span className="t-status">Not in the current partial read.</span> Its absence proves nothing; this is its last evidence.</div>}
            <InspectorStatus tone={roleTone(role.kind)} label={role.label} />
            {issues.length > 0 ? (
              <div style={{ display: "grid", gap: 4, marginTop: -4 }}>
                {issues.map((i) => <div key={i.short} className="t-body" style={{ color: ROLE_TEXT[i.severity], overflowWrap: "anywhere" }}>{i.text}</div>)}
              </div>
            ) : role.kind === "unknown" ? (
              <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: -4 }}>Attached and resolved, but the backend's endpoints could not be read - readiness is unverified.</div>
            ) : (
              <div className="t-body" style={{ color: "var(--color-text-secondary)", marginTop: -4 }}>Attachment, references, backend Service and endpoints are usable.</div>
            )}
          </div>
        }
        primary={gone ? undefined : { label: "Open HTTPRoute", onSelect: () => onOpenObject(REFS.httpRoute, route.namespace, route.name) }}
        secondary={!gone && svc?.resolved ? { label: "Open Service", onSelect: () => onOpenObject(REFS.service, svc.namespace, svc.name) } : undefined}
        more={gone ? undefined : more}>
        {notes.length > 0 && (
          <InspectorBlock title="Read notes" count={notes.length}>
            {notes.map((n, i) => <div key={i} className="t-body" style={{ color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{n.charAt(0).toUpperCase() + n.slice(1)}.</div>)}
          </InspectorBlock>
        )}
        <InspectorBlock title="Matches" count={route.matches.length}>
          <div className="insp-kv">
            {route.matches.map((m, i) => (
              <Fragment key={i}><span>{m.method || "any"}</span><span>{m.pathType} {m.pathValue}</span></Fragment>
            ))}
            <span>hostnames</span><span>{route.hostnames.length > 0 ? route.hostnames.join(", ") : "any (from the listener)"}</span>
          </div>
        </InspectorBlock>
        <InspectorBlock title="Backends" count={route.backends.length}>
          <div className="insp-kv">
            {route.backends.map((b, i) => (
              <Fragment key={i}>
                <span>{b.kind}</span>
                <span>{b.namespace}/{b.name}:{b.port}{b.weight ? ` · weight ${b.weight}` : ""}{i === 0 ? <span className="t-secondary"> (lane)</span> : null}</span>
              </Fragment>
            ))}
          </div>
          {svc && (
            <div className="t-secondary">Primary: {svc.resolved ? `${svc.type} :${svc.port}` : "Service not found"}{svc.resolved ? ` · pods ${route.pods.unknown ? "unknown" : `${route.pods.ready}/${route.pods.total} ready`}` : ""}</div>
          )}
        </InspectorBlock>
        <InspectorBlock title="Listener TLS">
          {scoped.length === 0 ? (
            <div className="t-body" style={{ color: "var(--color-text-warning)" }}>No listener on this Gateway matches the route's section.</div>
          ) : scoped.map((l) => {
            const refs = l.certificateRefs ?? [];
            return (
              <div key={l.name || `${l.protocol}:${l.port}`} style={{ display: "grid", gap: 2 }}>
                <span className="t-identifier">{listenerText(l)}{l.hostname ? ` · ${l.hostname}` : ""}</span>
                {refs.length === 0 ? <span className="t-secondary">No certificateRefs.</span> : refs.map((ref) => {
                  const cert = ref.kind === "Secret" ? certLookup.get(`${ref.namespace}/${ref.name}`) : undefined;
                  const st = certsLoaded ? certStatus(cert) : { text: "cert-manager state not loaded", color: "var(--color-text-tertiary)" };
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
        </InspectorBlock>
        <InspectorBlock title="Traffic">
          {role.kind === "fault" || !svc?.resolved ? <div className="t-secondary">No data path - nothing to measure.</div> : (
            <>
              <div className="insp-kv">
                <span>rate</span><span>{fmtRps(metric?.rps ?? null)} rps</span>
                <span>p50 / p99</span><span>{fmtMs(metric?.p50 ?? null)} / {fmtMs(metric?.p99 ?? null)}</span>
                <span>5xx</span><span style={{ color: errColor(metric?.errRate ?? null) }}>{fmtErr(metric?.errRate ?? null)}</span>
              </div>
              <div className="t-secondary">p50/p99 are the worst rule's values. 5xx is tinted at 1% and 5% - Klyx's thresholds.</div>
            </>
          )}
        </InspectorBlock>
        <InspectorBlock title="Attached policies" count={policies.length}>
          {policies.length === 0 ? <div className="t-secondary">None on this route or its Service. Gateway-wide policies are on the canvas's gateway node.</div> : policies.map((p) => <PolicyDetail key={policyKey(p)} p={p} />)}
        </InspectorBlock>
        {cnps.length > 0 && (
          <InspectorBlock title="Inferred network policies" count={cnps.length}>
            {cnps.map((p) => <PolicyDetail key={policyKey(p)} p={p} inferred />)}
            <div className="t-secondary">Inferred from the Service's pod selector - Klyx's inference, not the controller's statement.</div>
          </InspectorBlock>
        )}
      </InspectorLayout>
    </div>
  );
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
