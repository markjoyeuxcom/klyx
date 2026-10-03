import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { SettingsView } from "./SettingsView";
import type { FleetConfigDTO } from "../bridge/configsvc";

const mockGetFleetConfig = vi.fn<() => Promise<FleetConfigDTO | null>>();
const mockAddClusters = vi.fn();
vi.mock("../bridge/configsvc", () => ({
  getFleetConfig: () => mockGetFleetConfig(),
  addClusters: (ctxs: string[]) => mockAddClusters(ctxs),
  refreshNewContextCount: vi.fn().mockResolvedValue(undefined),
}));

const baseCfg: FleetConfigDTO = {
  path: "/home/me/.config/klyx/fleet.yaml",
  kubeconfigPath: "/home/me/.kube/config",
  warnings: ["cluster \"prd\": tag \"protected\" shadows a cluster field and is ignored; move it out of `tags:` to a top-level key"],
  clusters: [
    { name: "homelab-nelli", context: "kubernetes-admin@homelab-nelli", env: "homelab", region: "", provider: "", account: "", group: "", protected: false, hasMetrics: true },
    { name: "prd-weu", context: "prd-weu", env: "prd", region: "we", provider: "aks", account: "", group: "prd", protected: true, hasMetrics: false },
  ],
  contexts: [
    { name: "kubernetes-admin@homelab-nelli", inFleet: true },
    { name: "kubernetes-admin@homelab-orange", inFleet: false },
    { name: "prd-weu", inFleet: true },
  ],
};

describe("SettingsView", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetFleetConfig.mockResolvedValue(baseCfg);
  });

  it("renders the fleet file path, clusters, and load warnings", async () => {
    const { findByText, getByText, getAllByText } = render(<SettingsView />);
    expect(await findByText("/home/me/.config/klyx/fleet.yaml")).toBeTruthy();
    expect(getByText("homelab-nelli")).toBeTruthy();
    // prd-weu appears in the clusters table AND the contexts list.
    expect(getAllByText("prd-weu").length).toBeGreaterThanOrEqual(1);
    expect(getByText(/shadows a cluster field/)).toBeTruthy();
  });

  // Review 2026-10-03 #7: Settings shows EFFECTIVE protection and says when it
  // is inferred from a prd/prod/production token rather than set explicitly.
  it("distinguishes explicit and inferred protection", async () => {
    mockGetFleetConfig.mockResolvedValue({
      ...baseCfg,
      clusters: [
        ...baseCfg.clusters,
        { name: "aks-prd-we", context: "aks-prd-we", env: "", region: "", provider: "", account: "", group: "", protected: true, protectedInferred: true, hasMetrics: false },
      ],
    });
    const { findAllByText } = render(<SettingsView />);
    // Only aks-prd-we is inferred; prd-weu (explicit) still reads "yes".
    const inferred = await findAllByText("inferred");
    expect(inferred).toHaveLength(1);
    expect(inferred[0].getAttribute("title")).toMatch(/prd\/prod\/production token/);
  });

  it("marks contexts in/not-in fleet and only offers checkboxes for new ones", async () => {
    const { findAllByText, getAllByText, queryByLabelText, getByLabelText } = render(<SettingsView />);
    expect((await findAllByText("in fleet")).length).toBe(2);
    expect(getAllByText("not in fleet").length).toBe(1);
    expect(getByLabelText("select context kubernetes-admin@homelab-orange")).toBeTruthy();
    expect(queryByLabelText("select context prd-weu")).toBeNull();
  });

  it("add flow: select, add, success banner says restart", async () => {
    mockAddClusters.mockResolvedValue({ ok: true, error: "" });
    const { findByLabelText, getByRole, findByTestId } = render(<SettingsView />);
    fireEvent.click(await findByLabelText("select context kubernetes-admin@homelab-orange"));
    fireEvent.click(getByRole("button", { name: /add 1 to fleet/i }));
    await waitFor(() => expect(mockAddClusters).toHaveBeenCalledWith(["kubernetes-admin@homelab-orange"]));
    const banner = await findByTestId("settings-banner");
    expect(banner.textContent).toContain("connecting now");
  });

  it("add failure surfaces the error honestly", async () => {
    mockAddClusters.mockResolvedValue({ ok: false, error: "appended config failed validation (file unchanged)" });
    const { findByLabelText, getByRole, findByTestId } = render(<SettingsView />);
    fireEvent.click(await findByLabelText("select context kubernetes-admin@homelab-orange"));
    fireEvent.click(getByRole("button", { name: /add 1 to fleet/i }));
    const banner = await findByTestId("settings-banner");
    expect(banner.textContent).toContain("failed validation");
  });

  it("scan error renders instead of an empty context list", async () => {
    mockGetFleetConfig.mockResolvedValue({ ...baseCfg, contexts: [], scanError: "parse kubeconfig: yaml error" });
    const { findByText } = render(<SettingsView />);
    expect(await findByText(/Could not read the kubeconfig/)).toBeTruthy();
  });
});
