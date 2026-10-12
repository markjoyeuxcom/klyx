import type { Counts, HealthPart, HealthSummary, HealthTask, KindCoverage } from "../store/health";

// Health summary fixtures (1c slice 4 tests): parts and tasks as the
// Observer publishes them.

export const C = "c1";
export const zero: Counts = { total: 0, fault: 0, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0 };
export const kind = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => ({ kind: k, role: "counted", scope: "", coverage: "live", objects: 1, ...over });
export const dep = (k: string, over: Partial<KindCoverage> = {}): KindCoverage => kind(k, { role: "dependency", ...over });
export const verdict = (id: string, counts: Partial<Counts>, kinds: KindCoverage[], over: Partial<HealthPart> = {}): HealthPart => ({
  id, entity: id, scope: "all namespaces", classifier: "verdict", coverage: "live", kinds, obs: 1, ver: 1, counts: { ...zero, ...counts }, ...over,
});
export const native = (id: string, roles: Partial<Counts>, included: number, word: string, kinds: KindCoverage[], over: Partial<HealthPart> = {}): HealthPart => ({
  id, entity: id, scope: "all namespaces", classifier: "native", coverage: "live", kinds, obs: 1, ver: 1,
  native: { total: Object.values(roles).reduce((a, b) => a + (b ?? 0), 0), roles: { ...zero, ...roles }, included, word }, ...over,
});
export const evidence = (n: number, kinds: KindCoverage[], over: Partial<HealthPart> = {}): HealthPart => ({
  id: "warnings", entity: "warning events", scope: "all namespaces", classifier: "evidence", coverage: "live", kinds, obs: 1, ver: 1, evidence: n, ...over,
});
export const task = (id: string, parts: HealthPart[], over: Partial<HealthTask> = {}): HealthTask => ({ id, coverage: "live", parts, ...over });
export const absentPart = (id: string, k: string): HealthPart => native(id, {}, 0, "x", [kind(k, { coverage: "absent" })], { coverage: "absent" });
// every core task, quiet and live
export const core = (): HealthTask[] => [
  task("workloads", [verdict("workloads", { ok: 3 }, [kind("deployments"), kind("statefulsets"), kind("daemonsets"), dep("replicasets")])]),
  task("pods", [verdict("pods", { ok: 9 }, [kind("pods"), dep("replicasets")])]),
  task("nodes", [verdict("nodes", { ok: 1 }, [kind("nodes")])]),
  task("events", [evidence(0, [kind("events")], { asOf: "2026-10-12T12:00:00Z" })]),
  task("helm", [native("releases", { ok: 2 }, 0, "failed or pending", [kind("helm")])]),
];
export const summary = (tasks: HealthTask[], over: Partial<HealthSummary> = {}, cluster = C): HealthSummary => ({
  cluster, incarnation: 1, seq: 1, evaluatedAt: "2026-10-12T12:00:00Z", connection: "connected", namespaces: 4, tasks, ...over,
});
export const replace = (tasks: HealthTask[], t: HealthTask) => tasks.map((x) => (x.id === t.id ? t : x));
