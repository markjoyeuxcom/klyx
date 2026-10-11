import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import type { AttentionSlice, LensCount, VerdictLensCount } from "../store/fleet";
import { describeObservation } from "../health/verdict";
import { attentionRepollMs, fetchAttention } from "../bridge/attention";

// The cluster attention model (redesign spec §4 "fault discovery replaces
// TriageTape"): one shared summary, worded per area, consumed by the
// navigation tree, the mast Attention button and Overview. Areas are never
// summed into one object count (a workload and its pods are two
// representations of one fault); the mast counts AREAS needing attention.

export type AreaId = "workloads" | "pods" | "nodes" | "events" | "helm" | "flux" | "argo";
export type AreaState =
  | "loading"     // no read yet
  | "unreadable"  // the read failed - never quiet
  | "absent"      // the tool is not installed - omitted
  | "attention"   // known faults or degraded objects
  | "unverified"  // read, but capped or partly unclassified
  | "evidence"    // warning events: worth a look, never a fault
  | "quiet";      // read completely, nothing found

export type Area = {
  id: AreaId;
  label: string;
  groupId: string;        // navigation group (registry)
  destinationId: string;  // navigation destination (registry)
  state: AreaState;
  n: number;              // attention (or evidence) count; 0 otherwise
  tone: "danger" | "warning" | undefined;
  text: string;           // full wording, e.g. "3 need attention · 1 unverified"
};

const AREAS: { id: AreaId; label: string; groupId: string; destinationId: string }[] = [
  { id: "workloads", label: "Workloads", groupId: "workloads", destinationId: "workloads" },
  { id: "pods", label: "Pods", groupId: "workloads", destinationId: "pods" },
  { id: "nodes", label: "Nodes", groupId: "cluster", destinationId: "nodes" },
  { id: "events", label: "Events", groupId: "cluster", destinationId: "events" },
  { id: "helm", label: "Helm releases", groupId: "helm", destinationId: "helm" },
  { id: "flux", label: "Flux", groupId: "flux", destinationId: "flux" },
  { id: "argo", label: "Argo CD", groupId: "argo", destinationId: "argo" },
];

function verdictArea(c: VerdictLensCount): Pick<Area, "state" | "n" | "tone" | "text"> {
  if (c === "unreadable") return { state: "unreadable", n: 0, tone: undefined, text: "could not be read" };
  const d = describeObservation(c);
  if (d.attention > 0) return { state: "attention", n: d.attention, tone: d.tone === "danger" ? "danger" : "warning", text: d.text };
  if (!d.quiet) return { state: "unverified", n: 0, tone: undefined, text: d.text };
  return { state: "quiet", n: 0, tone: undefined, text: "none need attention" };
}

// Flux, Argo and Helm are not on the shared verdict yet: their legacy counts
// keep their own wording (Flux "not ready" still includes progressing).
function legacyArea(c: LensCount, word: string, tone: "danger" | "warning"): Pick<Area, "state" | "n" | "tone" | "text"> {
  if (c === "unreadable") return { state: "unreadable", n: 0, tone: undefined, text: "could not be read" };
  if (c === "absent") return { state: "absent", n: 0, tone: undefined, text: "not installed" };
  if (c > 0) return { state: "attention", n: c, tone, text: `${c} ${word}` };
  return { state: "quiet", n: 0, tone: undefined, text: `none ${word}` };
}

export function attentionAreas(a: AttentionSlice | null | undefined, cluster: string): Area[] {
  const mine = a && a.cluster === cluster ? a : null;
  return AREAS.map((meta) => {
    if (!mine || (mine.loading && mine.at === null)) return { ...meta, state: "loading" as const, n: 0, tone: undefined, text: "checking…" };
    const c = mine.counts;
    let w: Pick<Area, "state" | "n" | "tone" | "text">;
    switch (meta.id) {
      case "workloads": w = verdictArea(c.workloads); break;
      case "pods": w = verdictArea(c.pods); break;
      case "nodes": w = verdictArea(c.nodes); break;
      case "events":
        w = c.events === "unreadable" || c.events === "absent" ? { state: "unreadable", n: 0, tone: undefined, text: "could not be read" }
          : c.events > 0 ? { state: "evidence", n: c.events, tone: undefined, text: `${c.events} warning event${c.events === 1 ? "" : "s"}` }
          : { state: "quiet", n: 0, tone: undefined, text: "no warning events" };
        break;
      case "helm": w = legacyArea(c.helm, "failed", "danger"); break;
      case "flux": w = legacyArea(c.flux, "not ready", "danger"); break;
      case "argo": w = legacyArea(c.argo, "need attention", "warning"); break;
    }
    return { ...meta, ...w };
  }).filter((x) => x.state !== "absent");
}

// summaryOf: the mast's one-line reading - areas needing attention, and the
// gaps, never a sum of objects.
export function attentionSummary(areas: Area[]): { state: "loading" | "attention" | "gaps" | "quiet"; areas: number; tone: "danger" | "warning" | undefined; gaps: number } {
  if (areas.length > 0 && areas.every((x) => x.state === "loading")) return { state: "loading", areas: 0, tone: undefined, gaps: 0 };
  const lit = areas.filter((x) => x.state === "attention");
  const gaps = areas.filter((x) => x.state === "unreadable" || x.state === "unverified" || x.state === "loading").length;
  if (lit.length > 0) return { state: "attention", areas: lit.length, tone: lit.some((x) => x.tone === "danger") ? "danger" : "warning", gaps };
  return { state: gaps > 0 ? "gaps" : "quiet", areas: 0, tone: undefined, gaps };
}

// useClusterAttention keeps the shared summary current while a cluster is
// open: one read on entry and a gentle re-poll; leaving the cluster clears it.
export function useClusterAttention(cluster: string | null): void {
  useEffect(() => {
    if (!cluster) return;
    void fetchAttention(cluster);
    const t = setInterval(() => void fetchAttention(cluster), attentionRepollMs);
    return () => {
      clearInterval(t);
      useFleet.getState().clearAttention();
    };
  }, [cluster]);
}

// Opening the navigation tree from elsewhere (the mast Attention button).
// The Attention button opens it on the first destination needing attention.
export const OPEN_TREE_EVENT = "klyx:open-nav-tree";
export const openNavTree = (focus: "attention" | "current" = "attention") =>
  window.dispatchEvent(new CustomEvent(OPEN_TREE_EVENT, { detail: { focus } }));
