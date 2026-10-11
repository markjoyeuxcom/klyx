import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet, ResourceDetailDTO } from "../store/fleet";

// Deferred service calls: each test resolves/rejects them in the order the
// race needs.
type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void };
function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void; let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
const calls: Deferred<unknown>[] = [];
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  GitOpsService: {
    GetResourceDetail: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
    ResolveGitLink: vi.fn(async () => resolved),
  },
}));
let resolved: unknown = null;
vi.mock("@wailsio/runtime", () => ({
  Browser: { OpenURL: vi.fn(async () => {}) }, Clipboard: { SetText: vi.fn(async () => {}) },
}));

import { getResourceDetail, resolveGitLink } from "./gitops";

const KEY = "Kustomization/flux-system/apps";
const detailFrom = (marker: string): ResourceDetailDTO => ({
  kind: "Kustomization", namespace: "flux-system", name: "apps", appliedRevision: marker, attemptedRevision: marker,
  applyFailed: false, conditions: [], inventory: [],
});

beforeEach(() => {
  calls.length = 0;
  useFleet.setState({ actionStatus: null, gitops: { cluster: "A", resources: [], sources: [], loading: false, expandedKey: KEY, detail: null, detailCluster: null } });
});

describe("Flux bridge lifecycle fences (Codex Flux review #1)", () => {
  it("a delayed detail from cluster A never lands under the same key on cluster B", async () => {
    const p = getResourceDetail("A", "Kustomization", "flux-system", "apps");
    useFleet.getState().setGitOpsLoading("B");
    useFleet.getState().expand(KEY); // same key selected on B
    calls[0].resolve(detailFrom("from-A"));
    await p;
    expect(useFleet.getState().gitops.detail).toBeNull();
  });

  it("a late failure from A never collapses B's same-key selection", async () => {
    const p = getResourceDetail("A", "Kustomization", "flux-system", "apps");
    useFleet.getState().setGitOpsLoading("B");
    useFleet.getState().expand(KEY);
    calls[0].reject(new Error("boom"));
    await p;
    expect(useFleet.getState().gitops.expandedKey).toBe(KEY);
    expect(useFleet.getState().actionStatus).toBeNull();
  });

  it("overlapping reads: only the newest request lands", async () => {
    const first = getResourceDetail("A", "Kustomization", "flux-system", "apps");
    const second = getResourceDetail("A", "Kustomization", "flux-system", "apps");
    calls[1].resolve(detailFrom("newest"));
    await second;
    calls[0].resolve(detailFrom("stale"));
    await first;
    expect(useFleet.getState().gitops.detail?.appliedRevision).toBe("newest");
    expect(useFleet.getState().gitops.detailCluster).toBe("A");
  });

  // Design §5b: a failed detail read is the request's outcome - it lands
  // for its key (the lens shows it apart from evidence), never dropped.
  it("an error detail lands for the selection's key", async () => {
    const p = getResourceDetail("A", "Kustomization", "flux-system", "apps");
    calls[0].resolve({ kind: "Kustomization", namespace: "flux-system", name: "apps", error: "forbidden" });
    await p;
    expect(useFleet.getState().gitops.detail).toMatchObject({ name: "apps", error: "forbidden" });
  });

  it("a Git link read that failed is named, never 'no Git source'", async () => {
    resolved = { url: "", isDeepLink: false, copyText: "", error: "GitRepository flux-system/src: forbidden" };
    await resolveGitLink("A", "Kustomization", "flux-system", "apps");
    expect(useFleet.getState().actionStatus).toEqual({ kind: "error", message: "Could not resolve the Git source: GitRepository flux-system/src: forbidden" });
  });

  it("switching cluster drops the selection and detail", () => {
    useFleet.setState((s) => ({ gitops: { ...s.gitops, detail: detailFrom("A"), detailCluster: "A" } }));
    useFleet.getState().setGitOpsLoading("B");
    expect(useFleet.getState().gitops.expandedKey).toBeNull();
    expect(useFleet.getState().gitops.detail).toBeNull();
  });
});
