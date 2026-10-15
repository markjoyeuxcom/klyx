import { describe, it, expect, vi } from "vitest";

// The Line slice 8b-2: the bridge returns the generated call itself, so its
// cancel() stops the Git reads.
const call = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  GitService: { NotHereYet: (...a: unknown[]) => call(...a) },
}));
import { notHereYet } from "./notHereYet";

describe("notHereYet", () => {
  it("passes the nodes through and keeps the generated promise's cancel", () => {
    const cancel = vi.fn();
    const p = Object.assign(Promise.resolve({ results: [] }), { cancel });
    call.mockReturnValue(p);
    const nodes = [{ id: "Kustomization/flux-system/apps", url: "ssh://git@x/y.git", refKind: "branch", refName: "main", applied: "a".repeat(40), path: "./apps" }];
    const got = notHereYet(nodes);
    expect(call).toHaveBeenCalledWith(nodes);
    expect(got).toBe(p);
    got.cancel();
    expect(cancel).toHaveBeenCalled();
  });
});
