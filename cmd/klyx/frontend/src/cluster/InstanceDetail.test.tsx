import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, waitFor, act, screen } from "@testing-library/react";
import { useFleet, ResourceRef, InstanceRef, InstanceDetailDTO, AIResultDTO, ServiceBackingDTO, HPAScalingDTO } from "../store/fleet";
import { InstanceDetail, buildFoldedDiffRows, buildLineDiff, cursorPositionForOffset, detectGitOwner, formatDiffPlainText, formatKubernetesActionError, validateResourceYAML } from "./InstanceDetail";

vi.mock("../bridge/crd", () => ({
  getCRDDefinition: vi.fn(async () => ({ error: "not in this test" })),
  getInstanceDetail: vi.fn(async () => {}),
  copyText: vi.fn(async () => {}),
  revealSecretKey: vi.fn(async () => "hunter2"),
  dryRunInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
  applyInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
  // null = authoritative check unavailable -> the label-based fallback renders
  // (what most existing tests assert). Ownership tests override per-case.
  gitOpsOwnership: vi.fn(async () => null),
}));
import { getInstanceDetail, copyText, revealSecretKey, dryRunInstanceYAML, applyInstanceYAML, gitOpsOwnership } from "../bridge/crd";

vi.mock("../bridge/pods", () => ({
  openPodDetail: vi.fn(async () => {}),
}));
import { openPodDetail } from "../bridge/pods";

// ForwardPopover imports bridge/forwards which uses the Wails runtime and
// requires window. Stub it here so the test can run in jsdom without the
// Wails runtime bootstrap. This is a pre-existing issue introduced when
// ForwardPopover was added to InstanceDetail.
vi.mock("../bridge/forwards", () => ({
  startForward: vi.fn(async () => ""),
  stopForward: vi.fn(async () => {}),
}));

// DescribePane runs kubectl describe via the exec bridge; mocked so the
// describe test never needs a Wails runtime.
vi.mock("../bridge/exec", () => ({
  describeResource: vi.fn(async () => ({ ok: true, output: "Name:         web-tls", error: "" })),
}));
import { describeResource } from "../bridge/exec";

vi.mock("../bridge/ai", () => ({
  aiAvailability: vi.fn(async () => ({ backend: "off", ready: false })),
  aiEgressAllowed: vi.fn(async () => false),
  explain: vi.fn(async () => ({ ok: true, explanation: "", model: "", backend: "", groundedOn: "", error: "" })),
  suggestFix: vi.fn(async () => ({ ok: true, explanation: "", model: "", backend: "", groundedOn: "", error: "" })),
}));
import { aiAvailability, aiEgressAllowed, explain, suggestFix } from "../bridge/ai";

