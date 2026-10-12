import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet, type PodDetailDTO } from "../store/fleet";

// Mock the bindings before importing the bridge.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  PodsService: {
    DeletePod: vi.fn(),
    GetPodDetail: vi.fn(),
  },
}));

import { PodsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { deletePod, openPodDetail } from "./pods";

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}
const detailOf = (uid: string, yaml = uid) => ({ summary: { namespace: "default", name: "api", uid }, labels: {}, conditions: [], events: [], yaml, qosClass: "", serviceAccount: "" }) as unknown as PodDetailDTO;

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.getState().clearPods();
  useFleet.getState().clearActionStatus();
  useFleet.getState().setPodsVisit("homelab");
});

// The Pods lens reads the Observer's live rows (1c slice 3 §5.9; there is no
// list endpoint to re-read): a verb carries the pod OBJECT it was armed against.
describe("deletePod bridge", () => {
  it("success: reports the delete and carries the armed UID", async () => {
    vi.mocked(PodsService.DeletePod).mockResolvedValue({ ok: true, error: "" } as never);
    await deletePod("homelab", "default", "api-xyz", "uid-api-xyz");
    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("success");
    expect(status?.message).toBe("pod default/api-xyz deleted");
    expect(PodsService.DeletePod).toHaveBeenCalledWith("homelab", "default", "api-xyz", "uid-api-xyz");
  });

  it("error: reports the backend's reason - a replacement included", async () => {
    vi.mocked(PodsService.DeletePod).mockResolvedValue({ ok: false, error: "delete pod default/api-xyz: the object was replaced since the action was armed - its write was refused" } as never);
    await deletePod("homelab", "default", "api-xyz", "uid-api-xyz");
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error", message: expect.stringMatching(/replaced since the action was armed/) });
  });

  it("error: uses fallback message when error field is empty", async () => {
    vi.mocked(PodsService.DeletePod).mockResolvedValue({ ok: false, error: "" } as never);
    await deletePod("homelab", "default", "api-xyz", "uid-api-xyz");
    expect(useFleet.getState().actionStatus?.message).toBe("Delete failed");
  });
});

// Design §5b: a pod detail lands only for the selected OBJECT.
describe("openPodDetail", () => {
  it("drops an answer once the selection names another object with the same name", async () => {
    const first = deferred<unknown>();
    vi.mocked(PodsService.GetPodDetail).mockReturnValueOnce(first.promise as never);
    const p = openPodDetail("homelab", "default", "api", "uid-1");
    useFleet.getState().selectPod({ namespace: "default", name: "api", uid: "uid-2" });
    first.resolve(detailOf("uid-1"));
    await p;
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("drops an answer once another pod is selected, even without UIDs", async () => {
    const first = deferred<unknown>();
    vi.mocked(PodsService.GetPodDetail).mockReturnValueOnce(first.promise as never);
    const p = openPodDetail("homelab", "default", "api", "");
    useFleet.getState().selectPod({ namespace: "default", name: "web", uid: "" });
    first.resolve(detailOf(""));
    await p;
    expect(useFleet.getState().pods.detail).toBeNull();
  });

  it("only the latest read for the selection lands", async () => {
    const older = deferred<unknown>();
    vi.mocked(PodsService.GetPodDetail).mockReturnValueOnce(older.promise as never).mockResolvedValueOnce(detailOf("uid-1", "newer") as never);
    const a = openPodDetail("homelab", "default", "api", "uid-1");
    await openPodDetail("homelab", "default", "api", "uid-1");
    older.resolve(detailOf("uid-1", "older"));
    await a;
    expect(useFleet.getState().pods.detail?.yaml).toBe("newer");
  });

  it("re-opening the same object keeps its detail while the re-read is in flight", async () => {
    vi.mocked(PodsService.GetPodDetail).mockResolvedValueOnce(detailOf("uid-1") as never);
    await openPodDetail("homelab", "default", "api", "uid-1");
    const again = deferred<unknown>();
    vi.mocked(PodsService.GetPodDetail).mockReturnValueOnce(again.promise as never);
    const p = openPodDetail("homelab", "default", "api", "uid-1");
    expect(useFleet.getState().pods.detail?.yaml).toBe("uid-1");
    expect(useFleet.getState().pods.detailLoading).toBe(true);
    again.resolve(detailOf("uid-1"));
    await p;
    expect(useFleet.getState().pods.detailLoading).toBe(false);
  });

  it("a rejected read is the read's outcome, never an empty detail", async () => {
    vi.mocked(PodsService.GetPodDetail).mockRejectedValueOnce(new Error("bridge unavailable"));
    await openPodDetail("homelab", "default", "api", "uid-1");
    expect(useFleet.getState().pods.detail?.error).toBe("bridge unavailable");
    expect(useFleet.getState().pods.detailLoading).toBe(false);
  });
});
