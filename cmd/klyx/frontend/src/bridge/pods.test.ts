import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";

// Mock the bindings before importing the bridge.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  PodsService: {
    DeletePod: vi.fn(),
    ListPods: vi.fn().mockResolvedValue({ namespaces: [], pods: [] }),
    GetPodDetail: vi.fn(),
    OpenLivePods: vi.fn().mockResolvedValue({ ok: true, error: "" }),
    CloseLivePods: vi.fn().mockResolvedValue(undefined),
    CloseAll: vi.fn().mockResolvedValue(undefined),
  },
}));

// Capture Events.On handlers so tests can invoke them directly.
const eventHandlers: Record<string, (ev: unknown) => void> = {};
const offFns: Record<string, ReturnType<typeof vi.fn>> = {};
vi.mock("@wailsio/runtime", () => ({
  Events: {
    On: vi.fn((name: string, handler: (ev: unknown) => void) => {
      eventHandlers[name] = handler;
      const off = vi.fn();
      offFns[name] = off;
      return off;
    }),
  },
}));

import { PodsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { Events } from "@wailsio/runtime";
import { deletePod, openLivePods } from "./pods";

// Seed the store so we can verify list refresh.
function seedPods(cluster: string, namespace: string) {
  useFleet.setState((s) => ({
    pods: { ...s.pods, cluster, namespace, items: [], namespaces: [], loading: false, needsAttention: false, search: "", selected: null, detail: null, detailLoading: false },
  }));
}

describe("deletePod bridge", () => {
  beforeEach(() => {
    useFleet.getState().clearPods();
    useFleet.getState().clearActionStatus();
    vi.clearAllMocks();
  });

  it("success: sets success actionStatus with correct message", async () => {
    (PodsService.DeletePod as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: true, error: "" });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    seedPods("homelab", "default");

    await deletePod("homelab", "default", "api-xyz", "uid-api-xyz");

    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("success");
    expect(status?.message).toBe("pod default/api-xyz deleted");
    // the pod OBJECT the delete was armed against (design §5b)
    expect(PodsService.DeletePod).toHaveBeenCalledWith("homelab", "default", "api-xyz", "uid-api-xyz");
  });

  it("success: re-runs listPods on the current namespace", async () => {
    (PodsService.DeletePod as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: true, error: "" });
    (PodsService.ListPods as ReturnType<typeof vi.fn>).mockResolvedValue({ namespaces: [], pods: [] });
    seedPods("homelab", "monitoring");

    await deletePod("homelab", "monitoring", "grafana-abc", "uid-grafana-abc");

    expect(PodsService.ListPods).toHaveBeenCalledWith("homelab", "monitoring");
  });

  it("error: sets error actionStatus and does NOT refresh the list", async () => {
    (PodsService.DeletePod as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: false, error: "forbidden" });
    seedPods("homelab", "default");

    await deletePod("homelab", "default", "api-xyz", "uid-api-xyz");

    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("error");
    expect(status?.message).toBe("forbidden");
    // ListPods should NOT be called on failure.
    expect(PodsService.ListPods).not.toHaveBeenCalled();
  });

  it("error: uses fallback message when error field is empty", async () => {
    (PodsService.DeletePod as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: false, error: "" });
    seedPods("homelab", "default");

    await deletePod("homelab", "default", "api-xyz", "uid-api-xyz");

    expect(useFleet.getState().actionStatus?.message).toBe("Delete failed");
  });
});

