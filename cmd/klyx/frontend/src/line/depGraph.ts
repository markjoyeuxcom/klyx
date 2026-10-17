import type { DepGraph, DepNode, DepPair, DepSupply, DepWrite } from "../bridge/depGraph";
import type { NotHereYetResult } from "../bridge/notHereYet";
import type { AspectKind, RailSegment } from "../chrome/primitives";

// The dependency graph's model (The Line slice 8c, parent §5.1): the graph
// as a tree of steps from the selected node, in the parent's words. A node
// reached twice is drawn once; later it is a reference ("see above"). Every
// claim is a qualified observation: "upstream failure", "reported blocker"
// and "reported chain ends here" are the controllers' own reports, supply
// facts and change proximity are stated without causality, and "root
// cause" is never said.

export type DepEvidence = {
  condition?: string; // the failing condition, qualified by its generation
  supply: string[]; // supply facts
  proximity: string[]; // change proximity, two qualified times side by side
  writes: string[]; // the node's last recorded managed-field update (and its last main-resource one when that is older)
  inventory?: string; // "last applied <sha>; inventory N objects (M workloads)"
};

export type DepStep = {
  key: string;
  id: string; // the node's ID
  segment: RailSegment;
  segmentWords?: string;
  name: string;
  kind?: string; // the node's kind when its name leads with it (the drawing sets it apart)
  aspect: AspectKind;
  tags: string[];
  evidence: DepEvidence;
  chainEnd: boolean;
  ref?: boolean; // a node already drawn above
  git?: boolean; // the node has Git facts ("not here yet" asks for it)
  branches: DepStep[][];
};

// hhmm: a 24-hour clock, as the parent words times ("20:21").
const hhmm = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });

// shortSha abbreviates a complete commit id - sha1 (40 hex) or sha256 (64
// hex), bare or after "<ref>@sha1:" / "sha256:" - from its beginning.
export const shortSha = (rev: string) => {
  const m = /(?:^|[@:])([0-9a-f]{64}|[0-9a-f]{40})$/.exec(rev);
  return m ? m[1].slice(0, 7) : rev;
};

const INCOMPLETE = new Set(["depth", "node limit", "read limit", "unreadable", "replaced", "cancelled"]);

export function nodeName(n: DepNode): string {
  return n.stub && !n.name ? n.note ?? n.stub : `${n.kind} ${n.namespace ? `${n.namespace}/` : ""}${n.name}`;
}

// kindOf: the kind nodeName leads with, when it leads with one.
function kindOf(n: { kind: string; name: string; stub?: string }): string | undefined {
  return n.stub && !n.name ? undefined : n.kind || undefined;
}

// writeText: the last recorded managed-field update, never an audit entry:
// a manager is a workflow identifier, and nothing says which field changed.
export function writeText(w: DepWrite | undefined, what = "last managed-field update"): string {
  if (!w) return "no managed-field record";
  const sub = w.statusUpdate ? ", status update" : w.subresource ? `, subresource ${w.subresource}` : "";
  return `${what} ${hhmm(w.time)} (${w.manager}, ${w.operation}${sub})`;
}

// writeLines: the actual last update, and the last main-resource one when
// that is a different, older record (change proximity reads only that).
export function writeLines(last: DepWrite | undefined, spec: DepWrite | undefined): string[] {
  const lines = [writeText(last)];
  if (last && spec && (spec.time !== last.time || spec.manager !== last.manager || spec.operation !== last.operation || spec.subresource !== last.subresource)) {
    lines.push(writeText(spec, "last main-resource update"));
  }
  return lines;
}

// generationText qualifies a condition that describes an older spec.
const generationText = (n: DepNode) =>
  n.conditionGeneration > 0 && n.conditionGeneration < n.generation ? ` - for generation ${n.conditionGeneration}; the spec is at generation ${n.generation}` : "";

export function conditionText(n: DepNode): string | undefined {
  if (n.ready !== "False") return undefined;
  const at = n.conditionTime ? ` since ${hhmm(n.conditionTime)} (condition time)` : "";
  return `Ready=False${at}${n.reason ? `: ${n.reason}` : ""}${n.message ? ` - ${n.message}` : ""}${generationText(n)}`;
}

