import { describe, it, expect, beforeEach } from "vitest";
import {
  useHealth, resetHealthStore, applySummary, applyRows, addSub, openStarted, openSucceeded, dueForBootstrap, bootstrapSettled,
  type HealthPart, type HealthTask, type KindCoverage,
} from "../store/health";
import { C, absentPart, core, dep, evidence, kind, native, replace, summary, task, verdict } from "../test/healthFixtures";
import type { ClusterDTO } from "../store/fleet";
import { CAP_UNKNOWN, checkingText, clusterAttention, mastReading, strongestLine, provenance, lineText, type Area } from "./clusterAttention";

// Health 1c slice 4: the open cluster's attention from the Observer's
// publications, on the real health store.

const cluster = (over: Partial<ClusterDTO> = {}): ClusterDTO => ({ name: C, state: "Synced", ...over } as ClusterDTO);
const model = (c: ClusterDTO = cluster()) => clusterAttention(useHealth.getState(), C, c);
const area = (id: string, c?: ClusterDTO) => model(c).areas.find((a) => a.id === id) as Area;
const lineOf = (id: string, part: string, c?: ClusterDTO) => area(id, c)?.lines.find((l) => l.part === part);
const openRows = (h: string, t: string, p: string) => { addSub(h, C, t, p); openStarted(h, `tok-${h}`); openSucceeded(h, `tok-${h}`, 1); };

beforeEach(() => resetHealthStore());

describe("lifecycle (§1)", () => {
  it("no summary: every expected part checking; the mast says checking", () => {
    const m = model();
    expect(m.areas.map((a) => a.id)).toEqual(["workloads", "pods", "nodes", "events", "helm"]);
    expect(m.areas.every((a) => a.state === "checking")).toBe(true);
    expect(mastReading(m).state).toBe("checking");
  });

  it("rows before the summary answer their part; the others stay checking", () => {
    openRows("p", "pods", "pods");
    applyRows("p", { cluster: C, incarnation: 1, task: "pods", part: verdict("pods", { fault: 2, ok: 7 }, [kind("pods")]), evaluatedAt: "2026-10-12T12:00:05Z", rows: [], full: true });
    const m = model();
    expect(area("pods").state).toBe("attention");
    expect(lineOf("pods", "pods")).toMatchObject({ n: 2, tone: "danger", source: "rows", at: "2026-10-12T12:00:05Z" });
    expect(area("workloads").state).toBe("checking");
    expect(mastReading(m)).toMatchObject({ state: "attention", areas: 1 });
  });

  it("a Final ends the observation: no counts, nothing quiet; a late older Final is ignored; a newer incarnation clears it", () => {
    applySummary(summary(core()));
    applySummary(summary([], { seq: 2, final: true, evaluatedAt: "2026-10-12T12:05:00Z" }));
    expect(model()).toEqual({ ended: "2026-10-12T12:05:00Z", areas: [] });
    expect(mastReading(model()).state).toBe("ended");
    applySummary(summary([], { seq: 9, final: true, evaluatedAt: "2026-10-12T12:09:00Z" })); // late, same incarnation
    expect(model().ended).toBe("2026-10-12T12:05:00Z");
    applySummary(summary(core(), { incarnation: 2, seq: 1 }));
    expect(useHealth.getState().ended[C]).toBeUndefined(); // the store clears it, not only the model's guard
    expect(model().ended).toBeNull();
    expect(area("workloads").state).toBe("quiet");
  });

  it("a newer incarnation carries nothing over: its staggered parts start checking", () => {
    applySummary(summary(replace(core(), task("nodes", [verdict("nodes", { fault: 1 }, [kind("nodes")])]))));
    expect(area("nodes").state).toBe("attention");
    openRows("p", "pods", "pods");
    applyRows("p", { cluster: C, incarnation: 2, task: "pods", part: verdict("pods", { ok: 1 }, [kind("pods")]), evaluatedAt: "x", rows: [], full: true });
    expect(area("nodes").state).toBe("checking"); // the old incarnation's fault does not stand
    expect(area("pods").state).toBe("quiet");
  });
});

