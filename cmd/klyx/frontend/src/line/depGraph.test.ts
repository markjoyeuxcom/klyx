import { describe, it, expect } from "vitest";
import type { DepGraph, DepNode } from "../bridge/depGraph";
import { conditionText, depTree, notHereText, proximityText, shortSha, supplyText, writeLines, writeText, type DepStep } from "./depGraph";

// The Line slice 8c (parent §5.1): the graph as steps, in the parent's
// words - typed edges, reported blockers, "upstream failure", "reported
// chain ends here", incomplete stubs - and never "root cause".

const n = (id: string, over: Partial<DepNode> = {}): DepNode => {
  const [kind, namespace, name] = id.split("/");
  return { id, group: "", kind, namespace, name, uid: `uid-${name}`, ready: "True", conditionGeneration: 0, generation: 1, observedGeneration: 1,
    stale: false, suspended: false, failing: false, upstreamFailure: false, chainEnd: false, supply: [], ...over };
};

const at = "2026-10-15T20:21:00Z";
const graph: DepGraph = {
  start: "Kustomization/flux-system/apps",
  reads: 6,
  incomplete: ["depth: not followed past depth 6"],
  nodes: [
    n("Kustomization/flux-system/apps", { ready: "False", reason: "DependencyNotReady", message: "dependency 'flux-system/infra' is not ready", conditionTime: at, failing: true,
      conditionGeneration: 1, generation: 2, inventory: { lastApplied: "main@sha1:a1b9e3c0d4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9", entries: 38, workloads: 14 },
      git: { url: "ssh://git@x/y.git", refKind: "branch", refName: "main", applied: "a".repeat(40), path: "./apps" } }),
    n("Kustomization/flux-system/infra", { ready: "False", reason: "ArtifactFailed", conditionTime: at, failing: true, upstreamFailure: true }),
    n("GitRepository/flux-system/platform", { ready: "False", reason: "GitOperationFailed", conditionTime: at, failing: true, upstreamFailure: true, chainEnd: true,
      supply: [{ field: "spec.secretRef", namespace: "flux-system", name: "deploy-key", uid: "s1", supplier: { kind: "ExternalSecret", namespace: "flux-system", name: "deploy-key", uid: "es1", readAt: at, ready: "True" } }],
      lastSpecWrite: { time: "2026-10-15T20:15:00Z", manager: "kubectl", operation: "Update", statusUpdate: false } }),
    { ...n("stub:not-followed/x/y"), id: "stub:not-followed", kind: "", namespace: "", name: "", stub: "depth", note: "3 not followed past depth 6" },
  ],
  edges: [
    { from: "Kustomization/flux-system/apps", to: "GitRepository/flux-system/platform", type: "source", reported: false, cycle: false },
    { from: "Kustomization/flux-system/apps", to: "Kustomization/flux-system/infra", type: "dependsOn", reported: true, cycle: false },
    { from: "Kustomization/flux-system/infra", to: "GitRepository/flux-system/platform", type: "source", reported: true, cycle: false },
    { from: "Kustomization/flux-system/infra", to: "Kustomization/flux-system/apps", type: "dependsOn", reported: true, cycle: true },
    { from: "Kustomization/flux-system/infra", to: "stub:not-followed", type: "dependsOn", reported: false, cycle: false },
  ],
  proximity: [{ failing: "GitRepository/flux-system/platform", conditionTime: at, object: "Secret/flux-system/deploy-key", write: { time: "2026-10-15T20:16:00Z", manager: "external-secrets", operation: "Update", statusUpdate: false } }],
};

const flat = (steps: DepStep[]): DepStep[] => steps.flatMap((s) => [s, ...s.branches.flatMap(flat)]);