// supplyLines: the supply fact, then the managed-field records of what was
// read - the Secret's and its supplier's - or their honest absence.
export function supplyLines(s: DepSupply): string[] {
  const lines = [supplyText(s)];
  if (!s.error && !s.notFound) lines.push(`Secret ${s.namespace}/${s.name}: ${writeText(s.lastWrite)}`);
  if (s.supplier) lines.push(`ExternalSecret ${s.supplier.namespace}/${s.supplier.name}: ${writeText(s.supplier.lastWrite)}`);
  return lines;
}

export function supplyText(s: DepSupply): string {
  const secret = `Secret ${s.namespace}/${s.name}`;
  if (s.error) return `${secret} (${s.field}) could not be read: ${s.error}`;
  if (s.notFound) return `${secret} (${s.field}) not found`;
  if (s.supplier) {
    const p = s.supplier;
    const state = p.ready === "True" ? "Ready" : p.ready === "False" ? `not ready${p.reason ? `: ${p.reason}` : ""}` : "readiness unknown";
    return `Credential supplied by ExternalSecret ${p.namespace}/${p.name} (${state})`;
  }
  return `${secret} (${s.field}) (no supplier recorded${s.supplierNote ? `: ${s.supplierNote}` : ""})`;
}

// proximityText: "Secret platform-auth: last managed-field update 20:20
// (external-secrets, Update). Source Ready=False since 20:21 (condition
// time)." Both qualified; neither a cause.
export function proximityText(p: DepPair, failing: DepNode | undefined): string {
  const obj = p.object.split("/");
  const who = obj.length === 3 ? `${obj[0]} ${obj[1]}/${obj[2]}` : p.object;
  const node = failing ? `${failing.kind} ${failing.namespace}/${failing.name}` : p.failing;
  const gen = failing ? generationText(failing) : "";
  return `${who}: last managed-field update ${hhmm(p.write.time)} (${p.write.manager}, ${p.write.operation}). ${node} Ready=False since ${hhmm(p.conditionTime)} (condition time${gen ? gen.replace(/^ - /, "; ") : ""}).`;
}

function aspectOf(n: DepNode): AspectKind {
  if (n.stub === "not found") return "fault";
  if (n.stub && INCOMPLETE.has(n.stub)) return "checking";
  if (n.failing) return "fault";
  if (n.stale) return "syncing";
  if (n.suspended) return "paused";
  if (n.ready === "True") return "none";
  return "checking";
}

function tagsOf(n: DepNode, start: boolean): string[] {
  const t: string[] = [];
  if (n.upstreamFailure && !start) t.push("upstream failure");
  if (n.chainEnd) t.push("reported chain ends here");
  if (n.stub === "not found") t.push("not found");
  if (n.stale) t.push(`status at generation ${n.observedGeneration} of ${n.generation}`);
  if (n.suspended) t.push("suspended");
  if (n.declaredProducer) {
    const p = n.declaredProducer;
    t.push(`produced by ${p.kind} ${p.namespace}/${p.name}; Klyx does not read that kind's health`);
  } else if (n.kind === "ExternalArtifact" && n.generatorUid && !n.stub) t.push("producer recorded by identity only");
  return t;
}

// producerSegment draws a producer edge by its evidence (S1c §2): a
// verified or named generator as a source, a disagreeing one as
// incomplete - drawn as such, never as a verified edge.
function producerSegment(e: { producer?: string }): [RailSegment, string] {
  switch (e.producer) {
    case "verified":
      return ["source", "produced by (the identity it records)"];
    case "named":
      return ["source", "produced by (named by the artifact; its identity is not recorded)"];
    case "disagree":
      return ["incomplete", "the producer reference and the recorded identity disagree: not the generator this artifact records"];
  }
  return ["incomplete", `producer: ${e.producer ?? "not read"}`];
}

