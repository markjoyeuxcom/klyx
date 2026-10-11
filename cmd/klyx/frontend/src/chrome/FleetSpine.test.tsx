import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { FleetSpine, spineCodes } from "./FleetSpine";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";

const cluster = (name: string, state = "Synced", reason = ""): ClusterDTO => ({
  name, state, reason, nodesReady: 1, nodesTotal: 1, pods: 10, version: "v1.36.1",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "homelab", region: "", provider: "k3s", group: "", ageSeconds: 3,
});

describe("FleetSpine", () => {
  beforeEach(() => {
    useFleet.setState({ clusters: [], fleetBoard: {}, route: { name: "fleet" } });
    localStorage.removeItem("klyx-spine-expanded");
  });

  it("renders nothing with no clusters", () => {
    const { queryByTestId } = render(<FleetSpine />);
    expect(queryByTestId("fleet-spine")).toBeNull();
  });

  it("renders one block per cluster; click switches cluster from anywhere", () => {
    useFleet.setState({ clusters: [cluster("nelli"), cluster("blue")], route: { name: "cluster", cluster: "nelli", section: "pods" } });
    const { getByLabelText } = render(<FleetSpine />);
    fireEvent.click(getByLabelText("cluster blue"));
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "blue" });
  });

  it("the spine header is the Fleet destination (step 7a)", () => {
    useFleet.setState({ clusters: [cluster("dev")], route: { name: "cluster", cluster: "dev", section: "pods" } });
    const { getByRole } = render(<FleetSpine />);
    fireEvent.click(getByRole("button", { name: "Fleet" }));
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
    expect(getByRole("button", { name: "Fleet" }).getAttribute("aria-current")).toBe("page");
  });

  it("marks the selected cluster with aria-current", () => {
    useFleet.setState({ clusters: [cluster("nelli"), cluster("blue")], route: { name: "cluster", cluster: "nelli", section: "pods" } });
    const { getByLabelText } = render(<FleetSpine />);
    expect(getByLabelText("cluster nelli").getAttribute("aria-current")).toBe("true");
    expect(getByLabelText("cluster blue").getAttribute("aria-current")).toBeNull();
  });

  it("an unreachable cluster is an unpowered socket - dashed lamp, reason in the tooltip", () => {
    useFleet.setState({ clusters: [cluster("orange", "Failed", "connect timed out")] });
    const { getByLabelText } = render(<FleetSpine />);
    const block = getByLabelText("cluster orange");
    const lamp = block.querySelector("span") as HTMLElement;
    expect(lamp.style.border).toContain("dashed");
    expect(block.title).toContain("connect timed out");
  });

  it("workload faults (from the fleet board) light the lamp danger and blink it - the spine is the one legal blink site", () => {
    useFleet.setState({
      clusters: [cluster("prd")],
      fleetBoard: { prd: { cpuFraction: null, memFraction: null, workloads: { counts: { total: 7, fault: 2, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 5, attention: 2 }, coverage: "complete" }, flux: null, argo: null } },
    });
    const { getByLabelText } = render(<FleetSpine />);
    const block = getByLabelText("cluster prd");
    expect(block.title).toContain("2 workloads need attention");
    const lamp = block.querySelector("span") as HTMLElement;
    expect(lamp.style.background).toContain("danger");
    expect(lamp.className).toContain("klyx-lamp-blink");
  });

  it("expand toggle shows full names and persists the choice", () => {
    useFleet.setState({ clusters: [cluster("homelab-nelli")] });
    const { getByLabelText } = render(<FleetSpine />);
    fireEvent.click(getByLabelText("expand fleet spine"));
    expect(getByLabelText("cluster homelab-nelli").textContent).toContain("homelab-nelli");
    expect(localStorage.getItem("klyx-spine-expanded")).toBe("1");
    fireEvent.click(getByLabelText("collapse fleet spine"));
    expect(getByLabelText("cluster homelab-nelli").textContent).toBe("hn");
    expect(localStorage.getItem("klyx-spine-expanded")).toBe("0");
  });

  it("expanded rows show the broken count beside the name", () => {
    localStorage.setItem("klyx-spine-expanded", "1");
    useFleet.setState({
      clusters: [cluster("prd")],
      fleetBoard: { prd: { cpuFraction: null, memFraction: null, workloads: { counts: { total: 3, fault: 3, degraded: 0, unknown: 0, progressing: 0, paused: 0, ok: 0, attention: 3 }, coverage: "complete" }, flux: null, argo: null } },
    });
    const { getByLabelText } = render(<FleetSpine />);
    expect(getByLabelText("cluster prd").textContent).toContain("3");
    localStorage.removeItem("klyx-spine-expanded");
  });

  it("blocks carry a short identifying code", () => {
    useFleet.setState({ clusters: [cluster("homelab-nelli"), cluster("homelab-blue")] });
    const { getByLabelText } = render(<FleetSpine />);
    expect(getByLabelText("cluster homelab-nelli").textContent).toBe("hn");
    expect(getByLabelText("cluster homelab-blue").textContent).toBe("hb");
  });
});

describe("spineCodes", () => {
  it("uses segment initials when distinct", () => {
    expect(spineCodes(["homelab-blue", "homelab-nelli", "prd-weu", "prd-neu"])).toEqual({
      "homelab-blue": "hb", "homelab-nelli": "hn", "prd-weu": "pw", "prd-neu": "pn",
    });
  });

  it("falls back to last-segment letters on collision", () => {
    // homelab-blue vs homelab-bee both give "hb" -> fall back to "bl"/"be".
    expect(spineCodes(["homelab-blue", "homelab-bee"])).toEqual({
      "homelab-blue": "bl", "homelab-bee": "be",
    });
  });

  it("single-segment names use their first two letters", () => {
    expect(spineCodes(["nelli"])).toEqual({ nelli: "ne" });
  });

  it("degraded-only workloads glow amber and do not blink", () => {
    useFleet.setState({
      clusters: [cluster("stg")],
      fleetBoard: { stg: { cpuFraction: null, memFraction: null, workloads: { counts: { total: 5, fault: 0, degraded: 1, unknown: 0, progressing: 0, paused: 0, ok: 4, attention: 1 }, coverage: "complete" }, flux: null, argo: null } },
    });
    const { getByLabelText } = render(<FleetSpine />);
    const lamp = getByLabelText("cluster stg").querySelector("span") as HTMLElement;
    expect(lamp.style.background).toContain("warning");
    expect(lamp.className).not.toContain("klyx-lamp-blink");
  });

  it("a connected cluster without complete workload evidence is never green", () => {
    useFleet.setState({ clusters: [cluster("dev")], fleetBoard: {} });
    const { getByLabelText } = render(<FleetSpine />);
    const lamp = getByLabelText("cluster dev").querySelector("span") as HTMLElement;
    expect(lamp.style.background).not.toContain("success");
  });
});

describe("FleetSpine unknown workload verdicts", () => {
  it("a Synced cluster whose workloads are all unknown is never green, and says so", () => {
    useFleet.setState({
      clusters: [{ name: "dev", state: "Synced", reason: "" } as never],
      fleetBoard: { dev: { cpuFraction: null, memFraction: null, workloads: { counts: { total: 2, fault: 0, degraded: 0, unknown: 2, progressing: 0, paused: 0, ok: 0, attention: 0 }, coverage: "complete" }, flux: null, argo: null } },
    });
    const { getByLabelText } = render(<FleetSpine />);
    const block = getByLabelText("cluster dev");
    const lamp = block.querySelector("span") as HTMLElement;
    expect(lamp.style.background).not.toContain("success");
    expect(block.title).toContain("unverified");
  });
});
