import type { CertificateDTO, CondDTO, GatewayRefDTO, ListenerDTO, PolicyRefDTO, ResourceRef, RouteNodeDTO } from "../store/fleet";
import type { KindEvidence } from "../layout/coverage";

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

// ---- condition evidence ----------------------------------------------------

// One condition as Klyx may claim it. "absent" = no controller has written
// it (no evidence either way); "Unknown" = the controller says it is still
// deciding; "False" = an explicit negative. A condition observed for an
// older generation is last-observed: it describes the previous spec, so a
// stale False is never asserted as the current state.
export type CondState = { status: "True" | "False" | "Unknown" | "absent"; reason: string; message: string; stale: boolean; observedGeneration: number };
export function condState(c: CondDTO | undefined, generation: number | undefined, fallback?: boolean): CondState {
  // An older payload without condition evidence: True stays True, but false
  // is only "not True" - never promoted to an explicit False.
  if (!c) return { status: fallback ? "True" : "absent", reason: "", message: "", stale: false, observedGeneration: 0 };
  const status = c.status === "True" || c.status === "False" || c.status === "Unknown" ? c.status : "absent";
  // A condition proves the CURRENT spec only when it names the current
  // generation; a missing (0) or older observedGeneration is last observed.
  const stale = status !== "absent" && !!generation && c.observedGeneration < generation;
  return { status, reason: c.reason, message: c.message, stale, observedGeneration: c.observedGeneration };
}
const because = (c: CondState) => [c.reason, c.message].filter(Boolean).join(": ");
const observedFor = (c: CondState) => (c.observedGeneration > 0 ? `generation ${c.observedGeneration}` : "an unreported generation");

type GatewayLike = Pick<GatewayRefDTO, "accepted" | "programmed" | "acceptedCond" | "programmedCond" | "generation">;

// Gateway roles: an explicit, current Accepted=False is the fault (nothing
// through it is served by its controller). Absent status is unverified, a
// pending decision is progressing, and an explicit not-programmed is
// degraded (no data path configured yet). A last-observed negative is
// degraded "(last observed)" - the current state is unproven.
export function gatewayHealth(g: GatewayLike): Role {
  const a = condState(g.acceptedCond, g.generation, g.accepted);
  if (a.status === "absent") return { kind: "unknown", label: "no status reported" };
  if (a.status === "Unknown") return { kind: "progressing", label: "acceptance pending" };
  if (a.status === "False") return a.stale ? { kind: "degraded", label: "not accepted (last observed)" } : { kind: "fault", label: "not accepted" };
  const p = condState(g.programmedCond, g.generation, g.programmed);
  if (p.status === "absent") return { kind: "unknown", label: "programming not reported" };
  if (p.status === "Unknown") return { kind: "progressing", label: "programming" };
  if (p.status === "False") return { kind: "degraded", label: p.stale ? "not programmed (last observed)" : "not programmed" };
  if (a.stale || p.stale) return { kind: "progressing", label: "syncing" };
  return { kind: "ok", label: "programmed" };
}

