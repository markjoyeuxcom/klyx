import type { ClusterDTO, FleetView, Route, WorkloadKind } from "../store/fleet";
import { NAV_GROUPS, activeLocation, destState, entryOf, groupState, type CapState, type Destination, type NavGroup } from "./registry";
import type { NavBadge, NavCounts } from "./useNavCounts";

// The lens index's model (The Line slice 4 §4): the registry, the open
// cluster's navigation counts and capability states, as two sections -
// "Needs you" and "Everything else" - plus one line naming what is absent.
// Every number is navCounts'; nothing here counts.

export type IndexEntry = {
  key: string; // "dest:<id>" for a lens of its own, "lens:<group>" for a tool group, "sub:<id>" for a tool group's other task
  id: string; // the destination whose count and attention it carries (a tool lens: its first task)
  seq: number; // registry order
  label: string;
  kind: "dest" | "lens" | "sub";
  groupId: string;
  destination: Destination; // what selecting opens (a tool lens: its first available task, else its first task)
  state: Exclude<CapState, "absent">;
  badge: NavBadge | undefined;
  note: string | undefined;
  tone: "danger" | "warning" | undefined; // set only in "Needs you"
  current: boolean; // this entry is the page shown
  subs: IndexEntry[]; // a tool lens's other tasks, while it is open (never one already in "Needs you")
};

export type LensIndexModel = { needsYou: IndexEntry[]; rest: IndexEntry[]; absent: string[] };

// The lens groups that hold task lenses of their own (slice 4 §4.1); Config
// and Storage hold only raw kinds, reached through Resources and ⌘K.
const isToolLens = (g: NavGroup) => g.lens === "group";
const labelOf = (d: Destination) => d.indexLabel ?? d.label;
const tasksOf = (g: NavGroup) => g.destinations.filter((d) => d.type === "task");

// activeKey: the entry the route shows. A tool group's raw kinds belong to
// its lens; any other raw kind to the browser it was opened from; a kind
// shortcut or a custom kind mix to the lens that owns its section.
export function activeKey(route: Route, kindFilter: Record<WorkloadKind, boolean>): string | null {
  if (route.name !== "cluster") return null;
  const { group, destination } = activeLocation(route, kindFilter);
  if (group && isToolLens(group)) {
    const first = tasksOf(group)[0];
    return destination && destination.type === "task" && destination !== first ? `sub:${destination.id}` : `lens:${group.id}`;
  }
  if (destination?.type === "task") return `dest:${destination.id}`;
  if (route.resource || destination?.target.kind === "resource") return route.section === "crds" ? "dest:crds" : "dest:builtins";
  for (const g of NAV_GROUPS) {
    if (isToolLens(g)) continue;
    const owner = tasksOf(g).find((d) => d.target.kind === "section" && d.target.section === route.section);
    if (owner) return `dest:${owner.id}`;
  }
  return null;
}

const andList = (xs: string[]) => (xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);

// absentLine is the one quiet line naming every confirmed-absent lens.
export function absentLine(absent: string[]): string | null {
  if (absent.length === 0) return null;
  return `${andList(absent)} ${absent.length === 1 ? "is" : "are"} not installed here, so ${absent.length === 1 ? "it is" : "they are"} not listed.`;
}

