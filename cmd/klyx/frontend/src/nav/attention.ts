import { useEffect } from "react";
import { useFleet } from "../store/fleet";
import type { AttentionSlice, ClusterDTO, LensCount, VerdictLensCount } from "../store/fleet";
import { describeObservation } from "../health/verdict";
import { abandonAttention, attentionRepollMs, fetchAttention } from "../bridge/attention";
import { capState, type CapKey } from "./registry";

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
  // Coverage is independent of attention: a known fault AND capped or
  // unclassified rows is both lit and a gap.
  gap: boolean;
  stale: boolean;         // the latest read failed; this is the last good one
  at: number | null;      // when this area's value was read
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

type Reading = Pick<Area, "state" | "n" | "tone" | "text"> & { gap?: boolean };

function verdictArea(c: VerdictLensCount): Reading {
  if (c === "unreadable") return { state: "unreadable", n: 0, tone: undefined, text: "could not be read" };
  const d = describeObservation(c);
  if (d.attention > 0) return { state: "attention", n: d.attention, tone: d.tone === "danger" ? "danger" : "warning", text: d.text, gap: d.gap };
  if (!d.quiet) return { state: "unverified", n: 0, tone: undefined, text: d.text, gap: true };
  return { state: "quiet", n: 0, tone: undefined, text: "none need attention" };
}

// Helm: failed releases fault; pending past Klyx's deadline "appear stuck"
// (degraded) - the Helm lens's own rule.
function helmArea(c: LensCount, stuck: number): Reading {
  if (c === "unreadable") return { state: "unreadable", n: 0, tone: undefined, text: "could not be read" };
  if (c === "absent") return { state: "absent", n: 0, tone: undefined, text: "not installed" };
  if (c + stuck === 0) return { state: "quiet", n: 0, tone: undefined, text: "none failed or stuck" };
  const parts = [c > 0 ? `${c} failed` : "", stuck > 0 ? `${stuck} appear${stuck === 1 ? "s" : ""} stuck` : ""].filter(Boolean);
  return { state: "attention", n: c + stuck, tone: c > 0 ? "danger" : "warning", text: parts.join(" · ") };
}

// Flux, Argo and Helm are not on the shared verdict yet: their legacy counts
// keep their own wording (Flux "not ready" still includes progressing).
function legacyArea(c: LensCount, word: string, tone: "danger" | "warning"): Reading {
  if (c === "unreadable") return { state: "unreadable", n: 0, tone: undefined, text: "could not be read" };
  if (c === "absent") return { state: "absent", n: 0, tone: undefined, text: "not installed" };
  if (c > 0) return { state: "attention", n: c, tone, text: `${c} ${word}` };
  return { state: "quiet", n: 0, tone: undefined, text: `none ${word}` };
}

const hhmm = (t: number) => { const d = new Date(t); const p = (n: number) => String(n).padStart(2, "0"); return `${p(d.getHours())}:${p(d.getMinutes())}`; };

export function attentionAreas(a: AttentionSlice | null | undefined, cluster: string): Area[] {
  const mine = a && a.cluster === cluster ? a : null;
  return AREAS.map((meta): Area => {
    if (!mine || (mine.loading && mine.at === null)) return { ...meta, state: "loading", n: 0, tone: undefined, text: "checking…", gap: true, stale: false, at: null };
    const c = mine.counts;
    let w: Reading;
    switch (meta.id) {
      case "workloads": w = verdictArea(c.workloads); break;
      case "pods": w = verdictArea(c.pods); break;
      case "nodes": w = verdictArea(c.nodes); break;
      case "events":
        w = c.events === "unreadable" || c.events === "absent" ? { state: "unreadable", n: 0, tone: undefined, text: "could not be read" }
          : c.events > 0 ? { state: "evidence", n: c.events, tone: undefined, text: `${c.events} warning event${c.events === 1 ? "" : "s"}` }
          : { state: "quiet", n: 0, tone: undefined, text: "no warning events" };
        break;
      case "helm": w = helmArea(c.helm, c.helmStuck ?? 0); break;
      case "flux": w = legacyArea(c.flux, "not ready", "danger"); break;
      case "argo": w = legacyArea(c.argo, "need attention", "warning"); break;
    }
    const stale = (mine.stale ?? []).includes(meta.id);
    const at = mine.areaAt?.[meta.id] ?? null;
    const text = stale && at ? `${w.text} (last good ${hhmm(at)}, latest read failed)` : w.text;
    return { ...meta, ...w, text, gap: !!w.gap || stale, stale, at };
  }).filter((x) => x.state !== "absent");
}

// Installed domains this summary does not read (spec §4 requires every
// supported category; until the backend summary - health slice 1c - exists
// they are named, never implied checked). Their lenses read them live.
export type Uncovered = { id: string; label: string; groupId: string; destinationId: string };
const UNCOVERED: (Uncovered & { cap: CapKey })[] = [
  { id: "certs", label: "Certificates", groupId: "certs", destinationId: "certs", cap: "certManager" },
  { id: "eso", label: "External secrets", groupId: "eso", destinationId: "eso", cap: "externalSecrets" },
  { id: "gateway", label: "Gateways", groupId: "network", destinationId: "gateway", cap: "gatewayAPI" },
  { id: "karpenter", label: "Karpenter", groupId: "autoscaling", destinationId: "karpenter", cap: "karpenter" },
];
export function uncoveredDomains(c: ClusterDTO | undefined): Uncovered[] {
  return UNCOVERED.filter((u) => capState(c, u.cap) === "present").map((u) => ({ id: u.id, label: u.label, groupId: u.groupId, destinationId: u.destinationId }));
}

// attentionSummary: the mast's one-line reading - areas needing attention, and the
// gaps, never a sum of objects.
export function attentionSummary(areas: Area[]): { state: "loading" | "attention" | "gaps" | "quiet"; areas: number; tone: "danger" | "warning" | undefined; gaps: number } {
  if (areas.length > 0 && areas.every((x) => x.state === "loading")) return { state: "loading", areas: 0, tone: undefined, gaps: 0 };
  const lit = areas.filter((x) => x.state === "attention");
  // A gap counts whether or not the area is also lit.
  const gaps = areas.filter((x) => x.gap || x.state === "unreadable" || x.state === "unverified" || x.state === "loading").length;
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
      abandonAttention(); // a reply for the cluster just left is dropped
      useFleet.getState().clearAttention();
    };
  }, [cluster]);
}

// Opening the navigation tree from elsewhere (the mast Attention button).
// The Attention button opens it on the first destination needing attention.
export const OPEN_TREE_EVENT = "klyx:open-nav-tree";
export const openNavTree = (focus: "attention" | "current" = "attention") =>
  window.dispatchEvent(new CustomEvent(OPEN_TREE_EVENT, { detail: { focus } }));
