import type { BackendDTO, CertificateDTO, CondDTO, GatewayRefDTO, ListenerDTO, PolicyRefDTO, ResourceRef, RouteNodeDTO, RuleDTO } from "../store/fleet";
import { clock, type KindEvidence } from "../layout/coverage";

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

type GatewayLike = Pick<GatewayRefDTO, "accepted" | "programmed" | "acceptedCond" | "programmedCond" | "generation"> & { rule?: RuleDTO };

// Gateway roles: an explicit, current Accepted=False is the fault (nothing
// through it is served by its controller). Absent status is unverified, a
// pending decision is progressing, and an explicit not-programmed is
// degraded (no data path configured yet). A last-observed negative is
// degraded "(last observed)" - the current state is unproven.
// The Gateway and route verdicts are the relocated rules computed in Go
// (internal/gwapi rules.go, 1c addendum §2): the lens reads them. The detail
// texts below (gatewayVerdictDetail, routeIssues) stay presentation; the
// golden gate holds their worst issue to the Go verdict.
export const gatewayRole = (g: { rule: RuleDTO }): Role => ({ kind: g.rule.role as Role["kind"], label: g.rule.label });
export const routeRole = (r: { rule: RuleDTO }): Role => ({ kind: r.rule.role as Role["kind"], label: r.rule.label });

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
  // coverage S7a §3: the Go rule degrades a healthy Gateway by its listeners
  if (g.rule?.role === "degraded" && g.rule.label.startsWith("listener ")) return "Accepted and programmed, but its controller reports a listener failing (below).";
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

// routeIssues reads this route's status FOR THIS Gateway and every backend
// that carries traffic. Each issue says how sure Klyx is: fault = explicit
// current negative; degraded = partial or last observed; progressing = the
// controller is deciding; unknown = Klyx could not read or does not evaluate
// it. Weights split traffic within one rule's backendRefs, so each rule is
// judged on its own: a rule whose backends all have weight 0 serves nothing
// (fault); within a rule a weight-0 backend is not diagnosed, and a backend
// fault is degraded - the rule's other backends may still carry its traffic
// - unless every one of them faults.
export function routeIssues(r: Omit<RouteNodeDTO, "rule">, listeners: ListenerDTO[] = []): RouteIssue[] {
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

  if (r.backends.length === 0) {
    out.push({ short: "no backendRefs", text: "No backendRefs - a redirect or filter-only route, or a misconfiguration. Klyx does not evaluate filters.", severity: "unknown" });
    return out;
  }
  const order: number[] = [];
  const byRule = new Map<number, BackendDTO[]>();
  for (const b of r.backends) {
    const ri = b.rule ?? 0;
    if (!byRule.has(ri)) { byRule.set(ri, []); order.push(ri); }
    if (carriesTraffic(b)) byRule.get(ri)!.push(b);
  }
  const named = r.backends.filter(carriesTraffic).length > 1;
  for (const ri of order) {
    const carrying = byRule.get(ri)!;
    if (carrying.length === 0) {
      out.push(order.length > 1
        ? { short: `rule ${ri + 1}: every backend has weight 0`, text: `Every backendRef in rule ${ri + 1} has weight 0, so that rule's traffic reaches no backend.`, severity: "fault" }
        : { short: "every backend has weight 0", text: "Every backendRef has weight 0, so no backend receives this rule's traffic.", severity: "fault" });
      continue;
    }
    const found = carrying.flatMap((b) => {
      const is = backendIssue(b);
      return is ? [{ b, is }] : [];
    });
    const faults = found.filter((x) => x.is.severity === "fault").length;
    for (const { b, is } of found) {
      const demote = carrying.length > 1 && is.severity === "fault" && faults < carrying.length;
      out.push({
        short: named ? `${b.name}: ${is.short}` : is.short,
        text: `${named ? `${b.namespace}/${b.name}: ` : ""}${is.text}${demote ? " Other backends in its rule may still carry the traffic." : ""}`,
        severity: demote ? "degraded" : is.severity,
      });
    }
  }
  return out;
}

/** A backend carries traffic when its weight is unset (default 1) or positive. */
export const carriesTraffic = (b: BackendDTO) => b.weightSet !== true || b.weight > 0;

