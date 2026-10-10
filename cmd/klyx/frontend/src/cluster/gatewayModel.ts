import type { CertificateDTO, GatewayRefDTO, ListenerDTO, PolicyRefDTO, ResourceRef, RouteNodeDTO } from "../store/fleet";

// gatewayModel - the Gateway API lens's health and wording, shared by the
// list and the topology (spec §6 step 6). Gateway and route status reach the
// frontend as booleans: "not accepted" means the condition is not True -
// False, Unknown or not yet reported - and the copy never claims which.

export type Role = { kind: "fault" | "degraded" | "progressing" | "ok" | "unknown"; label: string };
export const RANK: Record<Role["kind"], number> = { fault: 0, degraded: 1, progressing: 2, unknown: 3, ok: 4 };
export const ROLE_TEXT: Record<Role["kind"], string> = {
  fault: "var(--color-text-danger)", degraded: "var(--color-text-warning)", progressing: "var(--color-text-secondary)",
  ok: "var(--color-text-secondary)", unknown: "var(--color-text-tertiary)",
};
export const roleTone = (k: Role["kind"]) => (k === "fault" ? "danger" : k === "degraded" ? "warning" : k === "ok" ? "success" : "muted") as "danger" | "warning" | "success" | "muted";

// A Gateway the controller has not accepted serves nothing (fault). One that
// is accepted but not programmed has no data path yet - provisioning or a
// missing address - so it needs attention, not alarm. A generation the
// controller has not observed is syncing; it never hides a fault.
export function gatewayHealth(g: Pick<GatewayRefDTO, "accepted" | "programmed" | "stale">): Role {
  if (!g.accepted) return { kind: "fault", label: g.stale ? "not accepted · syncing" : "not accepted" };
  if (!g.programmed) return { kind: "degraded", label: g.stale ? "not programmed · syncing" : "not programmed" };
  if (g.stale) return { kind: "progressing", label: "syncing" };
  return { kind: "ok", label: "programmed" };
}

export function gatewayVerdictDetail(g: Pick<GatewayRefDTO, "accepted" | "programmed" | "stale" | "generation" | "observedGeneration">): string {
  const sync = g.stale ? ` The controller has not observed generation ${g.generation ?? "?"} yet; status describes generation ${g.observedGeneration ?? "?"}.` : "";
  if (!g.accepted) return `Accepted is not True (False, Unknown or not reported) - the controller has not accepted this Gateway, so no route through it is served. Check the GatewayClass and its controller.${sync}`;
  if (!g.programmed) return `Accepted, but Programmed is not True - the data plane is not configured for this Gateway yet (address or proxy still provisioning, or a listener conflict).${sync}`;
  if (g.stale) return `Programmed for the previous generation.${sync}`;
  return "Accepted and programmed by its controller.";
}

export function addressText(addresses?: { value: string }[]): string {
  const values = (addresses ?? []).map((a) => a.value).filter(Boolean);
  return values.length > 0 ? values.join(", ") : "";
}

export function listenerText(l: ListenerDTO): string {
  return `${l.name ? `${l.name} ` : ""}${l.protocol || "listener"}${l.port > 0 ? `:${l.port}` : ""}`;
}

// ---- routes ----------------------------------------------------------------

export type RouteIssue = { short: string; text: string; severity: "fault" | "degraded" };

