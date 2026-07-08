import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { HelmView } from "./HelmView";
import { useFleet } from "../store/fleet";
import type { HelmReleaseDTO, HelmHistoryEntryDTO } from "../store/fleet";

// Mock bridge/helm so no Wails runtime is needed.
vi.mock("../bridge/helm", () => ({
  listHelmReleases: vi.fn().mockResolvedValue(undefined),
  openHelmRelease: vi.fn().mockResolvedValue(undefined),
  helmRollback: vi.fn().mockResolvedValue(undefined),
}));
import { openHelmRelease, helmRollback } from "../bridge/helm";

// The rollback composer (OperationDialog) registers protected-cluster grants
// via the config bridge; mock it so no Wails runtime is needed.
vi.mock("../bridge/configsvc", () => ({
  armProtected: vi.fn(async () => ({ ok: true, error: "" })),
}));

const deployed: HelmReleaseDTO = {
  name: "nginx-ingress",
  namespace: "ingress-nginx",
  chart: "ingress-nginx-4.10.0",
  appVersion: "1.10.0",
  status: "deployed",
  revision: 3,
  updatedUnix: Math.floor(Date.now() / 1000) - 600,
};

const failed: HelmReleaseDTO = {
  name: "cert-manager",
  namespace: "cert-manager",
  chart: "cert-manager-1.14.2",
  appVersion: "1.14.2",
  status: "failed",
  revision: 2,
  updatedUnix: Math.floor(Date.now() / 1000) - 3600,
};

const superseded: HelmReleaseDTO = {
  name: "monitoring",
  namespace: "monitoring",
  chart: "kube-prometheus-stack-56.0.0",
  appVersion: "0.70.0",
  status: "superseded",
  revision: 1,
  updatedUnix: 0,
};

const historyEntries: HelmHistoryEntryDTO[] = [
  { revision: 3, status: "deployed", chart: "ingress-nginx-4.10.0", appVersion: "1.10.0", description: "Upgrade complete", updatedUnix: Math.floor(Date.now() / 1000) - 600 },
  { revision: 2, status: "superseded", chart: "ingress-nginx-4.9.0", appVersion: "1.9.1", description: "Upgrade complete", updatedUnix: Math.floor(Date.now() / 1000) - 86400 },
  { revision: 1, status: "superseded", chart: "ingress-nginx-4.8.0", appVersion: "1.8.0", description: "Install complete", updatedUnix: Math.floor(Date.now() / 1000) - 172800 },
];

function seedReleases(releases: HelmReleaseDTO[]) {
  useFleet.setState((s) => ({
    helm: {
      ...s.helm,
      cluster: "homelab",
      releases,
      available: true,
      message: "",
      loading: false,
    },
  }));
}

function seedWithDetail(release: HelmReleaseDTO, history: HelmHistoryEntryDTO[], values: string) {
  useFleet.setState((s) => ({
    helm: {
      ...s.helm,
      cluster: "homelab",
      releases: [release],
      available: true,
      message: "",
      loading: false,
      selected: { namespace: release.namespace, name: release.name },
      history,
      values,
      detailLoading: false,
    },
  }));
}