describe("optional tasks carried by rows (review 1/3 #1)", () => {
  const certRows = (ver: number, coverage: "live" | "absent" = "live") => ({
    cluster: C, incarnation: 1, task: "certs", evaluatedAt: "2026-10-12T12:00:07Z", rows: [], full: true,
    part: { ...native("certificates", { fault: 1, ok: 2 }, 1, "not ready", [kind("certificates", { coverage })]), ver, obs: ver, coverage },
  });

  it("live rows arriving before any summary keep the task, whatever capability detection says", () => {
    openRows("c", "certs", "certificates");
    applyRows("c", certRows(1));
    expect(area("certs")).toMatchObject({ state: "attention", tone: "danger" }); // capability absent in the fleet DTO
    expect(mastReading(model())).toMatchObject({ state: "attention", areas: 1 });
  });

  it("newer live rows outrank an older summary that said absent", () => {
    applySummary(summary([...core(), task("certs", [absentPart("certificates", "certificates"), absentPart("issuers", "issuers")])]));
    expect(model().areas.some((a) => a.id === "certs")).toBe(false);
    openRows("c", "certs", "certificates");
    applyRows("c", certRows(5));
    expect(lineOf("certs", "certificates")).toMatchObject({ state: "attention", n: 1, source: "rows" });
  });
});

describe("evidence: counted kinds, dependencies, membership (§2.1)", () => {
  it("a live count with an unreadable dependency keeps its known fault and names the gap", () => {
    applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 1, ok: 2 }, [kind("deployments"), kind("statefulsets"), kind("daemonsets"), dep("replicasets", { coverage: "unreadable", class: "forbidden" })], { coverage: "unreadable" })]))));
    const l = lineOf("workloads", "workloads");
    expect(l).toMatchObject({ state: "attention", n: 1, tone: "danger", bound: "as-read" });
    expect(l?.gaps).toContain("ReplicaSets could not be read (forbidden) - a dependency");
    expect(area("workloads").gap).toBe(true);
  });

  it("live kinds with one missing: a lower bound on current attention", () => {
    applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { degraded: 3 }, [kind("deployments"), kind("statefulsets", { coverage: "unreadable", class: "timeout" }), kind("daemonsets")])]))));
    const l = lineOf("workloads", "workloads")!;
    expect(l).toMatchObject({ bound: "lower", text: "3+ need attention", tone: "warning" });
    expect(l.gaps).toContain("StatefulSets could not be read (timeout)");
  });

  it("live, stale and unreadable kinds: current and last observed, never a lower bound", () => {
    applySummary(summary(replace(core(), task("workloads", [verdict("workloads", { fault: 2 }, [kind("deployments"), kind("statefulsets", { coverage: "stale", lastGoodAt: "2026-10-12T11:58:00Z" }), kind("daemonsets", { coverage: "unreadable" })])]))));
    const l = lineOf("workloads", "workloads")!;
    expect(l.bound).toBe("mixed");
    expect(l.text).toBe("2 need attention - current and last observed");
    expect(l.text).not.toContain("+");
    expect(l.gaps.some((g) => g.startsWith("StatefulSets last good"))).toBe(true);
    expect(l.gaps).toContain("DaemonSets could not be read");
  });

  it("nothing counted read: no count", () => {
    applySummary(summary(replace(core(), task("nodes", [verdict("nodes", {}, [kind("nodes", { coverage: "unreadable", class: "forbidden" })], { coverage: "unreadable" })]))));
    expect(lineOf("nodes", "nodes")).toMatchObject({ state: "unreadable", n: 0, text: "could not be read (forbidden)" });
    expect(area("nodes").state).toBe("gaps");
  });

  it("routes need their parent Gateways read; Services and EndpointSlices only qualify", () => {
    const routes = (gw: Partial<KindCoverage>, svc: Partial<KindCoverage> = {}) => native("routes", { fault: 1, ok: 3 }, 1, "failing", [kind("httproutes"), dep("gateways", gw), dep("services", svc), dep("endpointslices")]);
    const gwTask = (r: HealthPart) => task("gateway", [native("gateways", { ok: 1 }, 0, "not programmed", [kind("gateways"), dep("gatewayclasses")]), r]);
    const c = cluster({ gatewayAPIVersion: "v1" });
    applySummary(summary([...core(), gwTask(routes({ coverage: "unreadable" }))]));
    expect(lineOf("gateway", "routes", c)).toMatchObject({ state: "unreadable", n: 0 });
    expect(lineOf("gateway", "routes", c)?.text).toMatch(/^attachment membership not established - Gateways could not be read/);
    applySummary(summary([...core(), gwTask(routes({ coverage: "stale", lastGoodAt: "2026-10-12T11:50:00Z" }))], { seq: 2 }));
    expect(lineOf("gateway", "routes", c)).toMatchObject({ n: 1, bound: "as-read" }); // a stale parent: a stale reading, not "mixed"
    applySummary(summary([...core(), gwTask(routes({}, { coverage: "unreadable" }))], { seq: 3 }));
    expect(lineOf("gateway", "routes", c)).toMatchObject({ state: "attention", n: 1, bound: "as-read" });
    expect(lineOf("gateway", "routes", c)?.gaps).toContain("Services could not be read - a dependency");
  });

  it("a discovery gap is named beside a live count; a truncated snapshot is a lower bound with its read time", () => {
    applySummary(summary(replace(replace(core(), task("pods", [verdict("pods", { ok: 1 }, [kind("pods", { discovery: "forbidden" })])])),
      task("events", [evidence(12, [kind("events", { truncated: true })], { asOf: "2026-10-12T12:00:00Z" })]))));
    expect(lineOf("pods", "pods")?.gaps).toContain("discovery re-check failing for Pods");
    const ev = lineOf("events", "warnings")!;
    expect(ev).toMatchObject({ state: "evidence", n: 12, bound: "lower", tone: undefined, text: "12+ warning events" });
    expect(lineText(ev)).toMatch(/\(as of \d\d:\d\d\)$/);
    expect(area("events").state).toBe("quiet"); // evidence is never attention
  });
});

