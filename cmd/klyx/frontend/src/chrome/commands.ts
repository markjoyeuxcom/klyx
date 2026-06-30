// Command index for the ⌘K palette. buildCommands flattens the *currently
// loaded* store state into a flat list of runnable commands. It indexes only
// what is already in memory — no fetching — so the palette is honest about
// "showing loaded data". Each command's run() performs the navigation (and any
// jump-to-resource bridge call) as a side effect.

import { useFleet, SECTION_LABELS, ClusterSection } from "../store/fleet";
import type { ResourceRef, RouteNodeDTO } from "../store/fleet";
import { openPodDetail } from "../bridge/pods";
import { openNodeDetail } from "../bridge/nodes";
import { openHelmRelease } from "../bridge/helm";
import { listPods } from "../bridge/pods";
import { getResourceDetail } from "../bridge/gitops";
import { copyText, getInstanceDetail } from "../bridge/crd";
import { setThemeChoice, THEMES, toggleTheme } from "../theme/ThemeProvider";
import { BUILTIN_CATALOG } from "../cluster/builtins";
import { supportsRiskFilter } from "../cluster/resourceRisk";

export type FleetStore = ReturnType<typeof useFleet.getState>;

export type Command = {
  id: string;
  group: string;
  title: string;
  hint?: string;
  run: () => void;
};

// Section display order mirrors the sidebar groups (triage-first, GitOps in top five).
const SECTION_ORDER: ClusterSection[] = [
  "overview",
  "workloads",
  "pods",
  "nodes",
  "events",
  "gitops",
  "argo",
  "helm",
  "gatewayApi",
  "resources",
  "crds",
];