describe("openLivePods bridge", () => {
  beforeEach(() => {
    useFleet.getState().clearPods();
    vi.clearAllMocks();
    // Reset captured handlers.
    for (const k of Object.keys(eventHandlers)) delete eventHandlers[k];
    for (const k of Object.keys(offFns)) delete offFns[k];
  });

  it("subscribes to data and status events and calls OpenLivePods", () => {
    seedPods("homelab", "");
    openLivePods("homelab", "");

    expect(Events.On).toHaveBeenCalledWith("livePods:homelab:", expect.any(Function));
    expect(Events.On).toHaveBeenCalledWith("livePodsStatus:homelab:", expect.any(Function));
    expect(PodsService.OpenLivePods).toHaveBeenCalledWith("homelab", "");
  });

  it("data event payload updates the pods store", () => {
    seedPods("homelab", "");
    openLivePods("homelab", "");

    const handler = eventHandlers["livePods:homelab:"];
    expect(handler).toBeDefined();
    handler({ data: { namespaces: ["default"], pods: [] } });

    expect(useFleet.getState().pods.namespaces).toEqual(["default"]);
  });

  it("status event sets live flag in store", () => {
    seedPods("homelab", "");
    openLivePods("homelab", "");

    const statusHandler = eventHandlers["livePodsStatus:homelab:"];
    expect(statusHandler).toBeDefined();
    statusHandler({ data: { live: true } });

    expect(useFleet.getState().pods.live).toBe(true);
  });

  it("cleanup unsubscribes both events and calls CloseLivePods", () => {
    seedPods("homelab", "");
    const cleanup = openLivePods("homelab", "");

    cleanup();

    expect(offFns["livePods:homelab:"]!).toHaveBeenCalled();
    expect(offFns["livePodsStatus:homelab:"]!).toHaveBeenCalled();
    expect(PodsService.CloseLivePods).toHaveBeenCalledWith("homelab", "");
  });

  it("stale-guard: emit from a replaced sub (old cluster) is dropped", () => {
    // Open A, then open B for a different cluster: B claims the store slice.
    // A late emit from A's still-registered handler must be dropped.
    openLivePods("old-cluster", "");
    const oldHandler = eventHandlers["livePods:old-cluster:"];
    openLivePods("homelab", "");

    oldHandler({ data: { namespaces: ["injected"], pods: [] } });

    // Slice belongs to "homelab"; the stale old-cluster update must be dropped.
    expect(useFleet.getState().pods.cluster).toBe("homelab");
    expect(useFleet.getState().pods.namespaces).toHaveLength(0);
  });

  // --- Mount race regressions (empty list + "manual" screenshot, 2026-06-10) ---

  it("REGRESSION: first data emit after a fresh mount (cleared store) is applied", () => {
    // No seedPods: store cluster is null, exactly like mounting the view after
    // a previous unmount's clearPods. The open must claim the slice itself or
    // the first emit is dropped by the cluster guard.
    openLivePods("homelab", "");

    const handler = eventHandlers["livePods:homelab:"];
    handler({ data: { namespaces: ["default"], pods: [] } });

    expect(useFleet.getState().pods.cluster).toBe("homelab");
    expect(useFleet.getState().pods.namespaces).toEqual(["default"]);
  });

  it("REGRESSION: first status emit after a fresh mount sets the live flag", () => {
    openLivePods("homelab", "");

    const statusHandler = eventHandlers["livePodsStatus:homelab:"];
    statusHandler({ data: { live: true } });

    expect(useFleet.getState().pods.live).toBe(true);
  });

  it("open claims the slice: loading shows until first data lands", () => {
    openLivePods("homelab", "");
    expect(useFleet.getState().pods.loading).toBe(true);

    eventHandlers["livePods:homelab:"]({ data: { namespaces: [], pods: [] } });
    expect(useFleet.getState().pods.loading).toBe(false);
  });

  it("ok:false (cluster not connected) falls back to listPods and retries until ok", async () => {
    vi.useFakeTimers();
    try {
      (PodsService.OpenLivePods as ReturnType<typeof vi.fn>)
        .mockResolvedValueOnce({ ok: false, error: "cluster not connected: homelab" })
        .mockResolvedValueOnce({ ok: true, error: "" });

      openLivePods("homelab", "");
      await vi.advanceTimersByTimeAsync(0); // flush the first open result

      // Degraded honestly: manual indicator + one-shot snapshot attempt.
      expect(useFleet.getState().pods.live).toBe(false);
      expect(PodsService.ListPods).toHaveBeenCalledWith("homelab", "");

      // Retry fires after the backoff and succeeds; no further retries scheduled.
      await vi.advanceTimersByTimeAsync(3000);
      expect(PodsService.OpenLivePods).toHaveBeenCalledTimes(2);
      await vi.advanceTimersByTimeAsync(10000);
      expect(PodsService.OpenLivePods).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("cleanup cancels a pending open retry", async () => {
    vi.useFakeTimers();
    try {
      (PodsService.OpenLivePods as ReturnType<typeof vi.fn>)
        .mockResolvedValue({ ok: false, error: "cluster not connected: homelab" });

      const cleanup = openLivePods("homelab", "");
      await vi.advanceTimersByTimeAsync(0);
      expect(PodsService.OpenLivePods).toHaveBeenCalledTimes(1);

      cleanup();
      await vi.advanceTimersByTimeAsync(10000);
      expect(PodsService.OpenLivePods).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
