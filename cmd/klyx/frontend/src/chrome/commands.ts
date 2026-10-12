// Command index for the ⌘K palette. buildCommands flattens the *currently
// loaded* store state into a flat list of runnable commands. It indexes only
// what is already in memory — no fetching — so the palette is honest about
// "showing loaded data". Each command's run() performs the navigation (and any
// jump-to-resource bridge call) as a side effect.

import { useFleet, ClusterSection } from "../store/fleet";
import type { ClusterDTO, GatewayRefDTO, NavIntent, ResourceRef, RouteNodeDTO, WorkloadKind } from "../store/fleet";
import { bestSub, useHealth } from "../store/health";
import { openPodDetail } from "../bridge/pods";
import { openNodeDetail } from "../bridge/nodes";
import { openHelmRelease } from "../bridge/helm";
import { getResourceDetail } from "../bridge/gitops";
import { copyText, getInstanceDetail } from "../bridge/crd";
import { stopForward } from "../bridge/forwards";
import { openShortcutsOverlay } from "./ShortcutsOverlay";
import { setThemeChoice, THEMES, toggleTheme } from "../theme/ThemeProvider";
import { BUILTIN_CATALOG } from "../cluster/builtins";
import { CAP_STATE_TEXT, NAV_GROUPS, connected, destState, navigate, type Destination, type NavGroup } from "../nav/registry";
import { openTerminal } from "../bridge/configsvc";
import { zoomIn, zoomOut, zoomPercent, zoomReset } from "../app/zoom";
import { supportsRiskFilter } from "../cluster/resourceRisk";

export type FleetStore = ReturnType<typeof useFleet.getState>;

// Palette groups (spec §4), in display order.
export type CommandGroup = "Application" | "Clusters" | "Views" | "Resources" | "Objects" | "Actions";

export type Command = {
  id: string;
  group: CommandGroup;
  title: string;
  hint?: string;
  run: () => void;
  // Not activatable, with the reason shown in place of the hint. An optional
  // action (Connect cluster) is what Enter does instead.
  disabled?: string;
  action?: { label: string; run: () => void };
  // A second palette step: activation lists these instead of running.
  next?: { title: string; options: Command[] };
  // Cluster-qualified result: offered only when the query names this cluster.
  qualifier?: string;
  // Re-checked against live state at activation: a reason means the target
  // became unavailable while the palette was open - explain, run nothing.
  guard?: () => string | null;
  // Searchable aliases (registry keywords), matched like hints.
  keywords?: string[];
  // Every eligible cluster's option, whether or not activation opens a step:
  // an open Choose cluster step rebuilds from these when eligibility shrinks
  // to one (the parent then runs directly) instead of emptying.
  choices?: Command[];
};

function openSection(s: FleetStore, section: ClusterSection, targetCluster?: string | null): void {
  if (targetCluster && (s.route.name !== "cluster" || s.route.cluster !== targetCluster)) {
    s.openCluster(targetCluster);
  }
  s.setSection(section);
}

// Verb commands navigate to the owning section and park a one-shot NavIntent;
// the view consumes it once its data is loaded and opens its existing confirm
// or operation composer. The palette never fires a day-2 API call directly -
// protected-cluster arming and danger confirms are never bypassed.
// lockMark flags verbs aimed at a prd-locked cluster right in the palette
// hint, so the typed-confirmation is never a surprise.
function lockMark(s: FleetStore, cluster: string): string {
  return s.clusters.find((c) => c.name === cluster)?.protected ? " · prd lock" : "";
}

function openWithIntent(s: FleetStore, section: ClusterSection, targetCluster: string, intent: NavIntent): void {
  openSection(s, section, targetCluster);
  s.requestNavIntent(intent);
}

function fluxKey(r: { kind: string; namespace: string; name: string }): string {
  return `${r.kind}/${r.namespace}/${r.name}`;
}

function nsName(ns: string, name: string): string {
  return ns ? `${ns}/${name}` : name;
}

function apiHint(ref: ResourceRef): string {
  const api = ref.group ? `${ref.group}/${ref.version}` : ref.version;
  return `${api} · ${ref.plural} · ${ref.scope.toLowerCase()}`;
}

function routeKey(r: RouteNodeDTO): string {
  return `${r.namespace}/${r.name}`;
}

