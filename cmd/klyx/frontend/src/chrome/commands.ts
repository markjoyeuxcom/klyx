// Command index for the ⌘K palette. buildCommands flattens the *currently
// loaded* store state into a flat list of runnable commands. It indexes only
// what is already in memory — no fetching — so the palette is honest about
// "showing loaded data". Each command's run() performs the navigation (and any
// jump-to-resource bridge call) as a side effect.

import { useFleet, ClusterSection } from "../store/fleet";
import { openChat } from "../ai/chatStore";
import type { ClusterDTO, GatewayRefDTO, NavIntent, ResourceRef, RevealRef, RouteNodeDTO, WorkloadKind } from "../store/fleet";
import { bestSub, useHealth } from "../store/health";
import { labelOf } from "../health/verdict";
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
import { reveal } from "../nav/reveal";
import { targetKey, usePendingExplain } from "../ai/AIExplain";
import type { ExplainTarget } from "../bridge/ai";

export type FleetStore = ReturnType<typeof useFleet.getState>;

// Palette groups (spec §4), in display order.
export type CommandGroup = "Application" | "Clusters" | "Views" | "Resources" | "Objects" | "Actions";

export type CommandTarget = { cluster: string; kind: string; namespace?: string; name: string; uid?: string };

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
  // The name the lens index shows when it differs from the title ("Workloads"
  // for "All workloads"): typing it whole picks the command, as its title would.
  shownAs?: string;
  // A verb's object (The Line slice 9 note §4.1): the console's empty query
  // lists the verbs whose target is the open inspector's pin.
  target?: CommandTarget;
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

// explainVerb parks an AI Explain for one object and reveals it: its lens
// selects it, and the inspector runs the request once AI is ready (spec
// 2026-10-13 §4.3). Read-only; the backend fences the object by UID.
function explainVerb(cluster: string, task: string, part: string, label: string, ref: RevealRef, target: ExplainTarget, kind: string): Command {
  return {
    id: `verb:explain:${cluster}/${task}/${part}/${ref.namespace ?? ""}/${ref.name}`,
    group: "Actions",
    target: { cluster, kind, namespace: ref.namespace, name: ref.name, uid: ref.uid },
    title: `explain ${label}`,
    hint: `${cluster} · AI, read-only`,
    run: () => {
      usePendingExplain.getState().request(targetKey(cluster, target));
      reveal(cluster, task, part, ref);
    },
  };
}

