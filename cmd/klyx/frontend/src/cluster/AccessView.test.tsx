import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, act, within } from "@testing-library/react";
import { useFleet } from "../store/fleet";
import type { AccessVerdictDTO, AccessWhoCanDTO } from "../store/fleet";
import { AccessView, describeQuery } from "./AccessView";

vi.mock("../bridge/rbac", () => ({
  canI: vi.fn(async () => ({}) as AccessVerdictDTO),
  whoCan: vi.fn(async () => ({}) as AccessWhoCanDTO),
}));
import { canI, whoCan } from "../bridge/rbac";

const verdict = (over: Partial<AccessVerdictDTO> = {}): AccessVerdictDTO => ({
  allowed: true, denied: false, reason: "RBAC: allowed by ClusterRoleBinding", evaluationError: "",
  subject: "system:serviceaccount:flux-system:kustomize-controller", error: "", ...over,
});
const grant = { subjectKind: "ServiceAccount", subjectName: "kustomize-controller", subjectNamespace: "flux-system", bindingKind: "ClusterRoleBinding", bindingNamespace: "", bindingName: "cluster-reconciler", roleKind: "ClusterRole", roleName: "cluster-admin" };
type D<T> = { promise: Promise<T>; resolve: (v: T) => void };
const deferred = <T,>(): D<T> => { let resolve!: (v: T) => void; const promise = new Promise<T>((r) => (resolve = r)); return { promise, resolve }; };

describe("AccessView (redesign step 5)", () => {
  beforeEach(() => { vi.clearAllMocks(); useFleet.setState({ navIntent: null }); });

  it("starts with an explanatory empty state; Can I? needs a verb and resource", () => {
    render(<AccessView cluster="prd" />);
    expect(screen.getByText("Ask an access question.")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Can I?" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("an answer names the exact question it answered and who was checked; no lamp", async () => {
    vi.mocked(canI).mockResolvedValue(verdict());
    const { container } = render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "deployments" } });
    fireEvent.change(screen.getByLabelText("api group"), { target: { value: "apps" } });
    fireEvent.click(screen.getByRole("button", { name: "Can I?" }));
    expect(await screen.findByText("Allowed")).toBeTruthy();
    expect(screen.getByText("Can I get deployments.apps cluster-wide as you?")).toBeTruthy();
    expect(screen.getByText(/checked as system:serviceaccount/)).toBeTruthy();
    expect(container.querySelector('[role="img"]')).toBeNull();
  });

  it("explicit deny vs default deny are distinguished", async () => {
    vi.mocked(canI).mockResolvedValue(verdict({ allowed: false, denied: true, reason: "" }));
    render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(screen.getByRole("button", { name: "Can I?" }));
    expect(await screen.findByText("Denied")).toBeTruthy();
    expect(screen.getByText("an authorizer explicitly denied it")).toBeTruthy();
  });

  it("editing the form marks the answer as answering a previous question", async () => {
    vi.mocked(canI).mockResolvedValue(verdict());
    render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "deployments" } });
    fireEvent.click(screen.getByRole("button", { name: "Can I?" }));
    await screen.findByText("Allowed");
    expect(screen.queryByText(/The form has changed since this answer/)).toBeNull();
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "secrets" } });
    expect(screen.getByText(/The form has changed since this answer/)).toBeTruthy();
    expect(screen.getByText("Can I get deployments cluster-wide as you?")).toBeTruthy();
  });

  it("the ServiceAccount subject is sent only in that mode, and is required there", async () => {
    vi.mocked(canI).mockResolvedValue(verdict());
    render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "deployments" } });
    fireEvent.click(screen.getByRole("button", { name: "As ServiceAccount" }));
    expect((screen.getByRole("button", { name: "Can I?" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("service account namespace"), { target: { value: "flux-system" } });
    fireEvent.change(screen.getByLabelText("service account name"), { target: { value: "kustomize-controller" } });
    fireEvent.click(screen.getByRole("button", { name: "Can I?" }));
    await screen.findByText("Allowed");
    expect(vi.mocked(canI).mock.calls[0][1]).toMatchObject({ saNamespace: "flux-system", saName: "kustomize-controller" });
  });

  it("who-can lists grant paths; a grant opens its binding and role from the inspector", async () => {
    vi.mocked(whoCan).mockResolvedValue({ grants: [grant], clusterWideOnly: true, error: "" });
    render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(screen.getByRole("button", { name: "Who can?" }));
    expect(await screen.findByText("1 grant path")).toBeTruthy();
    expect(screen.getByText(/Cluster-wide grants only/)).toBeTruthy();
    fireEvent.click(document.querySelector(".lens-row") as HTMLElement);
    const panel = within(screen.getByTestId("lens-inspector"));
    const open = vi.spyOn(useFleet.getState(), "openResource");
    fireEvent.click(panel.getByRole("button", { name: "Open ClusterRoleBinding" }));
    expect(open).toHaveBeenCalledWith(expect.objectContaining({ plural: "clusterrolebindings" }));
  });

  it("errors are shown in the answer, never as an empty result", async () => {
    vi.mocked(whoCan).mockResolvedValue({ grants: [], clusterWideOnly: false, error: "clusterrolebindings is forbidden" });
    render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(screen.getByRole("button", { name: "Who can?" }));
    expect(await screen.findByText("clusterrolebindings is forbidden")).toBeTruthy();
    expect(screen.queryByText("no RBAC grant matches")).toBeNull();
  });

  it("a late answer from a previous cluster never lands on the new one", async () => {
    const late = deferred<AccessVerdictDTO>();
    vi.mocked(canI).mockReturnValueOnce(late.promise);
    const { rerender } = render(<AccessView cluster="prd" />);
    fireEvent.change(screen.getByLabelText("resource"), { target: { value: "secrets" } });
    fireEvent.click(screen.getByRole("button", { name: "Can I?" }));
    rerender(<AccessView cluster="dev" />);
    await act(async () => { late.resolve(verdict()); await late.promise; });
    expect(screen.queryByText("Allowed")).toBeNull();
    expect(screen.getByText("Ask an access question.")).toBeTruthy();
  });

  it("a ServiceAccount check-access intent prefills the subject", () => {
    useFleet.setState({ navIntent: { kind: "access-check", cluster: "prd", namespace: "flux-system", name: "helm-controller" } as never });
    render(<AccessView cluster="prd" />);
    expect((screen.getByLabelText("service account name") as HTMLInputElement).value).toBe("helm-controller");
  });

  it("describeQuery reads as a sentence", () => {
    expect(describeQuery({ verb: "patch", group: "", resource: "deployments", subresource: "scale", name: "web", namespace: "apps", saNamespace: "a", saName: "b" }, true))
      .toBe('patch deployments/scale "web" in apps as ServiceAccount a/b');
  });
});
