import { Fragment, useEffect, useId, useRef, useState } from "react";
import { IconX } from "@tabler/icons-react";
import { useFleet, WorkloadCompareDTO } from "../store/fleet";
import { workloadCompare } from "../bridge/workloads";
import { useRestoreFocus } from "../chrome/useRestoreFocus";
import { absoluteTime } from "../chrome/Age";
import "../layout/layout.css";

// FleetCompare is the cross-environment drift view: the same workload
// (kind/namespace/name identity) side by side across every connected cluster,
// ordered dev -> stg -> prd, with differences AGAINST THE ORIGIN COLUMN marked.
// A separate dialog surface (spec §2: dialogs reuse the text roles, not the
// table API). Read-only, read once on open (Refresh re-reads every column).
//
// Evidence rules: a failed lookup is unreadable, never "not deployed"; with
// no readable origin nothing is marked as different; differences are evidence,
// not faults - marked with "≠" (shape, not colour alone), never a lamp.
// Replicas often differ on purpose (prd runs more) and stay neutral; a
// DaemonSet's count is the nodes it is scheduled on, so it is not compared.
// Two revisions are compared only when both were resolved.

const envRank: Record<string, number> = { dev: 0, development: 0, stg: 1, staging: 1, prd: 2, prod: 2, production: 2 };
const rankOfEnv = (env: string) => envRank[env.toLowerCase()] ?? 3;

export type ColumnDiff = {
  replicas: boolean;
  images: Set<string>;      // container names whose image differs
  resources: Set<string>;   // container names whose resources differ
  missing: string[];        // origin containers absent here
  extra: Set<string>;       // containers only here
  revision: boolean;
  otherSpec: boolean;       // pod spec differs beyond images/resources
  any: boolean;
};

/** diffAgainst compares one found column with a found origin column. */
export function diffAgainst(base: WorkloadCompareDTO, r: WorkloadCompareDTO): ColumnDiff {
  const baseBy = new Map(base.containers.map((c) => [c.name, c]));
  const hereNames = new Set(r.containers.map((c) => c.name));
  const images = new Set<string>(), resources = new Set<string>(), extra = new Set<string>();
  for (const c of r.containers) {
    const b = baseBy.get(c.name);
    if (!b) { extra.add(c.name); continue; }
    if (b.image !== c.image) images.add(c.name);
    if (b.resources !== c.resources) resources.add(c.name);
  }
  const missing = base.containers.filter((c) => !hereNames.has(c.name)).map((c) => c.name);
  const replicas = r.kind !== "DaemonSet" && base.desiredReplicas !== r.desiredReplicas;
  const revision = !!base.ownerRevision && !!r.ownerRevision && base.ownerRevision !== r.ownerRevision;
  const containersDiffer = images.size + resources.size + missing.length + extra.size > 0;
  const otherSpec = !!base.specHash && !!r.specHash && base.specHash !== r.specHash && !containersDiffer;
  return { replicas, images, resources, missing, extra, revision, otherSpec, any: replicas || containersDiffer || revision || otherSpec };
}

type Col = { cluster: string; env: string; region: string; result: WorkloadCompareDTO | null };