export function buildCommands(s: FleetStore): Command[] {
  const cmds: Command[] = [];
  const route = s.route;
  const cluster = route.name === "cluster" ? route.cluster : null;

  // --- Application -------------------------------------------------------------
  cmds.push({ id: "fleet", group: "Application", title: "Fleet board", hint: "every cluster, attention first", run: () => s.openFleet() });
  cmds.push({ id: "fleet:promotion", group: "Application", title: "Promotion tracker", hint: "applied GitOps revisions per cluster", run: () => s.openFleet("promotion") });
  cmds.push({ id: "fleet:expiry", group: "Application", title: "Expiry horizon", hint: "certificates and CAs fleet-wide, soonest first", run: () => s.openFleet("expiry") });
  cmds.push({ id: "fleet:backups", group: "Application", title: "Backups", hint: "Velero schedules fleet-wide, the longest without a retained success first", run: () => s.openFleet("backups") });
  cmds.push({ id: "forwards", group: "Application", title: "Port-forwards", hint: (s.forwards?.length ?? 0) > 0 ? `${s.forwards.length} active` : "none active", run: () => s.openForwards() });
  cmds.push({ id: "activity", group: "Application", title: "Activity log", hint: "recent action outcomes", run: () => s.openActivity() });
  cmds.push({ id: "settings", group: "Application", title: "Settings", hint: "fleet config · kubeconfig contexts", run: () => s.openSettings() });
  cmds.push({ id: "terminal", group: "Application", title: "Terminal", hint: "a shell with this fleet's kubeconfig", run: () => void openTerminal() });
  // Cluster chat (spec 2026-10-14 §8): the open cluster's conversation.
  if (cluster) {
    cmds.push({ id: `chat:${cluster}`, group: "Application", title: `Ask about ${cluster}`, hint: "read-only AI chat about this cluster", keywords: ["chat", "ai", "question", "explain"], run: () => openChat(cluster) });
  }
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
        hint: `Pod · ${labelOf(p.health)} · ${podCluster}`,
        run: () => {
          openSection(s, "pods", podCluster);
          void openPodDetail(podCluster, p.namespace, p.name, p.uid ?? "");
        },
      });
      if (p.uid) {
        cmds.push(explainVerb(podCluster, "pods", "pods", `pod ${p.namespace}/${p.name}`, { gvr: "v1/pods", namespace: p.namespace, name: p.name, uid: p.uid }, { kind: "row", task: "pods", part: "pods", uid: p.uid }, "Pod"));
      }
      cmds.push({
        id: `verb:logs:${podCluster}/${p.namespace}/${p.name}`,
        group: "Actions",
        target: { cluster: podCluster, kind: "Pod", namespace: p.namespace, name: p.name, uid: p.uid },
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
      if (n.uid) {
        cmds.push(explainVerb(nodeCluster, "nodes", "nodes", `node ${n.name}`, { gvr: "v1/nodes", name: n.name, uid: n.uid }, { kind: "row", task: "nodes", part: "nodes", uid: n.uid }, "Node"));
      }
      cmds.push({
        id: `verb:drain:${nodeCluster}/${n.name}`,
        group: "Actions",
        target: { cluster: nodeCluster, kind: "Node", name: n.name, uid: n.uid },
        title: `drain node ${n.name}`,
        hint: `${nodeCluster} · opens the drain composer${lockMark(s, nodeCluster)}`,
        run: () => openWithIntent(s, "nodes", nodeCluster, { kind: "drain-node", cluster: nodeCluster, node: n.name }),
      });
      cmds.push({
        id: `verb:cordon:${nodeCluster}/${n.name}`,
        group: "Actions",
        target: { cluster: nodeCluster, kind: "Node", name: n.name, uid: n.uid },
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
      if (workloadCluster && w.uid && w.gvr) {
        cmds.push(explainVerb(workloadCluster, "workloads", "workloads", `${w.kind.toLowerCase()} ${w.namespace}/${w.name}`, { gvr: w.gvr, namespace: w.namespace, name: w.name, uid: w.uid }, { kind: "row", task: "workloads", part: "workloads", uid: w.uid }, w.kind));
      }
      if (workloadCluster) {
        cmds.push({
          id: `verb:restart:${workloadCluster}/${key}`,
          group: "Actions",
          target: { cluster: workloadCluster, kind: w.kind, namespace: w.namespace, name: w.name, uid: w.uid },
          title: `restart ${w.kind.toLowerCase()} ${w.namespace}/${w.name}`,
          hint: `${workloadCluster}${lockMark(s, workloadCluster)}`,
          run: () => openWithIntent(s, "workloads", workloadCluster, { kind: "restart-workload", cluster: workloadCluster, workloadKind: w.kind as WorkloadKind, namespace: w.namespace, name: w.name }),
        });
        // Fleet compare: the same workload across every cluster. It opens the
        // existing compare panel; with one cluster there is nothing to compare.
        if (s.clusters.length > 1) {
          cmds.push({
            id: `verb:compare:${workloadCluster}/${key}`,
            group: "Actions",
            target: { cluster: workloadCluster, kind: w.kind, namespace: w.namespace, name: w.name, uid: w.uid },
            title: `compare ${w.kind.toLowerCase()} ${w.namespace}/${w.name} across the fleet`,
            hint: `${s.clusters.length} clusters · read-only`,
            run: () => openWithIntent(s, "workloads", workloadCluster, { kind: "compare-workload", cluster: workloadCluster, workloadKind: w.kind as WorkloadKind, namespace: w.namespace, name: w.name }),
          });
        }
        // DaemonSets have no replica count - scale does not apply.
        if (w.kind !== "DaemonSet") {
          cmds.push({
            id: `verb:scale:${workloadCluster}/${key}`,
            group: "Actions",
            target: { cluster: workloadCluster, kind: w.kind, namespace: w.namespace, name: w.name, uid: w.uid },
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
      cmds.push(explainVerb(helmCluster, "helm", "releases", `helm release ${r.namespace}/${r.name}`, { gvr: "", namespace: r.namespace, name: r.name }, { kind: "helm", namespace: r.namespace, name: r.name }, "HelmRelease"));
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
      if (r.uid && r.gvr) {
        cmds.push(explainVerb(gitopsCluster, "flux", "reconciliations", `${r.kind.toLowerCase()} ${r.namespace}/${r.name}`, { gvr: r.gvr, namespace: r.namespace, name: r.name, uid: r.uid }, { kind: "row", task: "flux", part: "reconciliations", uid: r.uid }, r.kind));
      }
      cmds.push({
        id: `verb:reconcile:${gitopsCluster}/${key}`,
        group: "Actions",
        target: { cluster: gitopsCluster, kind: r.kind, namespace: r.namespace, name: r.name, uid: r.uid },
        title: `reconcile ${r.kind.toLowerCase()} ${r.namespace}/${r.name}`,
        hint: `${gitopsCluster}${lockMark(s, gitopsCluster)}`,
        run: () => openWithIntent(s, "gitops", gitopsCluster, { kind: "reconcile-flux", cluster: gitopsCluster, fluxKind: r.kind, namespace: r.namespace, name: r.name }),
      });
      cmds.push({
        id: `verb:suspend:${gitopsCluster}/${key}`,
        group: "Actions",
        target: { cluster: gitopsCluster, kind: r.kind, namespace: r.namespace, name: r.name, uid: r.uid },
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
      if (a.uid && a.gvr) {
        cmds.push(explainVerb(argoCluster, "argo", "applications", `application ${a.namespace}/${a.name}`, { gvr: a.gvr, namespace: a.namespace, name: a.name, uid: a.uid }, { kind: "row", task: "argo", part: "applications", uid: a.uid }, "Application"));
      }
      cmds.push({
        id: `verb:argo-sync:${argoCluster}/${key}`,
        group: "Actions",
        target: { cluster: argoCluster, kind: "Application", namespace: a.namespace, name: a.name, uid: a.uid },
        title: `sync app ${a.namespace}/${a.name}`,
        hint: `${argoCluster} · opens the sync composer${lockMark(s, argoCluster)}`,
        run: () => openWithIntent(s, "argo", argoCluster, { kind: "sync-argo", cluster: argoCluster, namespace: a.namespace, name: a.name }),
      });
      cmds.push({
        id: `verb:argo-refresh:${argoCluster}/${key}`,
        group: "Actions",
        target: { cluster: argoCluster, kind: "Application", namespace: a.namespace, name: a.name, uid: a.uid },
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
    keywords: ["help", "keys", "cheatsheet", "?"],
    run: () => openShortcutsOverlay(),
  });

  // Fleet compare starts from a workload (its inspector's More menu, or the
  // palette verbs above). This entry makes it findable: it opens Workloads to
  // pick one - here, in the one connected cluster, or after Choose cluster -
  // and says when it cannot apply (Codex review of 05c9732: from the fleet
  // board it must not be a dead end).
  const compareBase = { id: "fleet-compare", group: "Application" as const, title: "Fleet compare", keywords: ["compare", "diff"] };
  const compareHint = "pick a workload, then More > Compare across the fleet";
  const connectedClusters = s.clusters.filter(connected);
  const openWorkloads = (c: string) => () => openSection(s, "workloads", c);
  if (s.clusters.length < 2) {
    cmds.push({ ...compareBase, hint: "one workload across every cluster", run: () => {}, disabled: "needs two or more clusters in the fleet" });
  } else if (cluster && connectedClusters.some((c) => c.name === cluster)) {
    cmds.push({ ...compareBase, hint: `${compareHint} · ${cluster}`, run: openWorkloads(cluster) });
  } else if (connectedClusters.length === 1) {
    cmds.push({ ...compareBase, hint: `${compareHint} · ${connectedClusters[0].name}`, run: openWorkloads(connectedClusters[0].name) });
  } else if (connectedClusters.length > 1) {
    const options = connectedClusters.map((c) => ({ id: `fleet-compare@${c.name}`, group: "Clusters" as const, title: c.name, hint: [c.env, c.region].filter(Boolean).join(" · ") || undefined, run: openWorkloads(c.name) }));
    cmds.push({ ...compareBase, hint: `${compareHint} · ${connectedClusters.length} clusters`, run: () => {}, next: { title: "Choose cluster for Fleet compare", options }, choices: options });
  } else {
    cmds.push({ ...compareBase, hint: "one workload across every cluster", run: () => {}, disabled: "no connected cluster" });
  }

  // Scratch: what Klyx itself created (debug pods, ad-hoc objects), in the
  // unmanaged klyx-scratch namespace. It opens the existing panel; deleting
  // there keeps its own confirm.
  const scratchHere = cluster && s.clusters.some((c) => c.name === cluster && connected(c)) ? cluster : null;
  const scratchConnected = s.clusters.filter(connected);
  const openScratch = (c: string) => () => openWithIntent(s, "pods", c, { kind: "open-scratch", cluster: c });
  const scratchBase = { id: "scratch", group: "Application" as const, title: "Scratch", keywords: ["debug pod", "ad-hoc", "klyx-scratch"] };
  if (scratchHere) {
    cmds.push({ ...scratchBase, hint: `debug pods and ad-hoc objects Klyx created · ${scratchHere}`, run: openScratch(scratchHere) });
  } else if (scratchConnected.length === 1) {
    cmds.push({ ...scratchBase, hint: `debug pods and ad-hoc objects Klyx created · ${scratchConnected[0].name}`, run: openScratch(scratchConnected[0].name) });
  } else if (scratchConnected.length > 1) {
    const options = scratchConnected.map((c) => ({ id: `scratch@${c.name}`, group: "Clusters" as const, title: c.name, hint: [c.env, c.region].filter(Boolean).join(" · ") || undefined, run: openScratch(c.name) }));
    cmds.push({ ...scratchBase, hint: `debug pods and ad-hoc objects Klyx created · ${scratchConnected.length} clusters`, run: () => {}, next: { title: "Choose cluster for Scratch", options }, choices: options });
  } else {
    cmds.push({ ...scratchBase, hint: "debug pods and ad-hoc objects Klyx created", run: () => {}, disabled: "no connected cluster" });
  }

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
  const base = [g.label, raw ? "resource list" : "", kindHint].filter(Boolean).join(" · ");
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
  // A lens is findable by its tool's name too: "helm" finds Helm's Releases,
  // "network" its Gateways and routes (Codex UI review 2026-10-13).
  const keywords = d.type === "task" ? [g.label, ...(d.keywords ?? [])] : d.keywords;
  // The name the lens index shows it by: its index label, or - the first
  // task of a tool lens - the tool's name ("Helm" for its Releases).
  const firstTask = g.destinations.find((x) => x.type === "task");
  const shownAs = d.type !== "task" ? undefined : d.indexLabel ?? (g.lens === "group" && firstTask === d ? g.label : undefined);
  const out: Command[] = [];
  if (here) {
    const mine = states.find((x) => x.c.name === here);
    if (mine && mine.state === "present") {
      out.push({ id, group, title: d.label, hint: [base, here].join(" · "), run: open(here), guard: live(here), keywords, shownAs });
    } else {
      const others = eligible.filter((c) => c.name !== here);
      const why = mine && mine.state !== "present" ? `${CAP_STATE_TEXT[mine.state]} on ${here}` : `not available on ${here}`;
      out.push(others.length > 0
        ? { id, group, title: d.label, hint: `${base} · ${why} - choose another cluster`, run: () => {}, next: chooser(others), keywords, shownAs, choices: chooser(others)?.options }
        : { id, group, title: d.label, hint: base, run: () => {}, disabled: why, keywords, shownAs });
    }
  } else if (eligible.length === 1) {
    out.push({ id, group, title: d.label, hint: [base, eligible[0].name].join(" · "), run: open(eligible[0].name), guard: live(eligible[0].name), keywords, shownAs, choices: chooser(eligible)?.options });
  } else if (eligible.length > 1) {
    out.push({ id, group, title: d.label, hint: `${base} · ${eligible.length} clusters`, run: () => {}, next: chooser(eligible), keywords, shownAs, choices: chooser(eligible)?.options });
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
    out.push({ id, group, title: d.label, hint: base, run: () => {}, disabled: why, keywords, shownAs,
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
