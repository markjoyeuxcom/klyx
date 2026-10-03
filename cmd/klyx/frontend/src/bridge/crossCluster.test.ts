import { describe, expect, it, vi, beforeEach } from "vitest";
import { useFleet, crdCountKey, type ResourceRef } from "../store/fleet";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  CRDService: { CountKind: vi.fn(), ListInstances: vi.fn(), GetInstanceDetail: vi.fn() },
  PodsService: { GetPodDetail: vi.fn() },
  NodesService: { GetNodeDetail: vi.fn() },
  NodeOpsService: {},
  HelmService: { GetHelmHistory: vi.fn(), GetHelmValues: vi.fn() },
}));
vi.mock("@wailsio/runtime", () => ({ Clipboard: {}, Events: { On: vi.fn(() => () => {}) } }));

import { CRDService, PodsService, NodesService, HelmService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { countKind, loadInstances, getInstanceDetail } from "./crd";
import { openPodDetail } from "./pods";
import { openNodeDetail } from "./nodes";
import { openHelmRelease } from "./helm";

// Review 2026-10-03 #5 (Codex's repros, adopted): a response still in flight
// when the user switches cluster must never land under the new cluster. DEV,
// STG and PRD share kinds and namespace/name, so GVR or ns/name alone is not an
// identity.
const ref = { group: "apps", version: "v1", plural: "deployments", kind: "Deployment", scope: "Namespaced" } as ResourceRef;
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}

describe("cross-cluster stale responses", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().clearCRDs();
    useFleet.getState().clearInstances();
    useFleet.getState().clearInstanceDetail();
  });

  it("counts the same kind independently per cluster", async () => {
    const old = deferred<unknown>();
    vi.mocked(CRDService.CountKind).mockReturnValueOnce(old.promise as never).mockResolvedValueOnce({ count: 22 } as never);
    useFleet.getState().setCRDLoading("A");
    const a = countKind("A", "apps", "v1", "deployments");
    await Promise.resolve();
    useFleet.getState().setCRDLoading("B");
    await countKind("B", "apps", "v1", "deployments");
    old.resolve({ count: 11 });
    await a;
    // B's count was not suppressed by A's in-flight request ...
    expect(CRDService.CountKind).toHaveBeenCalledWith("B", "apps", "v1", "deployments");
    // ... and A's late result never lands in B's slot.
    expect(useFleet.getState().crd.counts[crdCountKey("B", "apps", "v1", "deployments")].count).toBe(22);
  });

  it("never replaces B's resource list with a delayed A page", async () => {
    const old = deferred<unknown>();
    vi.mocked(CRDService.ListInstances).mockReturnValueOnce(old.promise as never).mockResolvedValueOnce({ items: [{ name: "from-B" }], nextToken: "" } as never);
    const a = loadInstances("A", ref);
    await loadInstances("B", ref);
    old.resolve({ items: [{ name: "from-A" }], nextToken: "" });
    await a;
    expect(useFleet.getState().instances.rows[0].name).toBe("from-B");
  });

  it("never replaces B's resource detail with a delayed A detail (same ns/name)", async () => {
    const old = deferred<unknown>();
    const instance = { namespace: "default", name: "api" };
    vi.mocked(CRDService.GetInstanceDetail).mockReturnValueOnce(old.promise as never).mockResolvedValueOnce({ yaml: "from-B" } as never);
    const a = getInstanceDetail("A", ref, instance);
    await getInstanceDetail("B", ref, instance);
    old.resolve({ yaml: "from-A" });
    await a;
    expect(useFleet.getState().instanceDetail.detail?.yaml).toBe("from-B");
  });

  it("pod, node and helm details are dropped when the cluster changed", async () => {
    const pod = deferred<unknown>();
    vi.mocked(PodsService.GetPodDetail).mockReturnValueOnce(pod.promise as never);
    useFleet.getState().setPodsLoading("A", "");
    const p = openPodDetail("A", "default", "api");
    useFleet.getState().setPodsLoading("B", ""); // switched cluster; same pod name selected there
    useFleet.getState().selectPod({ namespace: "default", name: "api" });
    pod.resolve({ summary: { name: "from-A" }, yaml: "from-A" });
    await p;
    expect(useFleet.getState().pods.detail).toBeNull();

    const node = deferred<unknown>();
    vi.mocked(NodesService.GetNodeDetail).mockReturnValueOnce(node.promise as never);
    useFleet.getState().setNodesLoading("A");
    const n = openNodeDetail("A", "node-1");
    useFleet.getState().setNodesLoading("B");
    useFleet.getState().selectNode({ name: "node-1" });
    node.resolve({ summary: { name: "from-A" }, yaml: "from-A" });
    await n;
    expect(useFleet.getState().nodes.detail).toBeNull();

    const hist = deferred<unknown>();
    vi.mocked(HelmService.GetHelmHistory).mockReturnValueOnce(hist.promise as never);
    vi.mocked(HelmService.GetHelmValues).mockResolvedValueOnce({ values: "from-A" } as never);
    useFleet.setState((s) => ({ helm: { ...s.helm, cluster: "A" } }));
    const h = openHelmRelease("A", "ns", "rel");
    useFleet.setState((s) => ({ helm: { ...s.helm, cluster: "B", selected: { namespace: "ns", name: "rel" } } }));
    hist.resolve({ history: [] });
    await h;
    expect(useFleet.getState().helm.values).not.toBe("from-A");
  });
});