/** One backend's own issue, as if it were the only backend; null when Klyx found nothing wrong. */
export function backendIssue(b: BackendDTO): RouteIssue | null {
  if (b.state === "unsupported" || (b.state === undefined && b.kind !== "Service")) {
    return { short: `${b.kind} backend not evaluated`, text: `The backend is a ${b.kind}; Klyx evaluates Service backends only.`, severity: "unknown" };
  }
  if (b.state === "not-found") return { short: "backend not found", text: `Backend Service ${b.namespace}/${b.name} not found.`, severity: "fault" };
  if (b.state === "no-port") {
    // coverage S7b §5: the Service port by number and the route's transport
    const t = b.transport || "TCP";
    const other = /\(([^)]*)\)/.exec(b.error ?? "")?.[1];
    return { short: `no ${t} port ${b.port}`, text: `Backend Service ${b.namespace}/${b.name} has no ${t} port ${b.port}${other ? ` - ${other}` : ""}.`, severity: "fault" };
  }
  if (b.state === "unreadable") return { short: "backend unreadable", text: `Backend Service ${b.namespace}/${b.name} could not be read${b.error ? ` - ${b.error}` : ""}; whether it exists is unknown.`, severity: "unknown" };
  if (b.state === "resolved") {
    const ep = b.endpoints;
    if (!ep || ep.error) return { short: "endpoints unreadable", text: `The backend's EndpointSlices could not be read${ep?.error ? ` - ${ep.error}` : ""}.`, severity: "unknown" };
    if (ep.total === 0) return { short: "no endpoints", text: "The backend Service has no endpoints.", severity: "fault" };
    if (ep.ready === 0) return { short: "no ready endpoints", text: `${ep.total === 1 ? "The backend's only endpoint is not ready" : `None of the backend's ${ep.total} endpoints is ready`}.`, severity: "fault" };
    if (ep.ready < ep.total) return { short: `${ep.ready}/${ep.total} endpoints ready`, text: `${endpointsText(ep.ready, ep.total, ep.nonPod)} - reduced capacity.`, severity: "degraded" };
    return null;
  }
  return { short: "backend not evaluated", text: "Klyx has no evidence for this backend.", severity: "unknown" };
}

/** What the lane's backend box says about the primary backend. */
export function primaryBackendText(r: RouteNodeDTO): string {
  const b = r.backends[0];
  if (!b) return "no backendRefs";
  if (b.state === "not-found") return "not found";
  if (b.state === "no-port") return `no ${b.transport || "TCP"} port ${b.port}`;
  if (b.state === "unreadable") return "unreadable";
  if (b.state === "unsupported" || b.kind !== "Service") return `${b.kind} - not evaluated`;
  const svc = r.services.find((s) => s.namespace === b.namespace && s.name === b.name);
  const ep = b.endpoints;
  const eps = !ep ? "" : ep.error ? " · endpoints unreadable" : ` · ${ep.ready}/${ep.total} endpoints`;
  return `${svc?.type ?? "Service"} :${b.port || svc?.port || "?"}${eps}`;
}

// routeKey is a route's namespace/name: the HTTPRoute traffic reads join by it.
export const routeKey = (r: { namespace: string; name: string }) => `${r.namespace}/${r.name}`;

// ---- route kinds and attachments (coverage S7b §5, §9) ---------------------

export const ROUTE_KINDS = ["HTTPRoute", "GRPCRoute", "TLSRoute", "TCPRoute", "UDPRoute"] as const;
const ROUTE_PLURALS: Record<string, string> = { HTTPRoute: "httproutes", GRPCRoute: "grpcroutes", TLSRoute: "tlsroutes", TCPRoute: "tcproutes", UDPRoute: "udproutes" };
/** A route's kind; a payload from before S7b carries none and is an HTTPRoute. */
export const routeKindOf = (r: { kind?: string }) => r.kind || "HTTPRoute";
/** The route kind a resource plural names, or "" for another resource. */
export const routeKindOfPlural = (plural: string) => Object.keys(ROUTE_PLURALS).find((k) => ROUTE_PLURALS[k] === plural) ?? "";
export const isL4 = (kind: string) => kind === "TCPRoute" || kind === "UDPRoute";
/** routeId names a route by kind, namespace and name: two kinds never collide. */
export const routeId = (r: { kind?: string; namespace: string; name: string }) => `${routeKindOf(r)}/${r.namespace}/${r.name}`;
/** laneKey names a topology lane: the route and the attachment it draws. */
export const laneKey = (r: { kind?: string; namespace: string; name: string; attachment?: string }) => (r.attachment ? `${routeId(r)}@${r.attachment}` : routeId(r));
/**
 * resolveLane finds a selection's lane: the lane key itself; else a route's
 * identity - kind/namespace/name, or the older namespace/name of an
 * HTTPRoute - names that route's worst lane.
 */