describe("HelmView", () => {
  beforeEach(() => {
    useFleet.getState().clearHelm();
    vi.clearAllMocks();
  });

  // ----- capability-absent state -----

  it("shows honest empty state when not available (helm not found)", () => {
    useFleet.setState((s) => ({
      helm: { ...s.helm, cluster: "homelab", available: false, message: "helm not found", loading: false },
    }));
    const { getByText } = render(<HelmView cluster="homelab" />);
    expect(getByText(/helm not found - install helm or set KLYX_HELM_PATH to inspect releases/)).toBeTruthy();
  });

  it("shows raw message when available:false and message is not PATH-related", () => {
    useFleet.setState((s) => ({
      helm: { ...s.helm, cluster: "homelab", available: false, message: "cluster not connected: homelab", loading: false },
    }));
    const { getByText } = render(<HelmView cluster="homelab" />);
    expect(getByText("cluster not connected: homelab")).toBeTruthy();
  });

  // ----- release list rendering -----

  it("renders rows for seeded releases", () => {
    seedReleases([deployed, failed]);
    const { getAllByText } = render(<HelmView cluster="homelab" />);
    // nginx-ingress appears once (name only); cert-manager appears in both name and namespace columns
    expect(getAllByText("nginx-ingress").length).toBeGreaterThanOrEqual(1);
    expect(getAllByText("cert-manager").length).toBeGreaterThanOrEqual(1);
  });

  it("shows empty state when no releases", () => {
    seedReleases([]);
    const { getByText } = render(<HelmView cluster="homelab" />);
    expect(getByText(/No Helm releases on this cluster/)).toBeTruthy();
  });

  it("deployed status dot renders with success color", () => {
    seedReleases([deployed]);
    const { container } = render(<HelmView cluster="homelab" />);
    const dots = container.querySelectorAll("span[style*='border-radius: 50%']");
    // At least one dot should have the success color
    const successDot = Array.from(dots).find((el) =>
      (el as HTMLElement).style.background.includes("color-text-success"),
    );
    expect(successDot).toBeTruthy();
  });

  it("failed status shows danger color text", () => {
    seedReleases([failed]);
    // The status "failed" should cause a danger-colored dot
    const { container } = render(<HelmView cluster="homelab" />);
    const dots = container.querySelectorAll("span[style*='border-radius: 50%']");
    const dangerDot = Array.from(dots).find((el) =>
      (el as HTMLElement).style.background.includes("color-text-danger"),
    );
    expect(dangerDot).toBeTruthy();
  });

  it("zero updatedUnix renders as em-dash", () => {
    seedReleases([superseded]);
    const { getAllByText } = render(<HelmView cluster="homelab" />);
    expect(getAllByText("—").length).toBeGreaterThan(0);
  });

  it("row click calls openHelmRelease", () => {
    seedReleases([deployed]);
    const { getByText } = render(<HelmView cluster="homelab" />);
    fireEvent.click(getByText("nginx-ingress"));
    expect(openHelmRelease).toHaveBeenCalledWith("homelab", "ingress-nginx", "nginx-ingress");
  });

  // ----- detail panel -----

  it("detail panel renders when release is selected with history", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getAllByText, getByText } = render(<HelmView cluster="homelab" />);
    // Header contains nginx-ingress (may appear multiple times between list + detail panel)
    expect(getAllByText("nginx-ingress").length).toBeGreaterThanOrEqual(1);
    // History section title
    expect(getByText("History")).toBeTruthy();
    // All 3 revisions appear in history table
    const rev3 = getAllByText("3");
    expect(rev3.length).toBeGreaterThanOrEqual(1);
    const rev2 = getAllByText("2");
    expect(rev2.length).toBeGreaterThanOrEqual(1);
    const rev1 = getAllByText("1");
    expect(rev1.length).toBeGreaterThanOrEqual(1);
    // Description text (appears in history rows)
    expect(getAllByText("Upgrade complete").length).toBeGreaterThanOrEqual(1);
  });

  it("rollback button only shows on non-current revisions", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getAllByText, queryAllByText } = render(<HelmView cluster="homelab" />);
    // Revisions 1 and 2 should have rollback buttons; 3 (current) should not
    const rollbackBtns = getAllByText("rollback");
    expect(rollbackBtns).toHaveLength(2);
    // Confirm there are exactly 2 rollback buttons (not on rev 3)
    expect(queryAllByText("rollback")).toHaveLength(2);
  });

  it("rollback button opens the composer with from → to tiles naming revision, chart, appVersion", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getAllByText, getByRole, getByText } = render(<HelmView cluster="homelab" />);
    const rollbackBtns = getAllByText("rollback");
    // Click the first one (revision 2 after sort desc: rev3, rev2, rev1)
    fireEvent.click(rollbackBtns[0]);
    expect(getByRole("dialog", { name: "Roll back nginx-ingress" })).toBeTruthy();
    // From/to context: current vs target, chart included - the target running
    // a different chart version is the interesting fact of this rollback.
    expect(getByText("rev 3 · ingress-nginx-4.10.0 (1.10.0)")).toBeTruthy();
    expect(getByText("rev 2 · ingress-nginx-4.9.0 (1.9.1)")).toBeTruthy();
  });

  it("confirming rollback passes the default options: wait on, hooks on, 120s", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getAllByText, getByRole } = render(<HelmView cluster="homelab" />);
    fireEvent.click(getAllByText("rollback")[0]);
    fireEvent.click(getByRole("button", { name: "Roll back" }));
    expect(helmRollback).toHaveBeenCalledWith("homelab", "ingress-nginx", "nginx-ingress", 2, {
      wait: true,
      noHooks: false,
      timeoutSeconds: 120,
    });
  });

  it("toggling wait/skip-hooks and timeout threads through to helmRollback", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getAllByText, getByRole, getByLabelText } = render(<HelmView cluster="homelab" />);
    fireEvent.click(getAllByText("rollback")[0]);
    fireEvent.change(getByLabelText("rollback timeout"), { target: { value: "300" } });
    fireEvent.click(getByRole("button", { name: /Skip hooks/ }));
    fireEvent.click(getByRole("button", { name: /Wait for readiness/ }));
    // Timeout only applies while waiting; with wait off the select is disabled.
    expect((getByLabelText("rollback timeout") as HTMLSelectElement).disabled).toBe(true);
    fireEvent.click(getByRole("button", { name: "Roll back" }));
    expect(helmRollback).toHaveBeenCalledWith("homelab", "ingress-nginx", "nginx-ingress", 2, {
      wait: false,
      noHooks: true,
      timeoutSeconds: 300,
    });
  });

  it("cancelling rollback dialog does not call helmRollback", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getAllByText, getByText } = render(<HelmView cluster="homelab" />);
    fireEvent.click(getAllByText("rollback")[0]);
    fireEvent.click(getByText("Cancel"));
    expect(helmRollback).not.toHaveBeenCalled();
  });

  // ----- values pane -----

  it("shows 'no user-supplied values' when values is empty string", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getByText } = render(<HelmView cluster="homelab" />);
    expect(getByText("no user-supplied values")).toBeTruthy();
  });

  it("renders values content in pre block when present", () => {
    seedWithDetail(deployed, historyEntries, "replicaCount: 2\nimage:\n  tag: latest");
    render(<HelmView cluster="homelab" />);
    expect(document.body.textContent).toContain("replicaCount: 2");
  });

  it("renders the masking hint above the values pane", () => {
    seedWithDetail(deployed, historyEntries, "secret: <redacted>");
    const { getByText } = render(<HelmView cluster="homelab" />);
    expect(getByText("secret-shaped values are masked; the source chart in Git holds the full set")).toBeTruthy();
  });

  it("close button clears selected release", () => {
    seedWithDetail(deployed, historyEntries, "");
    const { getByRole } = render(<HelmView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: "close release detail panel" }));
    expect(useFleet.getState().helm.selected).toBeNull();
  });

  it("sorts failed releases first (broken-first triage)", () => {
    seedReleases([deployed, superseded, failed]);
    const { getAllByTitle } = render(<HelmView cluster="homelab" />);
    const names = getAllByTitle(/^(nginx-ingress|cert-manager|monitoring)$/).map((el) => el.textContent);
    expect(names[0]).toBe("cert-manager");
  });

  it("status filter narrows to failed releases", () => {
    seedReleases([deployed, failed]);
    const { getByRole, queryByTitle } = render(<HelmView cluster="homelab" />);
    fireEvent.click(getByRole("button", { name: /^failed/ }));
    expect(queryByTitle("nginx-ingress")).toBeNull();
    expect(queryByTitle("cert-manager-1.14.2")).toBeTruthy(); // failed release's chart cell
  });

  it("namespace select and search narrow the list", () => {
    seedReleases([deployed, failed]);
    const { getByLabelText, queryByTitle, getByPlaceholderText } = render(<HelmView cluster="homelab" />);
    fireEvent.change(getByLabelText("filter release namespace"), { target: { value: "ingress-nginx" } });
    expect(queryByTitle("cert-manager")).toBeNull();
    fireEvent.change(getByLabelText("filter release namespace"), { target: { value: "" } });
    fireEvent.change(getByPlaceholderText("filter releases"), { target: { value: "prometheus" } });
    expect(queryByTitle("nginx-ingress")).toBeNull();
  });

  it("summary strip counts deployed and failed", () => {
    seedReleases([deployed, superseded, failed]);
    const { getByText } = render(<HelmView cluster="homelab" />);
    // deployed + superseded both count as settled
    const strip = getByText("releases").parentElement?.parentElement;
    expect(strip?.textContent).toContain("deployed");
    expect(strip?.textContent).toContain("failed");
    expect(strip?.textContent).toContain("3"); // total releases
  });

  it("j then Enter opens the keyboard-selected release", () => {
    seedReleases([failed, deployed]);
    render(<HelmView cluster="homelab" />);
    fireEvent.keyDown(window, { key: "j" });
    fireEvent.keyDown(window, { key: "Enter" });
    // failed sorts first, so the first kb selection is cert-manager
    expect(openHelmRelease).toHaveBeenCalledWith("homelab", "cert-manager", "cert-manager");
  });
});
