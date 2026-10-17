import { useFleet } from "./fleet";
import type { ArgoViewMode, ClusterSection, FluxViewMode, Route, WorkloadKind } from "./fleet";

// Session persistence for navigation + per-cluster view filters.
//
// Three storage tiers, chosen by how long each piece stays useful:
// - localStorage "klyx-route": the last route, restored once at boot after the
//   fleet seeds. Drill-in state (resource/instance/gateway) is deliberately
//   dropped - a relaunch lands on the section list, never on a possibly
//   deleted object.
// - localStorage "klyx-view-prefs": per-cluster namespace + structural toggles
//   (needs-attention, warnings-only, kind filter, near-limit sort).
// - sessionStorage "klyx-view-search": free-text filters. They survive section
//   switches within a run but reset on relaunch - a day-old query is noise.
//
// Hydration rule: a persisted value is applied ONLY while the field still
// holds its reset default. Palette jumps and Overview/TriageTape links set
// their target state (namespace request, needs-attention) immediately before
// the view mounts; hydration must never clobber an explicit intent.

const ROUTE_KEY = "klyx-route";
const PREFS_KEY = "klyx-view-prefs";
const SEARCH_KEY = "klyx-view-search";

// Guarded storage access: webview storage can be unavailable (privacy mode)
// or full - persistence is a nicety and must never take the app down.
function read(storage: Storage, key: string): string | null {
  try {
    return storage.getItem(key);
  } catch {
    return null;
  }
}
function write(storage: Storage, key: string, value: string): void {
  try {
    storage.setItem(key, value);
  } catch {
    // ignore - persistence is best-effort
  }
}

// ---- route ------------------------------------------------------------------

type PersistedRoute =
  | { name: "fleet"; view?: string }
  | { name: "forwards" }
  | { name: "settings" }
  | { name: "activity" }
  | { name: "cluster"; cluster: string; section: string; argoView?: string; fluxView?: string };

// Sections that exist today; a persisted route naming an unknown section (from
// an older or newer build) falls back to overview rather than rendering nothing.
const KNOWN_SECTIONS = new Set([
  "overview", "gitops", "argo", "helm", "certs", "externalsecrets",
  "gatewayApi", "flows", "resources", "crds", "workloads", "pods", "events", "nodes", "access", "upgrade", "connectivity", "karpenter", "velero",
]);

export function saveRoute(route: Route): void {
  let persisted: PersistedRoute;
  if (route.name === "cluster") {
    persisted = { name: "cluster", cluster: route.cluster, section: route.section, argoView: route.argoView, fluxView: route.fluxView };
  } else if (route.name === "fleet" && route.view) {
    persisted = { name: "fleet", view: route.view };
  } else {
    persisted = { name: route.name };
  }
  write(localStorage, ROUTE_KEY, JSON.stringify(persisted));
}

export function loadRoute(): PersistedRoute | null {
  const raw = read(localStorage, ROUTE_KEY);
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw) as PersistedRoute;
    if (parsed.name === "fleet") return parsed.view === "promotion" || parsed.view === "expiry" ? { name: "fleet", view: parsed.view } : { name: "fleet" };
    if (parsed.name === "forwards" || parsed.name === "settings" || parsed.name === "activity") return { name: parsed.name };
    if (parsed.name === "cluster" && typeof parsed.cluster === "string" && parsed.cluster !== "") {
      return {
        name: "cluster",
        cluster: parsed.cluster,
        section: KNOWN_SECTIONS.has(parsed.section) ? parsed.section : "overview",
        argoView: parsed.argoView,
        fluxView: parsed.fluxView,
      };
    }
    return null;
  } catch {
    return null;
  }
}

// initRoutePersistence subscribes to the store and mirrors every route change
// into localStorage. Returns the unsubscriber.
export function initRoutePersistence(): () => void {
  // Start from the current route: zustand calls this listener on EVERY state
  // change, and the first one at boot (the fleet seed) happens before the
  // saved route is restored - an undefined `last` used to save the boot
  // default over the saved route, so restore always found "fleet".
  let last: Route | undefined = useFleet.getState().route;
  return useFleet.subscribe((s) => {
    if (s.route === last) return;
    last = s.route;
    saveRoute(s.route);
  });
}