export function resolveLane(key: string | null, lanes: RouteNodeDTO[]): RouteNodeDTO | undefined {
  if (!key) return undefined;
  const exact = lanes.find((r) => laneKey(r) === key);
  if (exact) return exact;
  const id = key.split("@")[0];
  const parts = id.split("/");
  const want = parts.length === 2 ? `HTTPRoute/${id}` : id;
  const mine = lanes.filter((r) => routeId(r) === want);
  return mine.sort((a, b) => RANK[routeRole(a).kind] - RANK[routeRole(b).kind])[0];
}
/** A route's object reference, at the version it was read when known. */
export function routeRef(kind: string, apiVersion?: string): ResourceRef {
  const version = apiVersion?.includes("/") ? apiVersion.split("/")[1] : "v1";
  return { group: "gateway.networking.k8s.io", version, plural: ROUTE_PLURALS[kind] ?? "httproutes", kind: ROUTE_PLURALS[kind] ? kind : "HTTPRoute", scope: "Namespaced" };
}

/** Where a lane attaches: its listener, port or ListenerSet. */
export function attachmentWhere(r: Pick<RouteNodeDTO, "parentRef" | "parentSectionName">): string {
  const p = r.parentRef;
  const section = p?.sectionName ?? r.parentSectionName ?? "";
  const port = p?.port ? `:${p.port}` : "";
  const listener = section ? `listener ${section}${port}` : port ? `port ${p!.port}` : "all listeners";
  return p?.kind === "ListenerSet" ? `ListenerSet ${p.name} · ${listener}` : listener;
}

/** What one rule matches, in its kind's words (S7b §5's table). */
export function ruleMatchText(kind: string, r: Pick<RouteNodeDTO, "hostnames">, matches: RouteNodeDTO["matches"], scoped: ListenerDTO[]): string {
  if (isL4(kind)) {
    const on = scoped.map((l) => `${l.name || l.protocol}${l.port > 0 ? ` :${l.port}` : ""}`).join(", ");
    return `every connection${on ? ` on ${on}` : ""}`;
  }
  if (kind === "TLSRoute") return r.hostnames.length > 0 ? `SNI ${r.hostnames.join(", ")}` : "any SNI";
  if (kind === "GRPCRoute") return matches.length > 0 ? matches.map((m) => `${m.grpcService || "any service"}/${m.grpcMethod || "any method"}`).join(", ") : "every call";
  return matches.length > 0 ? matches.map((m) => `${m.method || ""}${m.method ? " " : ""}${m.pathValue || "/"}`).join(", ") : "every request";
}

/** What a lane's route box says it matches. */
export function laneMatchText(r: RouteNodeDTO): string {
  const kind = routeKindOf(r);
  if (isL4(kind)) return "every connection";
  const hosts = `${r.hostnames[0] ?? (kind === "TLSRoute" ? "any SNI" : "any host")}${r.hostnames.length > 1 ? ` +${r.hostnames.length - 1}` : ""}`;
  if (kind === "TLSRoute") return `SNI ${hosts}`;
  const m = r.matches[0];
  const first = kind === "GRPCRoute" ? (m ? `${m.grpcService || "any service"}/${m.grpcMethod || "any method"}` : "every call") : (m?.pathValue ?? "/");
  return `${hosts} · ${first}${r.matches.length > 1 ? ` +${r.matches.length - 1}` : ""}`;
}

/**
 * experimentalText is the one line the lens says about the experimental
 * channel's own group (S7b §7): its served kinds are recognised, never read
 * or judged. "" when the group is not served.
 */
export function experimentalText(kinds?: string[] | null, gap?: string): string {
  const ks = kinds ?? [];
  const parts: string[] = [];
  if (ks.length > 0) parts.push(`Experimental Gateway API kinds served: ${ks.join(", ")}; Klyx does not read them`);
  if (ks.includes("XListenerSet")) parts.push("Gateway API 1.5 promoted XListenerSet to ListenerSet (gateway.networking.k8s.io/v1)");
  if (gap) parts.push(ks.length > 0 ? `${gap}, so the list may be partial` : `The experimental Gateway API group is served, but ${gap}`);
  return parts.length > 0 ? `${parts.join(". ")}.` : "";
}

