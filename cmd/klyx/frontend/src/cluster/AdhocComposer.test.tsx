import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { useFleet, AdhocResultDTO, ClusterDTO } from "../store/fleet";
import { AdhocComposer } from "./AdhocComposer";

vi.mock("../bridge/crd", () => ({
  createResourceYAML: vi.fn(async () => [] as AdhocResultDTO[]),
}));
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));
import { createResourceYAML } from "../bridge/crd";
import { armProtected } from "../bridge/configsvc";

const okResult = (over: Partial<AdhocResultDTO> = {}): AdhocResultDTO => ({
  kind: "Pod", namespace: "klyx-scratch", name: "dbg", ok: true, error: "",
  namespaceOwnerKind: "", namespaceOwnerName: "", namespaceOwnerNamespace: "", namespacePrunes: false,
  ...over,
});

function seedCluster(protectedCluster: boolean) {
  const c: ClusterDTO = { name: "x", state: "connected", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1", gitopsTier: "Absent", gitopsReason: "", networkTier: "Absent", networkReason: "", env: "", region: "", provider: "", group: "", ageSeconds: 0, protected: protectedCluster };
  useFleet.setState({ clusters: [c] });
}

describe("AdhocComposer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    seedCluster(false);
  });

  it("gates create behind a clean dry-run of the current text", async () => {
    vi.mocked(createResourceYAML).mockResolvedValueOnce([okResult()]);
    const { getByText, getByPlaceholderText } = render(<AdhocComposer cluster="x" onClose={() => {}} />);
    const create = getByText("create") as HTMLButtonElement;
    expect(create.disabled).toBe(true); // no dry-run yet

    fireEvent.change(getByPlaceholderText(/apiVersion/), { target: { value: "apiVersion: v1\nkind: Pod\nmetadata:\n  name: dbg\n  namespace: klyx-scratch\n" } });
    fireEvent.click(getByText("dry-run"));
    await waitFor(() => expect((getByText("create") as HTMLButtonElement).disabled).toBe(false));
    expect(vi.mocked(createResourceYAML)).toHaveBeenCalledWith("x", expect.stringContaining("kind: Pod"), true);

    // Editing the text invalidates the dry-run - create locks again.
    fireEvent.change(getByPlaceholderText(/apiVersion/), { target: { value: "apiVersion: v1\nkind: Pod\nmetadata:\n  name: dbg2\n  namespace: klyx-scratch\n" } });
    expect((getByText("create") as HTMLButtonElement).disabled).toBe(true);
  });

  it("a failing doc keeps create locked and shows the per-doc error", async () => {
    vi.mocked(createResourceYAML).mockResolvedValueOnce([
      okResult(),
      okResult({ kind: "ConfigMap", name: "cfg", ok: false, error: "set metadata.namespace explicitly" }),
    ]);
    const { getByText, getByPlaceholderText, findByText } = render(<AdhocComposer cluster="x" onClose={() => {}} />);
    fireEvent.change(getByPlaceholderText(/apiVersion/), { target: { value: "docs" } });
    fireEvent.click(getByText("dry-run"));
    expect(await findByText(/set metadata.namespace explicitly/)).toBeTruthy();
    expect((getByText("create") as HTMLButtonElement).disabled).toBe(true);
  });

  it("renders the red prune warning when the namespace belongs to a pruning unit", async () => {
    vi.mocked(createResourceYAML).mockResolvedValueOnce([
      okResult({ namespace: "web", namespaceOwnerKind: "Kustomization", namespaceOwnerName: "web", namespaceOwnerNamespace: "flux-system", namespacePrunes: true }),
    ]);
    const { getByText, getByPlaceholderText, findByText } = render(<AdhocComposer cluster="x" onClose={() => {}} />);
    fireEvent.change(getByPlaceholderText(/apiVersion/), { target: { value: "doc" } });
    fireEvent.click(getByText("dry-run"));
    expect(await findByText(/PRUNES: this object is not in Git and will be DELETED/)).toBeTruthy();
  });

  it("protected cluster requires the typed name and arms the one-shot grant", async () => {
    seedCluster(true);
    vi.mocked(createResourceYAML).mockResolvedValue([okResult()]);
    const { getByText, getByPlaceholderText, findByPlaceholderText } = render(<AdhocComposer cluster="x" onClose={() => {}} />);
    fireEvent.change(getByPlaceholderText(/apiVersion/), { target: { value: "doc" } });
    fireEvent.click(getByText("dry-run"));

    const arm = (await findByPlaceholderText(/type "x" to arm/)) as HTMLInputElement;
    expect((getByText("create") as HTMLButtonElement).disabled).toBe(true); // clean dry-run but unarmed
    fireEvent.change(arm, { target: { value: "x" } });
    expect((getByText("create") as HTMLButtonElement).disabled).toBe(false);

    fireEvent.click(getByText("create"));
    await waitFor(() => expect(vi.mocked(armProtected)).toHaveBeenCalledWith("x", "x"));
    expect(vi.mocked(createResourceYAML)).toHaveBeenLastCalledWith("x", "doc", false);
  });
});
