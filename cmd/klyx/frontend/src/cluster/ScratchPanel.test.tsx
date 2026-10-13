import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { useFleet, AdhocObjectDTO, AdhocInventoryDTO, ClusterDTO } from "../store/fleet";
import { ScratchPanel } from "./ScratchPanel";

vi.mock("../bridge/crd", () => ({
  listAdhocObjects: vi.fn(async () => inv([])),
  deleteAdhocObject: vi.fn(async () => ({ ok: true, error: "" })),
}));
vi.mock("../bridge/pods", () => ({
  launchDiagnosticPod: vi.fn(async () => ({ kind: "Pod", namespace: "klyx-scratch", name: "dbg", ok: true, error: "", namespaceOwnerKind: "", namespaceOwnerName: "", namespaceOwnerNamespace: "", namespacePrunes: false, namespaceManaged: false, argoTracking: "", argoTrackingApp: "", argoTrackingPrunes: false, argoPruneOption: "", argoConfigKnown: false })),
}));
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));
import { listAdhocObjects, deleteAdhocObject } from "../bridge/crd";
import { launchDiagnosticPod } from "../bridge/pods";

const obj = (over: Partial<AdhocObjectDTO> = {}): AdhocObjectDTO => ({
  kind: "Pod", group: "", version: "v1", plural: "pods", scope: "Namespaced",
  namespace: "klyx-scratch", name: "dbg", uid: "uid-dbg", ageSeconds: 120, ...over,
});
function inv(objects: AdhocObjectDTO[], over: Partial<AdhocInventoryDTO> = {}): AdhocInventoryDTO {
  return { objects, unread: [], ...over };
}

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
    vi.mocked(listAdhocObjects).mockResolvedValue(inv([obj()]));
    const { findByText, getByText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    expect(await findByText("klyx-scratch/dbg")).toBeTruthy();
    expect(getByText("2m")).toBeTruthy();

    fireEvent.click(getByText("delete"));
    await waitFor(() => expect(vi.mocked(deleteAdhocObject)).toHaveBeenCalledWith("x", expect.objectContaining({ name: "dbg", plural: "pods", uid: "uid-dbg" })));
  });

  it("launches a TTL-bounded debug pod and reports back", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue(inv([]));
    const onLaunched = vi.fn();
    const { getByText, findByText } = render(<ScratchPanel cluster="x" onClose={() => {}} onLaunched={onLaunched} />);
    await findByText(/No scratch objects/);

    fireEvent.click(getByText("launch debug pod"));
    await waitFor(() => expect(vi.mocked(launchDiagnosticPod)).toHaveBeenCalledWith("x", "dbg", "nicolaka/netshoot", 3600));
    expect(onLaunched).toHaveBeenCalledWith("klyx-scratch");
  });

  it("delete-all removes namespaces LAST so cascades cannot race per-object deletes", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue(inv([
      obj({ kind: "Namespace", plural: "namespaces", scope: "Cluster", namespace: "", name: "klyx-scratch", uid: "uid-ns" }),
      obj(),
    ]));
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
    vi.mocked(listAdhocObjects).mockResolvedValue(inv([obj()]));
    const { findByText, getByText, getByPlaceholderText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    await findByText("klyx-scratch/dbg");
    expect((getByText("launch debug pod") as HTMLButtonElement).disabled).toBe(true);
    expect((getByText("delete") as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(getByPlaceholderText(/type "x" to arm/), { target: { value: "x" } });
    expect((getByText("launch debug pod") as HTMLButtonElement).disabled).toBe(false);
    expect((getByText("delete") as HTMLButtonElement).disabled).toBe(false);
  });

  it("a cluster change closes the panel and never deletes the old cluster's objects on the new one (Codex review 2026-10-13)", async () => {
    vi.mocked(listAdhocObjects).mockImplementation(async (c) => (c === "a" ? inv([obj()]) : new Promise<AdhocInventoryDTO>(() => {})));
    const onClose = vi.fn();
    const { findByText, queryByText, rerender } = render(<ScratchPanel cluster="a" onClose={onClose} />);
    await findByText("klyx-scratch/dbg");
    rerender(<ScratchPanel cluster="b" onClose={onClose} />);
    expect(onClose).toHaveBeenCalled();
    // The list read on a is gone, so there is nothing on screen to delete.
    expect(queryByText("klyx-scratch/dbg")).toBeNull();
    expect(queryByText("delete")).toBeNull();
    expect(vi.mocked(deleteAdhocObject)).not.toHaveBeenCalled();
  });

  it("drops a late inventory reply for an older read", async () => {
    let resolveA: (v: AdhocInventoryDTO) => void = () => {};
    vi.mocked(listAdhocObjects).mockImplementation((c) =>
      c === "a" ? new Promise<AdhocInventoryDTO>((r) => { resolveA = r; }) : Promise.resolve(inv([obj({ name: "on-b", uid: "uid-b" })])),
    );
    const { findByText, queryByText, rerender } = render(<ScratchPanel cluster="a" onClose={() => {}} />);
    rerender(<ScratchPanel cluster="b" onClose={() => {}} />);
    await findByText("klyx-scratch/on-b");
    resolveA(inv([obj({ name: "from-a", uid: "uid-a" })]));
    await new Promise((r) => setTimeout(r, 0));
    expect(queryByText("klyx-scratch/from-a")).toBeNull();
    expect(queryByText("klyx-scratch/on-b")).toBeTruthy();
  });

  it("names the kinds it could not read, and never calls an incomplete sweep empty", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue(inv([], { unread: ["clusterroles.rbac.authorization.k8s.io: forbidden"] }));
    const { findByTestId, getByText, queryByText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    expect((await findByTestId("scratch-unread")).textContent).toContain("Inventory incomplete - 1 kind could not be read");
    expect(getByText("clusterroles.rbac.authorization.k8s.io: forbidden")).toBeTruthy();
    expect(getByText(/No scratch objects in the kinds Klyx could read/)).toBeTruthy();
    expect(queryByText(/Everything Klyx creates ad-hoc shows up here/)).toBeNull();
  });

  it("a failed sweep reads as an error, never as no objects", async () => {
    vi.mocked(listAdhocObjects).mockResolvedValue(inv([], { error: "discovery failed: timeout" }));
    const { findByRole, queryByText } = render(<ScratchPanel cluster="x" onClose={() => {}} />);
    expect((await findByRole("alert")).textContent).toContain("discovery failed: timeout");
    expect(queryByText(/No scratch objects/)).toBeNull();
  });
});
