import type { ArgoAppDTO, ArgoApplicationSetDTO, CertificateDTO, GatewayRefDTO, HelmReleaseDTO, ListenerDTO, RouteNodeDTO, ExternalSecretDTO, ExternalSecretStoreDTO, FluxResourceDTO, FluxSourceDTO, IssuerDTO, KarpenterClaimDTO, KarpenterPoolDTO, RuleDTO, RuleStepDTO } from "../store/fleet";
import { condState, routeIssues, RANK } from "../cluster/gatewayModel";

// The relocated lens rules exactly as the backend computes them
// (internal/certmanager, internal/externalsecrets, internal/karpenter
// and internal/gitops/{flux,argo} rules.go), so lens tests can build DTOs the way the bridge sends them. Test
// support only - lenses read the Go-computed `rule`. src/health/nativeRules
// .test.ts holds every function here to the Go golden fixture, so this
// module cannot drift from the backend.


const nowSec = () => Math.floor(Date.now() / 1000);
const rule = (included: boolean, role: string, label: string): RuleDTO => ({ included, role, label });

type CertIn = Omit<CertificateDTO, "rule">;
type IssuerIn = Omit<IssuerDTO, "rule">;
type SecretIn = Omit<ExternalSecretDTO, "rule">;
type StoreIn = Omit<ExternalSecretStoreDTO, "rule">;
type ClaimIn = Omit<KarpenterClaimDTO, "rule">;
type PoolIn = Omit<KarpenterPoolDTO, "rule">;

const certExpired = (c: CertIn) => c.notAfterUnix > 0 && c.notAfterUnix <= nowSec();

export function certRule(c: CertIn): RuleDTO {
  const included = c.ready !== "Ready" || certExpired(c);
  if (certExpired(c)) return rule(included, "fault", "Expired");
  if (c.stale) return rule(included, "progressing", "Syncing…");
  if (c.ready === "Failed") return rule(included, "fault", "Failed");
  if (c.ready === "Issuing") return rule(included, "progressing", "Issuing");
  if (c.ready === "Ready") return rule(included, "ok", "Ready");
  if (c.ready === "Unknown" || !c.ready) return rule(included, "unknown", "Unknown");
  return rule(included, "degraded", c.ready);
}

export function issuerRule(i: IssuerIn): RuleDTO {
  const included = i.ready !== "Ready";
  if (i.ready === "Ready") return rule(included, "ok", "Ready");
  if (i.ready === "Failed") return rule(included, "fault", "Failed");
  if (i.ready === "Unknown" || !i.ready) return rule(included, "unknown", "Unknown");
  return rule(included, "degraded", i.ready);
}

export function secretRule(e: SecretIn): RuleDTO {
  const included = e.state !== "Synced" || !!e.stale;
  if (e.state === "Failed") return rule(included, "fault", "Failed");
  if (e.state === "Synced" && e.stale) return rule(included, "degraded", "Stale");
  if (e.state === "Synced") return rule(included, "ok", "Synced");
  if (e.state === "Unknown" || !e.state) return rule(included, "unknown", "Unknown");
  return rule(included, "degraded", e.state);
}

export function storeRule(s: StoreIn): RuleDTO {
  const included = s.state !== "Synced";
  if (s.state === "Synced") return rule(included, "ok", "Ready");
  if (s.state === "Failed") return rule(included, "fault", "Failed");
  if (s.state === "Unknown" || !s.state) return rule(included, "unknown", "Unknown");
  return rule(included, "degraded", s.state);
}

export const LAUNCH_DEADLINE_S = 900;

export function claimRule(c: ClaimIn): RuleDTO {
  if (c.ready === "ready") return rule(false, "ok", "ready");
  if (c.ready === "not-ready") {
    const link = c.failingStatus === "False" ? c.failingType : "";
    return rule(true, "fault", link ? `failed at ${link}` : "not ready");
  }
  const phase = c.nodeName ? "initializing" : "launching";
  if (c.ageSeconds > LAUNCH_DEADLINE_S) return rule(true, "degraded", `${phase}, appears stuck`);
  return rule(false, "progressing", phase);
}

export function poolRule(p: PoolIn): RuleDTO {
  if (p.ready === "ready") return rule(false, "ok", "ready");
  if (p.ready === "not-ready") return rule(true, "fault", "not ready");
  return rule(false, "unknown", "unknown");
}

// Flux (GitOps.tsx needsAttention / sourceNeedsAttention / fluxHealth).
function fluxRole(r: { ready: string; suspended: boolean; stale?: boolean; reason?: string }): [string, string] {
  if (r.suspended) return ["paused", "Suspended"];
  if (r.stale) return ["progressing", "Syncing…"];
  if (r.ready === "Failed") return ["fault", "Failed"];
  if (r.ready === "Reconciling") return ["progressing", "Reconciling"];
  if (r.ready === "Unknown") {
    return /^(Progressing|ProgressingWithRetry|Reconciling)$/.test(r.reason ?? "") ? ["progressing", "Progressing"] : ["unknown", "Unknown"];
  }
  if (r.ready !== "Ready") return ["degraded", r.ready || "Not ready"];
  return ["ok", "Ready"];
}

export function fluxRule(r: Omit<FluxResourceDTO, "rule">): RuleDTO {
  const [role, label] = fluxRole(r);
  return rule(!r.suspended && r.ready !== "Ready", role, label);
}