describe("depTree", () => {
  const steps = depTree(graph);
  const all = flat(steps);
  const byName = (re: RegExp) => all.filter((s) => re.test(s.name));

  it("starts at the selected node, with its inventory and qualified condition", () => {
    expect(steps).toHaveLength(1);
    const root = steps[0];
    expect(root.name).toBe("Kustomization flux-system/apps");
    expect(root.tags).not.toContain("upstream failure"); // the selected node is not upstream of itself
    expect(root.evidence.inventory).toBe("last applied a1b9e3c; inventory 38 objects (14 workloads)");
    expect(root.evidence.condition).toContain("for generation 1; the spec is at generation 2");
    expect(root.git).toBe(true);
  });

  it("types each edge and says the controllers' reports in their words", () => {
    const [infra] = byName(/^Kustomization flux-system\/infra$/);
    expect(infra.segment).toBe("reported");
    expect(infra.segmentWords).toBe("reported blocker (depends on)");
    expect(infra.tags).toContain("upstream failure");
    const platforms = byName(/^GitRepository flux-system\/platform/);
    // drawn once (the first reach, a plain source edge), then a reference
    expect(platforms[0].segment).toBe("source");
    expect(platforms[0].tags).toEqual(["upstream failure", "reported chain ends here"]);
    expect(platforms[1].ref).toBe(true);
    expect(platforms[1].name).toBe("GitRepository flux-system/platform (see above)");
    expect(platforms[1].segment).toBe("reported");
    for (const s of all) expect(`${s.name} ${s.tags.join(" ")} ${s.segmentWords ?? ""}`).not.toMatch(/root cause/i);
  });

  it("cuts and names a cycle, and the walk's summary is incomplete evidence", () => {
    const [cycle] = byName(/^cycle: back to Kustomization flux-system\/apps/);
    expect(cycle.segment).toBe("incomplete");
    expect(cycle.segmentWords).toBe("incomplete: cycle, reported blocker");
    const [summary] = all.filter((s) => s.id === "stub:not-followed");
    expect(summary.segment).toBe("incomplete");
    expect(summary.name).toBe("3 not followed past depth 6");
  });

  it("states supply and change proximity without causality", () => {
    const [platform] = byName(/^GitRepository flux-system\/platform$/);
    expect(platform.evidence.supply).toEqual([
      "Credential supplied by ExternalSecret flux-system/deploy-key (Ready)",
      "Secret flux-system/deploy-key: no managed-field record",
      "ExternalSecret flux-system/deploy-key: no managed-field record",
    ]);
    expect(platform.evidence.proximity[0]).toMatch(/^Secret flux-system\/deploy-key: last managed-field update \d\d:\d\d \(external-secrets, Update\)\. GitRepository flux-system\/platform Ready=False since \d\d:\d\d \(condition time\)\.$/);
    expect(platform.evidence.writes).toEqual(["no managed-field record"]); // lastWrite is the actual last update; this node has none
  });
});

describe("wording", () => {
  it("supply facts never name a supplier without one", () => {
    expect(supplyText({ field: "spec.secretRef", namespace: "a", name: "k" })).toBe("Secret a/k (spec.secretRef) (no supplier recorded)");
    expect(supplyText({ field: "spec.secretRef", namespace: "a", name: "k", supplierNote: "its owner reference names ExternalSecret k with UID 1, but that name now has UID 2" }))
      .toContain("no supplier recorded: its owner reference names");
    expect(supplyText({ field: "spec.secretRef", namespace: "a", name: "k", notFound: true })).toBe("Secret a/k (spec.secretRef) not found");
  });
  it("a status update is labelled as one; no record says so", () => {
    expect(writeText({ time: at, manager: "flux", operation: "Update", subresource: "status", statusUpdate: true })).toMatch(/, status update\)$/);
    expect(writeText(undefined)).toBe("no managed-field record");
  });
  it("a condition is only worded when Ready=False", () => {
    expect(conditionText(n("Kustomization/a/b"))).toBeUndefined();
  });
  it("proximity falls back to the failing ID when the node is unknown", () => {
    expect(proximityText(graph.proximity[0], undefined)).toContain("GitRepository/flux-system/platform Ready=False");
  });
  it("Git's not here yet", () => {
    const r = { id: "x", commits: [{ sha: "a".repeat(40), subject: "s", author: "a", time: at }], more: false, files: [], filesOmitted: 0 };
    expect(notHereText({ ...r, state: "behind" }, "./apps")).toBe("not here yet: 1 commit touch ./apps");
    expect(notHereText({ ...r, state: "behind", more: true }, "./apps")).toBe("not here yet: 1+ commits touch ./apps");
    expect(notHereText({ ...r, state: "behind", commits: [] }, "./apps")).toBe("behind T; no commit touches ./apps");
    expect(notHereText({ ...r, state: "at", commits: [] }, "./apps")).toBe("at T");
    expect(notHereText({ ...r, state: "unavailable", reason: "its applied revision is ahead of T", commits: [] }, "./apps")).toBe("not here yet: unavailable (its applied revision is ahead of T)");
    expect(notHereText(undefined, "./apps")).toBeUndefined();
  });
});

