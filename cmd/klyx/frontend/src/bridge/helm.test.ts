import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";

type Deferred<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): Deferred<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => (resolve = r)); return { promise, resolve }; };
const list = vi.fn();
const history = vi.fn();
const values = vi.fn();
const rollback = vi.fn();
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  HelmService: {
    ListHelmReleases: (...a: unknown[]) => list(...a),
    GetHelmHistory: (...a: unknown[]) => history(...a),
    GetHelmValues: (...a: unknown[]) => values(...a),
    HelmRollback: (...a: unknown[]) => rollback(...a),
  },
}));
import { listHelmReleases, openHelmRelease, helmRollback } from "./helm";

const rel = { name: "app", namespace: "ns", chart: "app-1.0.0", appVersion: "1.0", status: "deployed", revision: 2, updatedUnix: 1 };

describe("Helm bridge (redesign step 5)", () => {
  beforeEach(() => { useFleet.getState().clearHelm(); vi.clearAllMocks(); });

  it("a failed re-read keeps the last good snapshot, marked with the error", async () => {
    list.mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    await listHelmReleases("A");
    const at = useFleet.getState().helm.snapshotAt;
    expect(at).not.toBeNull();
    list.mockResolvedValueOnce({ available: false, message: "secrets is forbidden", releases: [], error: "secrets is forbidden" });
    await listHelmReleases("A");
    const h = useFleet.getState().helm;
    expect(h.releases).toEqual([rel]);
    expect(h.error).toBe("secrets is forbidden");
    expect(h.snapshotAt).toBe(at);
  });

  it("a failed first read is a failure, never an empty list; a missing CLI is a capability fact", async () => {
    list.mockResolvedValueOnce({ available: false, message: "context deadline exceeded", releases: [], error: "context deadline exceeded" });
    await listHelmReleases("A");
    expect(useFleet.getState().helm.error).toBe("context deadline exceeded");
    expect(useFleet.getState().helm.snapshotAt).toBeNull();
    list.mockResolvedValueOnce({ available: false, message: "helm not found", releases: [] });
    await listHelmReleases("A");
    expect(useFleet.getState().helm.error).toBeNull();
    expect(useFleet.getState().helm.available).toBe(false);
  });

  it("another cluster never inherits a snapshot", async () => {
    list.mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    await listHelmReleases("A");
    const pending = deferred<unknown>();
    list.mockReturnValueOnce(pending.promise);
    const p = listHelmReleases("B");
    expect(useFleet.getState().helm.releases).toEqual([]);
    expect(useFleet.getState().helm.snapshotAt).toBeNull();
    pending.resolve({ available: true, message: "", releases: [] });
    await p;
  });

  it("detail reads are pinned to their request: a slower first read never overwrites a newer one", async () => {
    list.mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    await listHelmReleases("A");
    const first = deferred<unknown>();
    history.mockReturnValueOnce(first.promise).mockResolvedValueOnce({ history: [{ revision: 2, status: "deployed", chart: "", appVersion: "", description: "new", updatedUnix: 1 }] });
    values.mockResolvedValue({ values: "" });
    const a = openHelmRelease("A", "ns", "app");
    await openHelmRelease("A", "ns", "app");
    first.resolve({ history: [{ revision: 1, status: "deployed", chart: "", appVersion: "", description: "old", updatedUnix: 1 }] });
    await a;
    expect(useFleet.getState().helm.history[0].description).toBe("new");
  });

  it("detail read errors are surfaced, never an empty history", async () => {
    list.mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    await listHelmReleases("A");
    history.mockResolvedValueOnce({ history: [], error: "release: not found" });
    values.mockResolvedValueOnce({ values: "", error: "forbidden" });
    await openHelmRelease("A", "ns", "app");
    expect(useFleet.getState().helm.historyError).toBe("release: not found");
    expect(useFleet.getState().helm.valuesError).toBe("forbidden");
  });
});

describe("Helm bridge fences (Codex f66e887c)", () => {
  beforeEach(() => { useFleet.getState().clearHelm(); vi.clearAllMocks(); });
  const opts = { wait: true, noHooks: false, timeoutSeconds: 120 };

  it("an older list success or failure finishing after a newer read never replaces it", async () => {
    const older = deferred<unknown>();
    list.mockReturnValueOnce(older.promise).mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    const a = listHelmReleases("A");
    await listHelmReleases("A");
    older.resolve({ available: true, message: "", releases: [] });
    await a;
    expect(useFleet.getState().helm.releases).toEqual([rel]);
    const olderFail = deferred<unknown>();
    list.mockReturnValueOnce(olderFail.promise).mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    const b = listHelmReleases("A");
    await listHelmReleases("A");
    olderFail.resolve({ available: false, message: "boom", releases: [], error: "boom" });
    await b;
    expect(useFleet.getState().helm.error).toBeNull();
  });

  it("a rollback on A finishing while B is viewed never re-lists A into view", async () => {
    list.mockResolvedValueOnce({ available: true, message: "", releases: [rel] });
    await listHelmReleases("A");
    const done = deferred<unknown>();
    rollback.mockReturnValueOnce(done.promise);
    const p = helmRollback("A", "ns", "app", 1, opts);
    list.mockResolvedValueOnce({ available: true, message: "", releases: [] });
    await listHelmReleases("B");
    list.mockClear();
    done.resolve({ ok: true, error: "" });
    await p;
    expect(list).not.toHaveBeenCalled();
    expect(useFleet.getState().helm.cluster).toBe("B");
  });

  it("a rollback on X never re-opens X once the operator inspects Y", async () => {
    const y = { ...rel, name: "y" };
    list.mockResolvedValue({ available: true, message: "", releases: [rel, y] });
    await listHelmReleases("A");
    history.mockResolvedValue({ history: [] });
    values.mockResolvedValue({ values: "" });
    const done = deferred<unknown>();
    rollback.mockReturnValueOnce(done.promise);
    const p = helmRollback("A", "ns", "app", 1, opts);
    await openHelmRelease("A", "ns", "y");
    history.mockClear();
    done.resolve({ ok: true, error: "" });
    await p;
    expect(history).not.toHaveBeenCalledWith("A", "ns", "app");
    expect(useFleet.getState().helm.selected?.name).toBe("y");
  });
});