function routeHint(r: RouteNodeDTO): string | undefined {
  const status = !r.accepted ? "not accepted" : !r.resolvedRefs ? "unresolved refs" : "";
  const hostnames = r.hostnames.filter(Boolean).join(", ");
  const backends = r.backends.map((b) => nsName(b.namespace, b.name)).join(", ");
  return [status, hostnames || backends].filter(Boolean).join(" · ") || undefined;
}

function openRelatedObject(s: FleetStore, cluster: string, ref: { kind: string; namespace: string; name: string; group: string; version: string; plural: string; scope: string }): void {
  if (ref.kind === "Pod") {
    // the Pods lens resolves the name to its row's object (design §5b)
    openSection(s, "pods", cluster);
    s.requestNavIntent({ kind: "select-pod", cluster, namespace: ref.namespace, name: ref.name });
    return;
  }
  openSection(s, "resources", cluster);
  s.openResource({ group: ref.group, version: ref.version, plural: ref.plural, kind: ref.kind, scope: ref.scope });
  s.openInstance(ref.namespace, ref.name);
}

export function buildCommands(s: FleetStore): Command[] {
  const cmds: Command[] = [];
  const route = s.route;
  const cluster = route.name === "cluster" ? route.cluster : null;

  // --- Application -------------------------------------------------------------
  cmds.push({ id: "fleet", group: "Application", title: "Fleet board", hint: "every cluster, attention first", run: () => s.openFleet() });
  cmds.push({ id: "fleet:promotion", group: "Application", title: "Promotion tracker", hint: "applied GitOps revisions per cluster", run: () => s.openFleet("promotion") });
  cmds.push({ id: "fleet:expiry", group: "Application", title: "Expiry horizon", hint: "certificates and CAs fleet-wide, soonest first", run: () => s.openFleet("expiry") });
  cmds.push({ id: "forwards", group: "Application", title: "Port-forwards", hint: (s.forwards?.length ?? 0) > 0 ? `${s.forwards.length} active` : "none active", run: () => s.openForwards() });
  cmds.push({ id: "activity", group: "Application", title: "Activity log", hint: "recent action outcomes", run: () => s.openActivity() });
  cmds.push({ id: "settings", group: "Application", title: "Settings", hint: "fleet config · kubeconfig contexts", run: () => s.openSettings() });
  cmds.push({ id: "terminal", group: "Application", title: "Terminal", hint: "a shell with this fleet's kubeconfig", run: () => void openTerminal() });
  cmds.push({ id: "zoom:in", group: "Application", title: "Zoom in", hint: `now ${zoomPercent()} · ⌘=`, run: () => zoomIn() });
  cmds.push({ id: "zoom:out", group: "Application", title: "Zoom out", hint: `now ${zoomPercent()} · ⌘-`, run: () => zoomOut() });
  cmds.push({ id: "zoom:reset", group: "Application", title: "Actual size", hint: `zoom ${zoomPercent()} · ⌘0`, run: () => zoomReset() });
  // Stopping a forward is the one palette verb that acts directly - it is how
  // the forwards popover already behaves (low-risk, instantly re-creatable).
  for (const f of s.forwards ?? []) {
    cmds.push({
      id: `verb:stop-forward:${f.id}`,
      group: "Actions",
      title: `stop forward ${f.namespace}/${f.targetName} :${f.localPort}`,
      hint: `${f.cluster} · ${f.status}`,
      run: () => void stopForward(f.id),
    });
  }

  // --- Clusters ------------------------------------------------------------------
  for (const c of s.clusters) {
    const hintParts = [c.env, c.region, c.protected ? "prd lock" : ""].filter(Boolean);
    cmds.push({
      id: `cluster:${c.name}`,
      group: "Clusters",
      title: c.name,
      hint: hintParts.join(" · ") || undefined,
      run: () => s.openCluster(c.name),
    });
  }

  // --- Views and Resources (the navigation registry) -------------------------------
  // Searchable from every route, independently of loaded object caches. A
  // destination resolves its clusters explicitly: inside a cluster it stays
  // there; elsewhere one eligible cluster is named and opened, several open a
  // Choose cluster step, none is shown disabled with the reason.
  for (const g of NAV_GROUPS) {
    for (const d of g.destinations) cmds.push(...destinationCommands(s, g, d, cluster));
  }
  // Built-in kinds the registry does not list (e.g. raw Nodes) stay reachable.
  const listed = new Set(NAV_GROUPS.flatMap((g) => g.destinations).flatMap((d) => (d.target.kind === "resource" ? [refKey(d.target.ref)] : [])));
  for (const cat of BUILTIN_CATALOG) {
    for (const entry of cat.entries) {
      if (entry.kind !== "gvr" || listed.has(refKey(entry.ref))) continue;
      const ref = entry.ref;
      const d: Destination = { id: `res:${ref.group || "core"}/${ref.version}/${ref.plural}`, label: ref.kind, type: "resource", target: { kind: "resource", ref } };
      cmds.push(...destinationCommands(s, { id: "builtin", label: cat.label, destinations: [d] }, d, cluster));
    }
  }

  // --- Current resource/detail actions ---------------------------------------
  if (cluster && route.name === "cluster" && route.resource) {
    const ref = route.resource;
    if (supportsRiskFilter(ref)) {
      cmds.push({
        id: `resource-risk:${ref.group}/${ref.version}/${ref.plural}`,
        group: "Actions",
        title: `show ${ref.kind} needing attention`,
        hint: apiHint(ref),
        run: () => {
          openSection(s, "resources", cluster);
          s.openResource(ref);
          s.setInstanceRiskOnly(true);
        },
      });
    }

    if (route.instance && s.instanceDetail.detail) {
      const instance = route.instance;
      const detail = s.instanceDetail.detail;
      const target = `${instance.namespace ? `${instance.namespace}/` : ""}${instance.name}`;
      cmds.push({
        id: `resource-copy-yaml:${ref.group}/${ref.version}/${ref.plural}/${target}`,
        group: "Actions",
        title: `copy YAML for ${target}`,
        hint: ref.kind,
        run: () => {
          void copyText(detail.yaml);
        },
      });
      cmds.push({
        id: `resource-refresh:${ref.group}/${ref.version}/${ref.plural}/${target}`,
        group: "Actions",
        title: `refresh ${target}`,
        hint: ref.kind,
        run: () => {
          void getInstanceDetail(cluster, ref, instance);
        },
      });
      for (const related of detail.related ?? []) {
        const relatedTarget = `${related.namespace ? `${related.namespace}/` : ""}${related.name}`;
        cmds.push({
          id: `resource-related:${related.group}/${related.version}/${related.plural}/${relatedTarget}`,
          group: "Objects",
          title: `${related.kind} ${relatedTarget}`,
          hint: related.relation || target,
          run: () => openRelatedObject(s, cluster, related),
        });
      }
    }
  }

  // --- Pods (when the slice is loaded) ---------------------------------------
  if (s.pods.items.length > 0 && s.pods.cluster) {
    const podCluster = s.pods.cluster;
    for (const p of s.pods.items) {
      cmds.push({
        id: `pod:${p.namespace}/${p.name}`,
        group: "Objects",
        title: `${p.namespace}/${p.name}`,
        hint: `Pod · ${p.phase || p.rank} · ${podCluster}`,
        run: () => {
          openSection(s, "pods", podCluster);
          void openPodDetail(podCluster, p.namespace, p.name, p.uid ?? "");
        },
      });
      cmds.push({
        id: `verb:logs:${podCluster}/${p.namespace}/${p.name}`,
        group: "Actions",
        title: `logs ${p.namespace}/${p.name}`,
        hint: podCluster,
        run: () => openWithIntent(s, "pods", podCluster, { kind: "logs-pod", cluster: podCluster, namespace: p.namespace, name: p.name }),
      });
    }
  }

  // --- Nodes (when loaded) ----------------------------------------------------
  if (s.nodes.items.length > 0 && s.nodes.cluster) {
    const nodeCluster = s.nodes.cluster;
    for (const n of s.nodes.items) {
      const health = !n.ready ? "not ready" : n.unschedulable ? "cordoned" : n.problems.length > 0 ? n.problems.join(", ") : "ready";
      const roles = n.roles.length > 0 ? n.roles.join(", ") : "no role";
      cmds.push({
        id: `node:${n.name}`,
        group: "Objects",
        title: n.name,
        hint: `Node · ${health} · ${roles} · ${n.version}`,
        run: () => {
          openSection(s, "nodes", nodeCluster);
          void openNodeDetail(nodeCluster, n.name, n.uid ?? "");
        },
      });
      cmds.push({
        id: `verb:drain:${nodeCluster}/${n.name}`,
        group: "Actions",
        title: `drain node ${n.name}`,
        hint: `${nodeCluster} · opens the drain composer${lockMark(s, nodeCluster)}`,
        run: () => openWithIntent(s, "nodes", nodeCluster, { kind: "drain-node", cluster: nodeCluster, node: n.name }),
      });
      cmds.push({
        id: `verb:cordon:${nodeCluster}/${n.name}`,
        group: "Actions",
        title: n.unschedulable ? `uncordon node ${n.name}` : `cordon node ${n.name}`,
        hint: `${nodeCluster}${lockMark(s, nodeCluster)}`,
        run: () => openWithIntent(s, "nodes", nodeCluster, { kind: "cordon-node", cluster: nodeCluster, node: n.name, cordon: !n.unschedulable }),
      });
    }
  }

  // --- Workloads (when loaded) -----------------------------------------------
  if (s.workloads.items.length > 0) {
    const workloadCluster = s.workloads.cluster ?? cluster;
    for (const w of s.workloads.items) {
      const key = fluxKey(w);
      cmds.push({
        id: `workload:${key}`,
        group: "Objects",
        title: `${w.kind.toLowerCase()} ${w.namespace}/${w.name}`,
        hint: w.rank,
        run: () => {
          openSection(s, "workloads", workloadCluster);
          // Expand the target row if it isn't already (toggle is a flip).
          if (!useFleet.getState().workloads.expanded.includes(key)) {
            s.toggleWorkloadExpand(key);
          }
        },
      });
      if (workloadCluster) {
        cmds.push({
          id: `verb:restart:${workloadCluster}/${key}`,
          group: "Actions",
          title: `restart ${w.kind.toLowerCase()} ${w.namespace}/${w.name}`,
          hint: `${workloadCluster}${lockMark(s, workloadCluster)}`,
          run: () => openWithIntent(s, "workloads", workloadCluster, { kind: "restart-workload", cluster: workloadCluster, workloadKind: w.kind as WorkloadKind, namespace: w.namespace, name: w.name }),
        });
        // DaemonSets have no replica count - scale does not apply.
        if (w.kind !== "DaemonSet") {
          cmds.push({
            id: `verb:scale:${workloadCluster}/${key}`,
            group: "Actions",
            title: `scale ${w.kind.toLowerCase()} ${w.namespace}/${w.name}`,
            hint: `${workloadCluster} · ${w.ready}/${w.desired} ready${lockMark(s, workloadCluster)}`,
            run: () => openWithIntent(s, "workloads", workloadCluster, { kind: "scale-workload", cluster: workloadCluster, workloadKind: w.kind as WorkloadKind, namespace: w.namespace, name: w.name }),
          });
        }
      }
    }
  }

  // --- Helm releases (when loaded) -------------------------------------------
  if (s.helm.releases.length > 0 && s.helm.cluster) {
    const helmCluster = s.helm.cluster;
    for (const r of s.helm.releases) {
      cmds.push({
        id: `helm:${r.namespace}/${r.name}`,
        group: "Objects",
        title: `${r.namespace}/${r.name}`,
        hint: `Helm release · ${r.chart}`,
        run: () => {
          openSection(s, "helm", helmCluster);
          void openHelmRelease(helmCluster, r.namespace, r.name);
        },
      });
    }
  }

  // --- Flux resources (when loaded) ------------------------------------------
  if (s.gitops.resources.length > 0 && s.gitops.cluster) {
    const gitopsCluster = s.gitops.cluster;
    for (const r of s.gitops.resources) {
      const key = fluxKey(r);
      cmds.push({
        id: `flux:${key}`,
        group: "Objects",
        title: `${r.kind.toLowerCase()} ${r.namespace}/${r.name}`,
        hint: [r.ready, r.suspended ? "suspended" : "", r.sourceKind && r.sourceName ? `${r.sourceKind} ${r.sourceName}` : ""].filter(Boolean).join(" · "),
        run: () => {
          openSection(s, "gitops", gitopsCluster);
          s.expand(key);
          void getResourceDetail(gitopsCluster, r.kind, r.namespace, r.name);
        },
      });
      cmds.push({
        id: `verb:reconcile:${gitopsCluster}/${key}`,
        group: "Actions",
        title: `reconcile ${r.kind.toLowerCase()} ${r.namespace}/${r.name}`,
        hint: `${gitopsCluster}${lockMark(s, gitopsCluster)}`,
        run: () => openWithIntent(s, "gitops", gitopsCluster, { kind: "reconcile-flux", cluster: gitopsCluster, fluxKind: r.kind, namespace: r.namespace, name: r.name }),
      });
      cmds.push({
        id: `verb:suspend:${gitopsCluster}/${key}`,
        group: "Actions",
        title: r.suspended ? `resume ${r.kind.toLowerCase()} ${r.namespace}/${r.name}` : `suspend ${r.kind.toLowerCase()} ${r.namespace}/${r.name}`,
        hint: `${gitopsCluster}${lockMark(s, gitopsCluster)}`,
        run: () => openWithIntent(s, "gitops", gitopsCluster, { kind: "suspend-flux", cluster: gitopsCluster, fluxKind: r.kind, namespace: r.namespace, name: r.name, suspend: !r.suspended }),
      });
    }
  }

  // --- Argo applications (when loaded) ---------------------------------------
  if (s.argo.apps.length > 0 && s.argo.cluster) {
    const argoCluster = s.argo.cluster;
    for (const a of s.argo.apps) {
      const key = `${a.namespace}/${a.name}`;
      cmds.push({
        id: `argo:${key}`,
        group: "Objects",
        title: `${a.namespace}/${a.name}`,
        hint: ["Argo CD application", a.syncStatus, a.healthStatus, a.project].filter(Boolean).join(" · "),
        run: () => {
          openSection(s, "argo", argoCluster);
          if (!useFleet.getState().argo.expanded.includes(key)) {
            s.toggleArgoExpand(key);
          }
        },
      });
      cmds.push({
        id: `verb:argo-sync:${argoCluster}/${key}`,
        group: "Actions",
        title: `sync app ${a.namespace}/${a.name}`,
        hint: `${argoCluster} · opens the sync composer${lockMark(s, argoCluster)}`,
        run: () => openWithIntent(s, "argo", argoCluster, { kind: "sync-argo", cluster: argoCluster, namespace: a.namespace, name: a.name }),
      });
      cmds.push({
        id: `verb:argo-refresh:${argoCluster}/${key}`,
        group: "Actions",
        title: `refresh app ${a.namespace}/${a.name}`,
        hint: argoCluster,
        run: () => openWithIntent(s, "argo", argoCluster, { kind: "refresh-argo", cluster: argoCluster, namespace: a.namespace, name: a.name }),
      });
    }
  }

  // --- Gateway API (when loaded) ---------------------------------------------
  // The Gateways are the Observer's shared rows (1c slice 3 §5.6): indexed
  // while a subscription holds this cluster's current ones - never a
  // previous cluster's or incarnation's (bestSub fences both).
  const gatewayRows = cluster ? (bestSub(useHealth.getState(), cluster, "gateway", "gateways")?.rows as GatewayRefDTO[] | null | undefined) : null;
  if (cluster && gatewayRows && gatewayRows.length > 0) {
    for (const g of gatewayRows) {
      cmds.push({
        id: `gateway:${g.namespace}/${g.name}`,
        group: "Objects",
        title: `${g.namespace}/${g.name}`,
        hint: ["Gateway", g.className, g.accepted ? "accepted" : "not accepted", g.programmed ? "programmed" : "pending"].filter(Boolean).join(" · "),
        run: () => {
          openSection(s, "gatewayApi", cluster);
          s.openGateway(g.namespace, g.name);
        },
      });
    }
  }

  if (cluster && s.network.cluster === cluster && s.network.topology?.routes?.length) {
    const gw = s.network.topology.gateway;
    for (const r of s.network.topology.routes) {
      const key = routeKey(r);
      cmds.push({
        id: `gateway-route:${key}`,
        group: "Objects",
        title: `HTTPRoute ${key}`,
        hint: routeHint(r),
        run: () => {
          openSection(s, "gatewayApi", cluster);
          s.openGateway(gw.namespace, gw.name);
          s.selectRoute(key);
        },
      });
    }
  }

  // --- Namespaces (from any loaded cluster object slice) ---------------------
  const namespaces = new Map<string, { cluster: string; name: string; sources: Set<string> }>();
  const addNamespace = (targetCluster: string | null | undefined, ns: string | undefined, source: string) => {
    const name = (ns ?? "").trim();
    const clusterName = targetCluster || cluster;
    if (!clusterName || !name) return;
    const key = `${clusterName}/${name}`;
    const existing = namespaces.get(key) ?? { cluster: clusterName, name, sources: new Set<string>() };
    existing.sources.add(source);
    namespaces.set(key, existing);
  };

  (s.pods.namespaces ?? []).forEach((ns) => addNamespace(s.pods.cluster, ns, "pods"));
  (s.pods.items ?? []).forEach((p) => addNamespace(s.pods.cluster, p.namespace, "pods"));
  (s.workloads.namespaces ?? []).forEach((ns) => addNamespace(s.workloads.cluster, ns, "workloads"));
  (s.workloads.items ?? []).forEach((w) => addNamespace(s.workloads.cluster, w.namespace, "workloads"));
  (s.events.namespaces ?? []).forEach((ns) => addNamespace(s.events.cluster, ns, "events"));
  (s.events.items ?? []).forEach((e) => addNamespace(s.events.cluster, e.namespace, "events"));
  (s.helm.releases ?? []).forEach((r) => addNamespace(s.helm.cluster, r.namespace, "helm"));
  (s.gitops.resources ?? []).forEach((r) => addNamespace(s.gitops.cluster, r.namespace, "flux"));
  (s.argo.apps ?? []).forEach((a) => {
    addNamespace(s.argo.cluster, a.namespace, "argo");
    addNamespace(s.argo.cluster, a.destNamespace, "argo destination");
  });
  s.network.topology?.routes?.forEach((r) => {
    addNamespace(s.network.cluster, r.namespace, "gateway routes");
    r.services.forEach((svc) => addNamespace(s.network.cluster, svc.namespace, "gateway backends"));
  });
  if (s.instances.ref?.kind === "Namespace") {
    s.instances.rows.forEach((r) => addNamespace(cluster, r.name, "namespaces"));
  }

  const repeatedNs = new Map<string, number>();
  for (const n of namespaces.values()) {
    repeatedNs.set(n.name, (repeatedNs.get(n.name) ?? 0) + 1);
  }
  for (const n of [...namespaces.values()].sort((a, b) => a.name.localeCompare(b.name) || a.cluster.localeCompare(b.cluster))) {
    const repeated = (repeatedNs.get(n.name) ?? 0) > 1;
    cmds.push({
      id: repeated ? `ns:${n.cluster}/${n.name}` : `ns:${n.name}`,
      group: "Objects",
      title: repeated ? `${n.cluster}/${n.name}` : n.name,
      hint: ["Namespace", ...[...n.sources].sort()].join(" · "),
      run: () => {
        openSection(s, "pods", n.cluster);
        // Route the namespace through the store: PodsView re-opens its live
        // subscription for it. A direct listPods here would be discarded by
        // the live sub's stale guard (or silently desync the live stream).
        s.requestPodsNamespace(n.cluster, n.name);
      },
    });
  }

  cmds.push({
    id: "shortcuts",
    group: "Application",
    title: "Keyboard shortcuts",
    hint: "also on ?",
    run: () => openShortcutsOverlay(),
  });

  // --- Theme ------------------------------------------------------------------
  cmds.push({
    id: "theme:toggle",
    group: "Application",
    title: "cycle theme",
    run: () => {
      toggleTheme();
    },
  });
  THEMES.forEach((theme) => {
    cmds.push({
      id: `theme:set:${theme.id}`,
      group: "Application",
      title: `theme: ${theme.label.toLowerCase()}`,
      run: () => {
        setThemeChoice(theme.id);
      },
    });
  });

  return cmds;
}

