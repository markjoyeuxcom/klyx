import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { useFleet, AdhocObjectDTO, ClusterDTO } from "../store/fleet";
import { ScratchPanel } from "./ScratchPanel";

vi.mock("../bridge/crd", () => ({
  listAdhocObjects: vi.fn(async () => [] as AdhocObjectDTO[]),
  deleteAdhocObject: vi.fn(async () => ({ ok: true, error: "" })),
}));
vi.mock("../bridge/pods", () => ({
  launchDiagnosticPod: vi.fn(async () => ({ kind: "Pod", namespace: "klyx-scratch", name: "dbg", ok: true, error: "", namespaceOwnerKind: "", namespaceOwnerName: "", namespaceOwnerNamespace: "", namespacePrunes: false, namespaceManaged: false, argoTracking: "", argoTrackingApp: "", argoTrackingPrunes: false, argoConfigKnown: false })),
}));
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));
import { listAdhocObjects, deleteAdhocObject } from "../bridge/crd";
import { launchDiagnosticPod } from "../bridge/pods";

const obj = (over: Partial<AdhocObjectDTO> = {}): AdhocObjectDTO => ({
  kind: "Pod", group: "", version: "v1", plural: "pods", scope: "Namespaced",
  namespace: "klyx-scratch", name: "dbg", ageSeconds: 120, ...over,
});

function seed(protectedCluster = false) {
  const c: ClusterDTO = { name: "x", state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 0, protected: protectedCluster };
  useFleet.setState({ clusters: [c] });
}

describe("ScratchPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    seed();
  });

  it("lists scratch objects with age and deletes one", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue([obj()]);
    const { findByText, getByText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    expect(await findByText("klyx-scratch/dbg")).toBeTruthy();
    expect(getByText("2m")).toBeTruthy();

    fireEvent.click(getByText("delete"));
    await waitFor(() => expect(vi.mocked(deleteAdhocObject)).toHaveBeenCalledWith("x", expect.objectContaining({ name: "dbg", plural: "pods" })));
  });

  it("launches a TTL-bounded debug pod and reports back", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue([]);
    const onLaunched = vi.fn();
    const { getByText, findByText } = render(<ScratchPanel cluster="x" onClose={() => {}} onLaunched={onLaunched} />);
    await findByText(/No scratch objects/);

    fireEvent.click(getByText("launch debug pod"));
    await waitFor(() => expect(vi.mocked(launchDiagnosticPod)).toHaveBeenCalledWith("x", "dbg", "nicolaka/netshoot", 3600));
    expect(onLaunched).toHaveBeenCalledWith("klyx-scratch");
  });

  it("delete-all removes namespaces LAST so cascades cannot race per-object deletes", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue([
      obj({ kind: "Namespace", plural: "namespaces", scope: "Cluster", namespace: "", name: "klyx-scratch" }),
      obj(),
    ]);
    const { findByText, getByText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    await findByText("klyx-scratch/dbg");
    fireEvent.click(getByText("delete all"));
    await waitFor(() => expect(vi.mocked(deleteAdhocObject)).toHaveBeenCalledTimes(2));
    const calls = vi.mocked(deleteAdhocObject).mock.calls;
    expect(calls[0][1].kind).toBe("Pod");
    expect(calls[1][1].kind).toBe("Namespace");
  });

  it("protected cluster keeps launch and deletes locked until the typed name arms", async () => {
    seed(true);
    vi.mocked(listAdhocObjects).mockResolvedValue([obj()]);
    const { findByText, getByText, getByPlaceholderText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    await findByText("klyx-scratch/dbg");
    expect((getByText("launch debug pod") as HTMLButtonElement).disabled).toBe(true);
    expect((getByText("delete") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(getByPlaceholderText(/type "x" to arm/), { target: { value: "x" } });
    expect((getByText("launch debug pod") as HTMLButtonElement).disabled).toBe(false);
    expect((getByText("delete") as HTMLButtonElement).disabled).toBe(false);
  });
});
