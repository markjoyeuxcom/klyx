import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";

// Mock the bindings before importing the bridge.
vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  WorkloadsService: {
    RolloutRestart: vi.fn(),
    ScaleWorkload: vi.fn(),
    RolloutUndo: vi.fn(),
    RolloutPause: vi.fn(),
  },
}));

import { WorkloadsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { rolloutPause, rolloutRestart, rolloutUndo, scaleWorkload } from "./workloads";

const ok = { ok: true, error: "" };

beforeEach(() => {
  useFleet.getState().clearWorkloads();
  useFleet.getState().clearActionStatus();
  vi.clearAllMocks();
});

// Design §5b: every workload verb carries the UID it was armed against (the
// rows are live; there is no list endpoint to re-read).
describe("workload verbs carry the armed UID", () => {
  it("restart, scale, undo and pause pass it to the backend", async () => {
    for (const fn of [WorkloadsService.RolloutRestart, WorkloadsService.ScaleWorkload, WorkloadsService.RolloutUndo, WorkloadsService.RolloutPause]) {
      vi.mocked(fn).mockResolvedValue(ok as never);
    }
    await rolloutRestart("homelab", "Deployment", "monitoring", "grafana", "uid-g");
    await scaleWorkload("homelab", "StatefulSet", "db", "pg", "uid-pg", 3);
    await rolloutUndo("homelab", "Deployment", "monitoring", "grafana", "uid-g", 2);
    await rolloutPause("homelab", "monitoring", "grafana", "uid-g", true);
    expect(WorkloadsService.RolloutRestart).toHaveBeenCalledWith("homelab", "Deployment", "monitoring", "grafana", "uid-g");
    expect(WorkloadsService.ScaleWorkload).toHaveBeenCalledWith("homelab", "StatefulSet", "db", "pg", "uid-pg", 3);
    expect(WorkloadsService.RolloutUndo).toHaveBeenCalledWith("homelab", "Deployment", "monitoring", "grafana", "uid-g", 2);
    expect(WorkloadsService.RolloutPause).toHaveBeenCalledWith("homelab", "monitoring", "grafana", "uid-g", true);
  });
});

describe("rolloutRestart bridge", () => {
  it("success: sets success actionStatus with correct message (Deployment)", async () => {
    vi.mocked(WorkloadsService.RolloutRestart).mockResolvedValue(ok as never);
    await rolloutRestart("homelab", "Deployment", "monitoring", "grafana", "uid-g");
    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("success");
    expect(status?.message).toBe("restart triggered for deployment monitoring/grafana");
  });

  it("success: message lowercases the kind (StatefulSet)", async () => {
    vi.mocked(WorkloadsService.RolloutRestart).mockResolvedValue(ok as never);
    await rolloutRestart("homelab", "StatefulSet", "db", "postgres", "uid-p");
    expect(useFleet.getState().actionStatus?.message).toBe("restart triggered for statefulset db/postgres");
  });

  it("error: reports the backend's reason - a replacement included", async () => {
    vi.mocked(WorkloadsService.RolloutRestart).mockResolvedValue({ ok: false, error: "rollout restart Deployment monitoring/grafana: the object was replaced since the action was armed - nothing was written" } as never);
    await rolloutRestart("homelab", "Deployment", "monitoring", "grafana", "uid-g");
    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("error");
    expect(status?.message).toMatch(/replaced since the action was armed - nothing was written/);
  });

  it("error: uses fallback message when error field is empty", async () => {
    vi.mocked(WorkloadsService.RolloutRestart).mockResolvedValue({ ok: false, error: "" } as never);
    await rolloutRestart("homelab", "Deployment", "monitoring", "grafana", "uid-g");
    expect(useFleet.getState().actionStatus?.message).toBe("Restart failed");
  });
});

describe("scaleWorkload bridge", () => {
  it("success: sets success actionStatus with kind, ns, name and replica count", async () => {
    vi.mocked(WorkloadsService.ScaleWorkload).mockResolvedValue(ok as never);
    await scaleWorkload("homelab", "Deployment", "monitoring", "grafana", "uid-g", 3);
    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("success");
    expect(status?.message).toBe("scaled deployment monitoring/grafana to 3");
  });

  it("error: sets error actionStatus", async () => {
    vi.mocked(WorkloadsService.ScaleWorkload).mockResolvedValue({ ok: false, error: "unsupported kind" } as never);
    await scaleWorkload("homelab", "DaemonSet", "kube-system", "cilium", "uid-c", 1);
    const status = useFleet.getState().actionStatus;
    expect(status?.kind).toBe("error");
    expect(status?.message).toBe("unsupported kind");
  });

  it("error: uses fallback message when error field is empty", async () => {
    vi.mocked(WorkloadsService.ScaleWorkload).mockResolvedValue({ ok: false, error: "" } as never);
    await scaleWorkload("homelab", "Deployment", "ns", "api", "uid-a", 1);
    expect(useFleet.getState().actionStatus?.message).toBe("Scale failed");
  });
});