const resource: ResourceRef = { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" };
const instance: InstanceRef = { namespace: "default", name: "web-tls" };
const detail: InstanceDetailDTO = {
  kind: "Certificate", namespace: "default", name: "web-tls", uid: "web-tls-uid", created: "", labels: { app: "web" },
  annotations: { owner: "platform" },
  conditions: [{ type: "Ready", status: "True", reason: "Issued", message: "Certificate is up to date" }],
  events: [{ type: "Warning", reason: "Failed", message: "order failed", count: 2, lastSeen: "" }],
  yaml: "apiVersion: cert-manager.io/v1\nkind: Certificate\n",
};

const secretResource: ResourceRef = { group: "", version: "v1", plural: "secrets", kind: "Secret", scope: "Namespaced" };
const secretInstance: InstanceRef = { namespace: "default", name: "app-secret" };
const secretDetail: InstanceDetailDTO = {
  kind: "Secret", namespace: "default", name: "app-secret", created: "", labels: {},
  conditions: [], events: [],
  yaml: "apiVersion: v1\nkind: Secret\ndata:\n  password: <masked>\n",
  secretKeys: [
    { key: "password", bytes: 7 },
    { key: "token", bytes: 3 },
  ],
};

function seed(over: Partial<{ ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean }> = {}, inst = instance) {
  useFleet.setState({ instanceDetail: { cluster: "x", resource, ref: inst, detail: detail, loading: false, ...over } });
}

function seedSecret(over: Partial<{ ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean }> = {}) {
  useFleet.setState({ instanceDetail: { cluster: "x", resource: secretResource, ref: secretInstance, detail: secretDetail, loading: false, ...over } });
}

beforeEach(() => { vi.clearAllMocks(); seed(); });

describe("InstanceDetail", () => {
  it("detects GitOps and Helm ownership markers", () => {
    expect(detectGitOwner({ "argocd.argoproj.io/instance": "console-dev" }, {})?.tool).toBe("Argo CD");
    expect(detectGitOwner({ "kustomize.toolkit.fluxcd.io/name": "apps", "kustomize.toolkit.fluxcd.io/namespace": "flux-system" }, {})?.detail).toBe("flux-system/apps");
    expect(detectGitOwner({}, { "meta.helm.sh/release-name": "grafana", "meta.helm.sh/release-namespace": "monitoring" })?.tool).toBe("Helm");
    expect(detectGitOwner({ app: "web" }, { owner: "platform" })).toBeNull();
  });

  it("builds a lightweight YAML line diff", () => {
    const diff = buildLineDiff("a\nb\nc", "a\nbeta\nc");
    expect(diff.added).toBe(1);
    expect(diff.removed).toBe(1);
    expect(diff.lines.some((l) => l.type === "removed" && l.text === "b")).toBe(true);
    expect(diff.lines.some((l) => l.type === "added" && l.text === "beta")).toBe(true);
  });

  it("folds long unchanged diff context with expandable blocks", () => {
    const before = Array.from({ length: 24 }, (_, i) => `line-${i + 1}`);
    const after = [...before];
    after[11] = "line-12-changed";
    const diff = buildLineDiff(before.join("\n"), after.join("\n"));

    const folded = buildFoldedDiffRows(diff, new Set(), 3, 8);
    const folds = folded.filter((row) => row.kind === "fold");
    expect(folds.map((row) => row.hidden)).toEqual([8, 9]);
    expect(folded.some((row) => row.kind === "line" && row.line.text === "line-1")).toBe(false);
    expect(folded.some((row) => row.kind === "line" && row.line.text === "line-11")).toBe(true);

    const firstFold = folds[0];
    if (firstFold.kind !== "fold") throw new Error("expected folded row");
    const expanded = buildFoldedDiffRows(diff, new Set([firstFold.id]), 3, 8);
    expect(expanded.some((row) => row.kind === "line" && row.line.text === "line-1")).toBe(true);
  });

  it("formats a YAML diff for clipboard review", () => {
    const diff = buildLineDiff("kind: Certificate\nspec:\n  secretName: old\n", "kind: Certificate\nspec:\n  secretName: new\n");
    expect(formatDiffPlainText(diff, "default/web-tls")).toBe([
      "YAML diff for default/web-tls",
      "+1 -1",
      "",
      "    1     1   kind: Certificate",
      "    2     2   spec:",
      "    3       -   secretName: old",
      "          3 +   secretName: new",
      "    4     4   ",
    ].join("\n"));
  });

  it("formats common Kubernetes action errors", () => {
    expect(formatKubernetesActionError('certificates.cert-manager.io "web-tls" is forbidden: User "alice@example.com" cannot patch resource "certificates" in API group "cert-manager.io" in the namespace "default"')).toBe(
      "Permission denied: alice@example.com cannot patch certificates.cert-manager.io in namespace default. Check RBAC for this cluster context.",
    );
    expect(formatKubernetesActionError('admission webhook "validate.nginx.ingress.kubernetes.io" denied the request: host is already defined')).toBe(
      "Admission denied by validate.nginx.ingress.kubernetes.io: host is already defined",
    );
    expect(formatKubernetesActionError('failed calling webhook "validate.cert-manager.io": Post "https://webhook": context deadline exceeded')).toBe(
      "Admission webhook timed out: validate.cert-manager.io. This is usually transient; retry after the webhook/controller recovers.",
    );
    expect(formatKubernetesActionError('Deployment.apps "web" is invalid: spec.selector: Invalid value: field is immutable')).toBe(
      "Immutable field: Kubernetes rejected this because one or more fields cannot be changed after creation. spec.selector: Invalid value: field is immutable",
    );
    expect(formatKubernetesActionError("Operation cannot be fulfilled on certificates.cert-manager.io \"web-tls\": the object has been modified; please apply your changes to the latest version")).toBe(
      "Conflict: live object changed while editing. Review the latest YAML, then reapply.",
    );
    expect(formatKubernetesActionError('Certificate.cert-manager.io "web-tls" is invalid: spec.dnsNames: Required value')).toBe(
      "Field validation failed: spec.dnsNames: Required value",
    );
  });

  it("calculates YAML editor cursor line and column", () => {
    expect(cursorPositionForOffset("apiVersion: v1\nkind: Pod\n", 0)).toEqual({ line: 1, column: 1 });
    expect(cursorPositionForOffset("apiVersion: v1\nkind: Pod\n", 15)).toEqual({ line: 2, column: 1 });
    expect(cursorPositionForOffset("apiVersion: v1\nkind: Pod\n", 20)).toEqual({ line: 2, column: 6 });
    expect(cursorPositionForOffset("apiVersion: v1\nkind: Pod\n", 999)).toEqual({ line: 3, column: 1 });
  });

  it("validates the local Kubernetes YAML identity envelope", () => {
    const result = validateResourceYAML("apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: web-tls\n  namespace: default\n  resourceVersion: \"10\"\n", resource, instance);
    expect(result.blocking).toBe(false);
    expect(result.issues.length).toBe(0);
    expect(result.identity.metadataName).toBe("web-tls");
    expect(result.identity.metadataResourceVersion).toBe("10");
  });

  it("returns blocking local YAML errors for malformed or incomplete YAML", () => {
    const malformed = validateResourceYAML("apiVersion cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: web-tls\n", resource, instance);
    expect(malformed.blocking).toBe(true);
    expect(malformed.issues.some((issue) => issue.message.includes("expected key: value") && issue.line === 1)).toBe(true);

    const incomplete = validateResourceYAML("apiVersion: cert-manager.io/v1\nkind: Certificate\n", resource, instance);
    expect(incomplete.blocking).toBe(true);
    expect(incomplete.issues.some((issue) => issue.message === "Missing required field: metadata.name")).toBe(true);
  });

  it("warns when edited YAML changes object identity", () => {
    const result = validateResourceYAML("apiVersion: v1\nkind: Secret\nmetadata:\n  name: other\n  namespace: kube-system\n", resource, instance);
    expect(result.blocking).toBe(false);
    expect(result.issues.some((issue) => issue.message === "apiVersion changed from cert-manager.io/v1 to v1")).toBe(true);
    expect(result.issues.some((issue) => issue.message === "kind changed from Certificate to Secret")).toBe(true);
    expect(result.issues.some((issue) => issue.message === "metadata.name changed from web-tls to other")).toBe(true);
    expect(result.issues.some((issue) => issue.message === "metadata.namespace changed from default to kube-system")).toBe(true);
  });

  it("describe button opens the read-only describe pane via the bridge", async () => {
    const { getByRole } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByRole("button", { name: "Describe" }));
    // typed instances describe by the unambiguous plural.group resource arg
    expect(describeResource).toHaveBeenCalledWith("x", "certificates.cert-manager.io", "default", "web-tls");
    expect((await screen.findByTestId("describe-formatted")).textContent).toContain("web-tls");
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("renders header, conditions, events, and YAML", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText("Certificate in default")).toBeTruthy();
    expect(getByText("app=web")).toBeTruthy();
    expect(getByText("Ready")).toBeTruthy();
    expect(getByText(/Certificate is up to date/)).toBeTruthy();
    expect(getByText(/order failed/)).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(getByText(/kind: Certificate/)).toBeTruthy();
  });

  // coverage S0b: an object Klyx shows sanitised is read-only - its omitted
  // values never reached the frontend, so an edit could only write
  // placeholders back
  it("shows a sanitised manifest read-only, with the reason and no editor", () => {
    seed({ detail: { ...detail, editEligible: false, describeMode: "withheld" } });
    render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(screen.getByTestId("manifest-sanitised").textContent).toContain("cannot be edited here");
    expect(screen.queryByLabelText("resource yaml")).toBeNull();
    expect(screen.queryByText("Dry-run")).toBeNull();
  });

  it("renders annotations as collapsed metadata", () => {
    const { getByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText("Annotations · 1")).toBeTruthy();
    expect(queryByText("owner=platform")).toBeNull();
    fireEvent.click(getByText("Annotations · 1"));
    expect(getByText("owner=platform")).toBeTruthy();
  });

  it("copy calls the bridge with the YAML", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.click(getByText("Copy"));
    expect(copyText).toHaveBeenCalledWith(detail.yaml);
  });

  it("renders YAML editor metadata and dirty state", () => {
    const { getByText, getAllByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));

    expect(getAllByText("default/web-tls").length).toBeGreaterThan(0);
    expect(getByText("saved")).toBeTruthy();
    expect(getByText(/3 lines/)).toBeTruthy();

    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${detail.yaml}metadata:\n  name: web-tls\n` } });
    expect(getByText("modified")).toBeTruthy();
    expect(getByText(/server dry-run required before trust/)).toBeTruthy();
  });

  it("shows cursor line and column in the YAML editor footer", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    const editor = getByLabelText("resource yaml") as HTMLTextAreaElement;

    const secondLineOffset = detail.yaml.indexOf("\n") + 1;
    editor.setSelectionRange(secondLineOffset, secondLineOffset);
    fireEvent.click(editor);
    expect(getByText("Ln 2, Col 1")).toBeTruthy();
  });

  it("shows diff counts and preview for edited YAML", () => {
    const next = `${detail.yaml}metadata:\n  name: web-tls\n`;
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(() => getByText(/Diff \+/)).toThrow();

    fireEvent.change(getByLabelText("resource yaml"), { target: { value: next } });
    expect(getByText("Diff +2 -0")).toBeTruthy();
    expect(getByText("+2 -0")).toBeTruthy();
    fireEvent.click(getByText("Diff +2 -0"));

    expect(getByLabelText("yaml diff preview")).toBeTruthy();
    expect(getByText("metadata:")).toBeTruthy();
    expect(getByText("name: web-tls")).toBeTruthy();
  });

  it("copies the rendered YAML diff as plain text", async () => {
    const next = `${detail.yaml}metadata:\n  name: web-tls\n`;
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: next } });
    fireEvent.click(getByText("Diff +2 -0"));
    fireEvent.click(getByText("Copy diff"));

    await waitFor(() => expect(copyText).toHaveBeenCalledWith(formatDiffPlainText(buildLineDiff(detail.yaml, next), "default/web-tls")));
    expect(await findByText("Copied")).toBeTruthy();
  });

  it("folds and expands unchanged YAML diff context", () => {
    const before = [
      "apiVersion: cert-manager.io/v1",
      "kind: Certificate",
      "metadata:",
      "  name: web-tls",
      "  namespace: default",
      "spec:",
      ...Array.from({ length: 20 }, (_, i) => `  field${i + 1}: old${i + 1}`),
      "",
    ].join("\n");
    const after = before.replace("  field10: old10", "  field10: new10");
    seed({ detail: { ...detail, yaml: before } });

    const { getByText, getAllByText, getByLabelText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: after } });
    fireEvent.click(getByText(/Diff \+/));

    expect(getAllByText(/unchanged lines/).length).toBeGreaterThan(0);
    expect(queryByText("field1: old1")).toBeNull();
    expect(getByText("field10: old10")).toBeTruthy();
    expect(getByText("field10: new10")).toBeTruthy();

    fireEvent.click(getAllByText(/unchanged lines/)[0]);
    expect(getByText("field1: old1")).toBeTruthy();
  });

  it("shows GitOps ownership warning only in the YAML editor", () => {
    seed({ detail: { ...detail, labels: { ...detail.labels, "argocd.argoproj.io/instance": "console-dev" } } });
    const { getByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(queryByText(/Live edits may be reverted/)).toBeNull();
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(getByText(/Argo CD-managed/)).toBeTruthy();
    expect(getByText(/Live edits may be reverted by GitOps or release reconciliation/)).toBeTruthy();
  });

  it("shows Flux ownership warning in the YAML editor", () => {
    seed({ detail: { ...detail, labels: { ...detail.labels, "kustomize.toolkit.fluxcd.io/name": "platform", "kustomize.toolkit.fluxcd.io/namespace": "flux-system" } } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(getByText(/Flux-managed \(flux-system\/platform\)/)).toBeTruthy();
  });

  it("does not show GitOps ownership warning for unmanaged YAML", () => {
    const { queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(queryByText(/Live edits may be reverted/)).toBeNull();
  });

  it("authoritative ownership names the owning unit and beats the label heuristic", async () => {
    // No Flux labels on the object - the label heuristic sees nothing - but the
    // inventory check names the owner: the authoritative banner must render.
    vi.mocked(gitOpsOwnership).mockResolvedValueOnce({
      ownerKind: "Kustomization", ownerNamespace: "flux-system", ownerName: "apps",
      ownerPrunes: true, ownerSuspended: false, basis: "flux-inventory",
      namespaceOwnerKind: "Kustomization", namespaceOwnerNamespace: "flux-system", namespaceOwnerName: "apps",
      namespacePrunes: true, namespaceManaged: false, namespaceBasis: "flux", argoApps: [], warnings: [],
    });
    const { getByText, findByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(await findByText(/Kustomization flux-system\/apps owns this object \(Flux inventory\)/)).toBeTruthy();
    expect(getByText(/reverted on the next reconcile/)).toBeTruthy();
    // The vaguer label-based copy is replaced, not duplicated.
    expect(queryByText(/appears .*-managed/)).toBeNull();
  });

  it("a degraded ownership check surfaces its warning, never a silent unmanaged", async () => {
    vi.mocked(gitOpsOwnership).mockResolvedValueOnce({
      ownerKind: "", ownerNamespace: "", ownerName: "", ownerPrunes: false, ownerSuspended: false, basis: "",
      namespaceOwnerKind: "", namespaceOwnerNamespace: "", namespaceOwnerName: "", namespacePrunes: false, namespaceManaged: false, namespaceBasis: "", argoApps: [],
      warnings: ["Argo ownership check failed: applications is forbidden"],
    });
    const { findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(await findByText(/Ownership check degraded: Argo ownership check failed/)).toBeTruthy();
  });

  it("dry-runs edited YAML", async () => {
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n` } });
    fireEvent.click(getByText("Dry-run"));

    expect(await findByText("Server dry-run passed")).toBeTruthy();
    expect(dryRunInstanceYAML).toHaveBeenCalledWith("x", resource, instance, "web-tls-uid", `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n`);
  });

  it("shows formatted server dry-run errors", async () => {
    vi.mocked(dryRunInstanceYAML).mockResolvedValueOnce({
      ok: false,
      error: 'certificates.cert-manager.io "web-tls" is forbidden: User "alice@example.com" cannot patch resource "certificates" in API group "cert-manager.io" in the namespace "default"',
    });
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n` } });
    fireEvent.click(getByText("Dry-run"));

    expect(await findByText("Permission denied: alice@example.com cannot patch certificates.cert-manager.io in namespace default. Check RBAC for this cluster context.")).toBeTruthy();
  });

  it("blocks dry-run and apply for local YAML errors", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: "apiVersion cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: web-tls\n" } });

    expect(getByText("Local YAML errors")).toBeTruthy();
    expect(getByText(/YAML parse error: expected key: value/)).toBeTruthy();
    expect(getByText(/local YAML errors must be fixed first/)).toBeTruthy();
    expect((getByText("Dry-run") as HTMLButtonElement).disabled).toBe(true);
    expect((getByText("Apply") as HTMLButtonElement).disabled).toBe(true);
    expect(dryRunInstanceYAML).not.toHaveBeenCalled();
  });

  it("jumps to the first local YAML validation error", () => {
    const badYaml = "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n\tname: web-tls\n";
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: badYaml } });

    fireEvent.click(getByText("Jump to line 4"));
    const editor = getByLabelText("resource yaml") as HTMLTextAreaElement;
    expect(document.activeElement).toBe(editor);
    expect(editor.selectionStart).toBe(badYaml.indexOf("\t"));
    expect(editor.selectionEnd).toBe(badYaml.indexOf("\n", editor.selectionStart));
    expect(getByText("Ln 4, Col 1")).toBeTruthy();
  });

  it("shows identity warnings without blocking dry-run", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: other\n  namespace: kube-system\n" } });

    expect(getByText("Local YAML warnings")).toBeTruthy();
    expect(getByText("metadata.name changed from web-tls to other")).toBeTruthy();
    expect(getByText("metadata.namespace changed from default to kube-system")).toBeTruthy();
    expect((getByText("Dry-run") as HTMLButtonElement).disabled).toBe(false);
    expect((getByText("Apply") as HTMLButtonElement).disabled).toBe(false);
  });

  it("blocks stale edited YAML when live resourceVersion changes", () => {
    const baseline = "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: web-tls\n  namespace: default\n  resourceVersion: \"10\"\n";
    const latest = "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: web-tls\n  namespace: default\n  resourceVersion: \"11\"\n";
    const draft = `${baseline}spec:\n  secretName: web-tls\n`;
    seed({ detail: { ...detail, yaml: baseline } });

    const { getByText, getByLabelText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: draft } });
    expect((getByText("Dry-run") as HTMLButtonElement).disabled).toBe(false);

    act(() => {
      useFleet.setState({ instanceDetail: { cluster: "x", resource, ref: instance, detail: { ...detail, yaml: latest }, loading: false } });
    });

    expect(getByText(/Live object changed since this editor opened/)).toBeTruthy();
    expect(getByText(/10 -> 11/)).toBeTruthy();
    expect(getByText(/live object changed; review latest YAML first/)).toBeTruthy();
    expect((getByText("Dry-run") as HTMLButtonElement).disabled).toBe(true);
    expect((getByText("Apply") as HTMLButtonElement).disabled).toBe(true);
    expect((getByLabelText("resource yaml") as HTMLTextAreaElement).value).toBe(draft);

    fireEvent.click(getByText("Review latest"));
    expect(queryByText(/Live object changed since this editor opened/)).toBeNull();
    expect((getByLabelText("resource yaml") as HTMLTextAreaElement).value).toBe(latest);
    expect(getByText("saved")).toBeTruthy();
  });

  it("applies edited YAML behind confirmation", async () => {
    const next = `${detail.yaml}metadata:\n  name: web-tls\n`;
    const { getByText, getByLabelText, getAllByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: next } });
    fireEvent.click(getByText("Apply"));
    expect(getByText(/Apply these YAML changes/)).toBeTruthy();
    expect(getAllByText(/\+2 -0/).length).toBeGreaterThanOrEqual(1);
    fireEvent.click(getByText("Apply changes"));

    await waitFor(() => expect(applyInstanceYAML).toHaveBeenCalledWith("x", resource, instance, "web-tls-uid", next));
  });

  it("shows formatted server apply errors", async () => {
    vi.mocked(applyInstanceYAML).mockResolvedValueOnce({
      ok: false,
      error: 'admission webhook "validate.cert-manager.io" denied the request: spec.secretName is required',
    });
    const next = `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n`;
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: next } });
    fireEvent.click(getByText("Apply"));
    fireEvent.click(getByText("Apply changes"));

    expect(await findByText("Admission denied by validate.cert-manager.io: spec.secretName is required")).toBeTruthy();
  });

  it("shows post-apply refresh state and refreshed resourceVersion", async () => {
    const baseline = "apiVersion: cert-manager.io/v1\nkind: Certificate\nmetadata:\n  name: web-tls\n  namespace: default\n  resourceVersion: \"10\"\nspec:\n  secretName: web-tls\n";
    const draft = baseline.replace("secretName: web-tls", "secretName: web-tls-next");
    const refreshed = draft.replace("resourceVersion: \"10\"", "resourceVersion: \"11\"");
    seed({ detail: { ...detail, yaml: baseline } });

    let finishRefresh: (() => void) | null = null;
    const refreshPromise = new Promise<void>((resolve) => {
      finishRefresh = () => {
        useFleet.setState({ instanceDetail: { cluster: "x", resource, ref: instance, detail: { ...detail, yaml: refreshed }, loading: false } });
        resolve();
      };
    });

    const { findByText, getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    vi.mocked(getInstanceDetail).mockImplementationOnce(async () => refreshPromise);
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: draft } });
    fireEvent.click(getByText("Apply"));
    fireEvent.click(getByText("Apply changes"));

    expect(await findByText("Applied, refreshing live object...")).toBeTruthy();
    act(() => finishRefresh?.());

    expect(await findByText("Applied and refreshed live object (resourceVersion 10 -> 11)")).toBeTruthy();
    expect((getByLabelText("resource yaml") as HTMLTextAreaElement).value).toBe(refreshed);
    expect(getByText("saved")).toBeTruthy();
    expect(getByText("rv 11")).toBeTruthy();
  });

  it("refresh re-fetches", () => {
    render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(screen.getByRole("button", { name: /^More/ }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Refresh" }));
    expect(getInstanceDetail).toHaveBeenCalledWith("x", resource, instance);
  });

  it("shows a no-events note when there are none", () => {
    seed({ detail: { ...detail, events: [] } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText(/no events/i)).toBeTruthy();
  });

  it("shows a loading state before the detail arrives", () => {
    seed({ detail: null, loading: true });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText(/Loading/i)).toBeTruthy();
  });

  it("closing the panel returns to the resource instance list", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource, instance } });
    const { getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByLabelText("close details"));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.instance).toBeUndefined();
    expect(r.name === "cluster" && r.resource).toEqual(resource);
  });

  it("renders related objects and opens generic resource detail", () => {
    const relatedDetail: InstanceDetailDTO = {
      ...detail,
      related: [{
        kind: "Secret",
        namespace: "default",
        name: "web-tls-secret",
        group: "",
        version: "v1",
        plural: "secrets",
        scope: "Namespaced",
        relation: "certificate secret",
      }],
    };
    useFleet.getState().openCluster("x");
    useFleet.getState().openResource(resource);
    useFleet.setState({ instanceDetail: { cluster: "x", resource, ref: instance, detail: relatedDetail, loading: false } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("default/web-tls-secret"));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.resource?.kind).toBe("Secret");
    expect(r.name === "cluster" && r.instance).toEqual({ namespace: "default", name: "web-tls-secret" });
  });
});

describe("InstanceDetail — secrets", () => {
  beforeEach(() => { vi.clearAllMocks(); seedSecret(); });

  it("renders data section with key names", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    expect(getByText("password")).toBeTruthy();
    expect(getByText("token")).toBeTruthy();
  });

  it("shows masked dots before reveal", () => {
    const { getAllByText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    // Both keys should have Reveal buttons initially.
    const revealButtons = getAllByText("Reveal");
    expect(revealButtons.length).toBe(2);
  });

  it("reveal button calls bridge and shows decoded value", async () => {
    const { getAllByText, getByText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    const revealButtons = getAllByText("Reveal");
    await act(async () => { fireEvent.click(revealButtons[0]); });
    await waitFor(() => expect(getByText("hunter2")).toBeTruthy());
    expect(revealSecretKey).toHaveBeenCalledWith("x", "default", "app-secret", "password");
  });

  it("hide button re-masks the value", async () => {
    const { getAllByText, getByText, queryByText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    const revealButtons = getAllByText("Reveal");
    // Reveal first.
    await act(async () => { fireEvent.click(revealButtons[0]); });
    await waitFor(() => expect(getByText("hunter2")).toBeTruthy());
    // Then hide.
    const hideBtn = getByText("Hide");
    await act(async () => { fireEvent.click(hideBtn); });
    await waitFor(() => expect(queryByText("hunter2")).toBeNull());
  });

  it("copy button calls bridge and clipboard without revealing in UI", async () => {
    const { getAllByText, queryByText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    const copyButtons = getAllByText("Copy");
    await act(async () => { fireEvent.click(copyButtons[0]); });
    await waitFor(() => expect(revealSecretKey).toHaveBeenCalled());
    await waitFor(() => expect(copyText).toHaveBeenCalledWith("hunter2"));
    // Value must NOT appear in the DOM after copy-only.
    expect(queryByText("hunter2")).toBeNull();
  });

  it("yaml section still shows <masked> placeholder", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    expect(getByText(/<masked>/)).toBeTruthy();
  });

  it("blocks applying masked Secret YAML", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    fireEvent.click(screen.getByRole("tab", { name: "Manifest" }));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${secretDetail.yaml}metadata:\n  name: app-secret\n` } });

    expect(getByText(/Masked Secret YAML cannot be applied/)).toBeTruthy();
    expect((getByText("Dry-run") as HTMLButtonElement).disabled).toBe(true);
    expect((getByText("Apply") as HTMLButtonElement).disabled).toBe(true);
  });

  it("non-secret detail has no data section", () => {
    seed();
    const { queryAllByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    // "Reveal" buttons should not appear for non-secret.
    expect(queryAllByText("Reveal").length).toBe(0);
  });
});

