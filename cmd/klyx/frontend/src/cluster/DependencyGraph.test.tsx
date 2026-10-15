import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, act, fireEvent } from "@testing-library/react";
import type { DepGraph } from "../bridge/depGraph";

// The Line slice 8c: the Dependencies section fetches on selection, cancels
// when the selection changes, applies a reply only for the selection that
// asked, asks Git only for nodes with Git facts, and re-walks on request.

type Call<T> = Promise<T> & { cancel: ReturnType<typeof vi.fn>; resolve: (v: T) => void; reject: (e: unknown) => void };
function call<T>(): Call<T> {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const p = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return Object.assign(p, { cancel: vi.fn(), resolve, reject });
}

const graphCalls: Call<DepGraph>[] = [];
const gitCalls: { nodes: unknown; c: Call<unknown> }[] = [];
vi.mock("../bridge/depGraph", () => ({ dependencyGraph: () => { const c = call<DepGraph>(); graphCalls.push(c); return c; } }));
vi.mock("../bridge/notHereYet", () => ({ notHereYet: (nodes: unknown) => { const c = call<unknown>(); gitCalls.push({ nodes, c }); return c; } }));
import { DependencyGraphSection } from "./DependencyGraph";

const node = (id: string, over: object = {}) => {
  const [kind, namespace, name] = id.split("/");
  return { id, group: "", kind, namespace, name, uid: `u-${name}`, ready: "True", conditionGeneration: 0, generation: 1, observedGeneration: 1,
    stale: false, suspended: false, failing: false, upstreamFailure: false, chainEnd: false, supply: [], ...over };
};
const graphOf = (name: string, over: Partial<DepGraph> = {}): DepGraph => ({
  start: `Kustomization/flux-system/${name}`, reads: 1, incomplete: [], proximity: [], edges: [],
  nodes: [node(`Kustomization/flux-system/${name}`, { git: { url: "ssh://git@x/y.git", refKind: "branch", refName: "main", applied: "a".repeat(40), path: "./apps" } })],
  ...over,
});

beforeEach(() => { graphCalls.length = 0; gitCalls.length = 0; });

describe("DependencyGraphSection", () => {
  it("cancels the walk on reselection and never applies the earlier reply", async () => {
    const { rerender, getByTestId } = render(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="apps" uid="u1" />);
    expect(graphCalls).toHaveLength(1);
    rerender(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="infra" uid="u2" />);
    expect(graphCalls[0].cancel).toHaveBeenCalled();
    expect(graphCalls).toHaveLength(2);
    await act(async () => graphCalls[0].resolve(graphOf("apps")));
    expect(getByTestId("dep-graph").textContent).toContain("Walking dependencies…");
    await act(async () => graphCalls[1].resolve(graphOf("infra")));
    expect(getByTestId("dep-graph").textContent).toContain("Kustomization flux-system/infra");
    expect(getByTestId("dep-graph").textContent).not.toContain("flux-system/apps");
  });

  it("asks Git only for nodes with Git facts, and shows what is not here yet", async () => {
    const { getByTestId } = render(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="apps" uid="u1" />);
    const g = graphOf("apps", { nodes: [...graphOf("apps").nodes, node("GitRepository/flux-system/platform")] });
    await act(async () => graphCalls[0].resolve(g));
    expect(gitCalls).toHaveLength(1);
    expect(gitCalls[0].nodes).toEqual([{ id: "Kustomization/flux-system/apps", url: "ssh://git@x/y.git", refKind: "branch", refName: "main", applied: "a".repeat(40), path: "./apps" }]);
    await act(async () => gitCalls[0].c.resolve({
      target: { ref: "refs/heads/main", name: "main", sha: "b".repeat(40), fetchedAt: "2026-10-15T20:31:00Z" },
      results: [{ id: "Kustomization/flux-system/apps", state: "behind", commits: [{ sha: "c".repeat(40), subject: "apps: raise replicas", author: "Ada", time: "2026-10-15T20:00:00Z" }], more: false,
        files: [{ status: "M", path: "apps/web.yaml" }], filesOmitted: 3 }],
    }));
    const text = getByTestId("dep-graph").textContent ?? "";
    expect(text).toContain("not here yet: 1 commit touch ./apps");
    expect(text).toContain("ccccccc apps: raise replicas");
    expect(text).toContain("M apps/web.yaml");
    expect(text).toContain("and 3 more files");
    expect(text).toContain("T main, fetched");
  });

  it("re-walks on request, says incomplete evidence, and a failed read", async () => {
    const { getByTestId, getByRole } = render(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="apps" uid="u1" />);
    await act(async () => graphCalls[0].resolve(graphOf("apps", { incomplete: ["read limit: 24 reads"] })));
    expect(getByTestId("dep-incomplete").textContent).toBe("incomplete: read limit: 24 reads");
    fireEvent.click(getByRole("button", { name: "Re-walk" }));
    expect(graphCalls).toHaveLength(2);
    await act(async () => graphCalls[1].reject(new Error("deadline exceeded")));
    expect(getByTestId("dep-graph").textContent).toContain("Dependency graph read failed: deadline exceeded");
  });

  // Design review of d4ed130: a failed fetch is disclosed beside the T it
  // kept, and a failed Git read says so; the graph stays usable.
  it("discloses a failed fetch and a failed Git read", async () => {
    const r1 = render(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="apps" uid="u1" />);
    await act(async () => graphCalls[0].resolve(graphOf("apps")));
    await act(async () => gitCalls[0].c.resolve({ target: { ref: "refs/heads/main", name: "main", sha: "b".repeat(40), fetchedAt: "2026-10-15T20:31:00Z", error: "auth failed" }, results: [] }));
    expect(r1.getByTestId("dep-graph").querySelector("[data-git-target]")?.textContent).toMatch(/^T main, history as of \d\d:\d\d; fetch failed: auth failed$/);
    r1.unmount();
    const r2 = render(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="apps" uid="u1" />);
    await act(async () => graphCalls[1].resolve(graphOf("apps")));
    await act(async () => gitCalls[1].c.reject(new Error("store unavailable")));
    expect(r2.getByTestId("dep-graph").querySelector("[data-git-error]")?.textContent).toBe("not here yet: unavailable (Git read failed: store unavailable)");
    expect(r2.getByRole("list", { name: "Dependencies" })).toBeTruthy();
  });

  it("cancels on unmount", () => {
    const { unmount } = render(<DependencyGraphSection cluster="prd" kind="Kustomization" namespace="flux-system" name="apps" uid="u1" />);
    unmount();
    expect(graphCalls[0].cancel).toHaveBeenCalled();
  });
});
