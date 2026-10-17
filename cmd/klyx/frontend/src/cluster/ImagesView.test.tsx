import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, screen, waitFor } from "@testing-library/react";
import type { ImageRows } from "../health/useImageRows";
import type { ImageAutomationDTO, ImageCommonDTO, ImagePolicyDTO, ImageRepositoryDTO, ImageRowDTO } from "../bridge/fluximages";
import { useFleet } from "../store/fleet";
import { ImagesView } from "./ImagesView";

vi.mock("../bridge/fluximages", () => ({
  getImageDetail: vi.fn(async (_c: string, _k: string, _ns: string, _n: string, uid: string) => ({ uid, events: [] })),
}));
vi.mock("../bridge/gitops", () => ({ reconcile: vi.fn(async () => {}), setSuspend: vi.fn(async () => {}) }));
import { getImageDetail } from "../bridge/fluximages";
import { reconcile } from "../bridge/gitops";

let rowsValue: ImageRows;
vi.mock("../health/useImageRows", () => ({ useImageRows: () => rowsValue }));
const live = (kind: string) => ({ kind, served: true, synced: true, coverage: "live" as const });
function setRows(rows: ImageRowDTO[], over: Partial<ImageRows> = {}) {
  rowsValue = {
    rows, coverage: [live("ImageRepository"), live("ImagePolicy"), live("ImageUpdateAutomation")], watchError: null, loading: false,
    rowsState: { updating: false, refreshing: false, evaluatedAt: null }, counts: null, ...over,
  };
}

const ok = { included: false, role: "ok", label: "Ready" };
const fault = { included: true, role: "fault", label: "Failed" };
const common = (kind: string, name: string, over: Partial<ImageCommonDTO> = {}): ImageCommonDTO => ({
  uid: `uid-${name}`, gvr: `image.toolkit.fluxcd.io/v1/${kind.toLowerCase()}s`, kind, namespace: "apps", name, generation: 1,
  suspended: false, ready: "Ready", clipped: [], incomplete: [], rule: ok, ...over,
});
const repo = (name: string, over: Partial<ImageRepositoryDTO> = {}): ImageRowDTO => {
  const r: ImageRepositoryDTO = { ...common("ImageRepository", name), image: "ghcr.io/org/app", exclusionList: [], scanned: true, tagCount: 42,
    scanTime: "2026-10-17T09:55:00Z", latestTags: ["v9", "v8"], policies: [], ...over };
  return { uid: r.uid, kind: "ImageRepository", rule: r.rule, repository: r };
};
const policy = (name: string, over: Partial<ImagePolicyDTO> = {}): ImageRowDTO => {
  const p: ImagePolicyDTO = { ...common("ImagePolicy", name), repositoryNamespace: "flux-system", repositoryName: "app", policyType: "semver", policyValue: ">=1.0.0",
    latest: { name: "ghcr.io/org/app", tag: "1.2.0", text: "ghcr.io/org/app:1.2.0" }, automations: [],
    repository: { state: "found", namespace: "flux-system", name: "app", label: "Ready", role: "ok" }, ...over };
  return { uid: p.uid, kind: "ImagePolicy", rule: p.rule, policy: p };
};
const automation = (name: string, over: Partial<ImageAutomationDTO> = {}): ImageRowDTO => {
  const a: ImageAutomationDTO = { ...common("ImageUpdateAutomation", name), sourceKind: "GitRepository", sourceName: "fleet", sourceNamespace: "flux-system",
    interval: "10m", pushBranch: "auto", lastPushCommit: "abc1234def5678", lastPushTime: "2026-10-17T09:51:00Z", observedComplete: true, uses: [], usesState: "found", ...over };
  return { uid: a.uid, kind: "ImageUpdateAutomation", rule: a.rule, automation: a };
};
const rowTexts = () => [...document.querySelectorAll(".lens-row")].map((r) => r.textContent ?? "");

