import { describe, it, expect, vi } from "vitest";

// The Line slice 8b: the bridge returns the generated call itself, so its
// cancel() reaches Go (no async wrapper erases it).
const call = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  GitOpsService: { DependencyGraph: (...a: unknown[]) => call(...a) },
}));
import { dependencyGraph } from "./depGraph";

describe("dependencyGraph", () => {
  it("passes the selection through and keeps the generated promise's cancel", () => {
    const cancel = vi.fn();
    const p = Object.assign(Promise.resolve({ start: "", nodes: [], edges: [], incomplete: [], proximity: [], reads: 0 }), { cancel });
    call.mockReturnValue(p);
    const got = dependencyGraph("prd", "Kustomization", "flux-system", "apps", "u1");
    expect(call).toHaveBeenCalledWith("prd", "Kustomization", "flux-system", "apps", "u1");
    expect(got).toBe(p);
    got.cancel();
    expect(cancel).toHaveBeenCalled();
  });
});