// restoreRouteOnBoot re-opens the persisted route once the fleet has seeded.
// It only acts while the app still sits on the boot default (fleet) - a user
// who already navigated during a slow seed wins over the saved route - and
// only into a cluster that still exists in the kubeconfig.
export function restoreRouteOnBoot(clusterNames: string[]): void {
  const current = useFleet.getState().route;
  if (current.name !== "fleet") return;
  const saved = loadRoute();
  if (!saved) return;
  if (saved.name === "fleet") {
    if (saved.view === "promotion" || saved.view === "expiry") useFleet.setState({ route: { name: "fleet", view: saved.view } });
    return;
  }
  if (saved.name === "forwards" || saved.name === "settings" || saved.name === "activity") {
    useFleet.setState({ route: { name: saved.name } });
    return;
  }
  if (!clusterNames.includes(saved.cluster)) return;
  const argoView: ArgoViewMode | undefined =
    saved.argoView === "applications" || saved.argoView === "projects" || saved.argoView === "applicationsets"
      ? saved.argoView
      : undefined;
  const fluxView: FluxViewMode | undefined = saved.fluxView === "reconciliation" || saved.fluxView === "sources" ? saved.fluxView : undefined;
  useFleet.setState({
    route: {
      name: "cluster",
      cluster: saved.cluster,
      // validated against KNOWN_SECTIONS in loadRoute - the cast is safe
      section: saved.section as ClusterSection,
      argoView,
      fluxView,
    },
  });
}

// ---- per-cluster view prefs ---------------------------------------------------

export type PodsViewPrefs = { namespace?: string; needsAttention?: boolean };
export type WorkloadsViewPrefs = { namespace?: string; needsAttention?: boolean; kinds?: WorkloadKind[]; nearLimitSort?: boolean };
export type EventsViewPrefs = { namespace?: string; warningsOnly?: boolean };
export type HelmViewPrefs = { namespace?: string };

type ClusterPrefs = {
  pods?: PodsViewPrefs;
  workloads?: WorkloadsViewPrefs;
  events?: EventsViewPrefs;
  helm?: HelmViewPrefs;
};

type AllPrefs = Record<string, ClusterPrefs>;

function loadAllPrefs(): AllPrefs {
  const raw = read(localStorage, PREFS_KEY);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? (parsed as AllPrefs) : {};
  } catch {
    return {};
  }
}

export function loadClusterPrefs(cluster: string): ClusterPrefs {
  return loadAllPrefs()[cluster] ?? {};
}

function saveViewPrefs<K extends keyof ClusterPrefs>(cluster: string, view: K, prefs: ClusterPrefs[K]): void {
  const all = loadAllPrefs();
  all[cluster] = { ...all[cluster], [view]: prefs };
  write(localStorage, PREFS_KEY, JSON.stringify(all));
}

export const savePodsViewPrefs = (cluster: string, p: PodsViewPrefs) => saveViewPrefs(cluster, "pods", p);
export const saveWorkloadsViewPrefs = (cluster: string, p: WorkloadsViewPrefs) => saveViewPrefs(cluster, "workloads", p);
export const saveEventsViewPrefs = (cluster: string, p: EventsViewPrefs) => saveViewPrefs(cluster, "events", p);
export const saveHelmViewPrefs = (cluster: string, p: HelmViewPrefs) => saveViewPrefs(cluster, "helm", p);

// ---- in-session search text (sessionStorage) ----------------------------------

type SearchView = "pods" | "workloads" | "events" | "helm";

function loadAllSearch(): Record<string, string> {
  const raw = read(sessionStorage, SEARCH_KEY);
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? (parsed as Record<string, string>) : {};
  } catch {
    return {};
  }
}

export function loadSearch(cluster: string, view: SearchView): string {
  return loadAllSearch()[`${cluster}/${view}`] ?? "";
}

export function saveSearch(cluster: string, view: SearchView, query: string): void {
  const all = loadAllSearch();
  const key = `${cluster}/${view}`;
  if (query === "") {
    if (!(key in all)) return;
    delete all[key];
  } else {
    all[key] = query;
  }
  write(sessionStorage, SEARCH_KEY, JSON.stringify(all));
}
