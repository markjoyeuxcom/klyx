import { describe, it, expect, beforeEach } from "vitest";
import { render, fireEvent } from "@testing-library/react";
import { FleetSpine, spineCodes, spineGroups, spineOrder } from "./FleetSpine";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { applySummary, resetHealthStore } from "../store/health";
import { core, kind, replace, summary, task, verdict } from "../test/healthFixtures";

// the cluster's summary with its workloads part's verdict counts (health 1c slice 5)
const workloads = (cluster: string, counts: Parameters<typeof verdict>[1]) =>
  applySummary(summary(replace(core(), task("workloads", [verdict("workloads", counts, [kind("deployments")])])), {}, cluster));

const cluster = (name: string, state = "Synced", reason = ""): ClusterDTO => ({
  name, state, reason, nodesReady: 1, nodesTotal: 1, pods: 10, version: "v1.36.1",
  gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
  env: "homelab", region: "", provider: "k3s", group: "", ageSeconds: 3,
});

describe("FleetSpine", () => {
  beforeEach(() => {
    useFleet.setState({ clusters: [], route: { name: "fleet" } });
    resetHealthStore();
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

  it("a fault area (from the cluster's summary) shows the fault aspect, and nothing blinks (The Line)", () => {
    useFleet.setState({ clusters: [cluster("prd")] });
    workloads("prd", { fault: 2, ok: 5 });
    const { getByLabelText } = render(<FleetSpine />);
    const block = getByLabelText("cluster prd");
    expect(block.title).toMatch(/^prd - synced\n/);
    expect(block.title).toContain("Workloads: 2 need attention");
    const lamp = block.querySelector("span") as HTMLElement;
    expect(lamp.getAttribute("data-aspect")).toBe("fault");
    expect(lamp.style.background).toContain("danger");
    expect(lamp.className).toBe("");
  });

  it("expand toggle shows full names and persists the choice", () => {
    useFleet.setState({ clusters: [cluster("homelab-nelli")] });
    const { getByLabelText } = render(<FleetSpine />);
    fireEvent.click(getByLabelText("expand fleet spine"));
    expect(getByLabelText("cluster homelab-nelli").textContent).toContain("homelab-nelli");
    expect(localStorage.getItem("klyx.layout.v1.spineExpanded")).toBe("1");
    fireEvent.click(getByLabelText("collapse fleet spine"));
    expect(getByLabelText("cluster homelab-nelli").textContent).toBe("hn");
    expect(localStorage.getItem("klyx.layout.v1.spineExpanded")).toBe("0");
  });

  it("expanded rows show the number of AREAS needing attention beside the name, never objects", () => {
    localStorage.setItem("klyx-spine-expanded", "1");
    useFleet.setState({ clusters: [cluster("prd")] });
    applySummary(summary(replace(replace(core(), task("workloads", [verdict("workloads", { fault: 3 }, [kind("deployments")])])),
      task("nodes", [verdict("nodes", { degraded: 4 }, [kind("nodes")])])), {}, "prd"));
    const { getByLabelText } = render(<FleetSpine />);
    expect(getByLabelText("cluster prd").textContent).toBe("prd2");
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

  it("six names that differ only in middle segments get distinct codes (step 7b)", () => {
    const names = ["dev-weu", "dev-neu", "stg-weu", "stg-neu", "prd-weu", "prd-neu"].map((x) => `aks-${x}-platform-01`);
    expect(Object.values(spineCodes(names))).toEqual(["dw", "dn", "sw", "sn", "pw", "pn"]);
  });
  it("names differing inside a segment get distinguishing prefixes", () => {
    expect(spineCodes(["aks-dev-weu-platform-01", "aks-dev-wus-platform-01"])).toEqual({
      "aks-dev-weu-platform-01": "we", "aks-dev-wus-platform-01": "wu",
    });
  });
  it("codes are always unique: identical-looking names get a numbered suffix", () => {
    const names = ["a-x", "a.x", "a_x", "ax"];
    const codes = spineCodes(names);
    expect(new Set(Object.values(codes)).size).toBe(names.length);
    expect(codes["a-x"]).toBe("ax");
    // fallbacks colliding with another cluster's code are resolved too
    const mixed = spineCodes(["homelab-blue", "homelab-bee", "bl", "be"]);
    expect(new Set(Object.values(mixed)).size).toBe(4);
  });
  it("single-segment names use their first two letters", () => {
    expect(spineCodes(["nelli"])).toEqual({ nelli: "ne" });
  });

  it("degraded-only workloads show the look-at aspect and do not blink", () => {
    resetHealthStore();
    useFleet.setState({ clusters: [cluster("stg")] });
    workloads("stg", { degraded: 1, ok: 4 });
    const { getByLabelText } = render(<FleetSpine />);
    const lamp = getByLabelText("cluster stg").querySelector("span") as HTMLElement;
    expect(lamp.getAttribute("data-aspect")).toBe("attention");
    expect(lamp.style.border).toContain("warning");
    expect(lamp.className).toBe("");
  });

  it("a connected cluster without complete workload evidence is never green", () => {
    useFleet.setState({ clusters: [cluster("dev")] });
    const { getByLabelText } = render(<FleetSpine />);
    const lamp = getByLabelText("cluster dev").querySelector("span") as HTMLElement;
    expect(lamp.style.background).not.toContain("success");
  });
});

describe("FleetSpine unknown workload verdicts", () => {
  it("a Synced cluster whose workloads are all unknown is never green, and says so", () => {
    resetHealthStore();
    useFleet.setState({ clusters: [{ name: "dev", state: "Synced", reason: "" } as never] });
    workloads("dev", { unknown: 2 });
    const { getByLabelText } = render(<FleetSpine />);
    const block = getByLabelText("cluster dev");
    const lamp = block.querySelector("span") as HTMLElement;
    expect(lamp.style.background).not.toContain("success");
    expect(block.title).toContain("unverified");
  });
});

describe("FleetSpine environment grouping (redesign step 7b)", () => {
  const env = (name: string, e: string): ClusterDTO => ({ ...cluster(name), env: e });

  it("groups dev → test → stg → prd, config order within, unknown then untagged last", () => {
    const cs = [env("aks-prd-weu", "PRD"), env("aks-dev-weu", "dev"), env("lab", ""), env("aks-stg-weu", "staging"), env("aks-prd-neu", "prod"), env("aks-dev-neu", "dev"), env("edge", "edge")];
    expect(spineGroups(cs).map((g) => [g.env, g.clusters.map((c) => c.name)])).toEqual([
      ["dev", ["aks-dev-weu", "aks-dev-neu"]],
      ["staging", ["aks-stg-weu"]],
      ["prd", ["aks-prd-weu"]],
      ["prod", ["aks-prd-neu"]],
      ["edge", ["edge"]],
      ["other", ["lab"]],
    ]);
  });

  it("no environment tags, or one environment for all: one flat list, no labels", () => {
    const untagged = [{ ...cluster("b"), env: "" }, { ...cluster("a"), env: "" }];
    expect(spineGroups(untagged)).toEqual([{ env: null, clusters: untagged }]);
    const homelab = [cluster("b"), cluster("a")];
    expect(spineGroups(homelab)).toEqual([{ env: null, clusters: homelab }]);
  });

  it("mod+N follows the spine's visual order", () => {
    const cs = [env("aks-prd-weu", "prd"), env("aks-dev-weu", "dev")];
    expect(spineOrder(cs).map((c) => c.name)).toEqual(["aks-dev-weu", "aks-prd-weu"]);
  });

  it("renders a labelled group per environment and migrates the old expansion key", () => {
    localStorage.removeItem("klyx.layout.v1.spineExpanded");
    localStorage.setItem("klyx-spine-expanded", "1");
    useFleet.setState({ clusters: [env("aks-prd-weu", "prd"), env("aks-dev-weu", "dev")] });
    const { getByRole, getByLabelText } = render(<FleetSpine />);
    expect(getByRole("group", { name: "dev clusters" }).textContent).toContain("aks-dev-weu");
    expect(getByRole("group", { name: "prd clusters" }).textContent).toContain("aks-prd-weu");
    expect(getByLabelText("collapse fleet spine")).toBeTruthy();
  });
});
