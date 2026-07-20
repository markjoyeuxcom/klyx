import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { AccessVerdictDTO, AccessWhoCanDTO } from "../store/fleet";
import { AccessView } from "./AccessView";

vi.mock("../bridge/rbac", () => ({
  canI: vi.fn(async () => ({}) as AccessVerdictDTO),
  whoCan: vi.fn(async () => ({}) as AccessWhoCanDTO),
}));
import { canI, whoCan } from "../bridge/rbac";

const verdict = (over: Partial<AccessVerdictDTO> = {}): AccessVerdictDTO => ({
  allowed: true, denied: false, reason: "RBAC: allowed by ClusterRoleBinding", evaluationError: "",
  subject: "system:serviceaccount:flux-system:kustomize-controller", error: "",
  ...over,
});

describe("AccessView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ navIntent: null });
  });

  it("runs can-i and renders an ALLOWED verdict naming the checked subject", async () => {
    vi.mocked(canI).mockResolvedValue(verdict());
    const { getByLabelText, getByText, findByText } = render(<AccessView cluster="prd" />);
    fireEvent.change(getByLabelText("resource"), { target: { value: "deployments" } });
    fireEvent.click(getByText("can i?"));
    expect(await findByText("ALLOWED")).toBeTruthy();
    expect(getByText(/checked as system:serviceaccount:flux-system:kustomize-controller/)).toBeTruthy();
    expect(getByText(/evaluated by the API server/)).toBeTruthy();
  });

  it("distinguishes explicit deny from default deny", async () => {
    vi.mocked(canI).mockResolvedValue(verdict({ allowed: false, denied: false, reason: "" }));
    const { getByLabelText, getByText, findByText } = render(<AccessView cluster="prd" />);
    fireEvent.change(getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(getByText("can i?"));
    expect(await findByText("DENIED")).toBeTruthy();
    expect(getByText(/no authorizer allowed it/)).toBeTruthy();
  });

  it("sends the service-account subject only when that mode is on", async () => {
    vi.mocked(canI).mockResolvedValue(verdict());
    const { getByLabelText, getByText, findByText } = render(<AccessView cluster="prd" />);
    fireEvent.click(getByText("service account"));
    fireEvent.change(getByLabelText("service account namespace"), { target: { value: "flux-system" } });
    fireEvent.change(getByLabelText("service account name"), { target: { value: "kustomize-controller" } });
    fireEvent.change(getByLabelText("resource"), { target: { value: "deployments" } });
    fireEvent.click(getByText("can i?"));
    await findByText("ALLOWED");
    expect(vi.mocked(canI)).toHaveBeenCalledWith("prd", expect.objectContaining({
      saNamespace: "flux-system", saName: "kustomize-controller", resource: "deployments",
    }));
  });

  it("renders who-can grant paths and the cluster-wide-only honesty note", async () => {
    vi.mocked(whoCan).mockResolvedValue({
      clusterWideOnly: true, error: "",
      grants: [{
        subjectKind: "Group", subjectName: "ops", subjectNamespace: "",
        bindingKind: "ClusterRoleBinding", bindingNamespace: "", bindingName: "global-secret-readers",
        roleKind: "ClusterRole", roleName: "secret-reader",
      }],
    });
    const { getByLabelText, getByText, findByText } = render(<AccessView cluster="prd" />);
    fireEvent.change(getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(getByText("who can?"));
    expect(await findByText(/1 grant path/)).toBeTruthy();
    expect(getByText(/cluster-wide grants only/)).toBeTruthy();
    expect(getByText(/global-secret-readers/)).toBeTruthy();
    expect(getByText(/static enumeration/)).toBeTruthy();
  });

  it("folds backend errors into the result well, never an empty panel", async () => {
    vi.mocked(whoCan).mockResolvedValue({ grants: [], clusterWideOnly: false, error: "list clusterrolebindings: forbidden" });
    const { getByLabelText, getByText, findByText } = render(<AccessView cluster="prd" />);
    fireEvent.change(getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(getByText("who can?"));
    expect(await findByText(/list clusterrolebindings: forbidden/)).toBeTruthy();
  });

  it("consumes an access-check NavIntent by prefilling the ServiceAccount subject", async () => {
    render(<AccessView cluster="prd" />);
    useFleet.setState({ navIntent: { kind: "access-check", cluster: "prd", namespace: "team-a", name: "eso" } });
    const { getByLabelText } = render(<AccessView cluster="prd" />);
    expect((getByLabelText("service account namespace") as HTMLInputElement).value).toBe("team-a");
    expect((getByLabelText("service account name") as HTMLInputElement).value).toBe("eso");
    expect(useFleet.getState().navIntent).toBeNull();
  });
});
