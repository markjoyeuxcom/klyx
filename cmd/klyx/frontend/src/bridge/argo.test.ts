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
    ListApplications: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
    ListProjects: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
    ListApplicationSets: vi.fn(() => { const d = deferred<unknown>(); calls.push(d); return d.promise; }),
  },
}));
import { listArgoApps } from "./argo";

const app = (name: string): ArgoAppDTO => ({
  namespace: "argocd", name, project: "default", syncStatus: "Synced", healthStatus: "Healthy", broken: false,
  revision: "", repoURL: "", path: "", chart: "", targetRevision: "", extraSources: 0, destNamespace: "", autoSync: false,
  opPhase: "", opMessage: "", conditions: [], resources: [], reconciledUnix: 0,
});

beforeEach(() => { calls.length = 0; useFleet.getState().clearArgo(); });

describe("Argo bridge fences (redesign step 4b)", () => {
  it("overlapping reads: only the newest lands", async () => {
    const older = listArgoApps("A");
    const newer = listArgoApps("A");
    calls[1].resolve({ available: true, apps: [app("newest")] });
    await newer;
    calls[0].resolve({ available: true, apps: [app("stale")] });
    await older;
    expect(useFleet.getState().argo.apps.map((a) => a.name)).toEqual(["newest"]);
  });

  it("a cluster switch starts empty - B never shows A's rows while B loads", async () => {
    const a = listArgoApps("A");
    calls[0].resolve({ available: true, apps: [app("from-A")] });
    await a;
    void listArgoApps("B");
    expect(useFleet.getState().argo.cluster).toBe("B");
    expect(useFleet.getState().argo.apps).toEqual([]);
  });

  it("a failed refresh keeps the last good rows and records the error", async () => {
    const first = listArgoApps("A");
    calls[0].resolve({ available: true, apps: [app("good")] });
    await first;
    const again = listArgoApps("A");
    calls[1].resolve({ available: false, message: "forbidden", error: "forbidden", apps: [] });
    await again;
    const s = useFleet.getState().argo;
    expect(s.apps.map((a) => a.name)).toEqual(["good"]);
    expect(s.error).toBe("forbidden");
    expect(s.available).toBe(true); // a failed read is not "not installed"
  });

  it("a transport failure is an error, never absence", async () => {
    const p = listArgoApps("A");
    calls[0].reject(new Error("timeout"));
    await p;
    expect(useFleet.getState().argo.error).toMatch(/timeout/);
  });
});