/** HTTP request metrics read HTTPRoute rules only (S7b §9). */
export const httpMetricsApply = (r: { kind?: string }) => routeKindOf(r) === "HTTPRoute";
export const noHttpMetricsText = (r: { kind?: string }) => `HTTP request metrics do not apply to ${routeKindOf(r)}`;

/**
 * laneIssues is routeIssues with the attachment's own verdict first when the
 * attachment rule decided before the route's status could (S7b §5): the
 * parent missing or not established, a ListenerSet not accepted, or the
 * controller judging the parent not established.
 */
export function laneIssues(r: RouteNodeDTO, listeners: ListenerDTO[] = []): RouteIssue[] {
  const issues = routeIssues(r, listeners);
  const role = routeRole(r);
  if (role.kind === "ok" || issues.some((i) => i.short === role.label)) return issues;
  const parent = r.parentRef ? `${r.parentRef.kind} ${r.parentRef.namespace}/${r.parentRef.name}` : "parent";
  let text = `${role.label.charAt(0).toUpperCase()}${role.label.slice(1)}.`;
  if (role.label.startsWith("the controller judging")) text = `The controller expected to judge this route on ${parent} is not established${r.controllerWhy ? ` - ${r.controllerWhy}` : ""}. Klyx judges only the expected controller's status entry and does not choose among others.`;
  else if (role.label.endsWith(" not found")) text = `The ${parent} does not exist in a complete, current read.`;
  else if (role.label.endsWith(" not established")) text = `Whether the ${parent} exists is not established - its read is not complete and current, so nothing is claimed.`;
  else if (role.label === "parent ListenerSet not accepted") text = `Its ${parent} is not accepted by its Gateway's controller, so nothing attaches through it.`;
  else if (role.label.startsWith("no status for this ")) text = `No status from the expected controller for this ${r.parentRef?.kind ?? "parent"} - attachment is unverified.`;
  // the route's own "no status for this Gateway" is the same fact, said once
  return [{ short: role.label, text, severity: role.kind }, ...issues.filter((i) => i.short !== "no status for this Gateway")];
}
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
// Secret; expiry is checked before Ready; Unknown stays neutral. A stale
// read names its last good time when the Observer knows it.
export function certStatus(c: CertificateDTO | undefined, evidence: KindEvidence, nowS: number, lastGood?: string | null): { text: string; color: string } {
  if (evidence === "pending" || evidence === "failed") return { text: "Certificates not read - cert-manager state unknown", color: "var(--color-text-tertiary)" };
  const last = evidence === "stale" ? (lastGood ? ` (last observed ${clock(lastGood)})` : " (last observed)") : "";
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
  listenerSet: { group: gw, version: "v1", plural: "listenersets", kind: "ListenerSet", scope: "Namespaced" },
  service: { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" },
  secret: { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" },
  certificate: { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" },
} satisfies Record<string, ResourceRef>;

export const POLICY_REFS: Record<string, ResourceRef> = {
  // v1 since Gateway API 1.4; 1.6 serves no other version
  BackendTLSPolicy: { group: gw, version: "v1", plural: "backendtlspolicies", kind: "BackendTLSPolicy", scope: "Namespaced" },
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
// byRoute is keyed by routeId: the backend names the route's kind (S7b),
// and a note without one is an HTTPRoute's.
export type SortedWarnings = { absentKinds: string[]; byRoute: Map<string, string[]>; gaps: string[] };
const ROUTE_NOTE = /^route (?:([A-Z][A-Za-z]*Route) )?([^/\s]+\/[^:\s]+)(?::| has )/;
export function sortWarnings(warnings: string[] = []): SortedWarnings {
  const out: SortedWarnings = { absentKinds: [], byRoute: new Map(), gaps: [] };
  for (const w of warnings) {
    const absent = /^(\S+) CRD not installed$/.exec(w);
    if (absent) { out.absentKinds.push(absent[1]); continue; }
    const route = ROUTE_NOTE.exec(w);
    if (route) {
      const id = `${route[1] ?? "HTTPRoute"}/${route[2]}`;
      const head = `route ${route[1] ? `${route[1]} ` : ""}${route[2]}`;
      out.byRoute.set(id, [...(out.byRoute.get(id) ?? []), w.slice(head.length).replace(/^:\s*/, "").trim()]);
      continue;
    }
    out.gaps.push(w);
  }
  return out;
}