const serviceResource: ResourceRef = { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" };
const serviceInstance: InstanceRef = { namespace: "default", name: "web" };

const readyBacking: ServiceBackingDTO = {
  ports: [{ name: "http", port: 80, protocol: "TCP" }],
  ready: 2,
  notReady: 1,
  addresses: [
    { ip: "10.0.0.1", ready: true, targetKind: "Pod", targetName: "web-pod-1" },
    { ip: "10.0.0.2", ready: true, targetKind: "Pod", targetName: "web-pod-2" },
    { ip: "10.0.0.3", ready: false, targetKind: "Pod", targetName: "web-pod-3" },
  ],
  selector: { app: "web" },
};

const zeroBacking: ServiceBackingDTO = {
  ports: [{ name: "http", port: 80, protocol: "TCP" }],
  ready: 0,
  notReady: 3,
  addresses: [
    { ip: "10.0.0.1", ready: false, targetKind: "Pod", targetName: "web-pod-1" },
  ],
  selector: {},
};

const serviceDetail: InstanceDetailDTO = {
  kind: "Service", namespace: "default", name: "web", created: "", labels: { app: "web" },
  conditions: [], events: [],
  yaml: "apiVersion: v1\nkind: Service\n",
  serviceBacking: readyBacking,
};

function seedService(over: Partial<{ ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean }> = {}) {
  useFleet.setState({ instanceDetail: { cluster: "x", resource: serviceResource, ref: serviceInstance, detail: serviceDetail, loading: false, ...over } });
}

describe("InstanceDetail — service backing", () => {
  beforeEach(() => { vi.clearAllMocks(); seedService(); });

  it("renders backing section with ready count", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    expect(getByText(/2 ready/)).toBeTruthy();
  });

  it("renders port row", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    expect(getByText(/80\/TCP/)).toBeTruthy();
  });

  it("renders IP addresses", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    expect(getByText("10.0.0.1")).toBeTruthy();
  });

  it("renders pod links for Pod-targeted addresses", () => {
    const { getByTestId } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    expect(getByTestId("pod-link-web-pod-1")).toBeTruthy();
  });

  it("pod link asks the Pods lens to select the pod and switches section to pods", () => {
    useFleet.getState().openCluster("x");
    seedService();
    const { getByTestId } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    fireEvent.click(getByTestId("pod-link-web-pod-1"));
    // the Pods lens resolves the name to its row's object (design §5b)
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", cluster: "x", namespace: "default", name: "web-pod-1" });
    expect(openPodDetail).not.toHaveBeenCalled(); // never a name-only read
    expect(useFleet.getState().route).toMatchObject({ section: "pods" });
  });

  it("renders selector chips", () => {
    // Both the labels section and the selector chips may render "app=web";
    // verify at least one appears.
    const { getAllByText } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    expect(getAllByText("app=web").length).toBeGreaterThanOrEqual(1);
  });

  it("zero-ready shows danger text", () => {
    seedService({ detail: { ...serviceDetail, serviceBacking: zeroBacking } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    expect(getByText(/no ready endpoints/)).toBeTruthy();
  });

  it("non-service detail has no backing section", () => {
    seed();
    const { queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(queryByText(/ready \/ /)).toBeNull();
    expect(queryByText(/no ready endpoints/)).toBeNull();
  });
});

const hpaResource: ResourceRef = { group: "autoscaling", version: "v2", plural: "horizontalpodautoscalers", kind: "HorizontalPodAutoscaler", scope: "Namespaced" };
const hpaInstance: InstanceRef = { namespace: "default", name: "web-hpa" };

const baseHPAScaling: HPAScalingDTO = {
  minReplicas: 2,
  maxReplicas: 10,
  currentReplicas: 4,
  desiredReplicas: 4,
  targetKind: "Deployment",
  targetName: "web",
  lastScaleUnix: 1780308000,
  metrics: [
    { name: "cpu", type: "Resource", target: "70%", current: "43%" },
  ],
};

const hpaDetail: InstanceDetailDTO = {
  kind: "HorizontalPodAutoscaler", namespace: "default", name: "web-hpa",
  created: "", labels: {},
  conditions: [], events: [],
  yaml: "kind: HorizontalPodAutoscaler\n",
  hpaScaling: baseHPAScaling,
};

function seedHPA(over: Partial<{ ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean }> = {}) {
  useFleet.setState({ instanceDetail: { cluster: "x", resource: hpaResource, ref: hpaInstance, detail: hpaDetail, loading: false, ...over } });
}

describe("InstanceDetail — HPA scaling", () => {
  beforeEach(() => { vi.clearAllMocks(); seedHPA(); });

  it("renders replica line with current, desired, min, max", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    // current → desired
    expect(getByText(/4 → 4/)).toBeTruthy();
    // min / max band
    expect(getByText(/min 2 \/ max 10/)).toBeTruthy();
  });

  it("renders metric row with name, current, and target", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(getByText("cpu")).toBeTruthy();
    expect(getByText(/43%/)).toBeTruthy();
    expect(getByText(/70%/)).toBeTruthy();
  });

  it("renders last-scaled age", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    // lastScaleUnix is non-zero so it should NOT show "never"
    expect(getByText(/last scaled:/i)).toBeTruthy();
  });

  it("shows 'never' for last scale when lastScaleUnix is 0", () => {
    seedHPA({ detail: { ...hpaDetail, hpaScaling: { ...baseHPAScaling, lastScaleUnix: 0 } } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(getByText(/never/)).toBeTruthy();
  });

  it("shows 'at max' tag when currentReplicas >= maxReplicas", () => {
    const atMaxScaling: HPAScalingDTO = { ...baseHPAScaling, currentReplicas: 10, desiredReplicas: 10 };
    seedHPA({ detail: { ...hpaDetail, hpaScaling: atMaxScaling } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(getByText("at max")).toBeTruthy();
  });

  it("does not show 'at max' tag when not at max", () => {
    const { queryByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(queryByText("at max")).toBeNull();
  });

  it("renders '—' for unknown current metric (current empty string)", () => {
    const unknownCurrentScaling: HPAScalingDTO = {
      ...baseHPAScaling,
      metrics: [{ name: "cpu", type: "Resource", target: "70%", current: "" }],
    };
    seedHPA({ detail: { ...hpaDetail, hpaScaling: unknownCurrentScaling } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(getByText("—")).toBeTruthy();
  });

  it("Deployment target renders workloads link", () => {
    const { getByTestId } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(getByTestId("hpa-target-link")).toBeTruthy();
  });

  it("clicking Deployment target link sets section to workloads", () => {
    useFleet.getState().openCluster("x");
    seedHPA();
    const { getByTestId } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    fireEvent.click(getByTestId("hpa-target-link"));
    expect(useFleet.getState().route).toMatchObject({ section: "workloads" });
  });

  it("non-Deployment target kind renders plain text (no link)", () => {
    const cronScaling: HPAScalingDTO = { ...baseHPAScaling, targetKind: "CustomResource", targetName: "my-cr" };
    seedHPA({ detail: { ...hpaDetail, hpaScaling: cronScaling } });
    const { queryByTestId, getByText } = render(<InstanceDetail cluster="x" resource={hpaResource} instance={hpaInstance} />);
    expect(queryByTestId("hpa-target-link")).toBeNull();
    expect(getByText(/CustomResource\/my-cr/)).toBeTruthy();
  });

  it("non-HPA detail has no scaling section", () => {
    seed();
    const { queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    // "min" and "max" are specific enough: won't appear in a vanilla cert-manager detail
    expect(queryByText(/min \d+ \/ max \d+/)).toBeNull();
  });
});

describe("InstanceDetail facts (the data path)", () => {
  it("renders per-kind facts in the summary, not metadata-only", () => {
    const httpRoute: InstanceDetailDTO = {
      kind: "HTTPRoute", namespace: "ollama-prod", name: "ollama", created: "", labels: {},
      // Health lives in status.parents[].conditions - carried with a scope so the
      // verdict picks it up and two parents never collide.
      conditions: [{ type: "Accepted", status: "True", reason: "Accepted", message: "", scope: "parent envoy-gateway-system/eg" }],
      events: [], yaml: "kind: HTTPRoute\n",
      facts: [
        { group: "Routing", label: "hostnames", value: "ollama.example.com", tone: "", kind: "row" },
        { group: "Routing", label: "rules", value: "1", tone: "", kind: "well" },
        { group: "Routing", label: "backends", value: "ollama:80 (w100)", tone: "", kind: "row" },
      ],
    };
    seed({ detail: httpRoute });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    // Facts render (previously the summary showed only labels/annotations).
    expect(getByText("ollama.example.com")).toBeTruthy();
    expect(getByText("ollama:80 (w100)")).toBeTruthy();
    // The scoped Accepted condition drives an honest verdict headline + scope chip.
    expect(getByText(/Accepted True/)).toBeTruthy();
    expect(getByText("parent envoy-gateway-system/eg")).toBeTruthy();
  });

  it("renders a meter fact as a bar keeping the value text", () => {
    const quota: InstanceDetailDTO = {
      kind: "ResourceQuota", namespace: "team", name: "compute", created: "", labels: {},
      conditions: [], events: [], yaml: "kind: ResourceQuota\n",
      facts: [
        { group: "Quota", label: "requests.cpu", value: "10 / 10 (100%)", tone: "danger", kind: "meter", pct: 100 },
        { group: "Quota", label: "pods", value: "3 / 50 (6%)", tone: "", kind: "meter", pct: 6 },
      ],
    };
    seed({ detail: quota });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    // The precise reading (value text) is preserved next to the bar.
    expect(getByText("10 / 10 (100%)")).toBeTruthy();
    expect(getByText("3 / 50 (6%)")).toBeTruthy();
  });

  it("renders nothing extra for a kind with no facts (graceful degrade)", () => {
    // The default Certificate detail has no facts; the panel still renders its
    // verdict, and no fact section appears.
    seed();
    const { getByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText(/Ready True/)).toBeTruthy();
    expect(queryByText("Routing")).toBeNull();
  });
});

describe("InstanceDetail cross-lens Explain (AI)", () => {
  it("hides Explain when no AI backend is configured", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "off", ready: false });
    seed();
    const { queryByRole } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    await act(async () => { await Promise.resolve(); });
    expect(queryByRole("button", { name: /explain/i })).toBeNull();
  });

  it("shows Explain and renders a grounded cross-lens result on click", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "local", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValueOnce(true);
    vi.mocked(explain).mockResolvedValueOnce({
      ok: true, backend: "local", model: "llama3.1",
      groundedOn: "Certificate/default/web-tls, ClusterIssuer/le-prod",
      explanation: "Issuance is stuck: the bound ClusterIssuer le-prod is not Ready.",
      error: "",
    });
    seed();
    const { findByRole, findByText, getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);

    const btn = await findByRole("button", { name: /explain/i });
    fireEvent.click(btn);

    expect(await findByText(/bound ClusterIssuer le-prod is not Ready/)).toBeTruthy();
    expect(getByText(/Grounded on:/)).toBeTruthy();
    expect(getByText(/verify before acting/)).toBeTruthy();
    // The shared, UID-fenced object path (no Klyx verdict for a generic kind).
    expect(vi.mocked(explain)).toHaveBeenCalledWith("x", { kind: "object", group: "cert-manager.io", version: "v1", plural: "certificates", namespace: "default", name: "web-tls", uid: "web-tls-uid" });
  });

  it("disables Explain when hosted egress is refused on a protected cluster", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "hosted", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValueOnce(false);
    seed();
    const { findByRole, queryByRole } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    // no live Explain action; the blocked reason is stated in More
    fireEvent.click(await findByRole("button", { name: /^More/ }));
    const item = (await findByRole("menuitem", { name: "Explain with AI" })) as HTMLButtonElement;
    expect(item.disabled).toBe(true);
    expect(item.title).toMatch(/protected cluster/i);
    expect(queryByRole("button", { name: /^explain$/i })).toBeNull();
  });

  // Codex review of f83c7cd (P2): a suggestion belongs to one UID.
  it("drops a late Suggest-fix reply after a same-name recreation", async () => {
    vi.mocked(aiAvailability).mockResolvedValue({ backend: "local", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValue(true);
    let resolve!: (v: AIResultDTO) => void;
    vi.mocked(suggestFix).mockReturnValueOnce(new Promise<AIResultDTO>((r) => (resolve = r)));
    seed();
    const { findByRole, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(await findByRole("button", { name: /^More/ }));
    fireEvent.click(await findByRole("menuitem", { name: /Suggest fix/ }));
    // the object is recreated under the same name before the reply lands
    act(() => seed({ detail: { ...detail, uid: "web-tls-uid-2" } }));
    await act(async () => resolve({ ok: true, backend: "local", model: "m", groundedOn: "g", explanation: "patch for the OLD object", error: "" }));
    expect(queryByText(/patch for the OLD object/)).toBeNull();
    expect(queryByText(/Klyx will not apply it/)).toBeNull();
  });

  it("clears a completed suggestion when the object is recreated or removed", async () => {
    vi.mocked(aiAvailability).mockResolvedValue({ backend: "local", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValue(true);
    vi.mocked(suggestFix).mockResolvedValueOnce({ ok: true, backend: "local", model: "m", groundedOn: "g", explanation: "patch for web-tls", error: "" });
    seed();
    const { findByRole, findByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(await findByRole("button", { name: /^More/ }));
    fireEvent.click(await findByRole("menuitem", { name: /Suggest fix/ }));
    expect(await findByText(/patch for web-tls/)).toBeTruthy();
    act(() => seed({ detail: { ...detail, uid: "web-tls-uid-2" } }));
    expect(queryByText(/patch for web-tls/)).toBeNull();
  });

  it("Suggest fix renders a copyable patch with a loud commit-to-Git disclaimer", async () => {
    vi.mocked(aiAvailability).mockResolvedValueOnce({ backend: "local", ready: true });
    vi.mocked(aiEgressAllowed).mockResolvedValueOnce(true);
    vi.mocked(suggestFix).mockResolvedValueOnce({
      ok: true, backend: "local", model: "llama3.1",
      groundedOn: "Deployment/app/api",
      explanation: "Scale to 1.\n```yaml\nspec:\n  replicas: 1\n```",
      error: "",
      redacted: 2,
    });
    seed();
    const { findByRole, findByText, getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);

    fireEvent.click(await findByRole("button", { name: /^More/ }));
    fireEvent.click(await findByRole("menuitem", { name: /Suggest fix/ }));

    expect(await findByText(/Klyx will not apply it/)).toBeTruthy();
    expect(getByText(/replicas: 1/)).toBeTruthy();
    expect(getByText("Copy suggestion")).toBeTruthy();
    // Review 2026-10-03 E: say what left the machine, and that redaction is heuristic.
    expect(getByText(/2 credential-like values redacted.*heuristic/)).toBeTruthy();
    expect(vi.mocked(suggestFix)).toHaveBeenCalledWith("x", "cert-manager.io", "v1", "certificates", "default", "web-tls", "web-tls-uid");
  });
});

// The Line slice 9 note §4.2: opened by UID, the generic inspector shows the
// object only when the read is that UID; another object at the name is
// Replaced, an authoritative NotFound is Removed, any other failure proves
// nothing (Not verified).
describe("an instance opened by UID", () => {
  const byUID: InstanceRef = { ...instance, expectUid: "web-tls-uid" };
  const gone = () => document.querySelector("[data-pinned-gone]")?.getAttribute("data-pinned-gone") ?? null;

  it("the read with that UID is the object", () => {
    seed({}, byUID);
    render(<InstanceDetail cluster="x" resource={resource} instance={byUID} />);
    expect(gone()).toBeNull();
    expect(screen.getByText("web-tls")).toBeTruthy();
  });

  it("another UID is Replaced; Open the new object re-opens the name without the expectation", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource, instance: byUID } });
    seed({ detail: { ...detail, uid: "web-tls-uid-2" } }, byUID);
    render(<InstanceDetail cluster="x" resource={resource} instance={byUID} />);
    expect(gone()).toBe("replaced");
    expect(screen.queryByText("order failed")).toBeNull(); // nothing of the new object is shown
    fireEvent.click(screen.getByRole("button", { name: "Open the new object" }));
    const r = useFleet.getState().route;
    expect(r.name === "cluster" && r.instance).toEqual({ namespace: "default", name: "web-tls" });
  });

  it("a NotFound read is Removed; any other failure is Not verified", () => {
    seed({ detail: { ...detail, uid: undefined, error: "certificates.cert-manager.io \"web-tls\" not found", notFound: true } }, byUID);
    const r = render(<InstanceDetail cluster="x" resource={resource} instance={byUID} />);
    expect(gone()).toBe("removed");
    r.unmount();
    seed({ detail: { ...detail, uid: undefined, error: "connection refused" } }, byUID);
    render(<InstanceDetail cluster="x" resource={resource} instance={byUID} />);
    expect(gone()).toBe("unverified");
  });
});

// Review of ff41862: adopting the replacement reads it, at the same name.
describe("Resources adoption", () => {
  it("Open the new object re-reads, at the same name", async () => {
    const expected = { ...instance, expectUid: "old-uid" };
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource, instance: expected } });
    seed({ detail: { ...detail, uid: "new-uid" } }, expected);
    function Routed() {
      const r = useFleet((s) => s.route);
      return r.name === "cluster" && r.resource && r.instance ? <InstanceDetail cluster={r.cluster} resource={r.resource} instance={r.instance} /> : null;
    }
    await act(async () => { render(<Routed />); });
    expect(getInstanceDetail).toHaveBeenCalledTimes(1);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Open the new object" })); });
    expect(getInstanceDetail).toHaveBeenCalledTimes(2);
  });

  it("opened by UID, a list that omits the name never makes it removed", () => {
    const expected = { ...instance, expectUid: "web-tls-uid" };
    seed({}, expected);
    render(<InstanceDetail cluster="x" resource={resource} instance={expected} removed />);
    expect(screen.queryByText("Certificate removed.")).toBeNull();
    expect(screen.getByRole("button", { name: "Describe" })).toBeTruthy();
  });
});

