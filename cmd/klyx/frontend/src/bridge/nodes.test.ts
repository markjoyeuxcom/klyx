import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({
  NodesService: { GetNodeDetail: vi.fn() },
  NodeOpsService: { Cordon: vi.fn(), StartDrain: vi.fn() },
}));
import { NodesService, NodeOpsService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { cordonNode, openNodeDetail, startDrain } from "./nodes";
import { useFleet, type NodeDetailDTO } from "../store/fleet";

function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => { resolve = r; });
  return { promise, resolve };
}
const detailOf = (uid: string) => ({ summary: { name: "n1", uid }, labels: {}, taints: [], conditions: [], events: [], yaml: uid, podsOnNode: [] }) as unknown as NodeDetailDTO;

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.getState().clearNodes();
  useFleet.getState().setNodesVisit("c");
});

// Design §5b: a node detail lands only for the selected OBJECT.
describe("openNodeDetail", () => {
  it("drops an answer once the selection names another object with the same name", async () => {
    const first = deferred<unknown>();
    vi.mocked(NodesService.GetNodeDetail).mockReturnValueOnce(first.promise as never);
    const p = openNodeDetail("c", "n1", "uid-1");
    useFleet.getState().selectNode({ name: "n1", uid: "uid-2" });
    first.resolve(detailOf("uid-1"));
    await p;
    expect(useFleet.getState().nodes.detail).toBeNull();
  });

  it("drops an answer once another node is selected, even without UIDs", async () => {
    const first = deferred<unknown>();
    vi.mocked(NodesService.GetNodeDetail).mockReturnValueOnce(first.promise as never);
    const p = openNodeDetail("c", "n1", "");
    useFleet.getState().selectNode({ name: "n2", uid: "" });
    first.resolve(detailOf(""));
    await p;
    expect(useFleet.getState().nodes.detail).toBeNull();
  });

  it("only the latest read for the selection lands", async () => {
    const older = deferred<unknown>();
    vi.mocked(NodesService.GetNodeDetail).mockReturnValueOnce(older.promise as never).mockResolvedValueOnce(detailOf("newer") as never);
    const a = openNodeDetail("c", "n1", "uid-1");
    await openNodeDetail("c", "n1", "uid-1");
    older.resolve(detailOf("older"));
    await a;
    expect(useFleet.getState().nodes.detail?.yaml).toBe("newer");
  });

  it("re-opening the same object keeps its detail while the re-read is in flight", async () => {
    vi.mocked(NodesService.GetNodeDetail).mockResolvedValueOnce(detailOf("uid-1") as never);
    await openNodeDetail("c", "n1", "uid-1");
    const again = deferred<unknown>();
    vi.mocked(NodesService.GetNodeDetail).mockReturnValueOnce(again.promise as never);
    const p = openNodeDetail("c", "n1", "uid-1");
    expect(useFleet.getState().nodes.detail?.yaml).toBe("uid-1");
    expect(useFleet.getState().nodes.detailLoading).toBe(true);
    again.resolve(detailOf("uid-1"));
    await p;
    expect(useFleet.getState().nodes.detailLoading).toBe(false);
  });

  it("a rejected read is the read's outcome, never an empty detail", async () => {
    vi.mocked(NodesService.GetNodeDetail).mockRejectedValueOnce(new Error("bridge unavailable"));
    await openNodeDetail("c", "n1", "uid-1");
    expect(useFleet.getState().nodes.detail?.error).toBe("bridge unavailable");
    expect(useFleet.getState().nodes.detailLoading).toBe(false);
  });
});

describe("verbs carry the armed UID", () => {
  it("cordon and drain pass it through", async () => {
    vi.mocked(NodeOpsService.Cordon).mockResolvedValueOnce({ ok: true, error: "" } as never);
    vi.mocked(NodeOpsService.StartDrain).mockResolvedValueOnce({ streamId: "s1" } as never);
    await cordonNode("c", "n1", "uid-1", true);
    expect(NodeOpsService.Cordon).toHaveBeenCalledWith("c", "n1", "uid-1", true);
    const opts = { ignoreDaemonSets: true, deleteEmptyDirData: false, force: false, timeoutSeconds: 120 };
    await startDrain("c", "n1", "uid-1", opts, "s1");
    expect(NodeOpsService.StartDrain).toHaveBeenCalledWith("c", "n1", "uid-1", opts, "s1");
  });

  it("a refused cordon reports the backend's reason", async () => {
    vi.mocked(NodeOpsService.Cordon).mockResolvedValueOnce({ ok: false, error: "cordon node n1: the object was replaced since the action was armed - nothing was written" } as never);
    await cordonNode("c", "n1", "uid-1", true);
    expect(useFleet.getState().actionStatus).toMatchObject({ kind: "error", message: expect.stringMatching(/replaced since the action was armed - nothing was written/) });
  });
});
