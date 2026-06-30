import { useEffect, useMemo, useState } from "react";
import { useFleet, GatewayRef, RouteNodeDTO, RouteMetricDTO } from "../store/fleet";
import type { CertificateDTO, ListenerDTO, PolicyRefDTO, ResourceRef } from "../store/fleet";
import { getGatewayTopology, getRouteMetrics } from "../bridge/gateway";
import { PolicyChip } from "./PolicyChip";

const ellipsis: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" };
const nb: React.CSSProperties = { background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", padding: "8px 9px", minWidth: 0 };
const lab: React.CSSProperties = { fontSize: 9, letterSpacing: 0.4, textTransform: "uppercase", color: "var(--color-text-tertiary)", marginBottom: 3 };
const nm: React.CSSProperties = { fontSize: 11, fontWeight: 600, fontFamily: "var(--font-mono)", ...ellipsis };
const chev: React.CSSProperties = { display: "flex", alignItems: "center", justifyContent: "center", color: "var(--color-text-tertiary)" };

const routeKey = (r: { namespace: string; name: string }) => `${r.namespace}/${r.name}`;
type RouteMode = "all" | "issues" | "global" | "policies";
type CertLookup = Map<string, CertificateDTO>;
type OpenObject = (ref: ResourceRef, namespace: string, name: string) => void;

const httpRouteRef: ResourceRef = { group: "gateway.networking.k8s.io", version: "v1", plural: "httproutes", kind: "HTTPRoute", scope: "Namespaced" };
const serviceRef: ResourceRef = { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" };
const secretRef: ResourceRef = { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" };
const certificateRef: ResourceRef = { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" };

const policyRefs: Record<string, ResourceRef> = {
  BackendTLSPolicy: { group: "gateway.networking.k8s.io", version: "v1alpha3", plural: "backendtlspolicies", kind: "BackendTLSPolicy", scope: "Namespaced" },
  ClientTrafficPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "clienttrafficpolicies", kind: "ClientTrafficPolicy", scope: "Namespaced" },
  BackendTrafficPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "backendtrafficpolicies", kind: "BackendTrafficPolicy", scope: "Namespaced" },
  SecurityPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "securitypolicies", kind: "SecurityPolicy", scope: "Namespaced" },
  EnvoyExtensionPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "envoyextensionpolicies", kind: "EnvoyExtensionPolicy", scope: "Namespaced" },
  CiliumNetworkPolicy: { group: "cilium.io", version: "v2", plural: "ciliumnetworkpolicies", kind: "CiliumNetworkPolicy", scope: "Namespaced" },
  CiliumClusterwideNetworkPolicy: { group: "cilium.io", version: "v2", plural: "ciliumclusterwidenetworkpolicies", kind: "CiliumClusterwideNetworkPolicy", scope: "Cluster" },
};

// laneRank orders lanes for the topology: 0 = broken (rejected, unresolved
// refs, or no backend at all), 1 = healthy. Sort is stable, so within each
// tier the API order is preserved.
function laneRank(r: RouteNodeDTO, listeners: ListenerDTO[]): number {
  return routeBroken(r, listeners) ? 0 : 1;
}
const dot = (ok: boolean) => (ok ? "var(--color-text-success)" : "var(--color-text-danger)");

function routeBroken(r: RouteNodeDTO, listeners: ListenerDTO[] = []): boolean {
  return routeIssueReasons(r, listeners).length > 0;
}

function routeIssueReasons(r: RouteNodeDTO, listeners: ListenerDTO[] = []): string[] {
  const svc = r.services[0];
  const issues: string[] = [];
  const listenerNames = new Set(listeners.map((l) => l.name).filter(Boolean));
  if (!r.accepted) issues.push("not accepted by Gateway");
  if (!r.resolvedRefs) issues.push("unresolved refs - check backendRef, ReferenceGrant, or listener section");
  if (r.parentSectionName && listenerNames.size > 0 && !listenerNames.has(r.parentSectionName)) issues.push(`listener ${r.parentSectionName} not found`);
  if (!svc) issues.push("no backend service");
  else if (!svc.resolved) issues.push(`backend Service ${svc.namespace}/${svc.name} not found`);
  else if (!r.pods.unknown && r.pods.total === 0) issues.push("backend has no ready endpoints");
  else if (!r.pods.unknown && r.pods.ready < r.pods.total) issues.push(`backend pods ${r.pods.ready}/${r.pods.total} ready`);
  return issues;
}

function routeHasGlobal(r: RouteNodeDTO): boolean {
  return r.services.some((svc) => svc.global);
}

function routeHasPolicies(r: RouteNodeDTO): boolean {
  return r.policies.length > 0 || r.services.some((svc) => svc.policies.length > 0 || svc.cnps.length > 0);
}

function routePolicyRefCount(r: RouteNodeDTO): number {
  return r.policies.length + r.services.reduce((n, svc) => n + svc.policies.length + svc.cnps.length, 0);
}

function tlsRefCount(listeners: ListenerDTO[]): number {
  return listeners.reduce((n, l) => n + (l.certificateRefs?.length ?? 0), 0);
}

