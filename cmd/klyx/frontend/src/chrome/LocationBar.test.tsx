import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { LocationBar } from "./LocationBar";
import { useFleet } from "../store/fleet";
import type { ForwardDTO } from "../store/fleet";

// Mock the forwards bridge so no Wails runtime is needed; assert dispatches.
vi.mock("../bridge/forwards", () => ({
  stopForward: vi.fn().mockResolvedValue(undefined),
  stopAllForwards: vi.fn().mockResolvedValue(undefined),
}));
import { stopAllForwards } from "../bridge/forwards";

vi.mock("./CommandPalette", () => ({ openCommandPalette: vi.fn() }));
import { openCommandPalette } from "./CommandPalette";
vi.mock("../bridge/configsvc", () => ({ openTerminal: vi.fn(async () => {}) }));
vi.mock("../bridge/healthRows", () => ({ startSummaries: vi.fn() }));
vi.mock("../ai/chatStore", () => ({ openChat: vi.fn() }));
import { openTerminal } from "../bridge/configsvc";

const active: ForwardDTO = {
  id: "c/team/api#1",
  cluster: "dev",
  namespace: "team",
  targetKind: "Pod",
  targetName: "api",
  localPort: 34567,
  targetPort: 8080,
  startedUnix: 1700000000,
  status: "active",
};
const broken: ForwardDTO = { ...active, id: "c/team/web#2", targetName: "web", status: "broken" };

describe("location bar forwards utility", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.getState().setForwards([]);
  });

  it("with no forwards there is no live chip - the Port-forwards utility opens the view", () => {
    useFleet.setState({ route: { name: "fleet" } });
    const { queryByTestId, getByRole } = render(<LocationBar />);
    expect(queryByTestId("forwards-chip")).toBeNull();
    fireEvent.click(getByRole("button", { name: "Port-forwards" }));
    expect(useFleet.getState().route).toEqual({ name: "forwards" });
  });

  it("opens the command palette from the well", () => {
    const { getByTestId } = render(<LocationBar />);
    fireEvent.click(getByTestId("command-bar"));
    expect(openCommandPalette).toHaveBeenCalled();
  });

  it("renders the chip with the forward count", () => {
    useFleet.getState().setForwards([active, broken]);
    const { getByTestId } = render(<LocationBar />);
    const chip = getByTestId("forwards-chip");
    expect(chip.textContent).toContain("2");
  });

  it("clicking a row navigates to the forwards section and closes the panel", () => {
    useFleet.getState().setForwards([active]);
    const { getByTestId, queryByTestId } = render(<LocationBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    fireEvent.click(getByTestId(`forward-row-${active.id}`));
    expect(useFleet.getState().route).toEqual({ name: "forwards" });
    expect(queryByTestId("forwards-panel")).toBeNull();
  });

  it("dispatches stop all", () => {
    useFleet.getState().setForwards([active, broken]);
    const { getByTestId } = render(<LocationBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    fireEvent.click(getByTestId("forwards-stop-all"));
    expect(stopAllForwards).toHaveBeenCalled();
  });

  it("styles a broken row with the warning colour", () => {
    useFleet.getState().setForwards([broken]);
    const { getByTestId } = render(<LocationBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    const row = getByTestId(`forward-row-${broken.id}`);
    // Broken rows render warning-coloured (presence assertion on the token).
    expect(row.getAttribute("style")).toContain("var(--color-text-warning)");
    expect(row.textContent).toContain("broken");
  });

  it("has no per-row stop buttons - management lives in the Forwards section", () => {
    useFleet.getState().setForwards([active, broken]);
    const { getByTestId, queryByTestId } = render(<LocationBar />);
    fireEvent.click(getByTestId("forwards-chip"));
    expect(queryByTestId(`forward-stop-${active.id}`)).toBeNull();
    expect(queryByTestId("forwards-view-all")).toBeNull();
  });
});

describe("location bar utilities", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ route: { name: "fleet" }, forwards: [], newContexts: 0 });
  });

  it("Activity and Settings are utilities with accessible names; the current one is marked", () => {
    const { getByRole } = render(<LocationBar />);
    fireEvent.click(getByRole("button", { name: "Activity" }));
    expect(useFleet.getState().route).toEqual({ name: "activity" });
    fireEvent.click(getByRole("button", { name: "Settings" }));
    expect(useFleet.getState().route).toEqual({ name: "settings" });
    expect(getByRole("button", { name: "Settings" }).getAttribute("aria-current")).toBe("page");
  });

  it("Ask opens the chat on the open cluster; off a cluster it is disabled with its reason", async () => {
    const { openChat } = await import("../ai/chatStore");
    const r1 = render(<LocationBar />);
    const off = r1.getByRole("button", { name: "Ask - open a cluster first" });
    expect(off.getAttribute("aria-disabled")).toBe("true");
    expect(off.getAttribute("title")).toMatch(/bounded to one cluster/);
    fireEvent.click(off);
    expect(openChat).not.toHaveBeenCalled();
    r1.unmount();
    useFleet.setState({ clusters: [{ name: "dev", state: "Synced" } as never], route: { name: "cluster", cluster: "dev", section: "overview" } });
    const r2 = render(<LocationBar />);
    fireEvent.click(r2.getByRole("button", { name: "Ask about dev" }));
    expect(openChat).toHaveBeenCalledWith("dev");
  });

  it("Terminal launches the escape hatch without changing route", () => {
    const { getByRole } = render(<LocationBar />);
    fireEvent.click(getByRole("button", { name: "Terminal" }));
    expect(openTerminal).toHaveBeenCalledTimes(1);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("Settings carries the new-context count in its name, not only in a tooltip", () => {
    useFleet.setState({ newContexts: 2 });
    const { getByRole } = render(<LocationBar />);
    expect(getByRole("button", { name: "Settings, 2 new kubeconfig contexts" }).textContent).toContain("2");
  });
});

