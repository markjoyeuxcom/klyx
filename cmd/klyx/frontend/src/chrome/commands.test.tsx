import { describe, it, expect, vi, beforeEach } from "vitest";
import { buildCommands, FleetStore } from "./commands";
import { useFleet } from "../store/fleet";

// Bridges hit the Wails runtime; stub them so run() is observable without it.
vi.mock("../bridge/pods", () => ({
  openPodDetail: vi.fn(),
  listPods: vi.fn(),
}));
vi.mock("../bridge/nodes", () => ({
  openNodeDetail: vi.fn(),
}));
vi.mock("../bridge/helm", () => ({
  openHelmRelease: vi.fn(),
}));
vi.mock("../bridge/gitops", () => ({
  getResourceDetail: vi.fn(),
}));
vi.mock("../bridge/crd", () => ({
  copyText: vi.fn(),
  getInstanceDetail: vi.fn(),
}));
vi.mock("../bridge/forwards", () => ({
  stopForward: vi.fn(),
}));
import { openPodDetail } from "../bridge/pods";
import { stopForward } from "../bridge/forwards";
import { openNodeDetail } from "../bridge/nodes";
import { openHelmRelease } from "../bridge/helm";
import { getResourceDetail } from "../bridge/gitops";
import { copyText, getInstanceDetail } from "../bridge/crd";

// A FleetStore stub with spied actions; callers override only the slices they
// exercise. The real store has dozens of fields, so we cast through unknown.
function makeStore(over: Partial<FleetStore>): FleetStore {
  const base = {
    clusters: [],
    route: { name: "fleet" },
    openFleet: vi.fn(),
    openCluster: vi.fn(),
    openForwards: vi.fn(),
    openSettings: vi.fn(),
    setSection: vi.fn(),
    openResource: vi.fn(),
    openInstance: vi.fn(),
    setBuiltinCategory: vi.fn(),
    setInstanceRiskOnly: vi.fn(),
    toggleWorkloadExpand: vi.fn(),
    expand: vi.fn(),
    toggleArgoExpand: vi.fn(),
    openGateway: vi.fn(),
    selectRoute: vi.fn(),
    requestNavIntent: vi.fn(),
    forwards: [],
    pods: { cluster: null, items: [], namespaces: [] },
    workloads: { cluster: null, items: [], namespaces: [], expanded: [] },
    events: { cluster: null, items: [], namespaces: [] },
    nodes: { cluster: null, items: [] },
    gitops: { cluster: null, resources: [] },
    argo: { cluster: null, apps: [], expanded: [] },
    helm: { cluster: null, releases: [] },
    network: { cluster: null, gateways: [], topology: null },
    instances: { ref: null, rows: [], riskOnly: false },
    instanceDetail: { ref: null, detail: null, loading: false },
  };
  return { ...base, ...over } as unknown as FleetStore;
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  // Reset the real store (used by commands.ts for the live expanded read).
  useFleet.setState({ workloads: { ...useFleet.getState().workloads, expanded: [] } });
});

