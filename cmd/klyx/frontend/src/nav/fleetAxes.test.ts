import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary } from "../store/health";
import type { ClusterDTO } from "../store/fleet";
import { core, dep, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention } from "./clusterAttention";
import { fleetAxes, fleetAxesLabel, type FleetAxis } from "./fleetAxes";

// The Line slice 4 §3: the fleet indicator counts clusters per axis, with a
// reason each - fault and warning exclusive, unreachable alone, the rest
// independent facts. Parent §12: a remote warning, a controller-only fault,
// a failed connection and a lost watch, read while inside another cluster.

const cl = (name: string, state = "Synced", over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, state, reason: "", env: "", region: "", ...over } as ClusterDTO);
const axesOf = (clusters: ClusterDTO[]) => fleetAxes(clusters, Object.fromEntries(clusters.map((c) => [c.name, clusterAttention(useHealth.getState(), c.name, c)])));
const on = (f: ReturnType<typeof fleetAxes>, a: FleetAxis) => f.axes[a].map((e) => e.cluster.name);
const reason = (f: ReturnType<typeof fleetAxes>, a: FleetAxis, name: string) => f.axes[a].find((e) => e.cluster.name === name)?.reason;

const faulty = (cluster: string) => applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 2, ok: 1 }, [kind("deployments")])])), {}, cluster));
const warned = (cluster: string) => applySummary(summary(replace(core(), task("nodes", [verdict("nodes", { degraded: 1 }, [kind("nodes")])])), {}, cluster));
const quiet = (cluster: string) => applySummary(summary(core(), {}, cluster));

beforeEach(() => resetHealthStore());

describe("fleet axes (slice 4 §3.1)", () => {
  it("a remote warning while inside another cluster: warning, with its first lit line", () => {
    quiet("open"); warned("remote");
    const f = axesOf([cl("open"), cl("remote")]);
    expect(on(f, "warning")).toEqual(["remote"]);
    expect(on(f, "fault")).toEqual([]);
    expect(reason(f, "warning", "remote")).toMatch(/^Nodes: 1 need attention/);
  });

  it("a controller-only fault: fault, named by its controller", () => {
    applySummary(summary([...core(), task("flux", [native("reconciliations", { ok: 2 }, 0, "not ready", [kind("kustomizations")]), native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")])], { controller: { state: "not ready", reason: "kustomize-controller 0/1" } })], {}, "b"));
    const f = axesOf([cl("b", "Synced", { fluxPresent: true })]);
    expect(on(f, "fault")).toEqual(["b"]);
    expect(reason(f, "fault", "b")).toBe("Flux: controller not ready - kustomize-controller 0/1");
  });

  it("a fault's reason is its danger line, never a warning listed before it", () => {
    applySummary(summary(replace(replace(core(), task("workloads", [verdict("workloads", { degraded: 1 }, [kind("deployments")])])),
      task("nodes", [verdict("nodes", { fault: 1 }, [kind("nodes")])])), {}, "b"));
    const f = axesOf([cl("b")]);
    expect(on(f, "fault")).toEqual(["b"]);
    expect(on(f, "warning")).toEqual([]); // exclusive: danger wins
    expect(reason(f, "fault", "b")).toMatch(/^Nodes: 1 need attention/);
  });

  it("a failed connection: unreachable alone, even with a fault on record", () => {
    faulty("x");
    const f = axesOf([cl("x", "Failed", { reason: "dial tcp" }), cl("u", "Unconnected")]);
    expect(on(f, "unreachable")).toEqual(["x", "u"]);
    expect(reason(f, "unreachable", "x")).toBe("failed: dial tcp");
    for (const a of ["fault", "warning", "connection", "unverified"] as const) expect(on(f, a)).toEqual([]);
  });

  it("a lost watch: a stale connection is on connection; a stale kind is unverified, named", () => {
    quiet("s");
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { ok: 3 }, [kind("pods", { coverage: "stale", lastGoodAt: "2026-10-15T09:41:00Z" })])])), {}, "k"));
    const f = axesOf([cl("s", "Stale", { reason: "watch lagging" }), cl("k")]);
    expect(on(f, "connection")).toEqual(["s"]);
    expect(reason(f, "connection", "s")).toBe("stale: watch lagging");
    expect(on(f, "unverified")).toEqual(["k"]);
    expect(reason(f, "unverified", "k")).toMatch(/^Pods: Pods last good \d\d:\d\d/);
  });

  it("overlaps are independent facts: degraded with a fault is on both; degraded with gaps on both", () => {
    faulty("d");
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { ok: 3 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])])), {}, "g"));
    const f = axesOf([cl("d", "Degraded", { reason: "metrics API down" }), cl("g", "Degraded")]);
    expect(on(f, "fault")).toEqual(["d"]);
    expect(on(f, "connection")).toEqual(["d", "g"]);
    expect(on(f, "unverified")).toEqual(["g"]);
    expect(reason(f, "unverified", "g")).toBe("Pods: ReplicaSets could not be read - a dependency");
  });

  it("connecting, no summary yet and an ended observation are unverified, each with its reason", () => {
    quiet("c"); quiet("e");
    applySummary(summary([], { seq: 2, final: true, evaluatedAt: "2026-10-15T09:00:00Z" }, "e"));
    const f = axesOf([cl("c", "Connecting"), cl("n"), cl("e")]);
    expect(on(f, "unverified")).toEqual(["c", "n", "e"]);
    expect(reason(f, "unverified", "c")).toBe("connecting");
    expect(reason(f, "unverified", "n")).toBe("health checking…");
    expect(reason(f, "unverified", "e")).toMatch(/^observation ended at /);
  });

  it("a full quiet read on a synced connection is on no axis", () => {
    quiet("q");
    const f = axesOf([cl("q")]);
    expect(Object.values(f.axes).every((x) => x.length === 0)).toBe(true);
  });

  it("clusters are listed in station order on every axis", () => {
    warned("p"); warned("d");
    const f = axesOf([cl("p", "Synced", { env: "prd" }), cl("d", "Synced", { env: "dev" })]);
    expect(on(f, "warning")).toEqual(["d", "p"]);
  });
});

describe("the indicator's accessible name (slice 4 §3.2)", () => {
  it("counts clusters per non-zero axis in chip order, then the fleet's size", () => {
    faulty("a"); quiet("q");
    const f = axesOf([cl("a"), cl("x", "Failed"), cl("n"), cl("m"), cl("q"), cl("z", "Connecting")]);
    expect(fleetAxesLabel(f)).toBe("Fleet: 1 cluster with a fault, 1 unreachable, 3 unverified, of 6");
  });

  it("all zero reads the fleet's size alone - never an all-clear", () => {
    quiet("a"); quiet("b");
    expect(fleetAxesLabel(axesOf([cl("a"), cl("b")]))).toBe("Fleet: 2 clusters");
    quiet("c");
    expect(fleetAxesLabel(axesOf([cl("c")]))).toBe("Fleet: 1 cluster");
  });

  it("plural axes", () => {
    warned("a"); warned("b");
    expect(fleetAxesLabel(axesOf([cl("a"), cl("b"), cl("s", "Stale")]))).toBe("Fleet: 2 clusters with warnings, 1 with a stale or degraded connection, of 3");
  });
});