function certKey(ns: string, name: string): string {
  return `${ns}/${name}`;
}

function certBySecret(certs: CertificateDTO[]): CertLookup {
  const out: CertLookup = new Map();
  for (const c of certs) {
    if (c.secretName) out.set(certKey(c.namespace, c.secretName), c);
  }
  return out;
}

function certStatus(c: CertificateDTO | undefined): { text: string; color: string } {
  if (!c) return { text: "no cert-manager match", color: "var(--color-text-tertiary)" };
  if (c.ready === "Ready") return { text: "cert ready", color: "var(--color-text-success)" };
  if (c.ready === "Issuing") return { text: "cert issuing", color: "var(--color-text-info)" };
  return { text: `cert ${c.ready.toLowerCase() || "unknown"}`, color: "var(--color-text-warning)" };
}

function openBtn(): React.CSSProperties {
  return {
    padding: "1px 6px",
    borderRadius: 4,
    border: "0.5px solid var(--color-border-tertiary)",
    background: "var(--color-background-secondary)",
    color: "var(--color-text-info)",
    fontSize: 10,
    fontFamily: "var(--font-mono)",
    cursor: "pointer",
    whiteSpace: "nowrap",
  };
}

function gatewayAddress(t: { gateway: { addresses?: { value: string }[] } }): string {
  const values = (t.gateway.addresses ?? []).map((a) => a.value).filter(Boolean);
  return values.length > 0 ? values.join(", ") : "—";
}

// Above this many namespaces the inline chips would wrap into a wall and shove
// the lanes off-screen, so we fall back to a native <select>. A proper
// searchable combobox primitive replaces this select in a later slice.
const NS_CHIP_LIMIT = 8;