describe("location bar crumbs and history", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useFleet.setState({ forwards: [], newContexts: 0, clusters: [{ name: "aks-prd-weu-platform-01", state: "Synced", protected: true, protectedInferred: false } as never] });
  });

  it("crumbs read Fleet / Station / Lens; the protected tag sits on the station", () => {
    useFleet.setState({ route: { name: "cluster", cluster: "aks-prd-weu-platform-01", section: "gitops", fluxView: "sources" } });
    const { getByRole, getByText, container } = render(<LocationBar />);
    const nav = getByRole("navigation", { name: "Location" });
    expect(nav.textContent).toBe("Fleet/aks-prd-weu-platform-01protected/Sources");
    expect(getByText("Sources").getAttribute("aria-current")).toBe("page");
    expect(getByText("protected").getAttribute("title")).toMatch(/^Protected - operations need typed confirmation/);
    // No global green freshness readout in the bar.
    expect(container.textContent).not.toMatch(/watching/i);
  });

  it("an inferred protection says so", () => {
    useFleet.setState({ clusters: [{ name: "aks-prd-weu-platform-01", state: "Synced", protected: true, protectedInferred: true } as never],
      route: { name: "cluster", cluster: "aks-prd-weu-platform-01", section: "overview" } });
    const { container } = render(<LocationBar />);
    const tag = container.querySelector(".locbar-protected") as HTMLElement;
    expect(tag.textContent).toBe("protected (inferred)"); // narrow windows hide "(inferred)" visually only
    expect(tag.getAttribute("title")).toMatch(/^Protected - inferred from the cluster's environment name/);
  });

  it("the station crumb names env · region beside the cluster", () => {
    useFleet.setState({ clusters: [{ name: "aks-prd-neu", state: "Synced", env: "prd", region: "northeurope" } as never],
      route: { name: "cluster", cluster: "aks-prd-neu", section: "overview" } });
    const { getByRole } = render(<LocationBar />);
    // the whole place stays in the name and title when a tight bar steps the
    // region out of sight (slice 13a.4)
    const station = getByRole("button", { name: "Station prd · North Europe, aks-prd-neu. Switch cluster" });
    expect(station.textContent).toBe("prd · North Europeaks-prd-neu");
    expect(station.getAttribute("title")).toBe("prd · North Europe aks-prd-neu - switch cluster");
    expect(station.querySelector(".station-region")?.textContent).toBe(" · North Europe");
  });

  it("on the fleet board the Fleet crumb is the current place; elsewhere it leads back", () => {
    useFleet.setState({ route: { name: "fleet" } });
    const r1 = render(<LocationBar />);
    expect(r1.getByRole("button", { name: "Fleet" }).getAttribute("aria-current")).toBe("page");
    expect(r1.getByRole("navigation", { name: "Location" }).textContent).toBe("Fleet");
    r1.unmount();
    useFleet.setState({ route: { name: "settings" } });
    const r2 = render(<LocationBar />);
    expect(r2.getByRole("navigation", { name: "Location" }).textContent).toBe("Fleet/Settings");
    fireEvent.click(r2.getByRole("button", { name: "Fleet" }));
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("the mark leads to the fleet board", () => {
    useFleet.setState({ route: { name: "activity" } });
    const { getByRole } = render(<LocationBar />);
    fireEvent.click(getByRole("button", { name: "Klyx fleet board" }));
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("Back is disabled with no history and names its target when there is one", async () => {
    const { _resetHistoryForTest, initHistory } = await import("../nav/history");
    _resetHistoryForTest();
    useFleet.setState({ route: { name: "fleet" } });
    const off = initHistory();
    const { getByRole, rerender } = render(<LocationBar />);
    expect((getByRole("button", { name: "Back" }) as HTMLButtonElement).disabled).toBe(true);
    useFleet.getState().openCluster("aks-prd-weu-platform-01");
    await new Promise<void>((r) => queueMicrotask(r));
    rerender(<LocationBar />);
    expect((getByRole("button", { name: "Back to Fleet board" }) as HTMLButtonElement).disabled).toBe(false);
    off();
  });
});

describe("location bar lens index toggle (slice 4 §4.3)", () => {
  it("is a disclosure for the index: aria-expanded, aria-controls, and it toggles", async () => {
    const { useLensIndexOpen } = await import("../nav/lensIndexOpen");
    useLensIndexOpen.setState({ open: true });
    useFleet.setState({ route: { name: "fleet" } });
    const { getByRole } = render(<LocationBar />);
    const btn = getByRole("button", { name: "Lens index" });
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    expect(btn.getAttribute("aria-controls")).toBe("lens-index");
    fireEvent.click(btn);
    expect(useLensIndexOpen.getState().open).toBe(false);
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    expect(btn.getAttribute("aria-controls")).toBeNull();
  });
});