export function lensIndex(c: ClusterDTO | undefined, counts: NavCounts, route: Route, kindFilter: Record<WorkloadKind, boolean>): LensIndexModel {
  const current = activeKey(route, kindFilter);
  const openGroup = current?.startsWith("lens:") || current?.startsWith("sub:") ? activeLocation(route, kindFilter).group?.id : undefined;
  const entries: IndexEntry[] = []; // registry order
  const subsByLens = new Map<string, IndexEntry[]>();
  const absent: string[] = [];
  let seq = 0;
  const quiet = (st: CapState) => (st === "checking" ? "checking…" : st === "unknown" ? "unknown" : undefined);
  const entry = (key: string, id: string, kind: IndexEntry["kind"], label: string, g: NavGroup, d: Destination, state: Exclude<CapState, "absent">, note: string | undefined): IndexEntry =>
    ({ key, id, seq: seq++, kind, label, groupId: g.id, destination: d, state, badge: counts.destination[id], note: quiet(state) ?? note, tone: undefined, current: key === current, subs: [] });

  for (const g of NAV_GROUPS) {
    const tasks = tasksOf(g);
    if (tasks.length === 0) continue;
    if (isToolLens(g)) {
      const st = groupState(c, g);
      if (st === "absent") { absent.push(g.label); continue; }
      const first = tasks[0];
      const opens = (st === "present" ? entryOf(c, g) : undefined) ?? first;
      entries.push(entry(`lens:${g.id}`, first.id, "lens", g.label, g, opens, st, counts.groupNote[g.id]));
      const subs: IndexEntry[] = [];
      for (const d of tasks.slice(1)) {
        const ds = destState(c, g, d);
        if (ds === "absent") continue;
        subs.push(entry(`sub:${d.id}`, d.id, "sub", labelOf(d), g, d, ds, counts.destinationNote[d.id]));
      }
      subsByLens.set(`lens:${g.id}`, subs);
      continue;
    }
    for (const d of tasks) {
      const ds = destState(c, g, d);
      if (ds === "absent") { absent.push(labelOf(d)); continue; }
      entries.push(entry(`dest:${d.id}`, d.id, "dest", labelOf(d), g, d, ds, counts.destinationNote[d.id]));
    }
  }

  // "Needs you" (§4.2): every entry or sub-entry whose destination is in
  // navCounts.attention, danger before warning, then count, then registry
  // order. A sub-entry reads "Lens · Sub".
  const byDest = new Map<string, IndexEntry>();
  for (const e of [...entries, ...[...subsByLens.values()].flat()]) byDest.set(e.id, e);
  const needsYou: IndexEntry[] = [];
  for (const id of counts.attention) {
    const e = byDest.get(id);
    if (!e || e.state !== "present") continue;
    // a not-ready controller is a danger light (its area's tone), whatever
    // its destination's own line says
    const tone = e.badge?.tone === "danger" || (e.note ?? "").includes("controller not ready") ? "danger" : "warning";
    const lens = e.kind === "sub" ? entries.find((x) => x.key === `lens:${e.groupId}`) : undefined;
    needsYou.push({ ...e, tone, label: lens ? `${lens.label} · ${e.label}` : e.label });
  }
  const rank = (t: IndexEntry["tone"]) => (t === "danger" ? 0 : 1);
  needsYou.sort((a, b) => rank(a.tone) - rank(b.tone) || (b.badge?.n ?? 0) - (a.badge?.n ?? 0) || a.seq - b.seq);

  const inNeeds = new Set(needsYou.map((e) => e.key));
  const withSubs = (e: IndexEntry): IndexEntry => (e.kind === "lens" && openGroup === e.groupId ? { ...e, subs: (subsByLens.get(e.key) ?? []).filter((s) => !inNeeds.has(s.key)) } : e);
  return {
    needsYou: needsYou.map(withSubs),
    rest: entries.filter((e) => !inNeeds.has(e.key)).map(withSubs),
    absent,
  };
}

// Fleet and app routes (§4.2): the index lists the fleet views, the current
// one marked; Settings, Activity and Forwards add no entries.
export const FLEET_VIEWS: { label: string; view: FleetView | undefined; title: string }[] = [
  { label: "Fleet board", view: undefined, title: "Every cluster, attention first" },
  { label: "Promotion tracker", view: "promotion", title: "Applied GitOps revisions per cluster - did a change land everywhere?" },
  { label: "Expiry horizon", view: "expiry", title: "Certificates, raw TLS secrets and webhook CAs fleet-wide, soonest first" },
];
