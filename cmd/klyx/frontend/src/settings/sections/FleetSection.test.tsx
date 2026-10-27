import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { ThemeProvider } from "../../theme/ThemeProvider";
import type { FleetConfigDTO } from "../../bridge/configsvc";
import type { ClusterEntry } from "../../bridge/fleetPlans";
import type { Plan } from "../../bridge/plan";
import { FleetSection } from "./FleetSection";
import { applyAnchor } from "../../app/SettingsView";
import { parseTags, protectionNow } from "./ClusterEditor";
import { _resetNavGuardForTest, mustAsk, resolveLeave } from "../navGuard";
import { useFleet } from "../../store/fleet";
import { act } from "@testing-library/react";

// Settings S2 §5: the Fleet page edits a cluster through its plan, and
// every notice offers the plan that resolves it.

const bridge = vi.hoisted(() => ({
  getClusterEntry: vi.fn(),
  planClusterEdit: vi.fn(),
  planClusterFromFile: vi.fn(),
  planClusterRemove: vi.fn(),
  planClusterRevert: vi.fn(),
  planClusterRetry: vi.fn(),
  applyClusterPlan: vi.fn(),
}));
vi.mock("../../bridge/fleetPlans", () => bridge);
vi.mock("../../bridge/configsvc", () => ({ addClusters: vi.fn(), refreshNewContextCount: vi.fn().mockResolvedValue(undefined) }));

const cfg = (over: Partial<FleetConfigDTO> = {}): FleetConfigDTO => ({
  path: "/home/me/.config/klyx/fleet.yaml",
  kubeconfigPath: "/home/me/.kube/config",
  warnings: [],
  clusters: [
    { name: "dev", context: "dev-ctx", env: "", region: "", provider: "", account: "", group: "", protected: false, hasMetrics: false },
    { name: "prd", context: "prd-ctx", env: "", region: "we", provider: "", account: "", group: "platform", protected: true, protectedInferred: true, protectedBy: "the name prd", hasMetrics: false },
  ],
  contexts: [],
  ...over,
});

const entry = (over: Partial<ClusterEntry> = {}): ClusterEntry => ({
  name: "dev",
  draft: { context: "dev-ctx", kubeconfig: "", group: "", environment: "", tags: {}, protected: false },
  inFile: true,
  protectedNow: false,
  contexts: ["dev-ctx", "dev-2"],
  endpoints: [],
  ...over,
});

const aPlan = (over: Partial<Plan> = {}): Plan => ({
  section: "fleet", kind: "save", cluster: "dev", class: "placement", digest: "d1", fileVersion: "v1", runtime: {},
  changes: [{ id: "fleet.group", label: "Group", from: "", to: "web" }], ends: [], continues: [], writes: true, activates: true, ...over,
});

function renderFleet(c = cfg()) {
  const reload = vi.fn(async () => true);
  render(<ThemeProvider><FleetSection cfg={c} reload={reload} ready={() => {}} say={() => {}} /></ThemeProvider>);
  return { reload };
}

