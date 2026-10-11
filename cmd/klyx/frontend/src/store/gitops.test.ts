import { describe, it, expect, beforeEach } from "vitest";
import { useFleet, FluxResourceDTO, FluxSourceDTO } from "./fleet";
import { fluxRule, fluxSourceRule, withRule } from "../test/backendRules";

const r = (over: Partial<FluxResourceDTO>): FluxResourceDTO => withRule<FluxResourceDTO>({
  kind: "Kustomization", namespace: "flux-system", name: "flux-system",
  ready: "Ready", reason: "", message: "", revision: "main@abc", readySinceSeconds: 1, suspended: false,
  sourceKind: "", sourceName: "", ...over,
}, fluxRule);

const src = (over: Partial<FluxSourceDTO>): FluxSourceDTO => withRule<FluxSourceDTO>({
  kind: "GitRepository", namespace: "flux-system", name: "flux-system",
  ready: "Ready", reason: "", message: "", revision: "main@def", url: "https://x/y", suspended: false, ...over,
}, fluxSourceRule);

beforeEach(() => useFleet.setState({ gitops: { cluster: null, resources: [], sources: [], loading: false, expandedKey: null, detail: null } }));

describe("gitops store", () => {
  it("setGitOps stores resources and sources for a cluster", () => {
    useFleet.getState().setGitOps("x", [r({ name: "a" })], [src({ name: "s" })]);
    const g = useFleet.getState().gitops;
    expect(g.cluster).toBe("x");
    expect(g.resources).toHaveLength(1);
    expect(g.sources).toHaveLength(1);
    expect(g.loading).toBe(false);
  });
  it("setGitOpsLoading marks loading for a cluster", () => {
    useFleet.getState().setGitOpsLoading("x");
    expect(useFleet.getState().gitops.loading).toBe(true);
    expect(useFleet.getState().gitops.cluster).toBe("x");
  });
  it("clearGitOps resets the slice", () => {
    useFleet.getState().setGitOps("x", [r({})], [src({})]);
    useFleet.getState().clearGitOps();
    expect(useFleet.getState().gitops).toEqual({ cluster: null, resources: [], sources: [], loading: false, watchError: null, expandedKey: null, detail: null });
  });
});