describe("ImagesView (coverage S1b)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setRows([]);
    useFleet.setState({ clusters: [{ name: "c", state: "Synced", fluxPresent: true, fluxImagesPresent: true } as never] });
  });

  it("lists broken-first, with each kind's key column and a blocked policy named", () => {
    setRows([repo("app"), policy("stuck", { ready: "Failed", reason: "DependencyNotReady", rule: fault, blockedBy: "flux-system/missing" }), automation("apps")]);
    render(<ImagesView cluster="c" />);
    expect(rowTexts()[0]).toContain("stuck");
    expect(rowTexts()[0]).toContain("blocked by ImageRepository flux-system/missing");
    expect(rowTexts().join("|")).toContain("42 tags");
    expect(rowTexts().join("|")).toContain("pushed abc1234");
  });

  it("a failing policy leads with its own verdict; a healthy repository is a separate block, and the image is the last resolved", async () => {
    setRows([policy("app", { ready: "Failed", reason: "Failure", message: "no tag matches", rule: fault })]);
    render(<ImagesView cluster="c" />);
    fireEvent.click(screen.getByText("app"));
    const panel = await screen.findByTestId("images-panel");
    expect(panel.textContent?.indexOf("Failed")).toBeLessThan(panel.textContent?.indexOf("Repository flux-system/app") ?? 0);
    expect(screen.getByTestId("images-repo-block").textContent).toContain("Ready");
    expect(panel.textContent).toContain("last resolved, before the failure");
  });

  it("the automation's push shows the bound branch, the source and the applied lines, never a deployment", async () => {
    vi.mocked(getImageDetail).mockImplementationOnce(async (_c, _k, _ns, _n, uid) => ({ uid, events: [], push: {
      branchState: "bound", branch: "auto", target: { checkoutFrom: "source", branch: "auto", repositoryURL: "https://github.com/org/fleet" },
      sourceState: "found", sourceLine: "other", sourceRevision: "main@sha1:fff", sourceBranchNote: "pushed to auto; this source tracks main",
      applied: [{ namespace: "flux-system", name: "apps", revision: "main@sha1:fff", applied: false }], appliedState: "found" } }));
    setRows([automation("apps")]);
    render(<ImagesView cluster="c" />);
    fireEvent.click(screen.getByText("apps"));
    const pushEl = await screen.findByTestId("images-push");
    await waitFor(() => expect(pushEl.textContent).toContain("pushed to auto, from the controller's event"));
    expect(pushEl.textContent).toContain("whether it includes the push is not known here");
    expect(pushEl.textContent).toContain("pushed to auto; this source tracks main");
    expect(pushEl.textContent).toContain("Kustomization flux-system/apps: at another revision");
    expect(pushEl.textContent).not.toMatch(/\b(is|was|been) deployed\b/);
    expect(screen.getByTestId("images-target").textContent).toContain("from the GitRepository");
    expect(getImageDetail).toHaveBeenCalledWith("c", "ImageUpdateAutomation", "apps", "apps", "uid-apps");
  });

  it("Scan now carries the armed UID, and its confirmation names the owner's consequence", async () => {
    setRows([repo("app", { owner: { kind: "Kustomization", namespace: "flux-system", name: "infra" } })]);
    render(<ImagesView cluster="c" />);
    fireEvent.click(screen.getByText("app"));
    fireEvent.click(await screen.findByRole("button", { name: /more/i }));
    fireEvent.click(await screen.findByText("Scan now"));
    expect(screen.getByText(/the annotation may be removed when the owner applies/)).toBeTruthy();
    fireEvent.click(await screen.findByRole("button", { name: "Scan now" }));
    await waitFor(() => expect(reconcile).toHaveBeenCalledWith("c", "ImageRepository", "apps", "app", "uid-app"));
  });

  it("an ImagePolicy at v1beta2 offers neither reconcile nor suspend", async () => {
    setRows([policy("old", { gvr: "image.toolkit.fluxcd.io/v1beta2/imagepolicies" })]);
    render(<ImagesView cluster="c" />);
    fireEvent.click(screen.getByText("old"));
    const panel = await screen.findByTestId("images-panel");
    expect(panel.textContent).toContain("reconcile and suspend are not offered");
    fireEvent.click(await screen.findByRole("button", { name: /more/i }));
    expect(screen.queryByText("Reconcile")).toBeNull();
    expect(screen.queryByText("Suspend")).toBeNull();
  });

  it("a same-name replacement never shows its data as the pinned object's", async () => {
    setRows([repo("app", { image: "old-evidence" })]);
    const { rerender } = render(<ImagesView cluster="c" />);
    fireEvent.click(screen.getByText("app"));
    await screen.findByTestId("images-panel");
    setRows([repo("app", { uid: "uid-NEW", image: "new-object-data" })]);
    rerender(<ImagesView cluster="c" />);
    const panel = await screen.findByTestId("images-panel");
    expect(panel.textContent).toContain("old-evidence");
    expect(panel.textContent).not.toContain("new-object-data");
  });

  it("not installed: an empty state, never an empty list", () => {
    setRows([], { coverage: ["ImageRepository", "ImagePolicy", "ImageUpdateAutomation"].map((k) => ({ kind: k, served: false, synced: false, coverage: "absent" as const })) });
    render(<ImagesView cluster="c" />);
    expect(screen.getByText("Flux image automation is not installed on this cluster.")).toBeTruthy();
  });
});