describe("Settings > Fleet (S2 §5)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    _resetNavGuardForTest();
    useFleet.setState({ route: { name: "settings", section: "fleet" } });
    bridge.getClusterEntry.mockResolvedValue(entry());
  });

  it("edits a cluster through its plan: the draft is planned, then applied, then read back", async () => {
    bridge.planClusterEdit.mockResolvedValue(aPlan());
    bridge.applyClusterPlan.mockResolvedValue({ ok: true });
    const { reload } = renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    fireEvent.change(within(editor).getByLabelText("Group"), { target: { value: "web" } });
    fireEvent.change(within(editor).getByLabelText("Region (tags.region)"), { target: { value: "ne" } });
    fireEvent.change(within(editor).getByLabelText("Other tags"), { target: { value: "team=core" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    expect(bridge.planClusterEdit).toHaveBeenCalledWith("dev", { context: "dev-ctx", kubeconfig: "", group: "web", environment: "", tags: { region: "ne", team: "core" }, protected: false });
    expect(bridge.applyClusterPlan).not.toHaveBeenCalled(); // nothing is written before Apply
    expect(screen.getByRole("region", { name: "Review the changes to dev" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(bridge.applyClusterPlan).toHaveBeenCalledWith("d1", ""));
    expect((await screen.findByTestId("settings-banner")).textContent).toContain("Applied");
    expect(reload).toHaveBeenCalled();
    expect(screen.queryByTestId("cluster-editor")).toBeNull();
  });

  it("loosens protection only through the typed interlock, with the cluster's name", async () => {
    bridge.getClusterEntry.mockResolvedValue(entry({ draft: { ...entry().draft, protected: true }, protectedBy: "protected: true" }));
    bridge.planClusterEdit.mockResolvedValue(aPlan({ class: "protection", changes: [{ id: "fleet.protection", label: "Protection", from: "on, set in fleet.yaml", to: "off" }],
      interlock: { phrase: "dev", why: "Destructive actions on dev will no longer ask for its name to be typed." } }));
    bridge.applyClusterPlan.mockResolvedValue({ ok: true });
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    fireEvent.click(within(editor).getByLabelText("Protection"));
    fireEvent.click(within(editor).getByRole("button", { name: "Review and apply" }));
    expect((await screen.findByTestId("plan-interlock")).textContent).toContain("no longer ask");
    expect(screen.getByTestId("plan-class-note").textContent).toContain("stale");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(screen.getByText("protection lock")).toBeTruthy();
    const confirm = screen.getByRole("button", { name: "Confirm and apply" });
    expect((confirm as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByPlaceholderText("dev"), { target: { value: "dev" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(bridge.applyClusterPlan).toHaveBeenCalledWith("d1", "dev"));
  });

  it("removes a cluster with its keychain choices, each re-planned", async () => {
    bridge.planClusterRemove.mockImplementation(async (_: string, chosen: string[]) => aPlan({
      kind: "remove", class: "removal", digest: `r-${chosen.join(",")}`, changes: [{ id: "fleet.cluster", label: "Cluster", from: "dev", to: "removed" }],
      records: [{ kind: "metrics", revision: "1", chosen: chosen.includes("metrics") }, { kind: "loki", revision: "2", chosen: chosen.includes("loki") }],
    }));
    bridge.applyClusterPlan.mockResolvedValue({ ok: true, note: "dev was removed, but the keychain still holds the loki credential" });
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "remove dev" }));
    const records = await screen.findByTestId("plan-records");
    expect(bridge.planClusterRemove).toHaveBeenCalledWith("dev", []);
    fireEvent.click(within(records).getByLabelText("delete the metrics credential"));
    await waitFor(() => expect(bridge.planClusterRemove).toHaveBeenLastCalledWith("dev", ["metrics"]));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("r-metrics"));
    // another choice keeps the one made: the plan binds both
    fireEvent.click(within(screen.getByTestId("plan-records")).getByLabelText("delete the loki credential"));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("r-metrics,loki"));
    fireEvent.click(within(screen.getByTestId("plan-records")).getByLabelText("delete the loki credential"));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("r-metrics"));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(bridge.applyClusterPlan).toHaveBeenCalledWith("r-metrics", ""));
    expect((await screen.findByTestId("settings-banner")).textContent).toContain("still holds the loki credential");
  });

  it("offers Revert and Retry on a cluster saved, not connected yet", async () => {
    bridge.planClusterRevert.mockResolvedValue(aPlan({ kind: "revert", class: "reconnect" }));
    bridge.planClusterRetry.mockResolvedValue(aPlan({ kind: "retry", class: "reconnect", writes: false }));
    renderFleet(cfg({ clusters: [{ ...cfg().clusters[0], held: "saved, not connected yet: connecting with context dev-2", canRevert: true, canRetry: true }] }));
    const held = screen.getByTestId("settings-held");
    expect(held.textContent).toContain("▲ Saved, not connected yet: connecting with context dev-2. Nothing new starts on dev until it connects.");
    expect(held.textContent).not.toMatch(/not connected yet:.*not connected yet/i);
    fireEvent.click(within(held).getByRole("button", { name: "Revert" }));
    await waitFor(() => expect(bridge.planClusterRevert).toHaveBeenCalledWith("dev"));
    expect((await screen.findByTestId("plan-class-note")).textContent).toContain("held until its new connection is usable");
    fireEvent.click(within(screen.getByTestId("settings-held")).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(bridge.planClusterRetry).toHaveBeenCalledWith("dev"));
  });

  it("says a connection that failed to build as it is, with Retry", () => {
    renderFleet(cfg({ clusters: [{ ...cfg().clusters[0], held: "failed to connect: credential exec plugin failed - exec plugin not found", canRetry: true }] }));
    const held = screen.getByTestId("settings-held");
    expect(held.textContent).toContain("▲ Failed to connect: credential exec plugin failed - exec plugin not found. Nothing new starts on dev until it connects.");
    expect(held.textContent).not.toContain("Saved");
    expect(within(held).queryByRole("button", { name: "Revert" })).toBeNull();
    expect(within(held).getByRole("button", { name: "Retry" })).toBeTruthy();
  });

  it("offers the file's values for drift, and Connect for a cluster only in the file", async () => {
    bridge.planClusterFromFile.mockResolvedValue(aPlan({ kind: "file", writes: false }));
    renderFleet(cfg({ clusters: [{ ...cfg().clusters[0], drift: "fleet.yaml says group: web" }], fileOnly: ["lab"] }));
    fireEvent.click(within(screen.getByTestId("settings-drift")).getByRole("button", { name: "Use the file's values" }));
    await waitFor(() => expect(bridge.planClusterFromFile).toHaveBeenCalledWith("dev"));
    bridge.planClusterFromFile.mockResolvedValueOnce(aPlan({ kind: "connect", class: "addition", cluster: "lab", writes: false }));
    fireEvent.click(within(screen.getByTestId("settings-file-only")).getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(bridge.planClusterFromFile).toHaveBeenCalledWith("lab"));
    // lab has no row: its plan shows above the table
    expect((await screen.findByRole("region", { name: "Connect lab" })).textContent).toContain("applying connects it and writes nothing");
  });

  it("says a reconnect is saved and not connected yet", async () => {
    bridge.planClusterEdit.mockResolvedValue(aPlan({ class: "reconnect", changes: [{ id: "fleet.context", label: "Context", from: "dev-ctx", to: "dev-2" }] }));
    bridge.applyClusterPlan.mockResolvedValue({ ok: true });
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    fireEvent.change(within(editor).getByLabelText("Context"), { target: { value: "dev-2" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Review and apply" }));
    expect((await screen.findByTestId("plan-class-note")).textContent).toContain("held until its new connection is usable");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect((await screen.findByTestId("settings-banner")).textContent).toBe("Saved: dev is not connected yet - connecting with the new terms. Nothing new starts on it until it connects.");
  });

  it("waits for every tag line to read as key=value before Review", async () => {
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    fireEvent.change(within(editor).getByLabelText("Other tags"), { target: { value: "team=core\nnope" } });
    expect(editor.textContent).toContain("Each line needs key=value: nope");
    expect((within(editor).getByRole("button", { name: "Review and apply" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(within(editor).getByLabelText("Other tags"), { target: { value: "team=core" } });
    expect((within(editor).getByRole("button", { name: "Review and apply" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows a replacement plan without applying it, and reads back after a lost answer", async () => {
    bridge.planClusterEdit.mockResolvedValue(aPlan());
    bridge.applyClusterPlan.mockResolvedValueOnce({ ok: false, error: "Something changed since this was planned", plan: aPlan({ digest: "d2" }) });
    const { reload } = renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    fireEvent.click(within(await screen.findByTestId("cluster-editor")).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("d2"));
    expect(bridge.applyClusterPlan).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("settings-banner").textContent).toContain("Something changed");
    bridge.applyClusterPlan.mockResolvedValueOnce({ ok: false, unknown: true, error: "the bridge closed" });
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(screen.getByTestId("settings-banner").textContent).toContain("lost the answer"));
    expect(reload).toHaveBeenCalled();
  });

  it("says when the name decides protection, which cannot be loosened here", async () => {
    bridge.getClusterEntry.mockResolvedValue(entry({ name: "prd", protectedBy: "the name prd", locked: "protection is inferred from the name prd, which is read-only here: renaming is a later slice" }));
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit prd" }));
    expect((await screen.findByTestId("cluster-protection-locked")).textContent).toContain("read-only here");
  });

  it("reads tags as key=value lines and names a bad one", () => {
    expect(parseTags("team=core\n\n env = dev ")).toEqual({ tags: { team: "core", env: "dev" }, bad: [] });
    expect(parseTags("nope\n=x").bad).toEqual(["nope", "=x"]);
  });

  // Codex's review of S2c (2026-10-27): its probes, R1-R3 and R5-R8, as
  // regression tests
  it("R1: a change to the draft withdraws its plan, and a late plan for an older draft is dropped", async () => {
    let answer: (p: Plan) => void = () => {};
    bridge.planClusterEdit.mockImplementationOnce(() => new Promise<Plan>((r) => { answer = r; }));
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    fireEvent.change(within(editor).getByLabelText("Group"), { target: { value: "first" } });
    fireEvent.click(within(editor).getByRole("button", { name: "Review and apply" }));
    await act(async () => answer(aPlan({ digest: "first" })));
    expect(screen.getByTestId("settings-plan").getAttribute("data-plan-digest")).toBe("first");
    fireEvent.change(within(editor).getByLabelText("Group"), { target: { value: "second" } });
    expect(screen.queryByTestId("settings-plan")).toBeNull();
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(bridge.applyClusterPlan).not.toHaveBeenCalled();
  });

  it("R2: an edited draft asks before leaving the section or opening another editor, and Cancel discards it", async () => {
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    expect(mustAsk()).toBe(false);
    fireEvent.change(within(editor).getByLabelText("Group"), { target: { value: "unsaved" } });
    expect(mustAsk()).toBe(true);
    act(() => useFleet.getState().openSettings("ai"));
    expect(useFleet.getState().route).toEqual({ name: "settings", section: "fleet" });
    await act(async () => resolveLeave("stay"));
    fireEvent.click(screen.getByRole("button", { name: "edit prd" }));
    expect(bridge.getClusterEntry).toHaveBeenCalledTimes(1); // asked first, not replaced
    await act(async () => resolveLeave("discard"));
    await waitFor(() => expect(bridge.getClusterEntry).toHaveBeenLastCalledWith("prd"));
    expect(mustAsk()).toBe(false);
    fireEvent.change(within(await screen.findByTestId("cluster-editor")).getByLabelText("Group"), { target: { value: "x" } });
    fireEvent.click(within(screen.getByTestId("cluster-editor")).getByRole("button", { name: "Cancel" }));
    expect(mustAsk()).toBe(false);
  });

  it("R3: after a lost answer, nothing is called current and nothing is enabled until the readback says so", async () => {
    let readBack: (ok: boolean) => void = () => {};
    const reload = vi.fn(() => new Promise<boolean>((r) => { readBack = r; }));
    bridge.planClusterEdit.mockResolvedValue(aPlan());
    bridge.applyClusterPlan.mockResolvedValue({ ok: false, unknown: true, error: "bridge closed" });
    render(<ThemeProvider><FleetSection cfg={cfg()} reload={reload} ready={() => {}} say={() => {}} /></ThemeProvider>);
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    fireEvent.click(within(await screen.findByTestId("cluster-editor")).getByRole("button", { name: "Review and apply" }));
    await screen.findByTestId("settings-plan");
    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    await waitFor(() => expect(reload).toHaveBeenCalled());
    expect(screen.getByTestId("settings-banner").textContent).not.toContain("what is shown is what runs now");
    expect((screen.getByRole("button", { name: "remove dev" }) as HTMLButtonElement).disabled).toBe(true);
    expect(mustAsk()).toBe(true); // leaving waits for the readback
    await act(async () => readBack(false));
    expect(screen.getByTestId("settings-banner").textContent).toContain("whether it applied is not known, and reading the fleet again failed");
    expect((screen.getByRole("button", { name: "remove dev" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("R5: says pending protection apart from the protection in force", () => {
    expect(protectionNow(entry({ protectedNow: true, protectedBy: "", pendingProtected: false }))).toBe("Protected now, as published when it last connected. Once its new connection is usable: not protected.");
    expect(protectionNow(entry({ protectedNow: false, pendingProtected: true, pendingProtectedBy: "protected: true" }))).toBe("Not protected now. Once its new connection is usable: protected, decided by protected: true.");
    expect(protectionNow(entry({ protectedNow: true, protectedBy: "the name prd" }))).toBe("Protected now, decided by the name prd.");
  });

  it("R6: suggests no contexts for a kubeconfig typed since the entry was read", async () => {
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    expect(editor.querySelectorAll("datalist option").length).toBe(2);
    fireEvent.change(within(editor).getByLabelText("Kubeconfig"), { target: { value: "/elsewhere/config" } });
    expect(editor.querySelectorAll("datalist option").length).toBe(0);
    expect(editor.textContent).toContain("No contexts are suggested for the kubeconfig typed below");
  });

  it("R7: keeps every DOM id unique with an editor open", async () => {
    const { container } = render(<ThemeProvider><FleetSection cfg={cfg()} reload={vi.fn(async () => true)} ready={() => {}} say={() => {}} /></ThemeProvider>);
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    await screen.findByTestId("cluster-editor");
    const ids = [...container.querySelectorAll<HTMLElement>("[id]")].map((e) => e.id);
    expect(ids.filter((id, i) => ids.indexOf(id) !== i)).toEqual([]);
  });

  it("R8: links the cluster's endpoints to Observability, through the leave guard", async () => {
    bridge.getClusterEntry.mockResolvedValue(entry({ endpoints: ["metrics", "loki"] }));
    renderFleet();
    fireEvent.click(screen.getByRole("button", { name: "edit dev" }));
    const editor = await screen.findByTestId("cluster-editor");
    fireEvent.click(within(editor).getByRole("button", { name: "dev's Loki endpoint in Observability" }));
    expect(useFleet.getState().route).toMatchObject({ name: "settings", section: "observability" });
    expect(within(editor).getByRole("button", { name: "dev's Metrics endpoint in Observability" })).toBeTruthy();
  });

  it("R4: while a cluster is held, the fleet is read again every ten seconds, and not otherwise", async () => {
    vi.useFakeTimers();
    try {
      const reload = vi.fn(async () => true);
      const { rerender } = render(<ThemeProvider><FleetSection cfg={cfg({ clusters: [{ ...cfg().clusters[0], held: "saved, not connected yet: connecting", canRetry: true }] })} reload={reload} ready={() => {}} say={() => {}} /></ThemeProvider>);
      await act(async () => { vi.advanceTimersByTime(10_000); });
      expect(reload).toHaveBeenCalledTimes(1);
      rerender(<ThemeProvider><FleetSection cfg={cfg()} reload={reload} ready={() => {}} say={() => {}} /></ThemeProvider>);
      await act(async () => { vi.advanceTimersByTime(30_000); });
      expect(reload).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("R7: an anchor with an editor open focuses the editor's control, not the table's cell", () => {
    const root = document.createElement("div");
    root.innerHTML = `<span data-setting="clusters[].group" data-anchor="clusters[].group@dev" tabindex="-1">web</span>
      <div data-setting="clusters[].group" data-anchor="clusters[].group@dev"><div class="setting-control"><input id="the-control" /></div></div>`;
    document.body.appendChild(root);
    try {
      expect(applyAnchor(root, "clusters[].group@dev")).toBeNull();
      expect(document.activeElement?.id).toBe("the-control");
    } finally {
      root.remove();
    }
  });
});
