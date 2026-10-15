import { describe, it, expect, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useFleet, type ClusterDTO } from "../store/fleet";
import { useHealth, resetHealthStore, applySummary, type HealthRef } from "../store/health";
import { core, kind, replace, summary, task, verdict } from "../test/healthFixtures";
import { clusterAttention } from "../nav/clusterAttention";
import { NeedsYou, useQueueFilter } from "./NeedsYou";
import { applyLine, resetLineStore, type LineDTO } from "../store/line";

// The Line slice 5b: "Needs you" on the fleet screen.

const cl = (name: string, env = "dev", state = "Synced"): ClusterDTO => ({ name, state, reason: "", env, region: "westeurope" } as ClusterDTO);
const ref = (name: string): HealthRef => ({ cluster: "x", gvr: "v1/pods", namespace: "shop", name, uid: `uid-${name}`, role: "fault", label: "CrashLoopBackOff" });
const mount = (clusters: ClusterDTO[]) => {
  const models = Object.fromEntries(clusters.map((c) => [c.name, clusterAttention(useHealth.getState(), c.name, c)]));
  return render(<NeedsYou clusters={clusters} models={models} />);
};

beforeEach(() => {
  resetHealthStore();
  resetLineStore();
  useQueueFilter.setState({ filter: "all" });
  useFleet.setState({ route: { name: "fleet" }, navIntent: null } as never);
});

describe("Needs you", () => {
  it("counts faults and look-at rows; a row reveals its own object on its own cluster", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 7 }, [kind("pods")], { top: ["a", "b", "c", "d", "e"].map(ref) })])), {}, "prd1"));
    mount([cl("prd1", "prd")]);
    expect(screen.getByRole("heading", { name: "Needs you" })).toBeTruthy();
    expect(screen.getByText("7 faults")).toBeTruthy(); // the model's count, not the five samples
    fireEvent.click(screen.getByRole("button", { name: /^Fault: Pod shop\/a, CrashLoopBackOff, on prd1/ }));
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "prd1", section: "pods" });
    expect(useFleet.getState().navIntent).toMatchObject({ kind: "select-pod", cluster: "prd1", namespace: "shop", name: "a" });
  });

  // The Line slice 8a (parent §3.7): a row's station is its LinePos tag. The
  // fill is the fleet's fault truth: a Pod fault fills it while the line's
  // GitOps comparison is clean (design review of 9705575).
  it("a row carries the LinePos tag; a Pod fault fills it though the line's GitOps flag is clean", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 1 }, [kind("pods")], { top: [ref("a")] })])), {}, "prd1"));
    applyLine({ version: 1, tracks: [{ name: "weu", kind: "region", label: "West Europe" }], siding: [],
      stations: [
        { cluster: "dev1", track: "weu", rank: 0, env: "dev", state: "matches", flags: { failing: false } },
        { cluster: "prd1", track: "weu", rank: 3, env: "prd", state: "matches", flags: { failing: false } },
      ] } as unknown as LineDTO);
    mount([cl("dev1"), cl("prd1", "prd")]);
    const row = screen.getByRole("button", { name: /^Fault: Pod shop\/a/ });
    const pos = row.querySelector("[data-linepos]") as HTMLElement;
    expect(pos.getAttribute("aria-label")).toBe("prd1, prd on West Europe");
    expect(pos.querySelectorAll("[data-sq]")).toHaveLength(2);
    expect((pos.querySelector("[data-here]") as HTMLElement).style.background).toContain("danger");
    expect(row.querySelector(".queue-cluster")).toBeNull(); // the tag replaces the place and name
  });

  it("a cluster the line does not place keeps its place and name", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 1 }, [kind("pods")], { top: [ref("a")] })])), {}, "prd1"));
    mount([cl("prd1", "prd")]);
    const row = screen.getByRole("button", { name: /^Fault: Pod shop\/a/ });
    expect(row.querySelector("[data-linepos]")).toBeNull();
    expect(row.querySelector(".queue-cluster")?.textContent).toBeTruthy();
  });

  it("+N more links to the part's lens", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 7 }, [kind("pods")], { top: ["a", "b", "c", "d", "e"].map(ref) })])), {}, "c1"));
    mount([cl("c1")]);
    fireEvent.click(screen.getByRole("button", { name: "+2 more in Pods, on c1" }));
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "c1", section: "pods" });
  });

  it("the filter is pressed state, and it lasts the session", () => {
    applySummary(summary(replace(core(), task("pods", [verdict("pods", { fault: 1 }, [kind("pods")], { top: [ref("a")] })])), {}, "dev1"));
    const r = mount([cl("dev1")]);
    fireEvent.click(screen.getByRole("button", { name: "Prd only" }));
    expect(screen.getByRole("button", { name: "Prd only" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText("Nothing in this filter, in the clusters read.")).toBeTruthy();
    r.unmount();
    mount([cl("dev1")]);
    expect(screen.getByRole("button", { name: "Prd only" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("nothing in the read clusters says so, and names the clusters not read", () => {
    applySummary(summary(core(), {}, "q"));
    mount([cl("q"), cl("x", "prd", "Failed")]);
    expect(screen.getByText("Nothing in the clusters read needs attention.")).toBeTruthy();
    expect(screen.getByText(/^Not read:/).textContent).toBe("Not read: x (failed)");
  });
});

describe("moreText (review of 087e5e2)", () => {
  it("a lower bound reads '+N or more in', never 'or more more'; both roles are split", async () => {
    const { moreText } = await import("./NeedsYou");
    const base = { key: "k", cluster: "c", place: "", rank: 0, tone: "danger" as const, area: "Pods", areaOrder: 0, task: "pods", part: "pods", kind: "more" as const, destinationId: "pods", lens: "Pods" };
    expect(moreText({ ...base, faults: 2, attention: 0, lower: false })).toBe("+2 more in Pods");
    expect(moreText({ ...base, faults: 2, attention: 0, lower: true })).toBe("+2 or more in Pods");
    expect(moreText({ ...base, faults: 1, attention: 2, lower: false })).toBe("+3 more in Pods (1 fault, 2 to look at)");
  });
});
