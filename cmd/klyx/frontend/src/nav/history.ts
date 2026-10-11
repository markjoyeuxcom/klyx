import { create } from "zustand";
import { useFleet, WORKLOAD_KINDS } from "../store/fleet";
import type { Route, WorkloadKind } from "../store/fleet";
import { activeLocation, sameRef } from "./registry";

// Navigation history (redesign step 7b; added to the spec after the Lens
// comparison): Back / Forward and a Recent menu. An entry is a place - the
// route plus the workloads kind filter that is part of a destination's
// identity - never an action: restoring one re-opens a view and its selection
// and runs nothing. One user navigation that takes several store writes (open
// a cluster, then its section) is one entry.

export type HistoryEntry = { route: Route; kindFilter?: Record<WorkloadKind, boolean> };

const MAX = 50;
const RECENT_MAX = 12;

type HistoryState = { past: HistoryEntry[]; future: HistoryEntry[]; recent: HistoryEntry[] };
export const useHistory = create<HistoryState>(() => ({ past: [], future: [], recent: [] }));

const ALL_KINDS = Object.fromEntries(WORKLOAD_KINDS.map((k) => [k, true])) as Record<WorkloadKind, boolean>;

function entryOf(route: Route, kindFilter: Record<WorkloadKind, boolean>): HistoryEntry {
  return route.name === "cluster" && route.section === "workloads" && !route.resource ? { route, kindFilter } : { route };
}

export function entryKey(e: HistoryEntry): string {
  return JSON.stringify([e.route, e.kindFilter ?? null]);
}

// entryLabel names a place for the Recent menu and the Back/Forward tooltips.
export function entryLabel(e: HistoryEntry): { cluster: string | null; place: string } {
  const r = e.route;
  if (r.name === "fleet") return { cluster: null, place: "Fleet board" };
  if (r.name === "forwards") return { cluster: null, place: "Port-forwards" };
  if (r.name === "activity") return { cluster: null, place: "Activity" };
  if (r.name === "settings") return { cluster: null, place: "Settings" };
  const { group, destination } = activeLocation(r, e.kindFilter ?? ALL_KINDS);
  let place = destination?.label ?? (r.resource ? r.resource.kind : group?.label ?? r.section);
  if (r.instance) place += ` › ${r.instance.namespace ? `${r.instance.namespace}/` : ""}${r.instance.name}`;
  if (r.gateway) place += ` › ${r.gateway.namespace}/${r.gateway.name}`;
  return { cluster: r.cluster, place };
}

const exists = (e: HistoryEntry) => {
  const r = e.route;
  return r.name !== "cluster" || useFleet.getState().clusters.some((c) => c.name === r.cluster);
};

let restoring = false;
let pending: HistoryEntry | null = null;

function remember(e: HistoryEntry) {
  const k = entryKey(e);
  useHistory.setState((h) => ({ recent: [e, ...h.recent.filter((x) => entryKey(x) !== k)].slice(0, RECENT_MAX) }));
}

// initHistory records route changes. The first change in a tick captures the
// place being left (from the previous state); a microtask commits it, so a
// multi-write navigation collapses into one entry.
export function initHistory(): () => void {
  const s0 = useFleet.getState();
  remember(entryOf(s0.route, s0.workloads.kindFilter));
  return useFleet.subscribe((s, prev) => {
    if (restoring) return;
    // A chosen kind filter on the workloads task is part of the place (All
    // workloads -> StatefulSets is a navigation); hydration and the view's
    // unmount reset are not.
    const onWorkloads = s.route.name === "cluster" && s.route.section === "workloads" && !s.route.resource;
    const kindsChanged = s.workloads.kindFilter !== prev.workloads.kindFilter && onWorkloads;
    if (kindsChanged && !s.workloads.kindExplicit && s.route === prev.route) {
      // Hydration settled what this place shows: rename it, record nothing.
      const here = entryOf(s.route, s.workloads.kindFilter);
      const routeKey = JSON.stringify(s.route);
      useHistory.setState((h) => ({ recent: h.recent.length > 0 && JSON.stringify(h.recent[0].route) === routeKey ? [here, ...h.recent.slice(1)] : h.recent }));
      return;
    }
    const kindsMoved = kindsChanged && !!s.workloads.kindExplicit;
    if (s.route === prev.route && !kindsMoved) return;
    if (pending) return;
    pending = entryOf(prev.route, prev.workloads.kindFilter);
    queueMicrotask(() => {
      const from = pending;
      pending = null;
      if (!from) return;
      const now = useFleet.getState();
      const here = entryOf(now.route, now.workloads.kindFilter);
      if (entryKey(from) === entryKey(here)) return;
      useHistory.setState((h) => ({ past: [...h.past, from].slice(-MAX), future: [] }));
      remember(here);
    });
  });
}