export function GatewayTopology({ cluster, gateway }: { cluster: string; gateway: GatewayRef }) {
  const net = useFleet((s) => s.network);
  const selectRoute = useFleet((s) => s.selectRoute);
  const openResource = useFleet((s) => s.openResource);
  const openInstance = useFleet((s) => s.openInstance);
  const routeMetrics = useFleet((s) => s.network.routeMetrics);
  const rmStatus = useFleet((s) => s.network.routeMetricsStatus);
  const rmStale = useFleet((s) => s.network.routeMetricsStale);
  const cm = useFleet((s) => s.certmanager);
  const [nsFilter, setNsFilter] = useState<string | null>(null);
  const [routeQuery, setRouteQuery] = useState("");
  const [routeMode, setRouteMode] = useState<RouteMode>("all");

  useEffect(() => {
    void getGatewayTopology(cluster, gateway);
    return () => useFleet.getState().clearNetwork();
  }, [cluster, gateway.namespace, gateway.name]);

  const isCurrent = net.selected && net.selected.namespace === gateway.namespace && net.selected.name === gateway.name;
  const t = isCurrent ? net.topology : null;
  const certs = cm.cluster === cluster ? cm.certificates : [];
  const certLookup = useMemo(() => certBySecret(certs), [certs]);
  const openObject: OpenObject = (ref, namespace, name) => {
    openResource(ref);
    openInstance(namespace, name);
  };

  // Poll the live route metrics (~20s) while a topology is open. The structural
  // topology is static (fetched on gateway-select); only these numbers poll.
  const routeKeysJoined = (t?.routes ?? []).map(routeKey).join(",");
  useEffect(() => {
    if (!cluster || !routeKeysJoined) return;
    const keys = routeKeysJoined.split(",");
    let alive = true;
    const tick = () => {
      if (alive && t) void getRouteMetrics(cluster, t.gateway.namespace, t.gateway.name, keys);
    };
    tick();
    const id = setInterval(tick, 20000);
    return () => {
      alive = false;
      clearInterval(id);
    };
  }, [cluster, routeKeysJoined]);

  if (net.topologyLoading && !t) return <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>Loading topology…</div>;
  if (!t) return <div style={{ padding: 24, color: "var(--color-text-secondary)", fontSize: 13 }}>Could not load the topology.</div>;

  // Namespace filter: a busy shared Gateway aggregates HTTPRoutes from many app
  // namespaces. The filter is opt-in (default "All" = the full data path) and only
  // appears when routes actually span more than one namespace.
  const nsCounts = new Map<string, number>();
  for (const r of t.routes) nsCounts.set(r.namespace, (nsCounts.get(r.namespace) ?? 0) + 1);
  const namespaces = [...nsCounts.keys()].sort();
  const showFilter = namespaces.length > 1;
  const activeNs = showFilter && nsFilter && nsCounts.has(nsFilter) ? nsFilter : null;
  const nsRoutes = activeNs ? t.routes.filter((r) => r.namespace === activeNs) : t.routes;
  const brokenCount = t.routes.filter((r) => routeBroken(r, t.gateway.listeners)).length;
  const globalCount = t.routes.filter(routeHasGlobal).length;
  const policyRouteCount = t.routes.filter(routeHasPolicies).length;
  const policyRefCount = t.routes.reduce((n, r) => n + routePolicyRefCount(r), 0) + t.gateway.policies.length + (t.clusterPolicies?.length ?? 0);
  const activeMode = routeMode === "issues" && brokenCount === 0
    ? "all"
    : routeMode === "global" && globalCount === 0
      ? "all"
      : routeMode === "policies" && policyRouteCount === 0
        ? "all"
        : routeMode;
  const modeRoutes = activeMode === "issues"
    ? nsRoutes.filter((r) => routeBroken(r, t.gateway.listeners))
    : activeMode === "global"
      ? nsRoutes.filter(routeHasGlobal)
      : activeMode === "policies"
        ? nsRoutes.filter(routeHasPolicies)
        : nsRoutes;

  // Route filter: with dozens of routes behind one Gateway the lanes become a
  // wall; substring-match on name, namespace, hostname, and backend service.
  const q = routeQuery.trim().toLowerCase();
  const filteredRoutes = q === ""
    ? modeRoutes
    : modeRoutes.filter((r) =>
        r.name.toLowerCase().includes(q) ||
        r.namespace.toLowerCase().includes(q) ||
        r.hostnames.some((h) => h.toLowerCase().includes(q)) ||
        r.services.some((sv) => sv.name.toLowerCase().includes(q)));

  // Diagnostic default ordering: broken lanes (rejected, unresolved refs, or no
  // backend) float to the top so they are never buried under healthy routes.
  const visibleRoutes = [...filteredRoutes].sort((a, b) => laneRank(a, t.gateway.listeners) - laneRank(b, t.gateway.listeners));

  const selected = visibleRoutes.find((r) => routeKey(r) === net.selectedRoute) ?? null;
  const listeners = t.gateway.listeners.length;
  const tlsRefs = tlsRefCount(t.gateway.listeners);

  return (
    <div style={{ padding: "14px 16px", display: "flex", flexDirection: "column", gap: 12, height: "100%", minHeight: 0, overflow: "hidden", boxSizing: "border-box" }}>
      <div style={{ display: "flex", alignItems: "center", gap: 10, flexShrink: 0 }}>
        <div style={{ fontSize: 15, fontWeight: 500 }}>{t.gateway.name}</div>
        <span style={{ fontSize: 10, padding: "2px 8px", borderRadius: 999, background: t.gateway.programmed ? "var(--color-background-success)" : "var(--color-background-warning)", color: t.gateway.programmed ? "var(--color-text-success)" : "var(--color-text-warning)" }}>{t.gateway.programmed ? "programmed" : "pending"}</span>
        <span style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>{t.gateway.className}</span>
        <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, color: gatewayAddress(t) === "—" ? "var(--color-text-tertiary)" : "var(--color-text-primary)", ...ellipsis }}>{gatewayAddress(t)}</span>
        {t.gateway.policies.length > 0 && (
          <div style={{ display: "flex", alignItems: "center", gap: 4 }}>
            <span style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.4, color: "var(--color-text-tertiary)" }}>policies</span>
            {t.gateway.policies.map((p) => (
              <PolicyChip key={`${p.kind}/${p.namespace}/${p.name}`} p={p} />
            ))}
          </div>
        )}
        {t.clusterPolicies && t.clusterPolicies.length > 0 && (
          <div style={{ display: "flex", alignItems: "center", gap: 4 }}>
            <span style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.4, color: "var(--color-text-tertiary)" }}>cluster-wide policies</span>
            {t.clusterPolicies.map((p) => (
              <PolicyChip key={`${p.kind}/${p.namespace}/${p.name}`} p={p} />
            ))}
          </div>
        )}
        <div style={{ flex: 1 }} />
        <button onClick={() => void getGatewayTopology(cluster, gateway)} style={{ padding: "3px 10px", fontSize: 11, borderRadius: 4, cursor: "pointer", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)" }}>Refresh</button>
      </div>

      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(120px, 1fr))", border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", overflow: "hidden", background: "var(--color-background-primary)", flexShrink: 0 }}>
        <TopologyCell label="routes" value={String(t.routes.length)} />
        <TopologyCell label="issues" value={String(brokenCount)} tone={brokenCount > 0 ? "warning" : "success"} />
        <TopologyCell label="namespaces" value={String(namespaces.length)} />
        <TopologyCell label="global" value={String(globalCount)} tone={globalCount > 0 ? "info" : "muted"} />
        <TopologyCell label="policies" value={String(policyRefCount)} tone={policyRefCount > 0 ? "info" : "muted"} />
        <TopologyCell label="listeners" value={String(listeners)} />
        <TopologyCell label="tls refs" value={String(tlsRefs)} tone={tlsRefs > 0 ? "info" : "muted"} />
        <TopologyCell label="address" value={gatewayAddress(t)} tone={gatewayAddress(t) === "—" ? "muted" : "default"} />
      </div>

      {t.error && (
        <div style={{ padding: "8px 10px", fontSize: 12, borderRadius: 4, background: "var(--color-background-danger)", color: "var(--color-text-danger)", border: "0.5px solid var(--color-border-danger)", flexShrink: 0 }}>{t.error}</div>
      )}

      {(showFilter || t.routes.length > 0) && (
        <div style={{ display: "flex", flexWrap: "wrap", gap: 6, alignItems: "center", flexShrink: 0 }}>
          <span style={{ fontSize: 9, color: "var(--color-text-tertiary)", marginRight: 2 }}>routes</span>
          <Chip label="all routes" count={t.routes.length} active={activeMode === "all"} onClick={() => setRouteMode("all")} />
          {brokenCount > 0 && <Chip label="issues" count={brokenCount} active={activeMode === "issues"} onClick={() => setRouteMode("issues")} />}
          {globalCount > 0 && <Chip label="global" count={globalCount} active={activeMode === "global"} onClick={() => setRouteMode("global")} />}
          {policyRouteCount > 0 && <Chip label="policies" count={policyRouteCount} active={activeMode === "policies"} onClick={() => setRouteMode("policies")} />}
          {showFilter && (<>
          <span style={{ fontSize: 9, color: "var(--color-text-tertiary)", marginRight: 2 }}>namespace</span>
          {namespaces.length <= NS_CHIP_LIMIT ? (
            <>
              <Chip label="All" active={activeNs === null} onClick={() => setNsFilter(null)} />
              {namespaces.map((ns) => (
                <Chip key={ns} label={ns} count={nsCounts.get(ns)} active={activeNs === ns} onClick={() => setNsFilter(ns)} />
              ))}
            </>
          ) : (
            <select
              value={activeNs ?? ""}
              onChange={(e) => setNsFilter(e.target.value || null)}
              style={{ fontSize: 12, fontFamily: "var(--font-mono)", padding: "3px 8px", borderRadius: 4, cursor: "pointer", border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", maxWidth: 280 }}
            >
              <option value="">All namespaces ({t.routes.length})</option>
              {namespaces.map((ns) => (
                <option key={ns} value={ns}>{ns} ({nsCounts.get(ns)})</option>
              ))}
            </select>
          )}
          </>)}
          <input
            value={routeQuery}
            onChange={(e) => setRouteQuery(e.target.value)}
            placeholder="filter routes"
            aria-label="filter routes"
            style={{ fontSize: 11, padding: "3px 8px", borderRadius: 4, border: "0.5px solid var(--color-border-tertiary)", background: "var(--color-background-primary)", color: "var(--color-text-primary)", width: 160 }}
          />
          {visibleRoutes.length !== modeRoutes.length && (
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>{visibleRoutes.length} of {modeRoutes.length} routes</span>
          )}
        </div>
      )}

      <div style={{ flex: 1, minHeight: 0, display: "grid", gridTemplateColumns: "minmax(560px, 1fr) minmax(320px, 420px)", gap: 12, alignItems: "stretch", overflow: "hidden" }}>
        <div style={{ minHeight: 0, overflow: "hidden", background: "var(--color-background-secondary)", borderRadius: "var(--border-radius-md)", border: "0.5px solid var(--color-border-tertiary)" }}>
          <div data-testid="gateway-route-scroll" style={{ height: "100%", overflowY: "auto", padding: "14px 12px", boxSizing: "border-box" }}>
            {t.routes.length === 0 && !t.error ? (
              <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No HTTPRoutes attached to this Gateway.</div>
            ) : visibleRoutes.length === 0 ? (
              <div style={{ color: "var(--color-text-secondary)", fontSize: 13 }}>No routes match the current filter.</div>
            ) : (
              visibleRoutes.map((r) => (
                <RouteLane
                  key={routeKey(r)}
                  route={r}
                  selected={net.selectedRoute === routeKey(r)}
                  metric={routeMetrics[routeKey(r)]}
                  listeners={t.gateway.listeners}
                  onSelect={() => selectRoute(routeKey(r))}
                />
              ))
            )}
            <div style={{ marginTop: 12, paddingTop: 10, borderTop: "0.5px dashed var(--color-border-secondary)", fontSize: 10, color: "var(--color-text-tertiary)" }}>⇄ global services show their fleet-confirmed mesh peers on the pods box · cluster peering is on the Fleet view</div>
            {rmStatus && (
              <div style={{ marginTop: 6, fontSize: 10, color: rmStatus.available ? "var(--color-text-tertiary)" : "var(--color-text-warning)" }}>
                {rmStatus.available
                  ? rmStatus.message
                    ? `route metrics · ${rmStatus.message}`
                    : `route metrics · updated ${ago(rmStatus.updatedAt)}${rmStale ? " · stale" : ""}`
                  : `route metrics unavailable: ${rmStatus.message}`}
              </div>
            )}
            {t.warnings && t.warnings.length > 0 && (
              <div style={{ marginTop: 10 }}>
                {t.warnings.map((w, i) => (
                  <div key={i} style={{ fontSize: 11, color: "var(--color-text-warning)", padding: "2px 0" }}>⚠︎ {w}</div>
                ))}
              </div>
            )}
          </div>
        </div>
        <aside data-testid="gateway-route-inspector-scroll" style={{ minHeight: 0, overflowY: "auto" }}>
          {selected ? (
            <RouteDetail route={selected} metric={routeMetrics[routeKey(selected)]} listeners={t.gateway.listeners} certLookup={certLookup} certsLoaded={cm.cluster === cluster} onOpenObject={openObject} />
          ) : (
            <EmptyRouteInspector routeCount={visibleRoutes.length} totalRoutes={t.routes.length} />
          )}
        </aside>
      </div>
    </div>
  );
}

