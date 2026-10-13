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
  it("Clusters lists only clusters; app destinations are Application, never Clusters", () => {
    const s = makeStore({
      clusters: [
        { name: "dev", env: "DEV", region: "westeurope", state: "Synced" } as never,
        { name: "prd", env: "PRD", region: "northeurope", state: "Synced", protected: true } as never,
      ],
    });
    const cmds = buildCommands(s);
    const clusterCmds = cmds.filter((c) => c.group === "Clusters");
    expect(clusterCmds.map((c) => c.title)).toEqual(["dev", "prd"]);
    expect(clusterCmds[0].hint).toBe("DEV · westeurope");
    expect(clusterCmds[1].hint).toBe("PRD · northeurope · prd lock");
    const app = cmds.filter((c) => c.group === "Application").map((c) => c.title);
    expect(app).toEqual(expect.arrayContaining(["Fleet board", "Port-forwards", "Activity log", "Settings", "Terminal", "Keyboard shortcuts"]));
  });

  it("navigates to the cluster when a cluster command runs", () => {
    const openCluster = vi.fn();
    const s = makeStore({ clusters: [{ name: "dev", env: "DEV", region: "we", state: "Synced" } as never], openCluster });
    const cmd = buildCommands(s).find((c) => c.id === "cluster:dev")!;
    cmd.run();
    expect(openCluster).toHaveBeenCalledWith("dev");
  });
  it("omits Pods when the pods slice is empty", () => {
    const s = makeStore({ pods: { cluster: "dev", items: [], namespaces: [] } as never });
    expect(buildCommands(s).some((c) => c.id.startsWith("pod:"))).toBe(false);
  });

  it("includes Pods when seeded and jumps to detail on run", () => {
    const setSection = vi.fn();
    const s = makeStore({
      setSection,
      pods: {
        cluster: "dev",
        namespaces: ["team"],
        items: [{ namespace: "team", name: "api-1", uid: "uid-api-1", phase: "Running", rank: "healthy" }],
      } as never,
    });
    const cmds = buildCommands(s);
    const pod = cmds.find((c) => c.id === "pod:team/api-1")!;
    expect(pod.title).toBe("team/api-1");
    pod.run();
    expect(setSection).toHaveBeenCalledWith("pods");
    expect(openPodDetail).toHaveBeenCalledWith("dev", "team", "api-1", "uid-api-1");
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
        items: [{ name: "aks-node-1", uid: "uid-aks-node-1", ready: true, unschedulable: false, problems: [], roles: ["worker"], version: "v1.30.0" }],
      } as never,
    });
    const cmd = buildCommands(s).find((c) => c.id === "node:aks-node-1")!;
    expect(cmd.hint).toContain("ready");
    cmd.run();
    // the node OBJECT the row names (design §5b)
    expect(openNodeDetail).toHaveBeenCalledWith("dev", "aks-node-1", "uid-aks-node-1");
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

  it("raw resource views open their instance list and keep the built-in category", () => {
    const openResource = vi.fn();
    const setBuiltinCategory = vi.fn();
    const s = makeStore({
      clusters: [{ name: "dev", state: "Synced" } as never],
      route: { name: "cluster", cluster: "dev", section: "overview" } as never,
      openResource,
      setBuiltinCategory,
    });
    const cmd = buildCommands(s).find((c) => c.id === "resource:res:policy/v1/poddisruptionbudgets")!;
    expect(cmd.title).toBe("PodDisruptionBudgets");
    expect(cmd.group).toBe("Resources");
    expect(cmd.hint).toContain("resource list");
    expect(cmd.hint).toContain("PodDisruptionBudget");
    cmd.run();
    expect(setBuiltinCategory).toHaveBeenCalledWith("Workloads");
    expect(openResource).toHaveBeenCalledWith(expect.objectContaining({ kind: "PodDisruptionBudget", plural: "poddisruptionbudgets" }));
  });

  it("built-in kinds the registry does not list stay reachable (raw Nodes)", () => {
    const s = makeStore({ clusters: [{ name: "dev", state: "Synced" } as never], route: { name: "cluster", cluster: "dev", section: "overview" } as never });
    expect(buildCommands(s).find((c) => c.id === "resource:res:core/v1/nodes")?.title).toBe("Node");
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
    const requestNavIntent = vi.fn();
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
          related: [
            { kind: "Secret", namespace: "apps", name: "web-tls", group: "", version: "v1", plural: "secrets", scope: "Namespaced", relation: "certificate secret" },
            { kind: "Pod", namespace: "apps", name: "web-0", group: "", version: "v1", plural: "pods", scope: "Namespaced", relation: "backs" },
          ],
        },
      } as never,
      requestNavIntent,
    });
    const cmds = buildCommands(s);
    cmds.find((c) => c.id === "resource-copy-yaml:cert-manager.io/v1/certificates/apps/web")!.run();
    expect(copyText).toHaveBeenCalledWith("kind: Certificate\n");
    cmds.find((c) => c.id === "resource-refresh:cert-manager.io/v1/certificates/apps/web")!.run();
    expect(getInstanceDetail).toHaveBeenCalledWith("dev", ref, { namespace: "apps", name: "web" });
    cmds.find((c) => c.id === "resource-related:/v1/secrets/apps/web-tls")!.run();
    expect(openResource).toHaveBeenCalledWith(expect.objectContaining({ kind: "Secret", plural: "secrets" }));
    expect(openInstance).toHaveBeenCalledWith("apps", "web-tls");
    // a related Pod is resolved by the Pods lens to its row's object (§5b)
    cmds.find((c) => c.id === "resource-related:/v1/pods/apps/web-0")!.run();
    expect(requestNavIntent).toHaveBeenCalledWith({ kind: "select-pod", cluster: "dev", namespace: "apps", name: "web-0" });
    expect(openPodDetail).not.toHaveBeenCalled();
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