// Design review of d4ed130: managed-field records are shown as they are,
// the condition keeps its generation in a proximity sentence, and a sha256
// id is abbreviated from its start.
describe("review of d4ed130", () => {
  const status = { time: "2026-10-15T20:30:00Z", manager: "status_writer", operation: "Update", subresource: "status", statusUpdate: true };
  const spec = { time: "2026-10-15T20:00:00Z", manager: "spec_writer", operation: "Apply", statusUpdate: false };
  it("the actual last update, then the older main-resource one", () => {
    const lines = writeLines(status, spec);
    expect(lines[0]).toMatch(/^last managed-field update \d\d:\d\d \(status_writer, Update, status update\)$/);
    expect(lines[1]).toMatch(/^last main-resource update \d\d:\d\d \(spec_writer, Apply\)$/);
    expect(writeLines(spec, spec)).toHaveLength(1);
  });
  it("a non-status subresource is named", () => {
    expect(writeText({ time: at, manager: "kubectl", operation: "Update", subresource: "scale", statusUpdate: false })).toMatch(/, subresource scale\)$/);
  });
  it("a proximity sentence keeps the condition's generation", () => {
    const failing = n("Kustomization/flux-system/apps", { ready: "False", conditionGeneration: 1, generation: 2, conditionTime: at });
    expect(proximityText({ failing: failing.id, conditionTime: at, object: "Secret/flux-system/age", write: spec }, failing))
      .toMatch(/\(condition time; for generation 1; the spec is at generation 2\)\.$/);
  });
  it("abbreviates a complete id from its start", () => {
    const sha256 = "0123456789abcdef".repeat(4);
    expect(shortSha("main@sha256:" + sha256)).toBe("0123456");
    expect(shortSha(sha256)).toBe("0123456");
    expect(shortSha("main@sha1:" + "a".repeat(40))).toBe("aaaaaaa");
    expect(shortSha("v1.2.3")).toBe("v1.2.3");
  });
});

// Coverage S1c §2: an artifact's producer edge is drawn by its evidence -
// verified and named as a source, disagreeing as incomplete, never a
// verified edge - and a producer of another kind is declared, not followed.
describe("producer edges", () => {
  const ks = "Kustomization/flux-system/apps", ea = "ExternalArtifact/flux-system/apps-a", ag = "ArtifactGenerator/flux-system/apps";
  const g = (producer: string, artOver: Partial<DepNode> = {}): DepGraph => ({
    start: ks, reads: 3, incomplete: [], proximity: [],
    nodes: [n(ks), n(ea, artOver), n(ag, { ready: "False", failing: true, upstreamFailure: producer !== "disagree" })],
    edges: [{ from: ks, to: ea, type: "source", reported: false, cycle: false }, { from: ea, to: ag, type: "producer", reported: false, cycle: false, producer }],
  });
  const gen = (graph: DepGraph) => flat(depTree(graph)).find((s) => s.id === ag)!;
  it("verified and named are sources, with their evidence", () => {
    expect(gen(g("verified"))).toMatchObject({ segment: "source", segmentWords: "produced by (the identity it records)" });
    expect(gen(g("named")).segmentWords).toContain("its identity is not recorded");
  });
  it("a disagreeing generator is drawn incomplete and is never an upstream failure", () => {
    const s = gen(g("disagree"));
    expect(s.segment).toBe("incomplete");
    expect(s.segmentWords).toContain("disagree");
    expect(s.tags).not.toContain("upstream failure");
  });
  it("an identity-only note only when no producer edge is followed", () => {
    const followed = flat(depTree(g("verified", { generatorUid: "uid-ag" }))).find((x) => x.id === ea)!;
    expect(followed.tags).not.toContain("producer recorded by identity only");
    const alone: DepGraph = { ...g("verified", { generatorUid: "uid-ag" }), edges: [{ from: ks, to: ea, type: "source", reported: false, cycle: false }] };
    expect(flat(depTree(alone)).find((x) => x.id === ea)!.tags).toContain("producer recorded by identity only");
  });
  it("a producer of another kind is a tag, not an edge", () => {
    const graph: DepGraph = { ...g("verified", { declaredProducer: { kind: "Pipeline", namespace: "ci", name: "build" } }), edges: [{ from: ks, to: ea, type: "source", reported: false, cycle: false }] };
    const s = flat(depTree(graph)).find((x) => x.id === ea)!;
    expect(s.tags).toContain("produced by Pipeline ci/build; Klyx does not read that kind's health");
    expect(s.branches).toHaveLength(0);
  });
});