function RouteLane({ route, selected, metric, listeners, onSelect }: { route: RouteNodeDTO; selected: boolean; metric: RouteMetricDTO | undefined; listeners: ListenerDTO[]; onSelect: () => void }) {
  const svc = route.services[0];
  const issues = routeIssueReasons(route, listeners);
  const broken = issues.length > 0;
  return (
    <div style={{ display: "grid", gridTemplateColumns: "3px minmax(0,1.2fr) 20px minmax(0,1fr) 20px minmax(0,0.9fr)", gap: 6, alignItems: "stretch", marginBottom: 8 }}>
      <div style={{ background: broken ? "var(--color-text-danger)" : "transparent", borderRadius: 0 }} />
      <button
        onClick={onSelect}
        style={{
          ...nb,
          borderColor: broken ? "var(--color-border-danger)" : "var(--color-border-info)",
          background: broken ? "var(--color-background-danger)" : nb.background,
          cursor: "pointer",
          textAlign: "left",
          color: "var(--color-text-primary)",
          boxShadow: selected ? "0 0 0 1px var(--color-text-info)" : undefined,
        }}
      >
        <div style={{ ...lab, color: "var(--color-text-info)" }}>httproute</div>
        <div style={{ ...nm, color: "var(--color-text-info)" }}>{route.name}</div>
        <div style={{ fontSize: 9, marginTop: 2, ...ellipsis }}>
          <span style={{ width: 6, height: 6, borderRadius: "50%", background: dot(route.accepted), display: "inline-block", marginRight: 4 }} />
          {route.accepted ? "accepted" : "rejected"} · {route.parentSectionName ? `listener ${route.parentSectionName}` : "all listeners"} · {route.matches[0]?.pathValue ?? "/"}
        </div>
        {issues.length > 0 && (
          <div style={{ fontSize: 9, color: "var(--color-text-warning)", marginTop: 3, ...ellipsis }}>{issues[0]}</div>
        )}
        {route.policies.length > 0 && (
          <div style={{ display: "flex", flexWrap: "wrap", gap: 3, marginTop: 4 }}>
            {route.policies.map((p) => (
              <PolicyChip key={`${p.kind}/${p.namespace}/${p.name}`} p={p} />
            ))}
          </div>
        )}
      </button>
      <div style={chev}>›</div>
      <div style={nb}>
        <div style={lab}>service</div>
        <div style={nm}>{svc ? svc.name : "—"}</div>
        <div style={{ fontSize: 9, color: svc?.resolved ? "var(--color-text-secondary)" : "var(--color-text-danger)", marginTop: 2 }}>
          {!svc ? "no backend" : svc.resolved ? `${svc.type} :${svc.port}` : "unresolved"}{route.backends.length > 1 ? ` · +${route.backends.length - 1}` : ""}
          {svc && <span style={{ color: route.pods.unknown ? "var(--color-text-tertiary)" : route.pods.ready < route.pods.total ? "var(--color-text-warning)" : "var(--color-text-secondary)" }}> · pods {route.pods.unknown ? "unknown" : `${route.pods.ready}/${route.pods.total}`}</span>}
        </div>
        {svc && (svc.policies.length > 0 || svc.cnps.length > 0) && (
          <div style={{ display: "flex", flexWrap: "wrap", gap: 3, marginTop: 4 }}>
            {svc.policies.map((p) => (
              <PolicyChip key={`${p.kind}/${p.namespace}/${p.name}`} p={p} align="right" />
            ))}
            {svc.cnps.map((p) => (
              <PolicyChip key={`${p.kind}/${p.namespace}/${p.name}`} p={p} align="right" />
            ))}
          </div>
        )}
        {svc && svc.global && (
          <div style={{ marginTop: 4, fontSize: 9, fontFamily: "var(--font-mono)", color: "var(--color-text-info)" }} title={svc.meshUnconfirmed ? "global service: some mesh peers could not be fleet-verified (off-fleet or not connected). Live dataplane health is not checked." : "global service: also present on these fleet mesh peers. Live dataplane health is not checked."}>
            ⇄ global{svc.meshClusters.length > 0 ? ` → ${svc.meshClusters.join(", ")}` : ""}{svc.meshUnconfirmed && svc.meshClusters.length === 0 ? " (peers unverified)" : svc.meshUnconfirmed ? " (+unverified)" : ""}
          </div>
        )}
      </div>
      <div style={chev}>›</div>
      <TrafficBox broken={broken} hasBackend={!!svc && svc.resolved} m={metric} />
    </div>
  );
}