describe("views from every route (redesign step 7c)", () => {
  const dev = { name: "dev", state: "Synced", fluxPresent: true } as never;
  const prd = { name: "aks-prd-weu-platform-01", state: "Synced", fluxPresent: false } as never;
  const view = (cmds: ReturnType<typeof buildCommands>, id: string) => cmds.find((c) => c.id === id)!;

  it("from Fleet with one eligible cluster: names it and opens it", () => {
    const openCluster = vi.fn();
    const setSection = vi.fn();
    const s = makeStore({ clusters: [dev], route: { name: "fleet" }, openCluster, setSection });
    const events = view(buildCommands(s), "view:events");
    expect(events.group).toBe("Views");
    expect(events.hint).toContain("dev");
    events.run();
    expect(openCluster).toHaveBeenCalledWith("dev");
    expect(setSection).toHaveBeenCalledWith("events");
  });

  it("from Fleet with several eligible clusters: a Choose cluster step, never a default", () => {
    const openCluster = vi.fn();
    const openWorkloads = vi.fn();
    const s = makeStore({ clusters: [dev, prd], route: { name: "fleet" }, openCluster, openWorkloads });
    const all = view(buildCommands(s), "view:workloads");
    expect(all.next?.title).toBe("Choose cluster for All workloads");
    expect(all.next?.options.map((o) => o.title)).toEqual(["dev", "aks-prd-weu-platform-01"]);
    all.run();
    expect(openCluster).not.toHaveBeenCalled();
    all.next!.options[1].run();
    expect(openCluster).toHaveBeenCalledWith("aks-prd-weu-platform-01");
    expect(openWorkloads).toHaveBeenCalled();
  });

  it("capability limits eligibility: Flux from Fleet goes to the one cluster that has it", () => {
    const s = makeStore({ clusters: [dev, prd], route: { name: "fleet" } });
    const flux = view(buildCommands(s), "view:flux");
    expect(flux.next).toBeUndefined();
    expect(flux.hint).toContain("dev");
  });

  it("no clusters: the view is listed, disabled, with Connect cluster", () => {
    const openSettings = vi.fn();
    const s = makeStore({ clusters: [], route: { name: "settings" }, openSettings });
    const pods = view(buildCommands(s), "view:pods");
    expect(pods.disabled).toBe("no clusters in the fleet");
    expect(pods.action?.label).toBe("Connect cluster");
    pods.action!.run();
    expect(openSettings).toHaveBeenCalled();
  });

  it("a connecting cluster is 'checking capability', not 'no matches'", () => {
    const s = makeStore({ clusters: [{ name: "dev", state: "Connecting" } as never], route: { name: "fleet" } });
    expect(view(buildCommands(s), "view:flux").disabled).toBe("checking capability…");
  });

  it("inside a cluster a view stays on it; one missing here offers the others explicitly", () => {
    const openCluster = vi.fn();
    const s = makeStore({ clusters: [dev, prd], route: { name: "cluster", cluster: "aks-prd-weu-platform-01", section: "overview" } as never, openCluster });
    const cmds = buildCommands(s);
    view(cmds, "view:events").run();
    expect(openCluster).not.toHaveBeenCalled();
    const flux = view(cmds, "view:flux");
    expect(flux.hint).toContain("not installed on aks-prd-weu-platform-01");
    expect(flux.next?.options.map((o) => o.title)).toEqual(["dev"]);
  });

  it("cluster-qualified results exist for other eligible clusters only", () => {
    const s = makeStore({ clusters: [dev, prd], route: { name: "cluster", cluster: "dev", section: "overview" } as never });
    const q = buildCommands(s).filter((c) => c.qualifier).map((c) => c.id);
    expect(q).toContain("view:events@aks-prd-weu-platform-01");
    expect(q).not.toContain("view:events@dev");
    expect(q).not.toContain("view:flux@aks-prd-weu-platform-01");
  });
});
