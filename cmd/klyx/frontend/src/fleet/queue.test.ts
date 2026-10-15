import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary, type HealthRef } from "../store/health";
import type { ClusterDTO } from "../store/fleet";
import { core, dep, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention } from "../nav/clusterAttention";
import { fleetQueue, objectKindOf, refTime, type QueueRow } from "./queue";

// The Line slice 5 §4 and parent §12: the queue's rows, words, order,
// filters and the clusters it has not read.

const cl = (name: string, env = "dev", state = "Synced", over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, state, reason: "", env, region: "westeurope", ...over } as ClusterDTO);
const ref = (gvr: string, name: string, namespace?: string, label = "CrashLoopBackOff", role = "fault", over: Partial<HealthRef> = {}): HealthRef =>
  ({ cluster: "x", gvr, namespace, name, role, label, uid: `uid-${name}`, ...over });
const queue = (clusters: ClusterDTO[], filter: "all" | "prd" | "faults" = "all") =>
  fleetQueue(clusters, Object.fromEntries(clusters.map((c) => [c.name, clusterAttention(useHealth.getState(), c.name, c)])), filter);
const brief = (r: QueueRow) => r.kind === "object" ? `${r.cluster} ${r.objectKind} ${r.ref.namespace ? `${r.ref.namespace}/` : ""}${r.ref.name} ${r.label}` : `${r.cluster} ${r.kind === "more" ? `+${r.count} ${r.lens}` : r.text}`;
const pods = (cluster: string, n: number, names: string[]) =>
  applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: n, ok: 1 }, [kind("pods")], { top: names.map((x) => ref("v1/pods", x, "shop")) })])), {}, cluster));

beforeEach(() => resetHealthStore());

describe("fleetQueue rows (parent §12)", () => {
  it("more than five faults in a part: the five samples, then +N more in the lens", () => {
    pods("a", 7, ["p1", "p2", "p3", "p4", "p5"]);
    const q = queue([cl("a")]);
    expect(q.rows.map(brief)).toEqual([
      "a Pod shop/p1 CrashLoopBackOff", "a Pod shop/p2 CrashLoopBackOff", "a Pod shop/p3 CrashLoopBackOff", "a Pod shop/p4 CrashLoopBackOff", "a Pod shop/p5 CrashLoopBackOff",
      "a +2 Pods",
    ]);
    expect(q.faults).toBe(5);
  });

  it("a paused native ref is never a row; a degraded one is a look-at row", () => {
    applySummary(summary([...core(), task("flux", [
      native("reconciliations", { fault: 1, paused: 1, degraded: 1 }, 3, "not ready", [kind("kustomizations")], { top: [
        ref("kustomize.toolkit.fluxcd.io/v1/kustomizations", "apps", "flux-system", "Failed"),
        ref("kustomize.toolkit.fluxcd.io/v1/kustomizations", "legacy", "flux-system", "Suspended", "paused"),
        ref("kustomize.toolkit.fluxcd.io/v1/kustomizations", "slow", "flux-system", "Progressing", "degraded"),
      ] }),
      native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")])])], {}, "a"));
    const q = queue([cl("a", "dev", "Synced", { fluxPresent: true })]);
    expect(q.rows.filter((r) => r.kind === "object").map((r) => [brief(r), r.tone])).toEqual([
      ["a Kustomization flux-system/apps Failed", "danger"],
      ["a Kustomization flux-system/slow Progressing", "warning"],
    ]);
    expect(q.rows.some((r) => r.kind === "object" && r.ref.name === "legacy")).toBe(false);
    expect(q.attention).toBe(1);
  });

  it("a controller-only failure is a row of its own, with its reason", () => {
    applySummary(summary([...core(), task("argo", [native("applications", { ok: 2 }, 0, "out of sync or unhealthy", [kind("applications")]), native("applicationsets", { ok: 1 }, 0, "need attention", [kind("applicationsets")])], { controller: { state: "not ready", reason: "0/1 ready" } })], {}, "a"));
    const q = queue([cl("a", "dev", "Synced", { argoPresent: true })]);
    expect(q.rows.map((r) => [r.kind, brief(r), r.tone])).toEqual([["controller", "a Argo CD: controller not ready - 0/1 ready", "danger"]]);
    expect(q.faults).toBe(1);
  });

  it("an unreadable kind is a coverage row, beside a lit part's samples", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 1, ok: 4 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })], { top: [ref("v1/pods", "api", "shop")] })])), {}, "a"));
    const q = queue([cl("a")]);
    expect(q.rows.map((r) => [r.kind, brief(r)])).toEqual([
      ["object", "a Pod shop/api CrashLoopBackOff"],
      ["coverage", "a Pods: ReplicaSets could not be read - a dependency"],
    ]);
  });

  it("same-name objects on two clusters are two rows, each its own cluster's and UID's", () => {
    pods("a", 1, ["api"]);
    pods("b", 1, ["api"]);
    const q = queue([cl("a"), cl("b")]);
    const objs = q.rows.filter((r): r is Extract<QueueRow, { kind: "object" }> => r.kind === "object");
    expect(objs.map((r) => [r.cluster, r.ref.name, r.key])).toEqual([["a", "api", "a/pods/pods/uid-api"], ["b", "api", "b/pods/pods/uid-api"]]);
  });
});

