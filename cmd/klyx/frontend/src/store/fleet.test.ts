import { describe, it, test, expect, beforeEach } from "vitest";
import { useFleet, crdCountKey } from "./fleet";

beforeEach(() => useFleet.setState({ clusters: [], route: { name: "fleet" } }));

describe("fleet store routing", () => {
  it("openCluster enters cluster scope on overview", () => {
    useFleet.getState().openCluster("homelab-nelli");
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "homelab-nelli", section: "overview" });
  });
  it("setSection changes section in cluster scope", () => {
    useFleet.getState().openCluster("x");
    useFleet.getState().setSection("gitops");
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "x", section: "gitops" });
  });
  it("setSection is a no-op at the fleet root", () => {
    useFleet.getState().setSection("gitops");
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });
  it("openArgo selects an Argo subview in cluster scope", () => {
    useFleet.getState().openCluster("x");
    useFleet.getState().openArgo("applicationsets");
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "x", section: "argo", argoView: "applicationsets" });
  });
  it("openWorkloads drops a generic resource route and applies the requested kind filter atomically", () => {
    useFleet.getState().openCluster("x");
    useFleet.getState().openResource({ group: "apps", version: "v1", plural: "replicasets", kind: "ReplicaSet", scope: "Namespaced" });
    useFleet.getState().openWorkloads(["StatefulSet"]);
    expect(useFleet.getState().route).toEqual({ name: "cluster", cluster: "x", section: "workloads" });
    expect(useFleet.getState().workloads.kindFilter).toEqual({
      Deployment: false,
      StatefulSet: true,
      DaemonSet: false,
    });
  });
  it("openFleet returns to the grid", () => {
    useFleet.getState().openCluster("x");
    useFleet.getState().openFleet();
    expect(useFleet.getState().route).toEqual({ name: "fleet" });
  });
  it("keeps the selected cluster resolvable after setClusters", () => {
    useFleet.getState().openCluster("x");
    useFleet.getState().setClusters([
      { name: "x", state: "Synced", reason: "", nodesReady: 1, nodesTotal: 1, pods: 1, version: "v1",
        gitopsTier: "Healthy", gitopsReason: "", networkTier: "Healthy", networkReason: "",
        env: "", region: "", provider: "", group: "", ageSeconds: 0 },
    ]);
    const st = useFleet.getState();
    expect(st.route).toMatchObject({ name: "cluster", cluster: "x" });
    expect(st.clusters.find((c) => c.name === "x")).toBeTruthy();
  });
});

test("instance first page replaces (idempotent), load-more appends", () => {
  const ref = { group: "cilium.io", version: "v2", plural: "ciliumendpoints", kind: "CiliumEndpoint", scope: "Namespaced" };
  useFleet.getState().openCluster("y");
  useFleet.getState().openResource(ref);
  const page = [{ namespace: "n", name: "a", created: "" }];
  // Two first-page sets (e.g. StrictMode double effect) must not duplicate.
  useFleet.getState().setInstancePage(page, "tok");
  useFleet.getState().setInstancePage(page, "tok");
  expect(useFleet.getState().instances.rows.length).toBe(1);
  // Load-more appends.
  useFleet.getState().addInstancePage([{ namespace: "n", name: "b", created: "" }], "");
  expect(useFleet.getState().instances.rows.length).toBe(2);
});

test("crd slice: set groups, toggle, count, search, groupBy", () => {
  useFleet.getState().setCRDs("x", [
    { group: "cilium.io", category: "CNI", kinds: [{ kind: "CiliumEndpoint", plural: "ciliumendpoints", scope: "Namespaced", version: "v2", operator: "cilium", shortNames: ["cep"] }] },
  ]);
  expect(useFleet.getState().crd.groups.length).toBe(1);

  useFleet.getState().toggleCRDGroup("cilium.io");
  expect(useFleet.getState().crd.expanded).toContain("cilium.io");
  useFleet.getState().toggleCRDGroup("cilium.io");
  expect(useFleet.getState().crd.expanded).not.toContain("cilium.io");

  const key = crdCountKey("cilium.io", "v2", "ciliumendpoints");
  useFleet.getState().setCRDCount(key, { count: 500, capped: true });
  expect(useFleet.getState().crd.counts[key].capped).toBe(true);

  useFleet.getState().setCRDGroupBy("scope");
  expect(useFleet.getState().crd.groupBy).toBe("scope");
  useFleet.getState().setCRDSearch("cep");
  expect(useFleet.getState().crd.search).toBe("cep");
});

import { useFleet as uf2 } from "./fleet";

