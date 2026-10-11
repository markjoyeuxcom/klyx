import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";
import type { ArgoAppDTO } from "../store/fleet";

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void; reject: (e: unknown) => void };
function deferred<T>(): Deferred<T> {
  let resolve!: (v: T) => void; let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}
const calls: Deferred<unknown>[] = [];
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  ArgoService: {
    ListProjects: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
  },
}));
import { listArgoProjects } from "./argo";
import { argoAppRule, withRule } from "../test/backendRules";

const app = (name: string): ArgoAppDTO => withRule<ArgoAppDTO>({
  namespace: "argocd", name, project: "default", syncStatus: "Synced", healthStatus: "Healthy", broken: false,
  revision: "", repoURL: "", path: "", chart: "", targetRevision: "", extraSources: 0, destNamespace: "", autoSync: false,
  opPhase: "", opMessage: "", conditions: [], resources: [], reconciledUnix: 0,
}, argoAppRule);

beforeEach(() => { calls.length = 0; vi.clearAllMocks(); useFleet.getState().clearArgo(); });

// The Applications and ApplicationSets are rows now (health/useArgoRows);
// the projects fetch keeps the fences the list reads had.
const project = (name: string) => ({ namespace: "argocd", name, description: "", sourceRepos: [], destinations: [], roles: 0, roleNames: [], orphanWarn: false,
  clusterResourceWhitelist: [], clusterResourceBlacklist: [], namespaceResourceWhitelist: [], namespaceResourceBlacklist: [] });

describe("Argo projects fetch fences (redesign step 4b)", () => {
  it("overlapping reads: only the newest lands", async () => {
    const older = listArgoProjects("A");
    const newer = listArgoProjects("A");
    calls[1].resolve({ available: true, projects: [project("newest")] });
    await newer;
    calls[0].resolve({ available: true, projects: [project("stale")] });
    await older;
    expect(useFleet.getState().argo.projects.map((p) => p.name)).toEqual(["newest"]);
  });

  it("a failed refresh keeps the last good rows and records the error", async () => {
    const first = listArgoProjects("A");
    calls[0].resolve({ available: true, projects: [project("good")] });
    await first;
    const again = listArgoProjects("A");
    calls[1].resolve({ available: false, message: "forbidden", error: "forbidden", projects: [] });
    await again;
    const s = useFleet.getState().argo;
    expect(s.projects.map((p) => p.name)).toEqual(["good"]);
    expect(s.projectsError).toBe("forbidden");
    expect(s.projectsAvailable).toBe(true); // a failed read is not "not installed"
  });
});

describe("Argo verbs (design §5b)", () => {
  it("carry the armed UID, and a completion never re-lists or relabels another cluster", async () => {
    const { ArgoService } = await import("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js");
    const pending: Deferred<unknown>[] = [];
    (ArgoService as unknown as Record<string, unknown>).RefreshApp = vi.fn(() => { const d = deferred<unknown>(); pending.push(d); return d.promise; });
    (ArgoService as unknown as Record<string, unknown>).SyncApp = vi.fn(() => { const d = deferred<unknown>(); pending.push(d); return d.promise; });
    const { refreshArgoApp, syncArgoApp } = await import("./argo");
    const opts = { revision: "", strategy: "hook" as const, prune: false, dryRun: false, force: false, replace: false, serverSideApply: false, resources: [] };
    const r = refreshArgoApp("A", "argocd", "same-name", "uid-1");
    const s = syncArgoApp("A", "argocd", "same-name", "uid-2", opts);
    expect(ArgoService.RefreshApp).toHaveBeenCalledWith("A", "argocd", "same-name", "uid-1");
    expect(ArgoService.SyncApp).toHaveBeenCalledWith("A", "argocd", "same-name", "uid-2", opts);
    // the operator moved to B, which has its own same-named app
    useFleet.setState((st) => ({ argo: { ...st.argo, cluster: "B", apps: [app("same-name")] } }));
    pending[0].resolve({ ok: true, error: "" });
    pending[1].resolve({ ok: true, error: "" });
    await r; await s;
    expect(useFleet.getState().argo.cluster).toBe("B");
    expect(useFleet.getState().argo.apps.map((a) => a.name)).toEqual(["same-name"]);
    expect(ArgoService.ListProjects).not.toHaveBeenCalled(); // no re-list of anything
    expect(useFleet.getState().actionStatus?.message).toMatch(/on A$/);
  });
});