export function FleetCompare({ origin, kind, namespace, name, onClose }: {
  origin: string; kind: string; namespace: string; name: string; onClose: () => void;
}) {
  const clusters = useFleet((s) => s.clusters);
  const ordered = [...clusters].sort((a, b) => rankOfEnv(a.env) - rankOfEnv(b.env) || a.env.localeCompare(b.env) || a.name.localeCompare(b.name));
  const [results, setResults] = useState<Record<string, WorkloadCompareDTO | null>>({});
  const [readAt, setReadAt] = useState<number | null>(null);
  const gen = useRef(0);
  const titleId = useId();
  const closeRef = useRef<HTMLButtonElement>(null);
  useRestoreFocus();
  useEffect(() => { closeRef.current?.focus(); }, []);

  // One read per cluster; every answer is pinned to the request that asked
  // it, so a Refresh never mixes columns from two reads.
  const read = () => {
    const g = ++gen.current;
    setResults({});
    setReadAt(null);
    for (const c of useFleet.getState().clusters) {
      void workloadCompare(c.name, kind, namespace, name)
        .catch((e): WorkloadCompareDTO => ({ found: false, kind, namespace, name, desiredReplicas: 0, readyReplicas: 0, containers: [], specHash: "", ownerKind: "", ownerNamespace: "", ownerName: "", ownerRevision: "", error: e instanceof Error ? e.message : "compare call failed" }))
        .then((r) => {
          if (g !== gen.current) return;
          setResults((prev) => ({ ...prev, [c.name]: r }));
          setReadAt(Date.now());
        });
    }
  };
  useEffect(read, [kind, namespace, name]);

  const columns: Col[] = ordered.map((c) => ({ cluster: c.name, env: c.env, region: c.region, result: results[c.name] ?? null }));
  const base = results[origin] ?? null;
  const baseOk = !!base && !base.error && base.found;
  const settled = columns.filter((c) => c.result !== null);
  const unreadable = settled.filter((c) => c.result!.error).length;
  const absent = settled.filter((c) => !c.result!.error && !c.result!.found).length;
  const differing = baseOk ? settled.filter((c) => c.cluster !== origin && !c.result!.error && c.result!.found && diffAgainst(base!, c.result!).any).length : 0;
  const pending = columns.length - settled.length;

  const originNote = !base ? `Reading ${origin}…`
    : base.error ? `Differences are not computed: the origin ${origin} could not be read (${base.error}).`
    : !base.found ? `Differences are not computed: ${kind} ${namespace}/${name} is not deployed on the origin ${origin}.`
    : null;

  return (
    <div className="l-dialog-scrim" onClick={onClose}>
      <div role="dialog" aria-modal="true" aria-labelledby={titleId} className="l-dialog l-dialog-wide" onClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => { e.stopPropagation(); if (e.key === "Escape") onClose(); }}>
        <div className="l-dialog-head">
          <div style={{ minWidth: 0 }}>
            <h2 id={titleId} className="t-workspace-title">Fleet compare</h2>
            <div className="t-secondary" style={{ overflowWrap: "anywhere" }}>
              <span className="t-identifier" style={{ color: "var(--color-text-secondary)" }}>{kind} {namespace}/{name}</span> · differences against {origin}
            </div>
          </div>
          <button ref={closeRef} className="l-btn" style={{ width: 28, padding: 0, justifyContent: "center" }} onClick={onClose} aria-label="close fleet compare"><IconX size={14} stroke={1.6} /></button>
        </div>
        <div className="l-dialog-summary" role="status">
          <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{columns.length}</span>
          <span className="t-secondary">cluster{columns.length === 1 ? "" : "s"}</span>
          {baseOk && differing > 0 && <span className="t-status" style={{ color: "var(--color-text-warning)" }}>≠ {differing} differ{differing === 1 ? "s" : ""} from {origin}</span>}
          {baseOk && pending === 0 && differing === 0 && settled.length > 1 && <span className="t-secondary">no differences found in the readable columns</span>}
          {absent > 0 && <span className="t-secondary">{absent} not deployed</span>}
          {unreadable > 0 && <span className="t-secondary">{unreadable} unreadable</span>}
          {pending > 0 && <span className="t-secondary">{pending} reading…</span>}
        </div>
        {originNote && <div className="t-body" role="status" style={{ padding: "0 16px 8px", color: base ? "var(--color-text-warning)" : "var(--color-text-tertiary)", overflowWrap: "anywhere" }}>{originNote}</div>}

        <div className="l-dialog-body">
          <div style={{ display: "grid", gridTemplateColumns: `repeat(${columns.length}, minmax(240px, 1fr))`, gap: 8, alignItems: "start" }}>
            {columns.map((col) => (
              <CompareColumn key={col.cluster} col={col} isOrigin={col.cluster === origin} diff={baseOk && col.result && !col.result.error && col.result.found && col.cluster !== origin ? diffAgainst(base!, col.result) : null} />
            ))}
          </div>
        </div>

        <div className="l-dialog-foot">
          <span className="t-secondary">Same kind/namespace/name on each cluster · read-only · ≠ marks a difference from the origin column{readAt ? ` · read ${absoluteTime(readAt)}` : ""}</span>
          <button className="l-btn" onClick={read} disabled={pending > 0}>{pending > 0 ? "Reading…" : "Refresh"}</button>
        </div>
      </div>
    </div>
  );
}

