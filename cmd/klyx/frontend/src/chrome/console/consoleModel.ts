import type { Command, CommandTarget } from "../commands";
import type { InspectorPinValue } from "../../nav/inspectorPin";
import type { IndexClusterCoverage, IndexHit } from "../../bridge/fleetIndex";
import type { AspectKind } from "../primitives/Aspect";
import { aspectOfVerdict } from "../primitives/Aspect";
import { lensOfKey } from "./openHit";

// The console's model (The Line slice 9 note §4.1, parent §6): four groups -
// Objects (the fleet index), Run (verbs), Go to (places, clusters, views
// and resources) and Ask (the chat) - and the words its rows and coverage
// line say.

export type ConsoleGroup = "Objects" | "Run" | "Go to" | "Ask";
export const CONSOLE_GROUPS: ConsoleGroup[] = ["Objects", "Run", "Go to", "Ask"];

// Application commands that act rather than go somewhere.
const RUN_APP = new Set(["terminal", "zoom:in", "zoom:out", "zoom:reset", "shortcuts"]);

export function consoleGroup(c: Command): ConsoleGroup {
  if (c.id.startsWith("chat:")) return "Ask";
  if (c.group === "Actions") return "Run";
  if (c.group === "Objects") return "Objects";
  if (c.group === "Application" && (RUN_APP.has(c.id) || c.id.startsWith("theme:"))) return "Run";
  return "Go to";
}

// The index serves these kinds' objects fleet-wide; the loaded-data rows of
// the open cluster remain only for kinds it does not index (Helm releases).
const INDEXED_OBJECT = /^(pod|node|workload|flux|argo|gateway|gateway-route):/;
export const coveredByIndex = (c: Command) => c.group === "Objects" && INDEXED_OBJECT.test(c.id);

/** matchesPin: a verb whose object is the open inspector's. */
export function matchesPin(t: CommandTarget | undefined, pin: InspectorPinValue | null): boolean {
  if (!t || !pin || t.cluster !== pin.cluster) return false;
  if (t.uid && pin.uid) return t.uid === pin.uid;
  return t.kind === pin.kind && (t.namespace ?? "") === (pin.namespace ?? "") && t.name === pin.name;
}

const clock = (iso?: string) => {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
};

/** hitAspect: a current role's mark; anything else is never healthy. */
export function hitAspect(h: IndexHit): AspectKind {
  return h.role.state === "current" ? aspectOfVerdict(h.role.role ?? "") : "checking";
}

const STATE_WORD: Record<string, string> = {
  stale: "stale", capped: "capped", oversize: "stopped at an oversized object", watchDenied: "not followed",
  partial: "partly indexed",
};

/** hitNote: namespace, the role's word, and the kind's state when not live. */
export function hitNote(h: IndexHit): string {
  const role = h.role.state === "current"
    ? (h.role.role && h.role.role !== "ok" ? h.role.label || h.role.role : "")
    : h.role.state === "lastObserved"
      ? `last observed ${h.role.role ?? "unknown"}${h.role.at ? ` at ${clock(h.role.at)}` : ""}`
      : "health unverified";
  const kind = h.kindState && h.kindState !== "live" ? `${STATE_WORD[h.kindState] ?? h.kindState}${h.asOf ? ` as of ${clock(h.asOf)}` : ""}` : "";
  return [h.namespace, role, kind].filter(Boolean).join(" · ");
}

const KIND_GAP: Record<string, (entries: number) => string> = {
  indexing: () => "still indexing",
  capped: (n) => `capped at ${n.toLocaleString()}`,
  oversize: () => "stopped at an oversized object",
  denied: () => "not permitted",
  watchDenied: () => "not followed (watch not permitted)",
  unsupported: () => "not served as metadata",
  stale: () => "stale",
  // coverage S7a: the Observer's budget left objects of the kind out
  partial: (n) => `partly indexed (${n.toLocaleString()} kept within Klyx's budget; more exist)`,
};

const usable = (word: string) => word === "connected" || word.startsWith("connected · ");

/**
 * coverageNotes names every gap in what was searched: clusters not indexed
 * or not connected, and kinds that are partial, refused or stale - each with
 * the lens to search instead.
 */
export function coverageNotes(cov: IndexClusterCoverage[]): string[] {
  const out: string[] = [];
  for (const c of cov) {
    if (!c.indexed) {
      out.push(`${c.cluster}: not indexed (${c.connection || "not connected"})`);
      continue;
    }
    if (!usable(c.connection)) out.push(`${c.cluster}: ${c.connection || "not connected"} - results as of its last read`);
    for (const k of c.kinds) {
      const gap = KIND_GAP[k.state];
      if (gap) out.push(`${c.cluster}: ${k.kind} ${gap(k.entries)} - search in ${lensOfKey(k.key)}`);
    }
  }
  return out;
}

/** coverageLine: "Searching 7 clusters · 14 kinds". */
export function coverageLine(clusters: number, kinds: number): string {
  return `Searching ${clusters} cluster${clusters === 1 ? "" : "s"} · ${kinds} kind${kinds === 1 ? "" : "s"}`;
}
