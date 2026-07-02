import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { CommandPalette } from "./CommandPalette";
import { useFleet } from "../store/fleet";
import type { ClusterDTO, PodSummaryDTO } from "../store/fleet";

vi.mock("../bridge/pods", () => ({ openPodDetail: vi.fn(), listPods: vi.fn() }));
vi.mock("../bridge/helm", () => ({ openHelmRelease: vi.fn() }));

const mkCluster = (name: string, env: string, region: string): ClusterDTO =>
  ({ name, env, region } as ClusterDTO);

const mkPod = (namespace: string, name: string): PodSummaryDTO =>
  ({ namespace, name, phase: "Running", rank: "healthy" } as PodSummaryDTO);

function seedClusters() {
  useFleet.setState({
    clusters: [mkCluster("dev-we", "DEV", "westeurope"), mkCluster("prd-ne", "PRD", "northeurope")],
    route: { name: "fleet" },
  });
}

function pressCmdK() {
  fireEvent.keyDown(window, { key: "k", metaKey: true });
}

beforeEach(() => {
  vi.clearAllMocks();
  useFleet.getState().clearPods();
  useFleet.setState({ clusters: [], route: { name: "fleet" } });
});

describe("CommandPalette", () => {
  it("opens on ⌘K and closes on Esc", () => {
    seedClusters();
    const { queryByTestId } = render(<CommandPalette />);
    expect(queryByTestId("command-palette")).toBeNull();
    pressCmdK();
    expect(queryByTestId("command-palette")).not.toBeNull();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(queryByTestId("command-palette")).toBeNull();
  });

  it("filters and ranks as you type", () => {
    seedClusters();
    const { getByTestId, queryByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.change(getByTestId("command-palette-input"), { target: { value: "prd" } });
    expect(queryByTestId("command-row-cluster:prd-ne")).not.toBeNull();
    expect(queryByTestId("command-row-cluster:dev-we")).toBeNull();
  });

  it("runs the selected command on Enter", () => {
    const openCluster = vi.fn();
    seedClusters();
    useFleet.setState({ openCluster });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.change(getByTestId("command-palette-input"), { target: { value: "dev-we" } });
    fireEvent.keyDown(getByTestId("command-palette-input"), { key: "Enter" });
    expect(openCluster).toHaveBeenCalledWith("dev-we");
  });

  it("runs a command on click", () => {
    const openCluster = vi.fn();
    seedClusters();
    useFleet.setState({ openCluster });
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.mouseDown(getByTestId("command-row-cluster:prd-ne"));
    expect(openCluster).toHaveBeenCalledWith("prd-ne");
  });

  it("does not open when ⌘K is pressed inside another input", () => {
    seedClusters();
    const { queryByTestId } = render(
      <div>
        <textarea data-testid="other" />
        <CommandPalette />
      </div>,
    );
    const ta = queryByTestId("other")!;
    fireEvent.keyDown(ta, { key: "k", metaKey: true });
    expect(queryByTestId("command-palette")).toBeNull();
  });

  it("shows a Pods group when the pods slice is seeded", () => {
    seedClusters();
    useFleet.getState().setPods("dev-we", "", { namespaces: ["team"], pods: [mkPod("team", "api-1")] });
    const { getByTestId, queryByTestId } = render(<CommandPalette />);
    pressCmdK();
    fireEvent.change(getByTestId("command-palette-input"), { target: { value: "api-1" } });
    expect(queryByTestId("command-row-pod:team/api-1")).not.toBeNull();
  });

  it("moves selection with arrow keys", () => {
    seedClusters();
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    const input = getByTestId("command-palette-input");
    // first row selected by default
    expect(getByTestId("command-row-cluster:dev-we").getAttribute("data-selected")).toBe("true");
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(getByTestId("command-row-cluster:dev-we").getAttribute("data-selected")).toBe("false");
    expect(getByTestId("command-row-cluster:prd-ne").getAttribute("data-selected")).toBe("true");
  });

  it("scrolls the selected row into view when navigating", () => {
    // jsdom does not implement scrollIntoView; install a spy so the palette's
    // guarded call is observable.
    const scrollSpy = vi.fn();
    window.HTMLElement.prototype.scrollIntoView = scrollSpy;
    seedClusters();
    const { getByTestId } = render(<CommandPalette />);
    pressCmdK();
    scrollSpy.mockClear();
    fireEvent.keyDown(getByTestId("command-palette-input"), { key: "ArrowDown" });
    expect(getByTestId("command-row-cluster:prd-ne").getAttribute("data-selected")).toBe("true");
    expect(scrollSpy).toHaveBeenCalledWith({ block: "nearest" });
  });
});