function EmptyRouteInspector({ routeCount, totalRoutes }: { routeCount: number; totalRoutes: number }) {
  const text = totalRoutes === 0
    ? "No HTTPRoutes are attached to this Gateway."
    : routeCount === 0
      ? "No route is visible with the current filters."
      : "Select a route to inspect matches, backends, policies, Cilium inference, and traffic.";
  return (
    <div style={{ background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", padding: "10px 12px", fontSize: 12 }}>
      <div style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginBottom: 4 }}>selected route</div>
      <div style={{ color: routeCount > 0 ? "var(--color-text-info)" : "var(--color-text-secondary)", fontFamily: "var(--font-mono)", fontWeight: 600 }}>route inspector</div>
      <div style={{ color: "var(--color-text-secondary)", marginTop: 8 }}>{text}</div>
    </div>
  );
}

function fmtMs(v: number | null): string {
  if (v == null) return "—";
  return v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${Math.round(v)}ms`;
}
function fmtRps(v: number | null): string {
  if (v == null) return "—";
  if (v >= 10) return `${Math.round(v)}`;
  // keep one decimal for sub-10 rates, but render whole numbers cleanly (0, 3)
  return Number.isInteger(v) ? `${v}` : v.toFixed(1);
}
function fmtErr(v: number | null): string {
  if (v == null) return "—";
  const pct = v * 100;
  return pct > 0 && pct < 0.1 ? "<0.1%" : `${pct < 1 ? pct.toFixed(1) : Math.round(pct)}%`;
}
function ago(iso: string): string {
  if (!iso) return "never";
  const secs = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
  return secs < 60 ? `${secs}s ago` : `${Math.floor(secs / 60)}m ago`;
}
function errColor(v: number | null): string {
  return v == null ? "var(--color-text-tertiary)" : v >= 0.05 ? "var(--color-text-danger)" : v >= 0.01 ? "var(--color-text-warning)" : "var(--color-text-success)";
}
// TrafficBox is the lane's live-traffic node: rps/p50/p99/err for the route.
// A broken or backend-less lane states "no data path" - the absence is said,
// never left blank. Metrics absence renders dashes (the footer explains why).
function TrafficBox({ broken, hasBackend, m }: { broken: boolean; hasBackend: boolean; m: RouteMetricDTO | undefined }) {
  if (broken || !hasBackend) {
    return (
      <div style={nb}>
        <div style={lab}>traffic</div>
        <div style={{ fontSize: 11, fontFamily: "var(--font-mono)", color: "var(--color-text-tertiary)" }}>— no data path</div>
      </div>
    );
  }
  const rps = m?.rps ?? null, p50 = m?.p50 ?? null, p99 = m?.p99 ?? null, err = m?.errRate ?? null;
  return (
    <div style={nb}>
      <div style={lab}>traffic</div>
      <div style={{ fontSize: 11, fontFamily: "var(--font-mono)" }}>{fmtRps(rps)} rps · p99 {fmtMs(p99)}</div>
      <div style={{ fontSize: 9, fontFamily: "var(--font-mono)", marginTop: 2, color: "var(--color-text-secondary)" }}>
        p50 {fmtMs(p50)} · <span style={{ color: errColor(err) }}>err {fmtErr(err)}</span>
      </div>
    </div>
  );
}


function TopologyCell({ label, value, tone = "default" }: { label: string; value: string; tone?: "default" | "success" | "warning" | "info" | "muted" }) {
  const color = tone === "success"
    ? "var(--color-text-success)"
    : tone === "warning"
      ? "var(--color-text-warning)"
      : tone === "info"
        ? "var(--color-text-info)"
        : tone === "muted"
          ? "var(--color-text-tertiary)"
          : "var(--color-text-primary)";
  return (
    <div style={{ padding: "8px 10px", borderRight: "0.5px solid var(--color-border-tertiary)", minWidth: 0 }}>
      <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginBottom: 2 }}>{label}</div>
      <div style={{ fontFamily: "var(--font-mono)", fontWeight: 600, color, ...ellipsis }}>{value}</div>
    </div>
  );
}

function Chip({ label, count, active, onClick }: { label: string; count?: number; active: boolean; onClick: () => void }) {
  return (
    <button
      onClick={onClick}
      style={{
        display: "inline-flex",
        alignItems: "center",
        gap: 4,
        padding: "2px 9px",
        fontSize: 11,
        borderRadius: 999,
        cursor: "pointer",
        fontFamily: "var(--font-mono)",
        border: active ? "0.5px solid var(--color-border-info)" : "0.5px solid var(--color-border-tertiary)",
        background: active ? "var(--color-background-info)" : "var(--color-background-primary)",
        color: active ? "var(--color-text-info)" : "var(--color-text-secondary)",
      }}
    >
      {label}
      {count !== undefined && <span style={{ fontSize: 9, opacity: 0.7 }}>{count}</span>}
    </button>
  );
}

function OpenObjectButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button type="button" aria-label={`open ${label}`} onClick={onClick} style={openBtn()}>
      open {label}
    </button>
  );
}

function OpenPolicyButton({ policy, onOpenObject }: { policy: PolicyRefDTO; onOpenObject: OpenObject }) {
  const ref = policyRefs[policy.kind];
  if (!ref) return null;
  return <OpenObjectButton label={policy.kind} onClick={() => onOpenObject(ref, policy.namespace, policy.name)} />;
}

function ListenerTLS({ route, listeners, certLookup, certsLoaded, onOpenObject }: { route: RouteNodeDTO; listeners: ListenerDTO[]; certLookup: CertLookup; certsLoaded: boolean; onOpenObject: OpenObject }) {
  const scoped = route.parentSectionName ? listeners.filter((l) => l.name === route.parentSectionName) : listeners;
  return (
    <div style={{ marginTop: 12, paddingTop: 10, borderTop: "0.5px solid var(--color-border-tertiary)" }}>
      <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>listener tls</div>
      {scoped.length === 0 ? (
        <div style={{ fontSize: 11, color: "var(--color-text-warning)" }}>No Gateway listener matches this route section.</div>
      ) : (
        <div style={{ display: "grid", gap: 8 }}>
          {scoped.map((l) => {
            const refs = l.certificateRefs ?? [];
            return (
              <div key={l.name || `${l.protocol}:${l.port}`}>
                <div style={{ fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: 600 }}>
                  {l.name || "unnamed"} · {l.protocol}:{l.port}{l.hostname ? ` · ${l.hostname}` : ""}
                </div>
                {refs.length === 0 ? (
                  <div style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>No certificateRefs on this listener.</div>
                ) : refs.map((ref) => {
                  const cert = ref.kind === "Secret" ? certLookup.get(certKey(ref.namespace, ref.name)) : undefined;
                  const st = certsLoaded ? certStatus(cert) : { text: "cert-manager state unavailable", color: "var(--color-text-tertiary)" };
                  return (
                    <div key={`${ref.kind}/${ref.namespace}/${ref.name}`} style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap", fontSize: 10, color: "var(--color-text-secondary)", fontFamily: "var(--font-mono)" }}>
                      <span>{ref.kind} {ref.namespace}/{ref.name} · <span style={{ color: st.color }}>{st.text}</span></span>
                      {ref.kind === "Secret" && <OpenObjectButton label="Secret" onClick={() => onOpenObject(secretRef, ref.namespace, ref.name)} />}
                      {cert && <OpenObjectButton label="Certificate" onClick={() => onOpenObject(certificateRef, cert.namespace, cert.name)} />}
                    </div>
                  );
                })}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}

function RouteDetail({ route, metric, listeners, certLookup, certsLoaded, onOpenObject }: { route: RouteNodeDTO; metric: RouteMetricDTO | undefined; listeners: ListenerDTO[]; certLookup: CertLookup; certsLoaded: boolean; onOpenObject: OpenObject }) {
  const issues = routeIssueReasons(route, listeners);
  return (
    <div style={{ background: "var(--color-background-primary)", border: "0.5px solid var(--color-border-tertiary)", borderRadius: "var(--border-radius-md)", padding: "10px 12px" }}>
      <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 8 }}>
        <span style={{ color: "var(--color-text-info)" }}>↳</span>
        <span style={{ fontFamily: "var(--font-mono)", fontWeight: 600, fontSize: 12 }}>{route.name}</span>
        <span style={{ fontSize: 10, padding: "1px 6px", borderRadius: 4, background: "var(--color-background-secondary)", color: "var(--color-text-secondary)" }}>HTTPRoute</span>
        <span style={{ fontSize: 10, padding: "1px 6px", borderRadius: 4, background: route.accepted ? "var(--color-background-success)" : "var(--color-background-danger)", color: route.accepted ? "var(--color-text-success)" : "var(--color-text-danger)" }}>{route.accepted ? "accepted" : "rejected"} · {route.resolvedRefs ? "resolvedRefs" : "unresolved"}</span>
        <span style={{ fontSize: 10, padding: "1px 6px", borderRadius: 4, background: issues.length > 0 ? "var(--color-background-warning)" : "var(--color-background-success)", color: issues.length > 0 ? "var(--color-text-warning)" : "var(--color-text-success)" }}>{issues.length > 0 ? `${issues.length} issue${issues.length === 1 ? "" : "s"}` : "path healthy"}</span>
        <OpenObjectButton label="HTTPRoute" onClick={() => onOpenObject(httpRouteRef, route.namespace, route.name)} />
      </div>
      <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 10, fontSize: 11, fontFamily: "var(--font-mono)" }}>
        <div>
          <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 5 }}>matches</div>
          <div style={{ color: "var(--color-text-secondary)", marginBottom: 4 }}>listener: {route.parentSectionName || "all matching listeners"}</div>
          {route.matches.map((m, i) => (<div key={i}>{m.pathType} {m.pathValue}{m.method ? ` · ${m.method}` : ""}</div>))}
          {route.hostnames.length > 0 && <div style={{ color: "var(--color-text-secondary)", marginTop: 4 }}>hostnames: {route.hostnames.join(", ")}</div>}
        </div>
        <div>
          <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 5 }}>backends</div>
          {route.backends.map((b, i) => (
            <div key={i} style={{ display: "flex", alignItems: "center", gap: 6, flexWrap: "wrap" }}>
              <span>{b.name}:{b.port}{b.weight ? ` · weight ${b.weight}` : ""}</span>
              {b.kind === "Service" && <OpenObjectButton label="Service" onClick={() => onOpenObject(serviceRef, b.namespace, b.name)} />}
            </div>
          ))}
        </div>
      </div>
      <div style={{ marginTop: 12, paddingTop: 10, borderTop: "0.5px solid var(--color-border-tertiary)" }}>
        <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>path health</div>
        {issues.length === 0 ? (
          <div style={{ fontSize: 11, color: "var(--color-text-success)" }}>Gateway attachment, references, backend Service, and endpoints are usable.</div>
        ) : (
          <div style={{ display: "grid", gap: 4 }}>
            {issues.map((issue) => (
              <div key={issue} style={{ fontSize: 11, color: "var(--color-text-warning)" }}>{issue}</div>
            ))}
          </div>
        )}
      </div>
      <ListenerTLS route={route} listeners={listeners} certLookup={certLookup} certsLoaded={certsLoaded} onOpenObject={onOpenObject} />
      <div style={{ marginTop: 12, paddingTop: 10, borderTop: "0.5px solid var(--color-border-tertiary)" }}>
        <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>traffic</div>
        <div style={{ display: "flex", gap: 14, fontFamily: "var(--font-mono)", fontSize: 11 }}>
          <span>{fmtRps(metric?.rps ?? null)} rps</span>
          <span>p50 {fmtMs(metric?.p50 ?? null)}</span>
          <span>p99 {fmtMs(metric?.p99 ?? null)}</span>
          <span style={{ color: errColor(metric?.errRate ?? null) }}>err {fmtErr(metric?.errRate ?? null)}</span>
        </div>
        <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginTop: 4 }}>p50/p99 are worst-rule values</div>
      </div>
      <div style={{ marginTop: 12, paddingTop: 10, borderTop: "0.5px solid var(--color-border-tertiary)" }}>
        <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>attached policies</div>
        {(() => {
          const svcPolicies = route.services.flatMap((s) => s.policies);
          const all = [...route.policies, ...svcPolicies];
          if (all.length === 0) {
            return <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>None on this route.</div>;
          }
          return all.map((p, idx) => (
            <div
              key={`${p.kind}/${p.namespace}/${p.name}`}
              style={{
                marginBottom: 12,
                paddingBottom: idx < all.length - 1 ? 8 : 0,
                borderBottom: idx < all.length - 1 ? "0.5px solid var(--color-border-tertiary)" : undefined,
              }}
            >
              <div style={{ display: "flex", gap: 6, alignItems: "center", fontFamily: "var(--font-mono)", fontSize: 11 }}>
                <span style={{ fontWeight: 600 }}>{p.kind} {p.namespace}/{p.name}</span>
                <OpenPolicyButton policy={p} onOpenObject={onOpenObject} />
              </div>
              <div style={{ fontSize: 10, color: "var(--color-text-secondary)" }}>
                Target: {p.targetKind} {p.targetNamespace}/{p.targetName}{p.targetSectionName ? ` (Section: ${p.targetSectionName})` : ""}
              </div>
              {p.summary && <div style={{ fontSize: 10, color: "var(--color-text-secondary)" }}>Features: {p.summary}</div>}
              {p.details.length > 0 && (
                <div style={{ marginTop: 4 }}>
                  {p.details.map((d, i) => (
                    <div key={i} style={{ fontFamily: "var(--font-mono)", fontSize: 10, color: "var(--color-text-secondary)" }}>{d.key}: {d.value}</div>
                  ))}
                </div>
              )}
            </div>
          ));
        })()}
        {(() => {
          const cnps = route.services.flatMap((s) => s.cnps);
          if (cnps.length === 0) return null;
          return (
            <div style={{ marginTop: 12, paddingTop: 10, borderTop: "0.5px dashed var(--color-border-tertiary)" }}>
              <div style={{ fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 6 }}>inferred network policies</div>
              {cnps.map((p) => (
                <div key={`${p.kind}/${p.namespace}/${p.name}`} style={{ marginBottom: 8 }}>
                  <div style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap", fontFamily: "var(--font-mono)", fontSize: 11, fontWeight: 600 }}>
                    <span>{`${p.kind} ${p.namespace}/${p.name}`}</span>
                    <OpenPolicyButton policy={p} onOpenObject={onOpenObject} />
                  </div>
                  <div style={{ fontSize: 10, color: "var(--color-text-secondary)" }}>{`Target: Pods selected via Service ${p.targetNamespace}/${p.targetName}`}</div>
                  <div style={{ fontSize: 10, color: "var(--color-text-secondary)" }}>{`Inferred via: ${p.match}`}</div>
                  {p.summary && <div style={{ fontSize: 10, color: "var(--color-text-secondary)" }}>{`Features: ${p.summary}`}</div>}
                  {p.details.map((d, i) => (
                    <div key={i} style={{ fontFamily: "var(--font-mono)", fontSize: 10, color: "var(--color-text-secondary)" }}>{d.key}: {d.value}</div>
                  ))}
                </div>
              ))}
              <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginTop: 4 }}>cluster-wide policies are shown in the topology header.</div>
            </div>
          );
        })()}
        <div style={{ fontSize: 10, color: "var(--color-text-tertiary)", marginTop: 4 }}>Gateway policies are shown in the topology header.</div>
      </div>
    </div>
  );
}