const refKey = (r: ResourceRef) => `${r.group}/${r.version}/${r.plural}`;
const builtinCategoryOf = (r: ResourceRef) => BUILTIN_CATALOG.find((c) => c.entries.some((e) => e.kind === "gvr" && refKey(e.ref) === refKey(r)))?.label;

// destinationCommands: the palette results for one registry destination.
function destinationCommands(s: FleetStore, g: NavGroup, d: Destination, here: string | null): Command[] {
  const raw = d.target.kind === "resource";
  const group: CommandGroup = d.type === "task" ? "Views" : "Resources";
  const kindHint = d.target.kind === "resource" ? `${d.target.ref.kind} · ${d.target.ref.group ? `${d.target.ref.group}/` : ""}${d.target.ref.version}` : "";
  const base = [g.label, raw ? "raw resource view" : "", kindHint].filter(Boolean).join(" · ");
  // Eligibility at activation, from live state (the index is built when the
  // palette opens): never navigate to a cluster that left, disconnected or
  // lost the capability since.
  const live = (c: string) => (): string | null => {
    const cl = useFleet.getState().clusters.find((x) => x.name === c);
    if (!cl) return `${c} is no longer in the fleet`;
    const st = destState(cl, g, d);
    if (st !== "present") return `${CAP_STATE_TEXT[st]} on ${c}`;
    if (!connected(cl)) return `${c} is not connected`;
    return null;
  };
  const open = (c: string) => () => {
    navigate(c, d, s);
    if (d.target.kind === "resource") {
      const cat = builtinCategoryOf(d.target.ref);
      if (cat) s.setBuiltinCategory(cat);
    }
  };
  const states = s.clusters.map((c) => ({ c, state: destState(c, g, d) }));
  const eligible = states.filter((x) => x.state === "present" && connected(x.c)).map((x) => x.c);
  const id = `${group === "Views" ? "view" : "resource"}:${d.id}`;
  const chooser = (clusters: ClusterDTO[]): Command["next"] => ({
    title: `Choose cluster for ${d.label}`,
    options: clusters.map((c) => ({ id: `${id}@${c.name}`, group: "Clusters", title: c.name, hint: [c.env, c.region, c.protected ? "prd lock" : ""].filter(Boolean).join(" · ") || undefined, run: open(c.name), guard: live(c.name) })),
  });
  const out: Command[] = [];
  if (here) {
    const mine = states.find((x) => x.c.name === here);
    if (mine && mine.state === "present") {
      out.push({ id, group, title: d.label, hint: [base, here].join(" · "), run: open(here), guard: live(here), keywords: d.keywords });
    } else {
      const others = eligible.filter((c) => c.name !== here);
      const why = mine && mine.state !== "present" ? `${CAP_STATE_TEXT[mine.state]} on ${here}` : `not available on ${here}`;
      out.push(others.length > 0
        ? { id, group, title: d.label, hint: `${base} · ${why} - choose another cluster`, run: () => {}, next: chooser(others), keywords: d.keywords, choices: chooser(others)?.options }
        : { id, group, title: d.label, hint: base, run: () => {}, disabled: why, keywords: d.keywords });
    }
  } else if (eligible.length === 1) {
    out.push({ id, group, title: d.label, hint: [base, eligible[0].name].join(" · "), run: open(eligible[0].name), guard: live(eligible[0].name), keywords: d.keywords, choices: chooser(eligible)?.options });
  } else if (eligible.length > 1) {
    out.push({ id, group, title: d.label, hint: `${base} · ${eligible.length} clusters`, run: () => {}, next: chooser(eligible), keywords: d.keywords, choices: chooser(eligible)?.options });
  } else {
    const anyConnected = s.clusters.some(connected);
    const connecting = s.clusters.some((c) => c.state === "Connecting");
    const checking = states.some((x) => x.state === "checking");
    const why = s.clusters.length === 0 ? "no clusters in the fleet"
      : checking ? "checking capability…"
      : !anyConnected && connecting ? "waiting for a cluster to connect"
      : !anyConnected ? "no connected cluster"
      : "not detected on any connected cluster";
    // Connect cluster only where connecting one is the way forward.
    const canConnect = s.clusters.length === 0 || (!anyConnected && !connecting);
    out.push({ id, group, title: d.label, hint: base, run: () => {}, disabled: why, keywords: d.keywords,
      ...(canConnect ? { action: { label: "Connect cluster", run: () => s.openSettings() } } : {}) });
  }
  // Cluster-qualified task results: "events prd-weu" opens Events there.
  if (d.type === "task") {
    for (const c of eligible) {
      if (c.name === here) continue;
      out.push({ id: `${id}@${c.name}`, group, title: `${d.label} · ${c.name}`, hint: g.label, qualifier: c.name, run: open(c.name), guard: live(c.name) });
    }
  }
  return out;
}
