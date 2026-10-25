import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import { SettingsView } from "./SettingsView";
import { ThemeProvider } from "../theme/ThemeProvider";
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
    const { findByText, getByText, getAllByText } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
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
    const { findAllByText } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    // Only aks-prd-we is inferred; prd-weu (explicit) still reads "yes".
    const inferred = await findAllByText("inferred");
    expect(inferred).toHaveLength(1);
    expect(inferred[0].getAttribute("title")).toMatch(/prd\/prod\/production token/);
  });

  it("marks contexts in/not-in fleet and only offers checkboxes for new ones", async () => {
    const { findAllByText, getAllByText, queryByLabelText, getByLabelText } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    expect((await findAllByText("in fleet")).length).toBe(2);
    expect(getAllByText("not in fleet").length).toBe(1);
    expect(getByLabelText("select context kubernetes-admin@homelab-orange")).toBeTruthy();
    expect(queryByLabelText("select context prd-weu")).toBeNull();
  });

  it("add flow: select, add, success banner says restart", async () => {
    mockAddClusters.mockResolvedValue({ ok: true, error: "" });
    const { findByLabelText, getByRole, findByTestId } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    fireEvent.click(await findByLabelText("select context kubernetes-admin@homelab-orange"));
    fireEvent.click(getByRole("button", { name: /add 1 to fleet/i }));
    await waitFor(() => expect(mockAddClusters).toHaveBeenCalledWith(["kubernetes-admin@homelab-orange"]));
    const banner = await findByTestId("settings-banner");
    expect(banner.textContent).toContain("connecting now");
  });

  it("add failure surfaces the error honestly", async () => {
    mockAddClusters.mockResolvedValue({ ok: false, error: "appended config failed validation (file unchanged)" });
    const { findByLabelText, getByRole, findByTestId } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    fireEvent.click(await findByLabelText("select context kubernetes-admin@homelab-orange"));
    fireEvent.click(getByRole("button", { name: /add 1 to fleet/i }));
    const banner = await findByTestId("settings-banner");
    expect(banner.textContent).toContain("failed validation");
  });

  it("scan error renders instead of an empty context list", async () => {
    mockGetFleetConfig.mockResolvedValue({ ...baseCfg, contexts: [], scanError: "parse kubeconfig: yaml error" });
    const { findByText } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    expect(await findByText(/Could not read the kubeconfig/)).toBeTruthy();
  });
});

// The Line slice 4 §2: the theme's home is Settings > Appearance.
describe("Settings > Appearance", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGetFleetConfig.mockResolvedValue(baseCfg);
  });

  it("lists every theme as a radio; choosing one sets the theme", async () => {
    const { findByRole, getByRole } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    const group = await findByRole("group", { name: "Theme" });
    const radios = Array.from(group.querySelectorAll<HTMLInputElement>('input[type="radio"]'));
    expect(radios.map((r) => r.value)).toEqual(["system", "night", "day", "light", "dark", "bench", "bench-light"]);
    fireEvent.click(getByRole("radio", { name: "Day" }));
    await waitFor(() => expect(document.documentElement.getAttribute("data-theme")).toBe("day"));
    expect((getByRole("radio", { name: "Day" }) as HTMLInputElement).checked).toBe(true);
    fireEvent.click(getByRole("radio", { name: "Night" }));
    await waitFor(() => expect(document.documentElement.getAttribute("data-theme")).toBe("night"));
  });

  // Settings review slice 0: a fleet.yaml that did not load is said first,
  // and adding clusters waits; a file edited since startup is said per row.
  it("says a fleet.yaml that did not load, and refuses to add clusters", async () => {
    mockGetFleetConfig.mockResolvedValue({ ...baseCfg, clusters: [], loadError: "invalid config \"/x/fleet.yaml\": unknown field: clusters[0] (a).alertmanager: \"matches\" at line 4 (did you mean \"match\"?)" });
    const { findByTestId, queryByRole, getByText } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    const banner = await findByTestId("settings-load-error");
    expect(banner.getAttribute("role")).toBe("alert");
    expect(banner.textContent).toContain("connected no cluster");
    expect(banner.textContent).toContain("did you mean");
    expect(getByText("Adding clusters waits until fleet.yaml loads.")).toBeTruthy();
    expect(queryByRole("button", { name: /to fleet/ })).toBeNull();
  });

  it("says when the file now differs from the running fleet", async () => {
    mockGetFleetConfig.mockResolvedValue({
      ...baseCfg,
      clusters: [{ ...baseCfg.clusters[0], drift: "fleet.yaml now differs (protection (the file protects it; it is not protected until Klyx restarts)): Klyx applies it at a restart" }, baseCfg.clusters[1]],
      fileOnly: ["k8s-new"],
    });
    const { findByTestId, getByTestId, getByRole } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    expect((await findByTestId("settings-drift")).textContent).toContain("not protected until Klyx restarts");
    expect(getByTestId("settings-file-only").textContent).toContain("k8s-new");
    expect(getByRole("button", { name: "Re-scan kubeconfig contexts" })).toBeTruthy();
  });

  it("says a file that does not load now, while the fleet keeps running", async () => {
    mockGetFleetConfig.mockResolvedValue({ ...baseCfg, fileError: "invalid config: yaml: line 3: did not find expected key" });
    const { findByTestId } = render(<ThemeProvider><SettingsView /></ThemeProvider>);
    expect((await findByTestId("settings-file-error")).textContent).toContain("keeps running the fleet it read at startup");
  });
});
