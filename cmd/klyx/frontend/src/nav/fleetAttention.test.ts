import { describe, it, expect, beforeEach } from "vitest";
import { useHealth, resetHealthStore, applySummary, applyRows, addSub, openStarted, openSucceeded, dueForBootstrap } from "../store/health";
import type { ClusterDTO } from "../store/fleet";
import { core, dep, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention, mastReading } from "./clusterAttention";
import { fleetMeters, healthReading, spineLamp } from "./fleetAttention";

// Health 1c slice 5 §2-§3: the fleet's lamps and meters from each cluster's
// attention model - the first matching lamp rule, both axes in the title.

const cl = (name: string, state = "Synced", over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name, state, reason: "", ...over } as ClusterDTO);
const model = (c: ClusterDTO) => clusterAttention(useHealth.getState(), c.name, c);
const lamp = (c: ClusterDTO) => spineLamp(c, model(c));
const faulty = (cluster: string, over = {}) => applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 2, ok: 1 }, [kind("deployments")])])), over, cluster));
const warned = (cluster: string) => applySummary(summary(replace(core(), task("nodes", [verdict("nodes", { degraded: 1 }, [kind("nodes")])])), {}, cluster));
const gappy = (cluster: string) => applySummary(summary(replace(core(), task("pods", [verdict("pods", { ok: 3 }, [kind("pods"), dep("replicasets", { coverage: "unreadable" })])])), {}, cluster));
const quiet = (cluster: string) => applySummary(summary(core(), {}, cluster));

beforeEach(() => resetHealthStore());