test("resource drill-in route + instances slice", () => {
  uf2.getState().openCluster("x");
  const ref = { group: "cilium.io", version: "v2", plural: "ciliumendpoints", kind: "CiliumEndpoint", scope: "Namespaced" };
  uf2.getState().openResource(ref);
  const r = uf2.getState().route;
  expect(r).toMatchObject({ name: "cluster", cluster: "x", section: "resources", resource: { kind: "CiliumEndpoint" } });
  expect(uf2.getState().instances.ref?.kind).toBe("CiliumEndpoint");
  expect(uf2.getState().instances.loading).toBe(true);

  uf2.getState().addInstancePage([{ namespace: "n", name: "a", created: "" }], "tok");
  expect(uf2.getState().instances.rows.length).toBe(1);
  expect(uf2.getState().instances.nextToken).toBe("tok");
  uf2.getState().addInstancePage([{ namespace: "n", name: "b", created: "" }], "");
  expect(uf2.getState().instances.rows.length).toBe(2);

  uf2.getState().setInstanceFilter("a");
  expect(uf2.getState().instances.filter).toBe("a");

  uf2.getState().setSection("gitops");
  const r2 = uf2.getState().route;
  expect(r2.name === "cluster" && r2.resource).toBeUndefined();

  uf2.getState().openResource(ref);
  uf2.getState().closeResource();
  const r3 = uf2.getState().route;
  expect(r3).toMatchObject({ name: "cluster", cluster: "x", section: "resources" });
  expect(r3.name === "cluster" && r3.resource).toBeUndefined();
});

import { useFleet as uf3 } from "./fleet";

test("instance detail drill-in route + slice", () => {
  uf3.getState().openCluster("x");
  const ref = { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" };
  uf3.getState().openResource(ref);
  uf3.getState().openInstance("default", "web-tls");
  const r = uf3.getState().route;
  expect(r).toMatchObject({ name: "cluster", section: "resources", resource: { kind: "Certificate" }, instance: { namespace: "default", name: "web-tls" } });
  expect(uf3.getState().instanceDetail.ref).toEqual({ namespace: "default", name: "web-tls" });
  expect(uf3.getState().instanceDetail.loading).toBe(true);

  uf3.getState().setInstanceDetail({ kind: "Certificate", namespace: "default", name: "web-tls", created: "", labels: {}, conditions: [], events: [], yaml: "kind: Certificate\n" });
  expect(uf3.getState().instanceDetail.detail?.yaml).toContain("Certificate");
  expect(uf3.getState().instanceDetail.loading).toBe(false);
  uf3.getState().setInstanceDetailLoading({ namespace: "default", name: "web-tls" });
  expect(uf3.getState().instanceDetail.detail?.yaml).toContain("Certificate");
  expect(uf3.getState().instanceDetail.loading).toBe(true);

  uf3.getState().closeInstance();
  const r2 = uf3.getState().route;
  expect(r2.name === "cluster" && r2.resource?.kind).toBe("Certificate");
  expect(r2.name === "cluster" && r2.instance).toBeUndefined();

  uf3.getState().openInstance("default", "web-tls");
  uf3.getState().openResource(ref);
  const rReopen = uf3.getState().route;
  expect(rReopen.name === "cluster" && rReopen.instance).toBeUndefined();
  uf3.getState().openInstance("default", "web-tls");
  uf3.getState().setSection("gitops");
  const r3 = uf3.getState().route;
  expect(r3.name === "cluster" && r3.resource).toBeUndefined();
  expect(r3.name === "cluster" && r3.instance).toBeUndefined();
});

it("action status set and clear", () => {
  useFleet.getState().setActionStatus({ kind: "success", message: "Reconcile requested" });
  expect(useFleet.getState().actionStatus?.message).toBe("Reconcile requested");
  useFleet.getState().clearActionStatus();
  expect(useFleet.getState().actionStatus).toBeNull();
});

import { useFleet as uf4 } from "./fleet";

test("network gateway drill-in route + slice", () => {
  uf4.getState().openCluster("x");
  uf4.getState().setSection("gatewayApi");
  uf4.getState().openGateway("infra", "eg");
  const r = uf4.getState().route;
  expect(r).toMatchObject({ name: "cluster", section: "gatewayApi", gateway: { namespace: "infra", name: "eg" } });
  expect(uf4.getState().network.selected).toEqual({ namespace: "infra", name: "eg" });
  expect(uf4.getState().network.topologyLoading).toBe(true);

  uf4.getState().setTopology({ gateway: { listeners: [], policies: [] } as any, routes: [], warnings: [] });
  expect(uf4.getState().network.topology?.routes.length).toBe(0);
  expect(uf4.getState().network.topologyLoading).toBe(false);

  uf4.getState().closeGateway();
  const r2 = uf4.getState().route;
  expect(r2.name === "cluster" && r2.gateway).toBeUndefined();

  uf4.getState().setGateways({ gatewayAPIServed: true, gateways: [{ namespace: "infra", name: "eg", className: "envoy-gateway", accepted: true, programmed: true }] });
  expect(uf4.getState().network.gateways.length).toBe(1);
  expect(uf4.getState().network.served).toBe(true);
});