// restore re-opens a place through the store actions that own its side state
// (a Gateway's topology slice, a resource's instance list), without recording.
function restore(e: HistoryEntry) {
  restoring = true;
  pending = null;
  // A NavIntent belongs to the navigation that raised it; one still waiting
  // for data must not fire on a place reached through history.
  useFleet.getState().clearNavIntent();
  try {
    const r = e.route;
    const nav = useFleet.getState();
    if (r.name !== "cluster") {
      useFleet.setState({ route: r });
    } else if (r.gateway) {
      useFleet.setState({ route: { name: "cluster", cluster: r.cluster, section: "gatewayApi" } });
      nav.openGateway(r.gateway.namespace, r.gateway.name);
    } else if (r.resource) {
      // Moving between instances of the list already on screen keeps that
      // list: openResource would reset rows to loading, and the mounted list
      // only re-reads when its cluster or kind changes.
      const s = useFleet.getState();
      const cur = s.route;
      const owned = cur.name === "cluster" && cur.cluster === r.cluster && !!cur.resource && sameRef(cur.resource, r.resource)
        && s.instances.cluster === r.cluster && !!s.instances.ref && sameRef(s.instances.ref, r.resource);
      if (owned) {
        useFleet.setState({ route: { name: "cluster", cluster: r.cluster, section: r.section, resource: r.resource } });
      } else {
        useFleet.setState({ route: { name: "cluster", cluster: r.cluster, section: r.section } });
        nav.openResource(r.resource);
      }
      if (r.instance) useFleet.getState().openInstance(r.instance.namespace, r.instance.name);
    } else {
      useFleet.setState((s) => ({ route: r, ...(e.kindFilter ? { workloads: { ...s.workloads, kindFilter: e.kindFilter, kindExplicit: true } } : {}) }));
    }
  } finally {
    restoring = false;
  }
  const now = useFleet.getState();
  remember(entryOf(now.route, now.workloads.kindFilter));
}

const current = (): HistoryEntry => {
  const s = useFleet.getState();
  return entryOf(s.route, s.workloads.kindFilter);
};

// The places Back / Forward would actually reach (skipping clusters that
// left the fleet) - what the mast buttons name and enable on.
export function backTarget(): HistoryEntry | undefined {
  const past = useHistory.getState().past;
  for (let i = past.length - 1; i >= 0; i--) if (exists(past[i])) return past[i];
  return undefined;
}
export function forwardTarget(): HistoryEntry | undefined {
  const future = useHistory.getState().future;
  for (let i = future.length - 1; i >= 0; i--) if (exists(future[i])) return future[i];
  return undefined;
}

// back / forward skip places whose cluster has left the fleet.
export function back(): boolean {
  const h = useHistory.getState();
  const past = [...h.past];
  const skipped: HistoryEntry[] = [];
  let target: HistoryEntry | undefined;
  while ((target = past.pop())) { if (exists(target)) break; skipped.push(target); }
  if (!target) { if (skipped.length) useHistory.setState({ past: [] }); return false; }
  useHistory.setState({ past, future: [...h.future, current()] });
  restore(target);
  return true;
}

export function forward(): boolean {
  const h = useHistory.getState();
  const future = [...h.future];
  let target: HistoryEntry | undefined;
  while ((target = future.pop())) { if (exists(target)) break; }
  if (!target) { useHistory.setState({ future: [] }); return false; }
  useHistory.setState({ past: [...h.past, current()].slice(-MAX), future });
  restore(target);
  return true;
}

// go opens a Recent place as an ordinary navigation (it is recorded).
export function go(e: HistoryEntry): void {
  if (!exists(e)) return;
  const from = current();
  restore(e);
  if (entryKey(from) !== entryKey(current())) useHistory.setState((h) => ({ past: [...h.past, from].slice(-MAX), future: [] }));
}

export function recentPlaces(): HistoryEntry[] {
  const here = entryKey(current());
  return useHistory.getState().recent.filter((e) => entryKey(e) !== here && exists(e));
}

/** Tests only. */
export function _resetHistoryForTest(): void {
  useHistory.setState({ past: [], future: [], recent: [] });
  pending = null;
  restoring = false;
}