describe("buildCommands", () => {
  it("lists clusters plus a fleet-overview command", () => {
    const s = makeStore({
      clusters: [
        { name: "dev", env: "DEV", region: "westeurope" } as never,
        { name: "prd", env: "PRD", region: "northeurope" } as never,
      ],
    });
    const cmds = buildCommands(s);
    const clusterCmds = cmds.filter((c) => c.group === "Clusters");
    expect(clusterCmds.map((c) => c.title)).toEqual(["dev", "prd", "Fleet overview", "Settings", "Port-forwards", "Activity log", "Keyboard shortcuts"]);
    expect(clusterCmds[0].hint).toBe("DEV · westeurope");
  });

  it("navigates to the cluster when a cluster command runs", () => {
    const openCluster = vi.fn();
    const s = makeStore({ clusters: [{ name: "dev", env: "DEV", region: "we" } as never], openCluster });
    const cmd = buildCommands(s).find((c) => c.id === "cluster:dev")!;
    cmd.run();
    expect(openCluster).toHaveBeenCalledWith("dev");
  });

  it("omits Sections when not inside a cluster", () => {
    const s = makeStore({ route: { name: "fleet" } });
    expect(buildCommands(s).some((c) => c.group === "Sections")).toBe(false);
  });

  it("includes Sections with cluster hint when inside a cluster", () => {
    const setSection = vi.fn();
    const s = makeStore({ route: { name: "cluster", cluster: "dev", section: "overview" } as never, setSection });
    const sections = buildCommands(s).filter((c) => c.group === "Sections");
    // 13 sections: observability removed; gitops label is "Flux" but id stays
    // "gitops"; Argo CD added as its own section; External Secrets added in
    // M12, and Certificates joined the palette alongside it (M11 gap).
    expect(sections.length).toBe(13);
    expect(sections[0].hint).toBe("dev");
    // gitops section is still reachable by id
    sections.find((c) => c.id === "section:gitops")!.run();
    expect(setSection).toHaveBeenCalledWith("gitops");
    // gitops section title is "Flux" (design principle 8)
    expect(sections.find((c) => c.id === "section:gitops")!.title).toBe("Flux");
    // observability section is gone
    expect(sections.find((c) => c.id === "section:observability")).toBeUndefined();
    // triage-first order keeps Nodes in the operational loop and Network as its own lane.
    const ids = sections.map((c) => c.id.replace("section:", ""));
    expect(ids).toEqual(["overview", "workloads", "pods", "nodes", "events", "gitops",
      "argo", "helm", "certs", "externalsecrets", "gatewayApi", "resources", "crds"]);
  });

  it("omits Pods when the pods slice is empty", () => {
    const s = makeStore({ pods: { cluster: "dev", items: [], namespaces: [] } as never });
    expect(buildCommands(s).some((c) => c.group === "Pods")).toBe(false);
  });

  it("includes Pods when seeded and jumps to detail on run", () => {
    const setSection = vi.fn();
    const s = makeStore({
      setSection,
      pods: {
        cluster: "dev",
        namespaces: ["team"],
        items: [{ namespace: "team", name: "api-1", phase: "Running", rank: "healthy" }],
      } as never,
    });
    const cmds = buildCommands(s);
    const pod = cmds.find((c) => c.id === "pod:team/api-1")!;
    expect(pod.title).toBe("team/api-1");
    pod.run();
    expect(setSection).toHaveBeenCalledWith("pods");
    expect(openPodDetail).toHaveBeenCalledWith("dev", "team", "api-1");
  });

  it("includes a Namespaces command that requests the pods namespace", () => {
    const requestPodsNamespace = vi.fn();
    const s = makeStore({
      requestPodsNamespace,
      pods: { cluster: "dev", namespaces: ["team"], items: [{ namespace: "team", name: "api-1", phase: "Running", rank: "healthy" }] } as never,
    });
    const ns = buildCommands(s).find((c) => c.id === "ns:team")!;
    ns.run();
    // The jump routes through the store; PodsView moves its live subscription.
    // A direct listPods would be dropped by the live sub's stale guard.
    expect(requestPodsNamespace).toHaveBeenCalledWith("dev", "team");
  });

  it("collects namespaces from non-pod slices too", () => {
    const s = makeStore({
      route: { name: "cluster", cluster: "dev", section: "workloads" } as never,
      workloads: {
        cluster: "dev",
        namespaces: ["apps"],
        items: [{ kind: "Deployment", namespace: "apps", name: "api", rank: "healthy" }],
        expanded: [],
      } as never,
      gitops: {
        cluster: "dev",
        resources: [{ kind: "Kustomization", namespace: "flux-system", name: "apps", ready: "Ready", suspended: false }],
      } as never,
    });
    const cmds = buildCommands(s);
    expect(cmds.find((c) => c.id === "ns:apps")?.hint).toContain("workloads");
    expect(cmds.find((c) => c.id === "ns:flux-system")?.hint).toContain("flux");
  });

  it("includes Helm releases that open detail on run", () => {
    const s = makeStore({
      helm: { cluster: "dev", releases: [{ namespace: "team", name: "redis", chart: "redis-18.0.0" }] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "helm:team/redis")!;
    cmd.run();
    expect(openHelmRelease).toHaveBeenCalledWith("dev", "team", "redis");
  });

  it("includes Nodes and opens the node inspector on run", () => {
    const s = makeStore({
      nodes: {
        cluster: "dev",
        items: [{ name: "aks-node-1", ready: true, unschedulable: false, problems: [], roles: ["worker"], version: "v1.30.0" }],
      } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "node:aks-node-1")!;
    expect(cmd.hint).toContain("ready");
    cmd.run();
    expect(openNodeDetail).toHaveBeenCalledWith("dev", "aks-node-1");
  });

  it("includes Workloads and expands the row on run", () => {
    const toggleWorkloadExpand = vi.fn();
    const s = makeStore({
      toggleWorkloadExpand,
      workloads: { items: [{ kind: "Deployment", namespace: "team", name: "api", rank: "healthy" }], expanded: [] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "workload:Deployment/team/api")!;
    expect(cmd.title).toBe("deployment team/api");
    cmd.run();
    expect(toggleWorkloadExpand).toHaveBeenCalledWith("Deployment/team/api");
  });

  it("includes built-in resources and opens their instance list", () => {
    const openResource = vi.fn();
    const setBuiltinCategory = vi.fn();
    const s = makeStore({
      route: { name: "cluster", cluster: "dev", section: "overview" } as never,
      openResource,
      setBuiltinCategory,
    });
    const cmd = buildCommands(s).find((c) => c.id === "builtin:policy/v1/poddisruptionbudgets")!;
    expect(cmd.title).toBe("PodDisruptionBudget");
    cmd.run();
    expect(setBuiltinCategory).toHaveBeenCalledWith("Workloads");
    expect(openResource).toHaveBeenCalledWith(expect.objectContaining({ kind: "PodDisruptionBudget", plural: "poddisruptionbudgets" }));
  });

  it("includes a broken-first action for supported resources", () => {
    const openResource = vi.fn();
    const setInstanceRiskOnly = vi.fn();
    const ref = { group: "", version: "v1", plural: "services", kind: "Service", scope: "Namespaced" };
    const s = makeStore({
      route: { name: "cluster", cluster: "dev", section: "resources", resource: ref } as never,
      openResource,
      setInstanceRiskOnly,
    });
    const cmd = buildCommands(s).find((c) => c.id === "resource-risk:/v1/services")!;
    cmd.run();
    expect(openResource).toHaveBeenCalledWith(ref);
    expect(setInstanceRiskOnly).toHaveBeenCalledWith(true);
  });

  it("includes current detail actions and related object jumps", () => {
    const ref = { group: "cert-manager.io", version: "v1", plural: "certificates", kind: "Certificate", scope: "Namespaced" };
    const openResource = vi.fn();
    const openInstance = vi.fn();
    const s = makeStore({
      route: { name: "cluster", cluster: "dev", section: "resources", resource: ref, instance: { namespace: "apps", name: "web" } } as never,
      openResource,
      openInstance,
      instanceDetail: {
        ref: { namespace: "apps", name: "web" },
        loading: false,
        detail: {
          kind: "Certificate",
          namespace: "apps",
          name: "web",
          created: "",
          labels: {},
          conditions: [],
          events: [],
          yaml: "kind: Certificate\n",
          related: [{ kind: "Secret", namespace: "apps", name: "web-tls", group: "", version: "v1", plural: "secrets", scope: "Namespaced", relation: "certificate secret" }],
        },
      } as never,
    });
    const cmds = buildCommands(s);
    cmds.find((c) => c.id === "resource-copy-yaml:cert-manager.io/v1/certificates/apps/web")!.run();
    expect(copyText).toHaveBeenCalledWith("kind: Certificate\n");
    cmds.find((c) => c.id === "resource-refresh:cert-manager.io/v1/certificates/apps/web")!.run();
    expect(getInstanceDetail).toHaveBeenCalledWith("dev", ref, { namespace: "apps", name: "web" });
    cmds.find((c) => c.id === "resource-related:/v1/secrets/apps/web-tls")!.run();
    expect(openResource).toHaveBeenCalledWith(expect.objectContaining({ kind: "Secret", plural: "secrets" }));
    expect(openInstance).toHaveBeenCalledWith("apps", "web-tls");
  });

  it("includes Flux objects and expands the selected resource", () => {
    const expand = vi.fn();
    const s = makeStore({
      expand,
      gitops: {
        cluster: "dev",
        resources: [{
          kind: "Kustomization",
          namespace: "flux-system",
          name: "apps",
          ready: "Ready",
          suspended: false,
          sourceKind: "GitRepository",
          sourceName: "platform",
        }],
      } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "flux:Kustomization/flux-system/apps")!;
    cmd.run();
    expect(expand).toHaveBeenCalledWith("Kustomization/flux-system/apps");
    expect(getResourceDetail).toHaveBeenCalledWith("dev", "Kustomization", "flux-system", "apps");
  });

  it("includes Argo applications and expands the selected app", () => {
    const toggleArgoExpand = vi.fn();
    const s = makeStore({
      toggleArgoExpand,
      argo: {
        cluster: "dev",
        apps: [{ namespace: "argocd", name: "platform", syncStatus: "Synced", healthStatus: "Healthy", project: "default" }],
        expanded: [],
      } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "argo:argocd/platform")!;
    cmd.run();
    expect(toggleArgoExpand).toHaveBeenCalledWith("argocd/platform");
  });

  it("includes Gateway routes and selects the topology lane", () => {
    const openGateway = vi.fn();
    const selectRoute = vi.fn();
    const s = makeStore({
      route: { name: "cluster", cluster: "dev", section: "gatewayApi" } as never,
      openGateway,
      selectRoute,
      network: {
        cluster: "dev",
        gateways: [],
        topology: {
          gateway: { namespace: "gateway-system", name: "shared", className: "envoy", listeners: [], accepted: true, programmed: true, policies: [] },
          routes: [{
            namespace: "apps",
            name: "api",
            hostnames: ["api.example.test"],
            matches: [],
            accepted: true,
            resolvedRefs: true,
            backends: [],
            services: [],
            pods: { ready: 1, total: 1, unknown: false },
            policies: [],
          }],
        },
      } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "gateway-route:apps/api")!;
    expect(cmd.title).toBe("HTTPRoute apps/api");
    cmd.run();
    expect(openGateway).toHaveBeenCalledWith("gateway-system", "shared");
    expect(selectRoute).toHaveBeenCalledWith("apps/api");
  });

  it("offers theme commands including midnight", () => {
    const s = makeStore({});
    const cmds = buildCommands(s);
    expect(cmds.some((c) => c.id === "theme:toggle" && c.title === "cycle theme")).toBe(true);
    expect(cmds.some((c) => c.id === "theme:set:crimson" && c.title === "theme: crimson")).toBe(true);
    const midnight = cmds.find((c) => c.id === "theme:set:midnight")!;
    expect(midnight.title).toBe("theme: midnight");
    midnight.run();
    expect(document.documentElement.getAttribute("data-theme")).toBe("midnight");
    expect(localStorage.getItem("klyx-theme")).toBe("midnight");
  });
});

describe("action verbs", () => {
  it("drain verb navigates to nodes and parks a drain intent - never calls a bridge", () => {
    const requestNavIntent = vi.fn();
    const setSection = vi.fn();
    const openCluster = vi.fn();
    const s = makeStore({
      requestNavIntent, setSection, openCluster,
      nodes: { cluster: "dev", items: [{ name: "aks-sys-1", ready: true, unschedulable: false, problems: [], roles: ["agent"], version: "v1.31.0" }] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "verb:drain:dev/aks-sys-1")!;
    expect(cmd.title).toBe("drain node aks-sys-1");
    expect(cmd.group).toBe("Actions");
    cmd.run();
    expect(openCluster).toHaveBeenCalledWith("dev");
    expect(setSection).toHaveBeenCalledWith("nodes");
    expect(requestNavIntent).toHaveBeenCalledWith({ kind: "drain-node", cluster: "dev", node: "aks-sys-1" });
    expect(openNodeDetail).not.toHaveBeenCalled();
  });

  it("cordon verb flips to uncordon for an already-cordoned node", () => {
    const requestNavIntent = vi.fn();
    const s = makeStore({
      requestNavIntent,
      nodes: { cluster: "dev", items: [{ name: "n1", ready: true, unschedulable: true, problems: [], roles: [], version: "" }] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "verb:cordon:dev/n1")!;
    expect(cmd.title).toBe("uncordon node n1");
    cmd.run();
    expect(requestNavIntent).toHaveBeenCalledWith({ kind: "cordon-node", cluster: "dev", node: "n1", cordon: false });
  });

  it("scale verb exists for Deployments but not DaemonSets", () => {
    const s = makeStore({
      workloads: { cluster: "dev", items: [
        { kind: "Deployment", namespace: "web", name: "api", rank: "healthy", ready: 2, desired: 2 },
        { kind: "DaemonSet", namespace: "kube-system", name: "cilium", rank: "healthy", ready: 3, desired: 3 },
      ], expanded: [] } as never,
    });
    const cmds = buildCommands(s);
    expect(cmds.some((c) => c.id === "verb:scale:dev/Deployment/web/api")).toBe(true);
    expect(cmds.some((c) => c.id === "verb:scale:dev/DaemonSet/kube-system/cilium")).toBe(false);
    expect(cmds.some((c) => c.id === "verb:restart:dev/DaemonSet/kube-system/cilium")).toBe(true);
  });

  it("suspend verb reads resume for suspended flux resources", () => {
    const requestNavIntent = vi.fn();
    const s = makeStore({
      requestNavIntent,
      gitops: { cluster: "dev", resources: [
        { kind: "Kustomization", namespace: "flux-system", name: "apps", ready: "True", suspended: true, sourceKind: "", sourceName: "" },
      ] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "verb:suspend:dev/Kustomization/flux-system/apps")!;
    expect(cmd.title).toBe("resume kustomization flux-system/apps");
    cmd.run();
    expect(requestNavIntent).toHaveBeenCalledWith({ kind: "suspend-flux", cluster: "dev", fluxKind: "Kustomization", namespace: "flux-system", name: "apps", suspend: false });
  });

  it("argo sync verb parks a sync intent for the composer", () => {
    const requestNavIntent = vi.fn();
    const s = makeStore({
      requestNavIntent,
      argo: { cluster: "nelli", apps: [
        { namespace: "argocd", name: "media", syncStatus: "OutOfSync", healthStatus: "Healthy", project: "default" },
      ], expanded: [] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "verb:argo-sync:nelli/argocd/media")!;
    expect(cmd.hint).toContain("sync composer");
    cmd.run();
    expect(requestNavIntent).toHaveBeenCalledWith({ kind: "sync-argo", cluster: "nelli", namespace: "argocd", name: "media" });
  });

  it("pod logs verb parks a logs intent", () => {
    const requestNavIntent = vi.fn();
    const s = makeStore({
      requestNavIntent,
      pods: { cluster: "dev", items: [{ namespace: "web", name: "api-1", phase: "Running", rank: "healthy" }], namespaces: [] } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "verb:logs:dev/web/api-1")!;
    cmd.run();
    expect(requestNavIntent).toHaveBeenCalledWith({ kind: "logs-pod", cluster: "dev", namespace: "web", name: "api-1" });
    expect(openPodDetail).not.toHaveBeenCalled();
  });

  it("stop-forward verb acts directly (the one confirmed-safe direct verb)", () => {
    const s = makeStore({
      forwards: [{ id: "f1", cluster: "dev", namespace: "web", targetKind: "Service", targetName: "api", localPort: 8080, targetPort: 80, startedUnix: 0, status: "active" }] as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "verb:stop-forward:f1")!;
    expect(cmd.title).toBe("stop forward web/api :8080");
    cmd.run();
    expect(stopForward).toHaveBeenCalledWith("f1");
  });
});