// The lane follows the PRIMARY backend (services[0]); extra backends are
// listed in the inspector and named by the topology's own warnings.
export function routeIssues(r: RouteNodeDTO, listeners: ListenerDTO[] = []): RouteIssue[] {
  const svc = r.services[0];
  const out: RouteIssue[] = [];
  const names = new Set(listeners.map((l) => l.name).filter(Boolean));
  if (!r.accepted) out.push({ short: "not accepted", text: "Not accepted by this Gateway (Accepted is not True for this parent).", severity: "fault" });
  if (!r.resolvedRefs) out.push({ short: "unresolved refs", text: "References not resolved (ResolvedRefs is not True) - check the backendRef, a cross-namespace ReferenceGrant, or the listener section.", severity: "fault" });
  if (r.parentSectionName && names.size > 0 && !names.has(r.parentSectionName)) out.push({ short: "listener missing", text: `Listener ${r.parentSectionName} does not exist on this Gateway.`, severity: "fault" });
  if (!svc) out.push({ short: "no backend", text: "No backend Service.", severity: "fault" });
  else if (!svc.resolved) out.push({ short: "backend missing", text: `Backend Service ${svc.namespace}/${svc.name} not found.`, severity: "fault" });
  else if (!r.pods.unknown && (r.pods.total === 0 || r.pods.ready === 0)) out.push({ short: "no ready endpoints", text: r.pods.total === 0 ? "The backend Service selects no pods." : r.pods.total === 1 ? "The backend's only pod is not ready." : `None of the backend's ${r.pods.total} pods is ready.`, severity: "fault" });
  else if (!r.pods.unknown && r.pods.ready < r.pods.total) out.push({ short: `${r.pods.ready}/${r.pods.total} ready`, text: `Backend pods ${r.pods.ready}/${r.pods.total} ready - serving at reduced capacity.`, severity: "degraded" });
  return out;
}

export function routeHealth(r: RouteNodeDTO, listeners: ListenerDTO[] = []): Role {
  const issues = routeIssues(r, listeners);
  const fault = issues.find((i) => i.severity === "fault");
  if (fault) return { kind: "fault", label: fault.short };
  if (issues.length > 0) return { kind: "degraded", label: issues[0].short };
  if (r.services[0]?.resolved && r.pods.unknown) return { kind: "unknown", label: "endpoints unverified" };
  return { kind: "ok", label: "path healthy" };
}

export const routeKey = (r: { namespace: string; name: string }) => `${r.namespace}/${r.name}`;
export const routeHasGlobal = (r: RouteNodeDTO) => r.services.some((s) => s.global);
export const routeHasPolicies = (r: RouteNodeDTO) => r.policies.length > 0 || r.services.some((s) => s.policies.length > 0 || s.cnps.length > 0);

// ---- TLS -------------------------------------------------------------------

export type CertLookup = Map<string, CertificateDTO>;
export function certBySecret(certs: CertificateDTO[]): CertLookup {
  const out: CertLookup = new Map();
  for (const c of certs) if (c.secretName) out.set(`${c.namespace}/${c.secretName}`, c);
  return out;
}
export function certStatus(c: CertificateDTO | undefined): { text: string; color: string } {
  if (!c) return { text: "not issued by cert-manager", color: "var(--color-text-tertiary)" };
  if (c.ready === "Ready") return { text: "certificate ready", color: "var(--color-text-secondary)" };
  if (c.ready === "Issuing") return { text: "certificate issuing", color: "var(--color-text-secondary)" };
  return { text: `certificate ${c.ready.toLowerCase() || "state unknown"}`, color: "var(--color-text-warning)" };
}

// ---- traffic ---------------------------------------------------------------

export function fmtMs(v: number | null): string {
  if (v == null) return "-";
  return v >= 1000 ? `${(v / 1000).toFixed(1)}s` : `${Math.round(v)}ms`;
}
export function fmtRps(v: number | null): string {
  if (v == null) return "-";
  if (v >= 10) return `${Math.round(v)}`;
  return Number.isInteger(v) ? `${v}` : v.toFixed(1);
}
export function fmtErr(v: number | null): string {
  if (v == null) return "-";
  const pct = v * 100;
  return pct > 0 && pct < 0.1 ? "<0.1%" : `${pct < 1 ? pct.toFixed(1) : Math.round(pct)}%`;
}
// Klyx's own attention thresholds for a route's 5xx share: 1% amber, 5% red.
// A healthy rate stays neutral - only faults glow.
export function errColor(v: number | null): string {
  return v == null ? "var(--color-text-tertiary)" : v >= 0.05 ? "var(--color-text-danger)" : v >= 0.01 ? "var(--color-text-warning)" : "var(--color-text-secondary)";
}

