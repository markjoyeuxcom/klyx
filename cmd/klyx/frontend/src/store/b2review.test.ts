import { describe, it, expect, vi, beforeEach } from "vitest";
import { useFleet } from "./fleet";
import type { PodRowDTO, NodeSummaryDTO } from "./fleet";

vi.mock("../../bindings/github.com/moomora/klyx/internal/appbridge/index.js", () => ({ CRDService: { ListCRDs: vi.fn(), CountKind: vi.fn() } }));
vi.mock("@wailsio/runtime", () => ({ Clipboard: { SetText: vi.fn() } }));
import { CRDService } from "../../bindings/github.com/moomora/klyx/internal/appbridge/index.js";
import { listCRDs, countKind } from "../bridge/crd";
import { crdCountKey } from "./fleet";

const pod = (name: string, namespace: string) => ({ name, namespace } as unknown as PodRowDTO);

// Codex's batch-2 review probes (R2, R7), adopted.
describe("read-failure scope honesty", () => {
  beforeEach(() => {
    useFleet.getState().clearPods();
    useFleet.getState().clearNodes();
    useFleet.getState().clearCRDs();
    vi.clearAllMocks();
  });

  // The Pods lens reads the Observer's rows (1c slice 3 §5.9): the slice
  // takes the adapter's payload; a visit to another cluster starts empty.
  it("a revisit of the SAME cluster keeps its rows until the rows answer", () => {
    const s = useFleet.getState();
    s.setPodsVisit("c");
    s.setPodsFromRows("c", { items: [pod("old", "a")], namespaces: ["a"], loading: false });
    s.setPodsVisit("c");
    expect(useFleet.getState().pods.items).toHaveLength(1);
  });

  it("never relabels another cluster's pods when a new cluster's visit begins (R2)", () => {
    const s = useFleet.getState();
    s.setPodsVisit("c");
    s.setPodsFromRows("c", { items: [pod("old", "a")], namespaces: ["a"], loading: false });
    s.setPodsVisit("d");
    expect(useFleet.getState().pods.items).toHaveLength(0);
    s.setPodsFromRows("d", { items: [], namespaces: [], loading: false, error: "Pod: forbidden" });
    expect(useFleet.getState().pods.items).toHaveLength(0);
    expect(useFleet.getState().pods.error).toBe("Pod: forbidden");
  });

  it("never relabels another cluster's nodes when the new cluster fails (R2)", () => {
    const s = useFleet.getState();
    s.setNodesVisit("dev");
    s.setNodesFromRows("dev", { items: [{ name: "same-node" }] as unknown as NodeSummaryDTO[], loading: false });
    s.setNodesVisit("prd"); // a new cluster's visit starts empty, before any read
    expect(useFleet.getState().nodes.items).toHaveLength(0);
    s.setNodesFromRows("prd", { items: [], loading: false, error: "forbidden" });
    expect(useFleet.getState().nodes.items).toHaveLength(0);
  });

  it("records a REJECTED CRD read instead of showing an empty browser (R7)", async () => {
    vi.mocked(CRDService.ListCRDs).mockRejectedValue(new Error("bridge unavailable"));
    await expect(listCRDs("c")).rejects.toThrow();
    expect(useFleet.getState().crd.error).toMatch(/bridge unavailable/);
  });

  it("a rejected count is unreadable, not stuck counting (R7)", async () => {
    vi.mocked(CRDService.CountKind).mockRejectedValue(new Error("boom"));
    await countKind("c", "g", "v1", "things");
    expect(useFleet.getState().crd.counts[crdCountKey("c", "g", "v1", "things")]?.error).toBe("boom");
  });
});
