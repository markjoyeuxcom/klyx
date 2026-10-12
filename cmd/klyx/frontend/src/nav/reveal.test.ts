import { describe, it, expect, vi, beforeEach } from "vitest";
vi.mock("../bridge/helm", () => ({ openHelmRelease: vi.fn(async () => {}) }));
import { openHelmRelease } from "../bridge/helm";
import { useFleet } from "../store/fleet";
import { kindOfRef, reveal, selectsOnReveal } from "./reveal";
import { TASKS } from "./clusterAttention";

// Health 1c slice 4 §5.4: a Top link enters the emitting part's view, by
// task, part and ref, through selection intents only.

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.setState({ route: { name: "cluster", cluster: "c", section: "overview" }, navIntent: null });
});
const route = () => useFleet.getState().route;
const intent = () => useFleet.getState().navIntent;

describe("reveal routes a Top ref to its part's view", () => {
  it("core parts use their selection intents", () => {
    reveal("c", "pods", "pods", { gvr: "v1/pods", namespace: "shop", name: "api-1" });
    expect(route()).toMatchObject({ section: "pods" });
    expect(intent()).toEqual({ kind: "select-pod", cluster: "c", namespace: "shop", name: "api-1" });
    reveal("c", "workloads", "workloads", { gvr: "apps/v1/statefulsets", namespace: "data", name: "pg" });
    expect(intent()).toEqual({ kind: "select-workload", cluster: "c", workloadKind: "StatefulSet", namespace: "data", name: "pg" });
    reveal("c", "nodes", "nodes", { gvr: "v1/nodes", name: "n1" });
    expect(intent()).toEqual({ kind: "select-node", cluster: "c", node: "n1" });
    reveal("c", "flux", "reconciliations", { gvr: "helm.toolkit.fluxcd.io/v2/helmreleases", namespace: "flux-system", name: "app" });
    expect(route()).toMatchObject({ section: "gitops", fluxView: "reconciliation" });
    expect(intent()).toEqual({ kind: "select-flux", cluster: "c", fluxKind: "HelmRelease", namespace: "flux-system", name: "app" });
  });

  it("a Helm release opens its detail (a selection, not a verb)", () => {
    reveal("c", "helm", "releases", { gvr: "helm/releases", namespace: "apps", name: "web" });
    expect(route()).toMatchObject({ section: "helm" });
    expect(openHelmRelease).toHaveBeenCalledWith("c", "apps", "web");
  });

  it("secondary parts open their view and park a reveal for the lens", () => {
    const cases: [string, string, Record<string, unknown>][] = [
      ["flux", "sources", { section: "gitops", fluxView: "sources" }],
      ["argo", "applicationsets", { section: "argo", argoView: "applicationsets" }],
      ["argo", "applications", { section: "argo", argoView: "applications" }],
      ["certs", "issuers", { section: "certs" }],
      ["eso", "stores", { section: "externalsecrets" }],
      ["gateway", "routes", { section: "gatewayApi" }],
      ["karpenter", "nodepools", { section: "karpenter" }],
    ];
    for (const [task, part, r] of cases) {
      const ref = { gvr: `g/v1/${part}`, name: "x" };
      reveal("c", task, part, ref);
      expect(route()).toMatchObject(r);
      expect(intent()).toEqual({ kind: "reveal", cluster: "c", task, part, ref });
    }
  });

  it("every part's lens selects what it reveals, and no route raises an action intent", () => {
    const actions = ["drain-node", "cordon-node", "restart-workload", "scale-workload", "reconcile-flux", "suspend-flux", "sync-argo", "refresh-argo", "logs-pod", "access-check"];
    for (const t of TASKS) {
      for (const p of t.parts) {
        if (t.task === "events") continue; // evidence: no Top refs
        expect(selectsOnReveal(t.task, p.part)).toBe(true);
        reveal("c", t.task, p.part, { gvr: "g/v1/x", namespace: "n", name: "x" });
        expect(actions).not.toContain(intent()?.kind ?? "");
      }
    }
  });

  it("kinds come from the ref's resource: Issuer and ClusterIssuer told apart", () => {
    expect(kindOfRef({ gvr: "cert-manager.io/v1/issuers" })).toBe("Issuer");
    expect(kindOfRef({ gvr: "cert-manager.io/v1/clusterissuers" })).toBe("ClusterIssuer");
    expect(kindOfRef({ gvr: "external-secrets.io/v1/clustersecretstores" })).toBe("ClusterSecretStore");
    expect(kindOfRef({ gvr: "source.toolkit.fluxcd.io/v1/ocirepositories" })).toBe("OCIRepository");
  });
});
