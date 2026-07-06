import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, fireEvent, waitFor, act } from "@testing-library/react";
import { useFleet, ResourceRef, InstanceRef, InstanceDetailDTO, ServiceBackingDTO, HPAScalingDTO } from "../store/fleet";
import { InstanceDetail, buildFoldedDiffRows, buildLineDiff, cursorPositionForOffset, detectGitOwner, formatDiffPlainText, formatKubernetesActionError, validateResourceYAML } from "./InstanceDetail";

vi.mock("../bridge/crd", () => ({
  getInstanceDetail: vi.fn(async () => {}),
  copyText: vi.fn(async () => {}),
  revealSecretKey: vi.fn(async () => "hunter2"),
  dryRunInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
  applyInstanceYAML: vi.fn(async () => ({ ok: true, error: "" })),
}));
import { getInstanceDetail, copyText, revealSecretKey, dryRunInstanceYAML, applyInstanceYAML } from "../bridge/crd";

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

const resource: ResourceRef = { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" };
const instance: InstanceRef = { namespace: "default", name: "web-tls" };
const detail: InstanceDetailDTO = {
  kind: "Certificate", namespace: "default", name: "web-tls", created: "", labels: { app: "web" },
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
  useFleet.setState({ instanceDetail: { ref: inst, detail: detail, loading: false, ...over } });
}

function seedSecret(over: Partial<{ ref: InstanceRef | null; detail: InstanceDetailDTO | null; loading: boolean }> = {}) {
  useFleet.setState({ instanceDetail: { ref: secretInstance, detail: secretDetail, loading: false, ...over } });
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
    const { getByRole, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByRole("button", { name: "describe" }));
    // typed instances describe by the unambiguous plural.group resource arg
    expect(describeResource).toHaveBeenCalledWith("x", "certificates.cert-manager.io", "default", "web-tls");
    expect(await findByText(/Name:\s+web-tls/)).toBeTruthy();
    fireEvent.keyDown(getByRole("dialog"), { key: "Escape" });
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("renders header, conditions, events, and YAML", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText("Certificate")).toBeTruthy();
    expect(getByText("app=web")).toBeTruthy();
    expect(getByText("Ready")).toBeTruthy();
    expect(getByText(/Certificate is up to date/)).toBeTruthy();
    fireEvent.click(getByText(/events 1/));
    expect(getByText(/order failed/)).toBeTruthy();
    fireEvent.click(getByText("yaml"));
    expect(getByText(/kind: Certificate/)).toBeTruthy();
  });

  it("renders annotations as collapsed metadata", () => {
    const { getByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    expect(getByText("Annotations (1)")).toBeTruthy();
    expect(queryByText("owner=platform")).toBeNull();
    fireEvent.click(getByText("Annotations (1)"));
    expect(getByText("owner=platform")).toBeTruthy();
  });

  it("copy calls the bridge with the YAML", () => {
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
    fireEvent.click(getByText("Copy"));
    expect(copyText).toHaveBeenCalledWith(detail.yaml);
  });

  it("renders YAML editor metadata and dirty state", () => {
    const { getByText, getAllByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));

    expect(getAllByText("default/web-tls").length).toBeGreaterThan(0);
    expect(getByText("saved")).toBeTruthy();
    expect(getByText(/3 lines/)).toBeTruthy();

    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${detail.yaml}metadata:\n  name: web-tls\n` } });
    expect(getByText("modified")).toBeTruthy();
    expect(getByText(/server dry-run required before trust/)).toBeTruthy();
  });

  it("shows cursor line and column in the YAML editor footer", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
    const editor = getByLabelText("resource yaml") as HTMLTextAreaElement;

    const secondLineOffset = detail.yaml.indexOf("\n") + 1;
    editor.setSelectionRange(secondLineOffset, secondLineOffset);
    fireEvent.click(editor);
    expect(getByText("Ln 2, Col 1")).toBeTruthy();
  });

  it("shows diff counts and preview for edited YAML", () => {
    const next = `${detail.yaml}metadata:\n  name: web-tls\n`;
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
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
    fireEvent.click(getByText("yaml"));
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
    fireEvent.click(getByText("yaml"));
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
    fireEvent.click(getByText("yaml"));
    expect(getByText(/Argo CD-managed/)).toBeTruthy();
    expect(getByText(/Live edits may be reverted by GitOps or release reconciliation/)).toBeTruthy();
  });

  it("shows Flux ownership warning in the YAML editor", () => {
    seed({ detail: { ...detail, labels: { ...detail.labels, "kustomize.toolkit.fluxcd.io/name": "platform", "kustomize.toolkit.fluxcd.io/namespace": "flux-system" } } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
    expect(getByText(/Flux-managed \(flux-system\/platform\)/)).toBeTruthy();
  });

  it("does not show GitOps ownership warning for unmanaged YAML", () => {
    const { getByText, queryByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
    expect(queryByText(/Live edits may be reverted/)).toBeNull();
  });

  it("dry-runs edited YAML", async () => {
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n` } });
    fireEvent.click(getByText("Dry-run"));

    expect(await findByText("Server dry-run passed")).toBeTruthy();
    expect(dryRunInstanceYAML).toHaveBeenCalledWith("x", resource, instance, `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n`);
  });

  it("shows formatted server dry-run errors", async () => {
    vi.mocked(dryRunInstanceYAML).mockResolvedValueOnce({
      ok: false,
      error: 'certificates.cert-manager.io "web-tls" is forbidden: User "alice@example.com" cannot patch resource "certificates" in API group "cert-manager.io" in the namespace "default"',
    });
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n` } });
    fireEvent.click(getByText("Dry-run"));

    expect(await findByText("Permission denied: alice@example.com cannot patch certificates.cert-manager.io in namespace default. Check RBAC for this cluster context.")).toBeTruthy();
  });

  it("blocks dry-run and apply for local YAML errors", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
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
    fireEvent.click(getByText("yaml"));
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
    fireEvent.click(getByText("yaml"));
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
    fireEvent.click(getByText("yaml"));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: draft } });
    expect((getByText("Dry-run") as HTMLButtonElement).disabled).toBe(false);

    act(() => {
      useFleet.setState({ instanceDetail: { ref: instance, detail: { ...detail, yaml: latest }, loading: false } });
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
    fireEvent.click(getByText("yaml"));
    fireEvent.change(getByLabelText("resource yaml"), { target: { value: next } });
    fireEvent.click(getByText("Apply"));
    expect(getByText(/Apply these YAML changes/)).toBeTruthy();
    expect(getAllByText(/\+2 -0/).length).toBeGreaterThanOrEqual(1);
    fireEvent.click(getByText("Apply changes"));

    await waitFor(() => expect(applyInstanceYAML).toHaveBeenCalledWith("x", resource, instance, next));
  });

  it("shows formatted server apply errors", async () => {
    vi.mocked(applyInstanceYAML).mockResolvedValueOnce({
      ok: false,
      error: 'admission webhook "validate.cert-manager.io" denied the request: spec.secretName is required',
    });
    const next = `${detail.yaml}metadata:\n  name: web-tls\n  namespace: default\n`;
    const { getByText, getByLabelText, findByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
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
        useFleet.setState({ instanceDetail: { ref: instance, detail: { ...detail, yaml: refreshed }, loading: false } });
        resolve();
      };
    });

    const { findByText, getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText("yaml"));
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
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText(/refresh/i));
    expect(getInstanceDetail).toHaveBeenCalledWith("x", resource, instance);
  });

  it("shows a no-events note when there are none", () => {
    seed({ detail: { ...detail, events: [] } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText(/events 0/));
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
    fireEvent.click(getByLabelText("close instance panel"));
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
    useFleet.setState({ instanceDetail: { ref: instance, detail: relatedDetail, loading: false } });
    const { getByText } = render(<InstanceDetail cluster="x" resource={resource} instance={instance} />);
    fireEvent.click(getByText(/related 1/));
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
    fireEvent.click(getByText("yaml"));
    expect(getByText(/<masked>/)).toBeTruthy();
  });

  it("blocks applying masked Secret YAML", () => {
    const { getByText, getByLabelText } = render(<InstanceDetail cluster="x" resource={secretResource} instance={secretInstance} />);
    fireEvent.click(getByText("yaml"));
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
  useFleet.setState({ instanceDetail: { ref: serviceInstance, detail: serviceDetail, loading: false, ...over } });
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

  it("pod link calls openPodDetail and switches section to pods", () => {
    useFleet.getState().openCluster("x");
    seedService();
    const { getByTestId } = render(<InstanceDetail cluster="x" resource={serviceResource} instance={serviceInstance} />);
    fireEvent.click(getByTestId("pod-link-web-pod-1"));
    expect(openPodDetail).toHaveBeenCalledWith("x", "default", "web-pod-1");
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
  useFleet.setState({ instanceDetail: { ref: hpaInstance, detail: hpaDetail, loading: false, ...over } });
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