describe("native lines and controllers (§2.1)", () => {
  // the backend always reports the operator's parts (coverage S1a); absent here
  const flux = (recs: HealthPart, srcs?: HealthPart, over: Partial<HealthTask> = {}) => task("flux", [recs, srcs ?? native("sources", { ok: 2 }, 0, "not ready", [kind("gitrepositories")]),
    absentPart("fluxinstance", "fluxinstances"), absentPart("resourcesets", "resourcesets"), absentPart("images", "imagerepositories")], over);
  const c = cluster({ fluxPresent: true });

  it("lit only by fault or degraded roles; progressing in the tool's word is unlit", () => {
    applySummary(summary([...core(), flux(native("reconciliations", { progressing: 2, ok: 5 }, 2, "not ready", [kind("kustomizations")]))]));
    expect(lineOf("flux", "reconciliations", c)).toMatchObject({ state: "unlit", n: 2, text: "2 not ready", tone: undefined });
    expect(area("flux", c).state).toBe("quiet");
    applySummary(summary([...core(), flux(native("reconciliations", { fault: 1, progressing: 2 }, 3, "not ready", [kind("kustomizations")]))], { seq: 2 }));
    expect(lineOf("flux", "reconciliations", c)).toMatchObject({ state: "attention", n: 3, tone: "danger" });
  });

  it("an unknown-only native part is a gap, never quiet, even with Included 0", () => {
    applySummary(summary([...core(), flux(native("reconciliations", { unknown: 2, ok: 1 }, 0, "not ready", [kind("kustomizations")]))]));
    expect(lineOf("flux", "reconciliations", c)?.gaps).toContain("2 unknown");
    expect(area("flux", c)).toMatchObject({ state: "gaps", gap: true });
  });

  it("a controller not ready lights the area with no count; unknown is a gap only; both coexist with object lines", () => {
    const quiet = native("reconciliations", { ok: 3 }, 0, "not ready", [kind("kustomizations")]);
    applySummary(summary([...core(), flux(quiet, undefined, { controller: { state: "not ready", reason: "kustomize-controller 0/1" } })]));
    expect(area("flux", c)).toMatchObject({ state: "attention", tone: "danger" });
    expect(area("flux", c).lines.every((l) => l.state !== "attention")).toBe(true);
    expect(mastReading(model(c))).toMatchObject({ state: "attention", areas: 1 });
    applySummary(summary([...core(), flux(quiet, undefined, { controller: { state: "unknown", reason: "unreadable" } })], { seq: 2 }));
    expect(area("flux", c)).toMatchObject({ state: "gaps", gap: true });
    expect(mastReading(model(c))).toMatchObject({ state: "quiet", areas: 0 });
    applySummary(summary([...core(), flux(native("reconciliations", { degraded: 1 }, 1, "not ready", [kind("kustomizations")]), undefined, { controller: { state: "unknown" } })], { seq: 3 }));
    expect(area("flux", c)).toMatchObject({ state: "attention", tone: "warning", gap: true });
  });
});