// depTree builds the steps from the selected node outward.
export function depTree(g: DepGraph): DepStep[] {
  const byId = new Map(g.nodes.map((n) => [n.id, n]));
  const out = new Map<string, typeof g.edges>();
  for (const e of g.edges) out.set(e.from, [...(out.get(e.from) ?? []), e]);
  const pairs = new Map<string, DepPair[]>();
  for (const p of g.proximity) pairs.set(p.failing, [...(pairs.get(p.failing) ?? []), p]);
  const drawn = new Set<string>();

  const step = (id: string, segment: RailSegment, words: string | undefined, path: string): DepStep => {
    const n = byId.get(id);
    const key = `${path}>${id}`;
    if (!n) return { key, id, segment, name: id, aspect: "checking", tags: [], evidence: { supply: [], proximity: [], writes: [] }, chainEnd: false, branches: [] };
    const incomplete = !!n.stub && INCOMPLETE.has(n.stub);
    const seg: RailSegment = incomplete ? "incomplete" : segment;
    const segWords = incomplete ? `incomplete: ${n.stub}` : words;
    if (drawn.has(id)) {
      return { key, id, segment: seg, segmentWords: segWords, name: `${nodeName(n)} (see above)`, kind: kindOf(n), aspect: aspectOf(n), tags: [], evidence: { supply: [], proximity: [], writes: [] }, chainEnd: false, ref: true, branches: [] };
    }
    drawn.add(id);
    const start = id === g.start;
    const evidence: DepEvidence = {
      condition: conditionText(n),
      supply: (n.supply ?? []).flatMap(supplyLines),
      proximity: (pairs.get(id) ?? []).map((p) => proximityText(p, n)),
      writes: n.stub ? [] : writeLines(n.lastWrite, n.lastSpecWrite),
      inventory: n.inventory ? `${n.inventory.lastApplied ? `last applied ${shortSha(n.inventory.lastApplied)}` : "no applied revision recorded"}; inventory ${n.inventory.entries} objects (${n.inventory.workloads} workloads)` : undefined,
    };
    const branches = (out.get(id) ?? []).map((e) => [
      e.cycle ? cycleStep(e.to, e.reported, key)
        : e.type === "producer" ? step(e.to, ...producerSegment(e), key)
        : step(e.to, e.reported ? "reported" : e.type === "source" ? "source" : "dependsOn", e.reported ? `reported blocker (${e.type === "source" ? "source" : "depends on"})` : undefined, key),
    ]);
    return {
      key, id, segment: seg, segmentWords: segWords, name: incomplete && n.name ? `${nodeName(n)}: ${n.note ?? n.stub}` : nodeName(n), kind: kindOf(n),
      aspect: aspectOf(n), tags: tagsOf(n, start), evidence, chainEnd: n.chainEnd, git: !!n.git, branches,
    };
  };
  // A cycle is an edge back to a node on its own path: cut here, and named.
  // A reported cycle keeps its report: it is still the controller's words.
  const cycleStep = (to: string, reported: boolean, path: string): DepStep => {
    const n = byId.get(to);
    return { key: `${path}>cycle>${to}`, id: to, segment: "incomplete", segmentWords: reported ? "incomplete: cycle, reported blocker" : "incomplete: cycle", name: `cycle: back to ${n ? nodeName(n) : to} (see above)`,
      aspect: "checking", tags: [], evidence: { supply: [], proximity: [], writes: [] }, chainEnd: false, ref: true, branches: [] };
  };
  if (!g.start) return [];
  return [step(g.start, "chain", undefined, "")];
}

// notHereText words a node's Git result.
export function notHereText(r: NotHereYetResult | undefined, path: string): string | undefined {
  if (!r) return undefined;
  switch (r.state) {
    case "at":
      return "at T";
    case "unavailable":
      return `not here yet: unavailable (${r.reason ?? "unknown"})`;
    case "behind":
      if (r.commits.length === 0) return `behind T; no commit touches ${path || "the repository"}`;
      return `not here yet: ${r.commits.length}${r.more ? "+" : ""} commit${r.commits.length === 1 && !r.more ? "" : "s"} touch ${path || "the repository"}`;
  }
}