export function fluxSourceRule(s: Omit<FluxSourceDTO, "rule">): RuleDTO {
  const [role, label] = fluxRole(s);
  return rule(!s.suspended && s.ready !== "Ready", role, label);
}

// Argo CD (Application.Broken / ArgoView.argoHealth; ApplicationSets: not
// healthy, row lamp healthy = ok else degraded "needs attention").
export function argoAppRule(a: Omit<ArgoAppDTO, "rule">): RuleDTO {
  const broken = a.syncStatus !== "Synced" || !["Healthy", "Progressing", "Suspended"].includes(a.healthStatus);
  if (a.healthStatus === "Degraded" || a.healthStatus === "Missing") return rule(broken, "fault", a.healthStatus);
  if (a.healthStatus === "Suspended") return rule(broken, "paused", "Suspended");
  if (a.syncStatus === "OutOfSync") return rule(broken, "degraded", "OutOfSync");
  if (a.healthStatus === "Progressing") return rule(broken, "progressing", "Progressing");
  if (a.healthStatus === "Healthy" && a.syncStatus === "Synced") return rule(broken, "ok", "Healthy");
  return rule(broken, "unknown", a.healthStatus || "Unknown");
}

export function argoAppSetRule(s: Omit<ArgoApplicationSetDTO, "rule">): RuleDTO {
  return s.healthy ? rule(false, "ok", "healthy") : rule(true, "degraded", "needs attention");
}

// Gateway API (gatewayModel gatewayHealth, verbatim; routes: the worst of
// the lens's own routeIssues, so the detail text the topology still renders
// stays held to the Go verdict). Included = role at or above degraded.
type GatewayIn = Pick<GatewayRefDTO, "accepted" | "programmed" | "acceptedCond" | "programmedCond" | "generation">;
export function gatewayRule(g: GatewayIn): RuleDTO {
  const a = condState(g.acceptedCond, g.generation, g.accepted);
  const verdict = (role: string, label: string) => rule(RANK[role as keyof typeof RANK] <= RANK.degraded, role, label);
  if (a.status === "absent") return verdict("unknown", "no status reported");
  if (a.status === "Unknown") return verdict("progressing", "acceptance pending");
  if (a.status === "False") return a.stale ? verdict("degraded", "not accepted (last observed)") : verdict("fault", "not accepted");
  const p = condState(g.programmedCond, g.generation, g.programmed);
  if (p.status === "absent") return verdict("unknown", "programming not reported");
  if (p.status === "Unknown") return verdict("progressing", "programming");
  if (p.status === "False") return verdict("degraded", p.stale ? "not programmed (last observed)" : "not programmed");
  if (a.stale || p.stale) return verdict("progressing", "syncing");
  return verdict("ok", "programmed");
}

export function routeRule(r: Omit<RouteNodeDTO, "rule">, listeners: ListenerDTO[] = []): RuleDTO {
  const issues = routeIssues(r, listeners);
  if (issues.length === 0) return rule(false, "ok", "resolved");
  const worst = [...issues].sort((x, y) => RANK[x.severity] - RANK[y.severity])[0];
  return rule(RANK[worst.severity] <= RANK.degraded, worst.severity, worst.short);
}

// withRule stamps a fixture with the backend's rule unless the test set one.
export function withRule<T extends { rule?: RuleDTO }>(dto: Omit<T, "rule"> & { rule?: RuleDTO }, f: (d: Omit<T, "rule">) => RuleDTO): T {
  return { ...dto, rule: dto.rule ?? f(dto) } as T;
}

// Helm (helmHealth.ts releaseHealth + HelmView needsAttention, verbatim):
// the rule at read time and its one time step, as the bridge sends them.
type ReleaseIn = Omit<HelmReleaseDTO, "rule" | "then">;
const PENDING_DEADLINE_GO_S = 600;
const IN_FLIGHT = new Set(["pending-install", "pending-upgrade", "pending-rollback", "uninstalling"]);
export function helmRule(r: ReleaseIn, nowS: number): RuleDTO {
  const verdict = (role: string, label: string) => rule(role === "fault" || role === "degraded", role, label);
  if (r.status === "deployed") return verdict("ok", "deployed");
  if (r.status === "failed") return verdict("fault", "failed");
  if (IN_FLIGHT.has(r.status)) {
    if (r.updatedUnix > 0 && nowS - r.updatedUnix > PENDING_DEADLINE_GO_S) return verdict("degraded", `${r.status}, appears stuck`);
    return verdict("progressing", r.status);
  }
  if (r.status === "superseded" || r.status === "uninstalled") return verdict("unknown", `${r.status} (historical)`);
  return verdict("unknown", r.status || "unknown");
}
export function helmStep(r: ReleaseIn, nowS: number): RuleStepDTO | undefined {
  if (!IN_FLIGHT.has(r.status) || r.updatedUnix <= 0 || nowS - r.updatedUnix > PENDING_DEADLINE_GO_S) return undefined;
  const atUnix = r.updatedUnix + PENDING_DEADLINE_GO_S + 1;
  return { atUnix, rule: helmRule(r, atUnix) };
}
// helmRelease stamps a fixture the way ListHelmReleases does, read now.
export function helmRelease(r: ReleaseIn): HelmReleaseDTO {
  const at = nowSec();
  const then = helmStep(r, at);
  return { ...r, rule: helmRule(r, at), ...(then ? { then } : {}) };
}