describe("badges: strongest line, exact parts (§2.2)", () => {
  it("the strongest line is never a sum; a secondary part can be stronger than the primary", () => {
    const c = cluster({ fluxPresent: true });
    applySummary(summary([...core(), task("flux", [
      native("reconciliations", { degraded: 1 }, 1, "not ready", [kind("kustomizations")]),
      native("sources", { fault: 3 }, 3, "not ready", [kind("gitrepositories")]),
    ])]));
    const s = strongestLine(area("flux", c).lines)!;
    expect(s).toMatchObject({ part: "sources", n: 3, tone: "danger", destinationId: "flux-sources" });
    expect(lineOf("flux", "reconciliations", c)).toMatchObject({ n: 1, destinationId: "flux" });
  });

  it("Karpenter's primary part is NodeClaims - the view its lens opens on", () => {
    const c = cluster({ karpenterPresent: true });
    applySummary(summary([...core(), task("karpenter", [
      native("nodepools", { ok: 1 }, 0, "not ready", [kind("nodepools")]),
      native("nodeclaims", { fault: 1, ok: 2 }, 1, "failing", [kind("nodeclaims")]),
    ])]));
    expect(area("karpenter", c).lines.map((l) => [l.part, l.destinationId])).toEqual([["nodeclaims", "karpenter"], ["nodepools", undefined]]);
    expect(strongestLine(area("karpenter", c).lines)).toMatchObject({ part: "nodeclaims", n: 1 });
  });
});

describe("the selector's A4 contract and provenance (§3)", () => {
  const podsPart = (ver: number, fault: number) => ({ ...verdict("pods", { fault, ok: 3 }, [kind("pods")]), ver, obs: ver });
  const withPods = (ver: number, fault: number, seq = ver) => summary(replace(core(), task("pods", [podsPart(ver, fault)])), { seq, evaluatedAt: `2026-10-12T12:00:0${seq}Z` });

  it("rows newer than the summary win with their own time; inside the first second they are held, updating; past it, the summary refreshing", () => {
    openRows("p", "pods", "pods");
    applySummary(withPods(1, 1), 0);
    applyRows("p", { cluster: C, incarnation: 1, task: "pods", part: podsPart(3, 4), evaluatedAt: "2026-10-12T12:00:09Z", rows: [], full: true }, 0);
    expect(lineOf("pods", "pods")).toMatchObject({ n: 4, source: "rows", at: "2026-10-12T12:00:09Z", updating: false });
    applySummary(withPods(5, 6, 2), 100); // the summary gets ahead
    expect(lineOf("pods", "pods")).toMatchObject({ n: 4, source: "rows", updating: true });
    dueForBootstrap(1100); // past the deadline
    expect(lineOf("pods", "pods")).toMatchObject({ n: 6, source: "summary", refreshing: true, at: "2026-10-12T12:00:02Z" });
    expect(provenance(area("pods"), lineOf("pods", "pods")!)).toMatch(/^cluster summary as of .* - refreshing$/);
  });

  it("a resync without any summary, and a failed re-bootstrap, are visible gaps", () => {
    openRows("p", "pods", "pods");
    applyRows("p", { cluster: C, incarnation: 1, task: "pods", part: podsPart(3, 0), evaluatedAt: "x", rows: [], full: true }, 0);
    applyRows("p", { cluster: C, incarnation: 1, task: "pods", part: podsPart(6, 0), evaluatedAt: "y", rows: null, full: false, base: 5, upserts: [], removed: [] }, 0);
    expect(lineOf("pods", "pods")?.gaps).toContain("rows re-reading");
    expect(area("pods").gap).toBe(true);
    const [d] = dueForBootstrap(0);
    bootstrapSettled("p", d.requestGen, { error: "timeout" }, 10);
    expect(lineOf("pods", "pods")?.gaps).toContain("last re-read failed: timeout");
  });
});