// ---- object refs -----------------------------------------------------------

const gw = "gateway.networking.k8s.io";
export const REFS = {
  gateway: { group: gw, version: "v1", plural: "gateways", kind: "Gateway", scope: "Namespaced" },
  gatewayClass: { group: gw, version: "v1", plural: "gatewayclasses", kind: "GatewayClass", scope: "Cluster" },
  httpRoute: { group: gw, version: "v1", plural: "httproutes", kind: "HTTPRoute", scope: "Namespaced" },
  service: { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" },
  secret: { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" },
  certificate: { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" },
} satisfies Record<string, ResourceRef>;

export const POLICY_REFS: Record<string, ResourceRef> = {
  BackendTLSPolicy: { group: gw, version: "v1alpha3", plural: "backendtlspolicies", kind: "BackendTLSPolicy", scope: "Namespaced" },
  ClientTrafficPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "clienttrafficpolicies", kind: "ClientTrafficPolicy", scope: "Namespaced" },
  BackendTrafficPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "backendtrafficpolicies", kind: "BackendTrafficPolicy", scope: "Namespaced" },
  SecurityPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "securitypolicies", kind: "SecurityPolicy", scope: "Namespaced" },
  EnvoyExtensionPolicy: { group: "gateway.envoyproxy.io", version: "v1alpha1", plural: "envoyextensionpolicies", kind: "EnvoyExtensionPolicy", scope: "Namespaced" },
  CiliumNetworkPolicy: { group: "cilium.io", version: "v2", plural: "ciliumnetworkpolicies", kind: "CiliumNetworkPolicy", scope: "Namespaced" },
  CiliumClusterwideNetworkPolicy: { group: "cilium.io", version: "v2", plural: "ciliumclusterwidenetworkpolicies", kind: "CiliumClusterwideNetworkPolicy", scope: "Cluster" },
};

export const POLICY_ABBREV: Record<string, string> = {
  ClientTrafficPolicy: "CTP", BackendTrafficPolicy: "BTP", SecurityPolicy: "SP", EnvoyExtensionPolicy: "EEP",
  BackendTLSPolicy: "BTLS", CiliumNetworkPolicy: "CNP", CiliumClusterwideNetworkPolicy: "CCNP",
};
export const policyKey = (p: PolicyRefDTO) => `${p.kind}/${p.namespace}/${p.name}`;

// ---- topology warnings -----------------------------------------------------

// The backend's topology warnings are free text (internal/fleet/gateway.go).
// They mean three different things, rendered differently: an optional policy
// kind that is not installed is a capability fact (neutral, one line); a note
// about one route belongs to that route's inspector (its lane already shows
// the diagnosis); anything else - a list that failed, a selector Klyx did not
// evaluate - is an evidence gap and stays in the notice.
export type SortedWarnings = { absentKinds: string[]; byRoute: Map<string, string[]>; gaps: string[] };
const ROUTE_NOTE = /^route ([^/\s]+\/[^:\s]+)(?::| has )/;
export function sortWarnings(warnings: string[] = []): SortedWarnings {
  const out: SortedWarnings = { absentKinds: [], byRoute: new Map(), gaps: [] };
  for (const w of warnings) {
    const absent = /^(\S+) CRD not installed$/.exec(w);
    if (absent) { out.absentKinds.push(absent[1]); continue; }
    const route = ROUTE_NOTE.exec(w);
    if (route) { out.byRoute.set(route[1], [...(out.byRoute.get(route[1]) ?? []), w.slice(`route ${route[1]}`.length).replace(/^:\s*/, "").trim()]); continue; }
    out.gaps.push(w);
  }
  return out;
}