export function gatewayVerdictDetail(g: GatewayLike): string {
  const a = condState(g.acceptedCond, g.generation, g.accepted);
  const p = condState(g.programmedCond, g.generation, g.programmed);
  if (a.status === "absent") return "No controller has written an Accepted condition - check that the GatewayClass exists and its controller is running. Klyx cannot tell whether it will be accepted.";
  if (a.status === "Unknown") return `The controller has not decided yet${because(a) ? ` (${because(a)})` : ""}.`;
  if (a.status === "False") return a.stale
    ? `Accepted was False for ${observedFor(a)}${because(a) ? ` - ${because(a)}` : ""}. The controller has not reported on generation ${g.generation}; whether the current Gateway is accepted is unverified.`
    : `Accepted is False${because(a) ? ` - ${because(a)}` : ""}. Its controller does not serve routes through it.`;
  if (p.status === "absent") return "Accepted, but no Programmed condition has been written - whether the data plane is configured is unverified.";
  if (p.status === "Unknown") return `Accepted; programming is in progress${because(p) ? ` (${because(p)})` : ""}.`;
  if (p.status === "False") return p.stale
    ? `Programmed was False for ${observedFor(p)}${because(p) ? ` - ${because(p)}` : ""}. The controller has not reported on generation ${g.generation}; whether the data plane is configured now is unverified.`
    : `Accepted, but Programmed is False${because(p) ? ` - ${because(p)}` : ""}. The data plane is not configured for it.`;
  if (a.stale || p.stale) return `Accepted and programmed as of ${observedFor(p.stale ? p : a)}; the controller has not reported on generation ${g.generation} yet.`;
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

export type RouteIssue = { short: string; text: string; severity: Role["kind"] };

const endpointsText = (r: number, t: number, nonPod: number) => `${r}/${t} endpoints ready${nonPod > 0 ? ` (${nonPod} not backed by a pod)` : ""}`;

// routeIssues reads this route's status FOR THIS Gateway and its primary
// backend (backends[0], the lane). Each issue says how sure Klyx is:
// fault = explicit current negative; degraded = partial or last observed;
// progressing = the controller is deciding; unknown = Klyx could not read
// or does not evaluate it. With several backends, a primary-backend
// problem is degraded: other backends may still carry traffic.
export function routeIssues(r: RouteNodeDTO, listeners: ListenerDTO[] = []): RouteIssue[] {
  const out: RouteIssue[] = [];
  const legacy = r.parentFound === undefined; // older payload: booleans only
  if (!legacy && !r.parentFound) {
    out.push({ short: "no status for this Gateway", text: "No controller has written status for this route's parent - attachment is unverified.", severity: "unknown" });
  } else {
    const a = condState(r.acceptedCond, r.generation, r.accepted);
    if (a.status === "False") out.push(a.stale
      ? { short: "not accepted (last observed)", text: `Accepted was False for ${observedFor(a)}${because(a) ? ` - ${because(a)}` : ""}; whether generation ${r.generation} is accepted is unverified.`, severity: "degraded" }
      : { short: "not accepted", text: `Not accepted by this Gateway${because(a) ? ` - ${because(a)}` : ""}.`, severity: "fault" });
    else if (a.status === "Unknown") out.push({ short: "acceptance pending", text: `The controller has not decided${because(a) ? ` (${because(a)})` : ""}.`, severity: "progressing" });
    else if (a.status === "absent") out.push({ short: "acceptance not reported", text: "No Accepted condition for this Gateway.", severity: "unknown" });
    const rr = condState(r.resolvedRefsCond, r.generation, r.resolvedRefs);
    if (rr.status === "False") out.push(rr.stale
      ? { short: "unresolved refs (last observed)", text: `ResolvedRefs was False for ${observedFor(rr)}${because(rr) ? ` - ${because(rr)}` : ""}; the current generation is unverified.`, severity: "degraded" }
      : { short: "unresolved refs", text: `References not resolved${because(rr) ? ` - ${because(rr)}` : ""}. Check the backendRef, a cross-namespace ReferenceGrant, or the listener section.`, severity: "fault" });
    else if (rr.status === "Unknown") out.push({ short: "refs pending", text: `ResolvedRefs is Unknown${because(rr) ? ` (${because(rr)})` : ""}.`, severity: "progressing" });
    else if (rr.status === "absent") out.push({ short: "refs not reported", text: "No ResolvedRefs condition for this Gateway - whether the references resolve is unverified.", severity: "unknown" });
    // positive conditions for an older generation describe the previous spec
    const stalePositive = [a, rr].filter((c) => c.status === "True" && c.stale);
    if (stalePositive.length > 0) out.push({ short: "status syncing", text: `Accepted/ResolvedRefs describe ${observedFor(stalePositive[0])}; the controller has not reported on generation ${r.generation} yet.`, severity: "progressing" });
  }
  const names = new Set(listeners.map((l) => l.name).filter(Boolean));
  if (r.parentSectionName && names.size > 0 && !names.has(r.parentSectionName)) out.push({ short: "listener missing", text: `Listener ${r.parentSectionName} does not exist on this Gateway.`, severity: "fault" });

  const b = r.backends[0];
  const several = r.backends.length > 1;
  const primary = (sev: "fault" | "degraded") => (several && sev === "fault" ? "degraded" : sev);
  const scope = several ? ` This is the primary backend; ${r.backends.length - 1} other backend${r.backends.length === 2 ? " is" : "s are"} not diagnosed on the lane.` : "";
  if (!b) {
    out.push({ short: "no backendRefs", text: "No backendRefs - a redirect or filter-only route, or a misconfiguration. Klyx does not evaluate filters.", severity: "unknown" });
  } else if (b.state === "unsupported" || (b.state === undefined && b.kind !== "Service")) {
    out.push({ short: `${b.kind} backend not evaluated`, text: `The primary backend is a ${b.kind}; Klyx evaluates Service backends only.${scope}`, severity: "unknown" });
  } else if (b.state === "not-found") {
    out.push({ short: several ? "primary backend not found" : "backend not found", text: `Backend Service ${b.namespace}/${b.name} not found.${scope}`, severity: primary("fault") });
  } else if (b.state === "unreadable") {
    out.push({ short: "backend unreadable", text: `Backend Service ${b.namespace}/${b.name} could not be read${b.error ? ` - ${b.error}` : ""}; whether it exists is unknown.`, severity: "unknown" });
  } else if (b.state === "resolved") {
    const ep = b.endpoints;
    if (!ep || ep.error) out.push({ short: "endpoints unreadable", text: `The backend's EndpointSlices could not be read${ep?.error ? ` - ${ep.error}` : ""}.`, severity: "unknown" });
    else if (ep.total === 0) out.push({ short: "no endpoints", text: `The backend Service has no endpoints.${scope}`, severity: primary("fault") });
    else if (ep.ready === 0) out.push({ short: "no ready endpoints", text: `${ep.total === 1 ? "The backend's only endpoint is not ready" : `None of the backend's ${ep.total} endpoints is ready`}.${scope}`, severity: primary("fault") });
    else if (ep.ready < ep.total) out.push({ short: `${ep.ready}/${ep.total} endpoints ready`, text: `${endpointsText(ep.ready, ep.total, ep.nonPod)} - reduced capacity.`, severity: "degraded" });
  } else {
    out.push({ short: "backend not evaluated", text: "Klyx has no evidence for the primary backend.", severity: "unknown" });
  }
  return out;
}

export function routeHealth(r: RouteNodeDTO, listeners: ListenerDTO[] = []): Role {
  const issues = routeIssues(r, listeners);
  if (issues.length === 0) return { kind: "ok", label: "resolved" };
  const worst = [...issues].sort((x, y) => RANK[x.severity] - RANK[y.severity])[0];
  return { kind: worst.severity, label: worst.short };
}

/** What the lane's backend box says about the primary backend. */
export function primaryBackendText(r: RouteNodeDTO): string {
  const b = r.backends[0];
  if (!b) return "no backendRefs";
  if (b.state === "not-found") return "not found";
  if (b.state === "unreadable") return "unreadable";
  if (b.state === "unsupported" || b.kind !== "Service") return `${b.kind} - not evaluated`;
  const svc = r.services.find((s) => s.namespace === b.namespace && s.name === b.name);
  const ep = b.endpoints;
  const eps = !ep ? "" : ep.error ? " · endpoints unreadable" : ` · ${ep.ready}/${ep.total} endpoints`;
  return `${svc?.type ?? "Service"} :${b.port || svc?.port || "?"}${eps}`;
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
// Listener TLS evidence from the cert-manager watch (the accepted coverage
// rules): unread means unknown; a stale read is last observed; a complete
// read without a match proves only that no Certificate in it writes this
// Secret; expiry is checked before Ready; Unknown stays neutral.
export function certStatus(c: CertificateDTO | undefined, evidence: KindEvidence, nowS: number): { text: string; color: string } {
  if (evidence === "pending" || evidence === "failed") return { text: "Certificates not read - cert-manager state unknown", color: "var(--color-text-tertiary)" };
  const last = evidence === "stale" ? " (last observed)" : "";
  if (!c) return { text: `no matching Certificate in this read${last}`, color: "var(--color-text-tertiary)" };
  if (c.notAfterUnix > 0 && c.notAfterUnix <= nowS) return { text: `▲ certificate expired${last}`, color: "var(--color-text-danger)" };
  if (c.ready === "Ready") return { text: `certificate ready${last}`, color: "var(--color-text-secondary)" };
  if (c.ready === "Issuing") return { text: `certificate issuing${last}`, color: "var(--color-text-secondary)" };
  if (!c.ready || c.ready === "Unknown") return { text: `certificate state unknown${last}`, color: "var(--color-text-tertiary)" };
  return { text: `certificate ${c.ready.toLowerCase()}${last}`, color: "var(--color-text-warning)" };
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