describe("words, order and filters", () => {
  it("a rule-backed since, else condition evidence, else no time", () => {
    expect(refTime(ref("v1/pods", "a", "s", "x", "fault", { since: new Date(2026, 9, 15, 9, 0).toISOString() }))).toBe("since 09:00");
    expect(refTime(ref("v1/pods", "a", "s", "x", "fault", { condition: { type: "Ready", status: "False", since: new Date(2026, 9, 15, 9, 0).toISOString() } }))).toBe("Ready=False since 09:00 (condition time)");
    expect(refTime(ref("v1/pods", "a"))).toBeNull();
  });

  it("kinds come from the ref's GVR", () => {
    expect(objectKindOf("v1/pods")).toBe("Pod");
    expect(objectKindOf("apps/v1/deployments")).toBe("Deployment");
    expect(objectKindOf("cert-manager.io/v1/clusterissuers")).toBe("ClusterIssuer");
    expect(objectKindOf("example.com/v1/widgets")).toBe("Widget");
  });

  it("prd first, then fault before look-at, then area order; Prd only and Faults filter", () => {
    pods("dev1", 1, ["d"]);
    applySummary(summary(replace(replace(core(), task("workloads", [verdict("workloads", { degraded: 1 }, [kind("deployments")], { top: [ref("apps/v1/deployments", "w", "shop", "Unavailable", "degraded")] })])),
      task("pods", [verdict("pods", { fault: 1 }, [kind("pods")], { top: [ref("v1/pods", "p", "shop")] })])), {}, "prd1"));
    const clusters = [cl("dev1"), cl("prd1", "prd")];
    expect(queue(clusters).rows.map(brief)).toEqual(["prd1 Pod shop/p CrashLoopBackOff", "prd1 Deployment shop/w Unavailable", "dev1 Pod shop/d CrashLoopBackOff"]);
    expect(queue(clusters, "prd").rows.map((r) => r.cluster)).toEqual(["prd1", "prd1"]);
    expect(queue(clusters, "faults").rows.map(brief)).toEqual(["prd1 Pod shop/p CrashLoopBackOff", "dev1 Pod shop/d CrashLoopBackOff"]);
  });

  it("a cluster with no reading is named as not read, never quiet", () => {
    applySummary(summary(core(), {}, "e"));
    applySummary(summary([], { seq: 2, final: true }, "e"));
    const q = queue([cl("x", "dev", "Failed", { reason: "dial tcp" }), cl("n", "dev", "Connecting"), cl("e"), cl("c")]);
    expect(q.rows).toEqual([]);
    expect(q.notRead.map((n) => [n.cluster, n.why.replace(/ at .*/, " at T")])).toEqual([
      ["x", "failed: dial tcp"], ["n", "connecting"], ["e", "observation ended at T"], ["c", "health checking…"],
    ]);
  });
});
