import { describe, it, expect, beforeEach } from "vitest";
import { useFleet } from "./fleet";
import {
  saveRoute,
  loadRoute,
  restoreRouteOnBoot,
  initRoutePersistence,
  loadClusterPrefs,
  savePodsViewPrefs,
  saveWorkloadsViewPrefs,
  loadSearch,
  saveSearch,
} from "./persist";

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  useFleet.setState({ clusters: [], route: { name: "fleet" } });
});

describe("route persistence", () => {
  it("round-trips a cluster route, dropping drill-in state", () => {
    saveRoute({
      name: "cluster",
      cluster: "prd-weu",
      section: "resources",
      resource: { group: "apps", version: "v1", plural: "deployments", kind: "Deployment", scope: "Namespaced" },
      instance: { namespace: "web", name: "api" },
    });
    expect(loadRoute()).toEqual({ name: "cluster", cluster: "prd-weu", section: "resources", argoView: undefined });
  });

  it("round-trips top-level routes", () => {
    saveRoute({ name: "settings" });
    expect(loadRoute()).toEqual({ name: "settings" });
  });

  it("falls back to overview for an unknown persisted section", () => {
    localStorage.setItem("klyx-route", JSON.stringify({ name: "cluster", cluster: "x", section: "not-a-section" }));
    expect(loadRoute()).toMatchObject({ name: "cluster", cluster: "x", section: "overview" });
  });

  it("tolerates corrupt storage", () => {
    localStorage.setItem("klyx-route", "{nope");
    expect(loadRoute()).toBeNull();
  });

  it("restores a cluster route when the cluster still exists", () => {
    saveRoute({ name: "cluster", cluster: "dev-neu", section: "pods" });
    restoreRouteOnBoot(["dev-neu", "prd-weu"]);
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "dev-neu", section: "pods", argoView: undefined });
  });

  it("does not restore into a cluster missing from the kubeconfig", () => {
    saveRoute({ name: "cluster", cluster: "gone", section: "pods" });
    restoreRouteOnBoot(["dev-neu"]);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("never clobbers a route the user already navigated to", () => {
    saveRoute({ name: "cluster", cluster: "dev-neu", section: "pods" });
    useFleet.getState().openCluster("prd-weu");
    restoreRouteOnBoot(["dev-neu", "prd-weu"]);
    expect(useFleet.getState().route).toMatchObject({ name: "cluster", cluster: "prd-weu", section: "overview" });
  });

  it("restores the argo subview only when valid", () => {
    saveRoute({ name: "cluster", cluster: "x", section: "argo", argoView: "projects" });
    restoreRouteOnBoot(["x"]);
    expect(useFleet.getState().route).toMatchObject({ section: "argo", argoView: "projects" });
  });

  it("restores the Flux destination and the Hubble flows section (step 7a); junk views drop", () => {
    saveRoute({ name: "cluster", cluster: "x", section: "gitops", fluxView: "sources" });
    restoreRouteOnBoot(["x"]);
    expect(useFleet.getState().route).toMatchObject({ section: "gitops", fluxView: "sources" });
    useFleet.setState({ route: { name: "fleet" } });
    localStorage.setItem("klyx-route", JSON.stringify({ name: "cluster", cluster: "x", section: "gitops", fluxView: "bogus" }));
    restoreRouteOnBoot(["x"]);
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "x", section: "gitops" });
    useFleet.setState({ route: { name: "fleet" } });
    saveRoute({ name: "cluster", cluster: "x", section: "flows" });
    restoreRouteOnBoot(["x"]);
    expect(useFleet.getState().route).toMatchObject({ section: "flows" });
  });

  it("restores a fleet view (step 7d); an unknown view lands on the board", () => {
    saveRoute({ name: "fleet", view: "expiry" });
    restoreRouteOnBoot(["x"]);
    expect(useFleet.getState().route).toEqual({ name: "fleet", view: "expiry" });
    useFleet.setState({ route: { name: "fleet" } });
    localStorage.setItem("klyx-route", JSON.stringify({ name: "fleet", view: "bogus" }));
    restoreRouteOnBoot(["x"]);
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });

  it("initRoutePersistence mirrors store navigation into storage", () => {
    const off = initRoutePersistence();
    useFleet.getState().openCluster("stg-weu");
    useFleet.getState().setSection("events");
    off();
    expect(loadRoute()).toEqual({ name: "cluster", cluster: "stg-weu", section: "events", argoView: undefined });
  });
});

describe("view prefs", () => {
  it("stores prefs per cluster and per view", () => {
    savePodsViewPrefs("a", { namespace: "web", needsAttention: true });
    savePodsViewPrefs("b", { namespace: "infra" });
    saveWorkloadsViewPrefs("a", { namespace: "", kinds: ["Deployment"], nearLimitSort: true });
    expect(loadClusterPrefs("a").pods).toEqual({ namespace: "web", needsAttention: true });
    expect(loadClusterPrefs("a").workloads).toEqual({ namespace: "", kinds: ["Deployment"], nearLimitSort: true });
    expect(loadClusterPrefs("b").pods).toEqual({ namespace: "infra" });
    expect(loadClusterPrefs("c")).toEqual({});
  });

  it("tolerates corrupt prefs storage", () => {
    localStorage.setItem("klyx-view-prefs", "[not json");
    expect(loadClusterPrefs("a")).toEqual({});
  });
});

describe("search persistence (session-scoped)", () => {
  it("round-trips and clears per cluster+view", () => {
    saveSearch("a", "pods", "crash");
    expect(loadSearch("a", "pods")).toBe("crash");
    expect(loadSearch("a", "events")).toBe("");
    saveSearch("a", "pods", "");
    expect(loadSearch("a", "pods")).toBe("");
  });
});
