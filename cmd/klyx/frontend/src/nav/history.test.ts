import { describe, it, expect, beforeEach } from "vitest";
import { useFleet } from "../store/fleet";
import type { ClusterDTO } from "../store/fleet";
import { _resetHistoryForTest, back, backTarget, entryLabel, forward, go, initHistory, recentPlaces, useHistory } from "./history";

const tick = () => new Promise<void>((r) => queueMicrotask(r));
const c = (name: string) => ({ name, state: "Synced", env: "", protected: false } as unknown as ClusterDTO);
const all = { Deployment: true, StatefulSet: true, DaemonSet: true };
let off = () => {};

beforeEach(() => {
  off();
  _resetHistoryForTest();
  useFleet.setState((s) => ({ clusters: [c("dev"), c("prd")], route: { name: "fleet" }, navIntent: null, workloads: { ...s.workloads, kindFilter: all } }));
  off = initHistory();
});

describe("navigation history (redesign step 7b)", () => {
  it("one navigation of several store writes is one entry", async () => {
    const s = useFleet.getState();
    s.openCluster("dev");
    s.setSection("pods");
    await tick();
    expect(useHistory.getState().past.map((e) => e.route)).toEqual([{ name: "fleet" }]);
  });

  it("back and forward restore places; a new navigation drops the forward stack", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().setSection("events"); await tick();
    expect(back()).toBe(true);
    expect(useFleet.getState().route).toMatchObject({ cluster: "dev", section: "overview" });
    expect(forward()).toBe(true);
    expect(useFleet.getState().route).toMatchObject({ section: "events" });
    back(); await tick();
    useFleet.getState().setSection("nodes"); await tick();
    expect(useHistory.getState().future).toEqual([]);
    expect(forward()).toBe(false);
  });

  it("restoring never replays an action: no NavIntent is re-raised", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().setSection("nodes");
    useFleet.getState().requestNavIntent({ kind: "drain-node", cluster: "dev", node: "n1" });
    await tick();
    useFleet.getState().clearNavIntent(); // the view consumed it
    useFleet.getState().setSection("pods"); await tick();
    back();
    expect(useFleet.getState().route).toMatchObject({ section: "nodes" });
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("a workloads kind destination comes back with its kind filter", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().openWorkloads(["StatefulSet"]); await tick();
    useFleet.getState().setSection("pods"); await tick();
    back();
    expect(useFleet.getState().workloads.kindFilter).toEqual({ Deployment: false, StatefulSet: true, DaemonSet: false });
    expect(entryLabel({ route: useFleet.getState().route, kindFilter: useFleet.getState().workloads.kindFilter }).place).toBe("StatefulSets");
  });

  it("a Gateway topology comes back through openGateway, so its read is not fenced out", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().openGateway("infra", "eg"); await tick();
    useFleet.getState().setSection("pods"); await tick();
    useFleet.setState((s) => ({ network: { ...s.network, selected: null } }));
    back();
    expect(useFleet.getState().route).toMatchObject({ section: "gatewayApi", gateway: { namespace: "infra", name: "eg" } });
    expect(useFleet.getState().network.selected).toEqual({ namespace: "infra", name: "eg" });
  });

  it("places on a cluster that left the fleet are skipped", async () => {
    useFleet.getState().openCluster("prd"); await tick();
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.setState({ clusters: [c("dev")] });
    expect(back()).toBe(true);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("recent places exclude the current one, dedupe, and opening one is recorded", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().setSection("events"); await tick();
    useFleet.getState().openFleet(); await tick();
    useFleet.getState().openCluster("dev"); await tick();
    const places = recentPlaces().map((e) => entryLabel(e).place);
    expect(places).toEqual(["Fleet board", "Events"]);
    go(recentPlaces()[1]);
    expect(useFleet.getState().route).toMatchObject({ section: "events" });
    back();
    expect(useFleet.getState().route).toMatchObject({ section: "overview" });
  });

  it("choosing a kind inside the workloads view is a navigation; hydration is not (step 7b review)", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().openWorkloads(["Deployment", "StatefulSet", "DaemonSet"]); await tick();
    const before = useHistory.getState().past.length;
    useFleet.getState().hydrateWorkloadKinds(["DaemonSet"]); await tick();
    expect(useHistory.getState().past.length).toBe(before);
    useFleet.getState().setWorkloadKindFilter(["StatefulSet"]); await tick();
    expect(useHistory.getState().past.length).toBe(before + 1);
    expect(entryLabel(recentPlaces()[0]).place).toBe("DaemonSets");
    back();
    expect(useFleet.getState().workloads.kindFilter).toEqual({ Deployment: false, StatefulSet: false, DaemonSet: true });
    expect(useFleet.getState().workloads.kindExplicit).toBe(true);
  });

  it("a NavIntent still waiting for data never fires on a place reached through history", async () => {
    useFleet.getState().openCluster("dev"); await tick();
    useFleet.getState().setSection("nodes");
    useFleet.getState().requestNavIntent({ kind: "drain-node", cluster: "dev", node: "n1" });
    await tick();
    useFleet.getState().setSection("pods"); await tick(); // left before the view consumed it
    expect(useFleet.getState().navIntent).not.toBeNull();
    back();
    expect(useFleet.getState().route).toMatchObject({ section: "nodes" });
    expect(useFleet.getState().navIntent).toBeNull();
  });

  it("the Back target skips clusters that left the fleet; none reachable means none offered", async () => {
    useFleet.getState().openCluster("prd"); await tick();
    useFleet.getState().openCluster("dev"); await tick();
    expect(entryLabel(backTarget()!).cluster).toBe("prd");
    useFleet.setState({ clusters: [c("dev")] });
    expect(backTarget()?.route).toEqual({ name: "fleet" });
    _resetHistoryForTest();
    useHistory.setState({ past: [{ route: { name: "cluster", cluster: "gone", section: "pods" } }] });
    expect(backTarget()).toBeUndefined();
  });
});