function openSection(s: FleetStore, section: ClusterSection, targetCluster?: string | null): void {
  if (targetCluster && (s.route.name !== "cluster" || s.route.cluster !== targetCluster)) {
    s.openCluster(targetCluster);
  }
  s.setSection(section);
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
    openSection(s, "pods", cluster);
    void openPodDetail(cluster, ref.namespace, ref.name);
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

  // --- Clusters ---------------------------------------------------------------
  for (const c of s.clusters) {
    const hintParts = [c.env, c.region].filter(Boolean);
    cmds.push({
      id: `cluster:${c.name}`,
      group: "Clusters",
      title: c.name,
      hint: hintParts.join(" · ") || undefined,
      run: () => s.openCluster(c.name),
    });
  }
  cmds.push({
    id: "fleet",
    group: "Clusters",
    title: "Fleet overview",
    run: () => s.openFleet(),
  });
  cmds.push({
    id: "settings",
    group: "Clusters",
    title: "Settings",
    hint: "fleet config · kubeconfig contexts",
    run: () => s.openSettings(),
  });
  cmds.push({
    id: "forwards",
    group: "Clusters",
    title: "Port-forwards",
    hint: (s.forwards?.length ?? 0) > 0 ? `${s.forwards.length} active` : undefined,
    run: () => s.openForwards(),
  });

  // --- Sections (only inside a cluster) --------------------------------------
  if (cluster) {
    for (const section of SECTION_ORDER) {
      cmds.push({
        id: `section:${section}`,
        group: "Sections",
        title: SECTION_LABELS[section],
        hint: cluster,
        run: () => s.setSection(section),
      });
    }

    for (const cat of BUILTIN_CATALOG) {
      for (const entry of cat.entries) {
        if (entry.kind === "lens") {
          cmds.push({
            id: `builtin:lens:${entry.section}:${entry.label}`,
            group: "Built-in resources",
            title: entry.label,
            hint: `${cat.label} · ${entry.hint}`,
            run: () => {
              openSection(s, entry.section, cluster);
              s.setBuiltinCategory(cat.label);
            },
          });
          continue;
        }
        const ref = entry.ref;
        cmds.push({
          id: `builtin:${ref.group}/${ref.version}/${ref.plural}`,
          group: "Built-in resources",
          title: ref.kind,
          hint: `${cat.label} · ${apiHint(ref)}`,
          run: () => {
            openSection(s, "resources", cluster);
            s.setBuiltinCategory(cat.label);
            s.openResource(ref);
          },
        });
      }
    }
  }

  // --- Current resource/detail actions ---------------------------------------
  if (cluster && route.name === "cluster" && route.resource) {
    const ref = route.resource;
    if (supportsRiskFilter(ref)) {
      cmds.push({
        id: `resource-risk:${ref.group}/${ref.version}/${ref.plural}`,
        group: "Resource actions",
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
        group: "Resource actions",
        title: `copy YAML for ${target}`,
        hint: ref.kind,
        run: () => {
          void copyText(detail.yaml);
        },
      });
      cmds.push({
        id: `resource-refresh:${ref.group}/${ref.version}/${ref.plural}/${target}`,
        group: "Resource actions",
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
          group: "Related objects",
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
        group: "Pods",
        title: `${p.namespace}/${p.name}`,
        hint: p.phase || p.rank,
        run: () => {
          openSection(s, "pods", podCluster);
          void openPodDetail(podCluster, p.namespace, p.name);
        },
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
        group: "Nodes",
        title: n.name,
        hint: `${health} · ${roles} · ${n.version}`,
        run: () => {
          openSection(s, "nodes", nodeCluster);
          void openNodeDetail(nodeCluster, n.name);
        },
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
        group: "Workloads",
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
    }
  }

  // --- Helm releases (when loaded) -------------------------------------------
  if (s.helm.releases.length > 0 && s.helm.cluster) {
    const helmCluster = s.helm.cluster;
    for (const r of s.helm.releases) {
      cmds.push({
        id: `helm:${r.namespace}/${r.name}`,
        group: "Helm releases",
        title: `${r.namespace}/${r.name}`,
        hint: r.chart,
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
        group: "Flux objects",
        title: `${r.kind.toLowerCase()} ${r.namespace}/${r.name}`,
        hint: [r.ready, r.suspended ? "suspended" : "", r.sourceKind && r.sourceName ? `${r.sourceKind} ${r.sourceName}` : ""].filter(Boolean).join(" · "),
        run: () => {
          openSection(s, "gitops", gitopsCluster);
          s.expand(key);
          void getResourceDetail(gitopsCluster, r.kind, r.namespace, r.name);
        },
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
        group: "Argo applications",
        title: `${a.namespace}/${a.name}`,
        hint: [a.syncStatus, a.healthStatus, a.project].filter(Boolean).join(" · "),
        run: () => {
          openSection(s, "argo", argoCluster);
          if (!useFleet.getState().argo.expanded.includes(key)) {
            s.toggleArgoExpand(key);
          }
        },
      });
    }
  }

  // --- Gateway API (when loaded) ---------------------------------------------
  if (cluster && s.network.gateways.length > 0) {
    for (const g of s.network.gateways) {
      cmds.push({
        id: `gateway:${g.namespace}/${g.name}`,
        group: "Gateways",
        title: `${g.namespace}/${g.name}`,
        hint: [g.className, g.accepted ? "accepted" : "not accepted", g.programmed ? "programmed" : "pending"].filter(Boolean).join(" · "),
        run: () => {
          openSection(s, "gatewayApi", cluster);
          s.openGateway(g.namespace, g.name);
        },
      });
    }
  }

  if (cluster && s.network.topology?.routes?.length) {
    const gw = s.network.topology.gateway;
    for (const r of s.network.topology.routes) {
      const key = routeKey(r);
      cmds.push({
        id: `gateway-route:${key}`,
        group: "Gateway routes",
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
    addNamespace(cluster, r.namespace, "gateway routes");
    r.services.forEach((svc) => addNamespace(cluster, svc.namespace, "gateway backends"));
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
      group: "Namespaces",
      title: repeated ? `${n.cluster}/${n.name}` : n.name,
      hint: [...n.sources].sort().join(" · "),
      run: () => {
        openSection(s, "pods", n.cluster);
        void listPods(n.cluster, n.name);
      },
    });
  }

  // --- Theme ------------------------------------------------------------------
  cmds.push({
    id: "theme:toggle",
    group: "Theme",
    title: "cycle theme",
    run: () => {
      toggleTheme();
    },
  });
  THEMES.forEach((theme) => {
    cmds.push({
      id: `theme:set:${theme.id}`,
      group: "Theme",
      title: `theme: ${theme.label.toLowerCase()}`,
      run: () => {
        setThemeChoice(theme.id);
      },
    });
  });

  return cmds;
}