const DIFF = "var(--color-text-warning)";

function Mark({ on }: { on: boolean }) {
  return on ? <span aria-label="differs from origin" style={{ color: DIFF }}>≠ </span> : null;
}

function CompareColumn({ col, isOrigin, diff }: { col: Col; isOrigin: boolean; diff: ColumnDiff | null }) {
  const r = col.result;
  return (
    <section data-testid={`compare-col-${col.cluster}`} aria-label={`${col.cluster}${isOrigin ? " (origin)" : ""}`} className="gw-node"
      style={{ gap: 8, boxShadow: isOrigin ? "inset 0 0 0 1px var(--color-text-info)" : undefined }}>
      <div style={{ display: "flex", alignItems: "baseline", gap: 6, minWidth: 0, flexWrap: "wrap" }}>
        <span className="t-identifier" style={{ fontWeight: 600, overflowWrap: "anywhere" }}>{col.cluster}</span>
        {col.env && <span className="t-legend">{col.env}</span>}
        {col.region && <span className="t-secondary">{col.region}</span>}
        {isOrigin && <span className="t-secondary" style={{ color: "var(--color-text-info)" }}>origin</span>}
        {diff?.any && <span className="t-status" style={{ color: DIFF, marginLeft: "auto" }}>≠ differs</span>}
      </div>
      {r === null ? <span className="t-secondary">reading…</span>
        : r.error ? <span className="t-body" style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>Could not read: {r.error}</span>
        : !r.found ? <span className="t-secondary">Not deployed on this cluster.</span>
        : (
          <div className="insp-kv" style={{ gridTemplateColumns: "84px minmax(0, 1fr)" }}>
            <span>{r.kind === "DaemonSet" ? "scheduled" : "replicas"}</span>
            <span>
              <Mark on={!!diff?.replicas} />{r.readyReplicas}/{r.desiredReplicas} ready
              {r.kind === "DaemonSet" && <span className="t-secondary" style={{ display: "block" }}>per node count - not compared</span>}
            </span>
            {r.containers.map((ct) => (
              <Fragment key={ct.name}>
                <span>{ct.name}</span>
                <span>
                  <span style={{ display: "block", overflowWrap: "anywhere", color: diff?.images.has(ct.name) ? DIFF : undefined }}><Mark on={!!diff?.images.has(ct.name)} />{ct.image}</span>
                  {ct.resources
                    ? <span className="t-secondary" style={{ display: "block", color: diff?.resources.has(ct.name) ? DIFF : undefined }}><Mark on={!!diff?.resources.has(ct.name)} />{ct.resources}</span>
                    : diff?.resources.has(ct.name) && <span className="t-secondary" style={{ display: "block", color: DIFF }}>≠ no requests/limits</span>}
                  {diff?.extra.has(ct.name) && <span className="t-secondary" style={{ display: "block", color: DIFF }}>≠ only on this cluster</span>}
                </span>
              </Fragment>
            ))}
            {diff && diff.missing.length > 0 && <><span>missing</span><span style={{ color: DIFF }}>≠ {diff.missing.join(", ")} (on the origin)</span></>}
            <span>gitops</span>
            <span>
              {r.ownerKind ? <span style={{ display: "block", overflowWrap: "anywhere" }}>{r.ownerKind} {r.ownerNamespace ? `${r.ownerNamespace}/` : ""}{r.ownerName}</span> : <span className="t-secondary">no GitOps owner label</span>}
              {r.ownerRevision && <span style={{ display: "block", overflowWrap: "anywhere", color: diff?.revision ? DIFF : "var(--color-text-secondary)" }}><Mark on={!!diff?.revision} />{r.ownerRevision}</span>}
              {r.ownerKind && !r.ownerRevision && <span className="t-secondary" style={{ display: "block" }}>{r.ownerRevisionNote || "revision not resolved"} - not compared</span>}
            </span>
            {diff?.otherSpec && <><span>pod spec</span><span style={{ color: DIFF }}>≠ differs beyond images and resources (env, volumes, probes…)</span></>}
          </div>
        )}
    </section>
  );
}