// Slice 6 §4: once the coverage-parity gate passed, "not covered" went. A
// part the summary does not carry is checking - a gap, never quiet, never a
// count - qualified while capability detection has not established its tool.
describe("a part the summary does not carry is checking, never quiet (slice 6 §4)", () => {
  it("a missing core task, a task missing a part, a present capability without its task, an unknown capability without its task; a confirmed absence omitted", () => {
    const tasks = core().filter((t) => t.id !== "nodes");
    tasks.push(task("certs", [native("certificates", { ok: 1 }, 0, "not ready", [kind("certificates")])])); // issuers missing
    applySummary(summary(tasks));
    const c = cluster({ certManagerPresent: true, fluxPresent: true, argoPresent: false });
    const m = model(c);
    const at = (id: string) => m.areas.find((a) => a.id === id);
    expect(at("nodes")).toMatchObject({ state: "checking", gap: true });
    expect(at("certs")?.lines.find((l) => l.part === "issuers")).toMatchObject({ state: "checking", n: 0, text: "checking…", gaps: [] });
    expect(at("certs")).toMatchObject({ state: "gaps", gap: true }); // its answered part stands, the missing one is a gap
    expect(at("flux")?.lines.every((l) => l.state === "checking" && l.gaps.length === 0)).toBe(true); // present: no qualifier
    expect(at("argo")).toBeUndefined(); // confirmed absent
    const unknown = model(cluster({ state: "Connecting" } as Partial<ClusterDTO>));
    for (const id of ["flux", "argo", "eso", "gateway", "karpenter"]) {
      const a = unknown.areas.find((x) => x.id === id);
      expect(a, id).toBeDefined(); // never silently gone
      expect(checkingText(a!), id).toBe(`checking… · ${CAP_UNKNOWN}`);
    }
    // a carried task's missing part is qualified too; its answered part stands
    const certs = unknown.areas.find((x) => x.id === "certs")!;
    expect(certs.lines.find((l) => l.part === "issuers")?.gaps).toEqual([CAP_UNKNOWN]);
    expect(certs.lines.find((l) => l.part === "certificates")?.state).toBe("quiet");
    expect(mastReading(m)).toMatchObject({ state: "quiet", gaps: 3 }); // nodes, certs, flux: never an all-clear
  });

  it("a part the Observer reports absent has no line; a task with every part absent is omitted", () => {
    applySummary(summary([...core(), task("eso", [absentPart("externalsecrets", "externalsecrets"), absentPart("stores", "secretstores")])]));
    expect(model(cluster({ externalSecretsPresent: true })).areas.some((a) => a.id === "eso")).toBe(false);
  });

  it("the mast counts areas and gaps apart, and is never an unqualified all-clear", () => {
    applySummary(summary(replace(core(), task("nodes", [verdict("nodes", { unknown: 1, ok: 2 }, [kind("nodes")])]))));
    expect(mastReading(model())).toEqual({ state: "quiet", areas: 0, tone: undefined, gaps: 1, read: 5 });
  });

  it("checkingText speaks only for an area nothing has answered", () => {
    applySummary(summary([...core(), task("flux", [native("reconciliations", { ok: 1 }, 0, "not ready", [kind("kustomizations")])], { controller: { state: "unknown" } })]));
    const fx = model(cluster({ fluxPresent: true })).areas.find((a) => a.id === "flux")!;
    expect(checkingText(fx)).toBeNull(); // a part answered, and a controller state known
  });
});