// Review of efbfb31: every opening reads, the same object opened again
// included - never a cleared detail left loading.
describe("an identical reopening", () => {
  it("reads again and settles", async () => {
    const expected = { ...instance, expectUid: "web-tls-uid" };
    useFleet.setState({ route: { name: "cluster", cluster: "x", section: "resources", resource, instance: expected } });
    seed({}, expected);
    vi.mocked(getInstanceDetail).mockImplementation(async () => { useFleet.getState().setInstanceDetail({ ...detail }); });
    function Routed() {
      const r = useFleet((s) => s.route);
      return r.name === "cluster" && r.resource && r.instance ? <InstanceDetail cluster={r.cluster} resource={r.resource} instance={r.instance} /> : null;
    }
    await act(async () => { render(<Routed />); });
    expect(getInstanceDetail).toHaveBeenCalledTimes(1);
    await act(async () => { useFleet.getState().openInstance(instance.namespace, instance.name, expected.expectUid); });
    expect(getInstanceDetail).toHaveBeenCalledTimes(2);
    expect(useFleet.getState().instanceDetail).toMatchObject({ loading: false, detail: { uid: "web-tls-uid" } });
    vi.mocked(getInstanceDetail).mockReset();
  });
});

// The Line slice 15b: the generic inspector's own blocks.
describe("InstanceDetail - the generic inspector (slice 15b)", () => {
  const rich: InstanceDetailDTO = {
    ...detail,
    generation: 4,
    observedGeneration: 3,
    created: "2026-10-10T10:00:00Z",
    deleting: "2026-10-16T09:00:00Z",
    finalizers: ["finalizers.fluxcd.io", "example.com/custom"],
    owners: [{ apiVersion: "apps/v1", kind: "ReplicaSet", name: "web-5f", controller: true }, { apiVersion: "example.com/v1", kind: "Widget", name: "w1" }],
    related: [{ kind: "ReplicaSet", namespace: "default", name: "web-5f", group: "apps", version: "v1", plural: "replicasets", scope: "Namespaced", relation: "owner" }],
    managers: [{ manager: "kustomize-controller", operation: "Apply", time: "2026-10-12T10:00:00Z" }, { manager: "cert-manager", operation: "Update", subresource: "status" }],
    summary: [
      { name: "Ready", type: "string", format: "", description: "Custom resource definition column (in JSONPath format): .status.x", jsonPath: ".status.x", priority: 0, numeric: false, value: "True" },
      { name: "Secret", type: "string", format: "", description: "", jsonPath: "", priority: 1, numeric: false, value: "web-tls" },
      { name: "Empty", type: "string", format: "", description: "", jsonPath: "", priority: 0, numeric: false, value: "" },
    ],
    fieldsJSON: JSON.stringify({ spec: { secretName: "web-tls", dnsNames: ["a.example", "b.example"] }, status: { notAfter: "2027-01-01T00:00:00Z" } }),
    annotations: { owner: "platform", "kubectl.kubernetes.io/last-applied-configuration": "{\"kind\":\"Certificate\"}" },
    conditions: [{ type: "Ready", status: "True", reason: "Issued", message: "ok", observedGeneration: 3 }],
  };

  it("draws the summary at every priority without empty values", () => {
    seed({ detail: rich });
    const { getByTestId } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    const s = getByTestId("instance-summary");
    expect(s.textContent).toContain("Secretweb-tls");
    expect(s.textContent).not.toContain("Empty");
  });

  it("states the object: generation behind, deletion waiting on finalizers, owners, managers", () => {
    seed({ detail: rich });
    const { getByTestId, getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    const o = getByTestId("instance-object");
    expect(o.textContent).toContain("Not yet observed: generation 4, observed 3");
    expect(o.textContent).toContain("waiting on its finalizers");
    expect(o.textContent).toContain("finalizers.fluxcd.io · the owning Flux controller");
    expect(o.textContent).toContain("kustomize-controller · Apply");
    expect(o.textContent).toContain("cert-manager · Update · status");
    // a resolvable owner links; an unresolvable one is named, never dropped
    expect(getByText("web-5f").closest("button")).toBeTruthy();
    expect(getByText("w1").closest("button")).toBeNull();
    // the owner left Related, which draws nothing when empty
    expect(document.body.textContent).not.toContain("No direct related objects found.");
  });

  it("draws spec and status as trees, and says a condition was recorded for an older generation", () => {
    seed({ detail: rich });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText("secretName")).toBeTruthy();
    expect(getByText("2 items")).toBeTruthy();
    expect(getByText("notAfter")).toBeTruthy();
    expect(getByText("for generation 3 of 4")).toBeTruthy();
  });

  it("collapses last-applied-configuration behind Show", () => {
    seed({ detail: rich });
    const { getByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText(/^Annotations/));
    expect(queryByText(/kubectl\.kubernetes\.io\/last-applied-configuration=/)).toBeNull();
    fireEvent.click(getByText("Show"));
    expect(getByText(/"kind": "Certificate"/)).toBeTruthy();
  });

  it("says why the fields are not drawn", () => {
    seed({ detail: { ...rich, fieldsJSON: "", fieldsOmitted: "too large" } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText("Too large to draw here; see Manifest.")).toBeTruthy();
  });
});
