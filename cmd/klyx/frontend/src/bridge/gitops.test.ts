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
    Open: vi.fn(async () => {}),
    Close: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
    GetResourceDetail: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
  },
}));
const handlers: Record<string, (ev: unknown) => void> = {};
vi.mock("@wailsio/runtime", () => ({
  Events: { On: vi.fn((name: string, h: (ev: unknown) => void) => { handlers[name] = h; return () => {}; }) },
  Browser: {}, Clipboard: {},
}));

import { getResourceDetail, closeGitOps, openGitOps } from "./gitops";

const KEY = "Kustomization/flux-system/apps";
const detailFrom = (marker: string): ResourceDetailDTO => ({
  kind: "Kustomization", namespace: "flux-system", name: "apps", appliedRevision: marker, attemptedRevision: marker,
  applyFailed: false, conditions: [], inventory: [],
});
const flush = () => new Promise((r) => setTimeout(r, 0));

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

  it("a late closeGitOps(A) never clears B's freshly opened state", async () => {
    const p = closeGitOps("A");
    useFleet.getState().setGitOpsLoading("B");
    calls[0].resolve(undefined);
    await p;
    expect(useFleet.getState().gitops.cluster).toBe("B");
    expect(useFleet.getState().gitops.loading).toBe(true);
  });

  it("a late watch callback for A never overwrites B", async () => {
    await openGitOps("A");
    useFleet.getState().setGitOpsLoading("B");
    handlers["gitops:updated"]({ data: { cluster: "A", resources: [{ kind: "Kustomization", namespace: "x", name: "from-A" }], sources: [] } });
    await flush();
    expect(useFleet.getState().gitops.cluster).toBe("B");
    expect(useFleet.getState().gitops.resources).toEqual([]);
  });

  it("switching cluster drops the selection and detail", () => {
    useFleet.setState((s) => ({ gitops: { ...s.gitops, detail: detailFrom("A"), detailCluster: "A" } }));
    useFleet.getState().setGitOpsLoading("B");
    expect(useFleet.getState().gitops.expandedKey).toBeNull();
    expect(useFleet.getState().gitops.detail).toBeNull();
  });
});