describe("the spine lamp: first matching rule (§2)", () => {
  it("unreachable wins, and its title keeps the last health reading", () => {
    faulty("a");
    const l = lamp(cl("a", "Failed", { reason: "dial tcp" }));
    expect(l.kind).toBe("unreachable");
    expect(l.title).toMatch(/^a - failed: dial tcp\n/);
    expect(l.title).toContain("Workloads: 2 need attention");
  });

  it("a danger area blinks; a warning area glows; areas, never objects", () => {
    faulty("a");
    expect(lamp(cl("a"))).toMatchObject({ kind: "danger", tone: "danger", blink: true });
    warned("b");
    expect(lamp(cl("b"))).toMatchObject({ kind: "warning", tone: "warning", blink: false });
  });

  it("stale plus a fault: the fault lamp, and the stale connection stays in the title", () => {
    faulty("a");
    const l = lamp(cl("a", "Stale", { reason: "watch lagging" }));
    expect(l.kind).toBe("danger");
    expect(l.title).toMatch(/^a - stale: watch lagging\n/);
  });

  it("stale plus gaps: the connection's lamp, both readings in the title", () => {
    gappy("a");
    const l = lamp(cl("a", "Stale", { reason: "watch lagging" }));
    expect(l.kind).toBe("connection");
    expect(l.title).toContain("stale: watch lagging");
    expect(l.title).toMatch(/Pods: none need attention · ReplicaSets could not be read - a dependency \(cluster summary as of /);
  });

  it("degraded plus an unknown controller: the connection's lamp, the controller gap named", () => {
    applySummary(summary([...core(), task("flux", [native("reconciliations", { ok: 2 }, 0, "not ready", [kind("kustomizations")]), native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")])], { controller: { state: "unknown", reason: "unreadable" } })], {}, "a"));
    const l = lamp(cl("a", "Degraded", { reason: "metrics API down", fluxPresent: true }));
    expect(l.kind).toBe("connection");
    expect(l.title).toContain("Flux: controller health unknown");
  });

  it("connecting with a retained quiet reading is never the healthy lamp", () => {
    quiet("a");
    const l = lamp(cl("a", "Connecting"));
    expect(l).toMatchObject({ kind: "connecting", tone: "muted" });
    expect(l.title).toMatch(/^a - connecting\n/);
    expect(l.title).toContain("capability not yet known"); // detection is still checking: nothing is implied
  });

  it("gaps, checking and an ended observation are unverified; only a full quiet read on a synced connection is healthy", () => {
    gappy("a");
    expect(lamp(cl("a")).kind).toBe("unverified");
    expect(lamp(cl("none")).kind).toBe("unverified"); // no summary: checking
    expect(lamp(cl("none")).title).toContain("health checking…");
    quiet("q");
    expect(lamp(cl("q"))).toMatchObject({ kind: "healthy", tone: "success", blink: false });
    applySummary(summary([], { seq: 2, final: true, evaluatedAt: "2026-10-12T12:05:00Z" }, "q"));
    expect(lamp(cl("q"))).toMatchObject({ kind: "unverified" });
    expect(lamp(cl("q")).title).toMatch(/observation ended at /);
  });

  it("a part the summary does not carry is checking: the lamp stays unverified and names it (slice 6 §4)", () => {
    quiet("a");
    const l = lamp(cl("a", "Synced", { certManagerPresent: true }));
    expect(l.kind).toBe("unverified");
    expect(l.title).toMatch(/Certificates Certificates: checking… \(not read yet\)/);
    expect(l.title).not.toContain("not covered");
  });
});

describe("rows on the fleet (§1, §4)", () => {
  it("optional fault rows before any summary light the lamp, with the rows' provenance", () => {
    addSub("c", "a", "certs", "certificates"); openStarted("c", "t"); openSucceeded("c", "t", 1);
    applyRows("c", { cluster: "a", incarnation: 1, task: "certs", evaluatedAt: "2026-10-12T12:00:07Z", rows: [], full: true,
      part: native("certificates", { fault: 1 }, 1, "not ready", [kind("certificates")]) });
    const l = lamp(cl("a"));
    expect(l.kind).toBe("danger");
    expect(l.title).toMatch(/Certificates Certificates: 1 not ready \(Certificates lens rows as of /);
  });

  it("a newer incarnation's staggered parts after a Final inherit nothing", () => {
    faulty("a", { namespaces: 9 });
    applySummary(summary([], { seq: 2, final: true }, "a"));
    addSub("p", "a", "pods", "pods"); openStarted("p", "t"); openSucceeded("p", "t", 2);
    applyRows("p", { cluster: "a", incarnation: 2, task: "pods", evaluatedAt: "x", rows: [], full: true, part: verdict("pods", { ok: 1 }, [kind("pods")]) });
    const m = model(cl("a"));
    expect(m.ended).toBeNull();
    expect(m.areas.find((x) => x.id === "workloads")?.state).toBe("checking"); // the old fault does not stand
    expect(useHealth.getState().summaries.a).toBeUndefined(); // nor the old summary's time or namespaces
    expect(lamp(cl("a")).kind).toBe("unverified");
  });

  it("the open cluster's lamp agrees with its mast when rows are newer than the summary", () => {
    quiet("a");
    addSub("p", "a", "pods", "pods"); openStarted("p", "t"); openSucceeded("p", "t", 1);
    applyRows("p", { cluster: "a", incarnation: 1, task: "pods", evaluatedAt: "x", rows: [], full: true, part: { ...verdict("pods", { fault: 1 }, [kind("pods")]), ver: 5, obs: 5 } });
    const m = model(cl("a"));
    expect(mastReading(m)).toMatchObject({ state: "attention", areas: 1 });
    expect(lamp(cl("a")).kind).toBe("danger");
  });
});

describe("the board meters: clusters, never objects (§3)", () => {
  it("attention and unknown coverage are counted apart; no summary and ended are unknown, never quiet", () => {
    faulty("a"); warned("b"); gappy("c"); quiet("d");
    quiet("e"); applySummary(summary([], { seq: 2, final: true }, "e"));
    const clusters = [cl("a"), cl("b"), cl("c"), cl("d"), cl("e"), cl("f"), cl("x", "Failed")];
    const models = Object.fromEntries(clusters.map((c) => [c.name, model(c)]));
    expect(fleetMeters(clusters, models)).toEqual({ attention: 2, attentionDanger: true, unknown: 3 }); // c gaps, e ended, f checking
    expect(fleetMeters([cl("b")], { b: models.b })).toEqual({ attention: 1, attentionDanger: false, unknown: 0 });
  });

  it("each unknown-coverage cluster's reading names why", () => {
    gappy("c");
    expect(healthReading(model(cl("c"))).join("\n")).toContain("ReplicaSets could not be read");
  });
});

// Codex review of 6f74371: a lit area keeps its other gaps, and every
// answered line - quiet and unlit too - keeps its provenance.
describe("the health reading keeps every gap and every time", () => {
  const flux = (recs: ReturnType<typeof native>, srcs: ReturnType<typeof native>, controller?: { state: string; reason?: string }) =>
    task("flux", [recs, srcs], controller ? { controller } : {});

  it("a lit area keeps its unreadable part and its unknown controller, with the reason", () => {
    applySummary(summary([...core(), flux(native("reconciliations", {}, 0, "not ready", [kind("kustomizations", { coverage: "unreadable", class: "forbidden" })], { coverage: "unreadable" }),
      native("sources", { fault: 1 }, 1, "not ready", [kind("gitrepositories")]), { state: "unknown", reason: "kustomize-controller unreadable" })], {}, "a"));
    const t = lamp(cl("a", "Synced", { fluxPresent: true })).title;
    expect(t).toMatch(/Flux Sources: 1 not ready \(cluster summary as of /);
    expect(t).toMatch(/Flux Reconciliations: could not be read \(forbidden\) \(cluster summary as of /);
    expect(t).toContain("Flux: controller health unknown - kustomize-controller unreadable");
  });

  it("failing certificates beside unreadable issuers: both named", () => {
    applySummary(summary([...core(), task("certs", [native("certificates", { fault: 2 }, 2, "not ready", [kind("certificates")]),
      native("issuers", {}, 0, "not ready", [kind("issuers", { coverage: "unreadable" }), kind("clusterissuers", { coverage: "unreadable" })], { coverage: "unreadable" })])], {}, "a"));
    const t = lamp(cl("a", "Synced", { certManagerPresent: true })).title;
    expect(t).toContain("Certificates Certificates: 2 not ready");
    expect(t).toContain("Certificates Issuers: could not be read");
  });

  it("quiet and unlit lines are dated; an unreachable cluster's last reading is dated too", () => {
    applySummary(summary([...core(), flux(native("reconciliations", { progressing: 2, ok: 1 }, 2, "not ready", [kind("kustomizations")]),
      native("sources", { ok: 1 }, 0, "not ready", [kind("gitrepositories")]))], {}, "a"));
    const t = lamp(cl("a", "Synced", { fluxPresent: true })).title;
    expect(t).toMatch(/Flux Reconciliations: 2 not ready \(cluster summary as of /);
    expect(t).toMatch(/no attention: Workloads, Pods, Nodes, Events, Helm releases, Flux Sources, HPAs \(cluster summary as of /);
    const u = lamp(cl("a", "Failed", { reason: "dial tcp", fluxPresent: true })).title;
    expect(u).toMatch(/^a - failed: dial tcp\n/);
    expect(u).toMatch(/no attention: .* \(cluster summary as of /);
  });

  it("held rows during the first second are captioned as the rows', updating - never the newer summary's time", () => {
    applySummary(summary(core(), { evaluatedAt: "2026-10-12T12:00:00Z" }, "a"), 0);
    addSub("p", "a", "pods", "pods"); openStarted("p", "t"); openSucceeded("p", "t", 1);
    applyRows("p", { cluster: "a", incarnation: 1, task: "pods", evaluatedAt: "2026-10-12T12:00:03Z", rows: [], full: true, part: { ...verdict("pods", { ok: 2 }, [kind("pods")]), ver: 3, obs: 3 } }, 0);
    applySummary(summary(replace(core(), task("pods", [{ ...verdict("pods", { ok: 2 }, [kind("pods")]), ver: 6, obs: 6 }])), { seq: 2, evaluatedAt: "2026-10-12T12:00:09Z" }, "a"), 100);
    expect(lamp(cl("a")).title).toMatch(/no attention: Pods \(Pods lens rows as of .* - updating\)/);
    dueForBootstrap(1200); // past the deadline: the summary's counts, refreshing
    expect(lamp(cl("a")).title).toMatch(/Pods \(cluster summary as of .* - refreshing\)/);
  });
});

