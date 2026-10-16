import { useMemo, type ReactNode } from "react";
import { InspectorBlock } from "../layout/InspectorLayout";
import { Age, absoluteTime } from "../chrome/Age";
import { Aspect } from "../chrome/primitives/Aspect";
import { CopyButton } from "../chrome/CopyButton";
import { ObjectTree, ScalarValue, type PathSeg, type TitleOf } from "../chrome/ObjectTree";
import { Arr, Obj, parseLossless, type JValue } from "../chrome/losslessJSON";
import { useFleet, type InstanceDetailDTO, type RelatedRefDTO } from "../store/fleet";
import { detectGitOwner } from "./InstanceDetail";

// The generic inspector's own blocks (The Line slice 15b): the object's
// table row, its object facts and its fields as a tree. All read the one
// snapshot the detail was built from.

// SummaryBlock: the object's printer columns at every priority - kubectl's
// default and -o wide view of this one object. Empty values are left out.
export function SummaryBlock({ d }: { d: InstanceDetailDTO }) {
  const cells = (d.summary ?? []).filter((c) => c.value !== "");
  if (cells.length === 0) return null;
  return (
    <InspectorBlock title="Summary">
      <div className="insp-kv gen-kv" data-testid="instance-summary">
        {cells.map((c, i) => (
          <SummaryRow key={i} label={c.name} title={c.description} value={c.value} numeric={c.numeric} />
        ))}
      </div>
    </InspectorBlock>
  );
}

function SummaryRow({ label, title, value, numeric }: { label: string; title: string; value: string; numeric: boolean }) {
  return (
    <>
      <span title={title || undefined}>{label}</span>
      <span className={numeric ? "gen-val gen-num" : "gen-val"}>{value}</span>
    </>
  );
}

// Finalizers whose controller is known, so a waiting deletion names it.
const FINALIZER_OWNERS: Record<string, string> = {
  kubernetes: "the namespace controller",
  foregroundDeletion: "the garbage collector (foreground deletion)",
  orphan: "the garbage collector (orphaning dependents)",
  "kubernetes.io/pvc-protection": "the PVC protection controller",
  "kubernetes.io/pv-protection": "the PV protection controller",
  "finalizers.fluxcd.io": "the owning Flux controller",
  "resources-finalizer.argocd.argoproj.io": "the Argo CD application controller",
  "external-secrets.io/externalsecret-cleanup": "the External Secrets controller",
};

// ObjectBlock: what the object is, beyond its fields - identity,
// generation against what its controller observed, a deletion in progress
// and what it waits on, owners, who manages it and who wrote it.
export function ObjectBlock({ cluster, d, related }: { cluster: string; d: InstanceDetailDTO; related: RelatedRefDTO[] }) {
  const gen = d.generation ?? 0;
  const observed = d.observedGeneration ?? -1;
  const behind = gen > 0 && observed >= 0 && observed < gen;
  const git = detectGitOwner(d.labels ?? {}, d.annotations ?? {});
  const helm = d.labels?.["app.kubernetes.io/managed-by"] === "Helm" ? (d.annotations?.["meta.helm.sh/release-name"] ?? "a Helm release") : null;
  const owners = d.owners ?? [];
  const openOwner = (kind: string, name: string) => {
    const ref = related.find((r) => r.relation === "owner" && r.kind === kind && r.name === name);
    if (!ref) return null;
    return () => {
      useFleet.getState().openResource({ group: ref.group, version: ref.version, plural: ref.plural, kind: ref.kind, scope: ref.scope });
      useFleet.getState().openInstance(ref.namespace, ref.name);
    };
  };
  const rows: [string, ReactNode][] = [];
  if (d.created) rows.push(["Created", <>{absoluteTime(Date.parse(d.created))} <span className="gen-dim">· <Age iso={d.created} suffix=" ago" /></span></>]);
  if (d.uid) rows.push(["UID", <span className="gen-mono gen-copy">{d.uid}<CopyButton text={d.uid} label="copy UID" size={11} /></span>]);
  if (gen > 0) {
    rows.push(["Generation", behind
      ? <span className="gen-flag"><Aspect kind="syncing" label="not yet observed" /> Not yet observed: generation {gen}, observed {observed}</span>
      : <span className="gen-mono">{gen}{observed >= 0 ? <span className="gen-dim"> · observed</span> : null}</span>]);
  }
  if (d.deleting) {
    rows.push(["Deleting", <span className="gen-flag"><Aspect kind="attention" label="being deleted" /> Deleting since {absoluteTime(Date.parse(d.deleting))} <span className="gen-dim">· <Age iso={d.deleting} suffix=" ago" /></span>
      {(d.finalizers?.length ?? 0) > 0 ? <span className="gen-dim">, waiting on its finalizers</span> : null}</span>]);
  }
  if ((d.finalizers?.length ?? 0) > 0) {
    rows.push(["Finalizers", <ul className="gen-list">{d.finalizers!.map((f) => (
      <li key={f}><span className="gen-mono">{f}</span>{FINALIZER_OWNERS[f] ? <span className="gen-dim"> · {FINALIZER_OWNERS[f]}</span> : null}</li>
    ))}</ul>]);
  }
  if (owners.length > 0) {
    rows.push(["Owners", <ul className="gen-list">{owners.map((o) => {
      const go = openOwner(o.kind, o.name);
      const text = <>{o.kind} <span className="gen-mono">{o.name}</span>{o.controller ? <span className="gen-dim"> · controller</span> : null}</>;
      return <li key={`${o.kind}/${o.name}`}>{go ? <button className="gen-link" onClick={go}>{text}</button> : text}</li>;
    })}</ul>]);
  }
  if (git || helm) rows.push(["Managed by", <>{git ? `${git.tool} ${git.detail}` : null}{git && helm ? " · " : null}{helm ? `Helm release ${helm}` : null}</>]);
  if ((d.managers?.length ?? 0) > 0) {
    rows.push(["Field managers", <ul className="gen-list">{d.managers!.map((m, i) => (
      <li key={i}><span className="gen-mono">{m.manager}</span><span className="gen-dim"> · {m.operation}{m.subresource ? ` · ${m.subresource}` : ""}</span>
        {m.time ? <span className="gen-dim" title={absoluteTime(Date.parse(m.time))}> · <Age iso={m.time} suffix=" ago" /></span> : null}</li>
    ))}</ul>]);
  }
  void cluster;
  if (rows.length === 0) return null;
  return (
    <InspectorBlock title="Object">
      <div className="insp-kv gen-kv" data-testid="instance-object">
        {rows.map(([k, v]) => <ObjRow key={k} label={k}>{v}</ObjRow>)}
      </div>
    </InspectorBlock>
  );
}

function ObjRow({ label, children }: { label: string; children: ReactNode }) {
  return <><span>{label}</span><span className="gen-val">{children}</span></>;
}

const titleCase = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

// Top-level fields beyond spec and status that get a block of their own;
// the rest share one tree (implementation review 1).
export const OWN_FIELD_BLOCKS = 20;

// FieldsBlocks: spec, status and the other top-level fields as trees, in
// that order. A Secret never sends its fields; a large object says why.
// Numbers are drawn as the server wrote them (losslessJSON), and however
// many top-level fields an object has, at most OWN_FIELD_BLOCKS + 2 blocks
// are drawn - the rest are one paged tree.
export function FieldsBlocks({ d, titleOf }: { d: InstanceDetailDTO; titleOf?: TitleOf }) {
  const fields = useMemo<Obj | null>(() => {
    if (!d.fieldsJSON) return null;
    try {
      const v = parseLossless(d.fieldsJSON);
      return v instanceof Obj ? v : null;
    } catch {
      return null;
    }
  }, [d.fieldsJSON]);
  const parts = useMemo(() => {
    if (!fields) return null;
    const named = (k: string) => fields.entries.filter(([key]) => key === k);
    const others = fields.entries.filter(([k]) => k !== "spec" && k !== "status");
    const rest = others.slice(OWN_FIELD_BLOCKS);
    return { own: [...named("spec"), ...named("status"), ...others.slice(0, OWN_FIELD_BLOCKS)], rest: rest.length > 0 ? new Obj(rest) : null };
  }, [fields]);
  if (d.fieldsOmitted === "too large") return <div className="gen-note">Too large to draw here; see Manifest.</div>;
  if (d.fieldsOmitted === "unreadable" || (d.fieldsJSON && !fields)) return <div className="gen-note">The fields could not be drawn; see Manifest.</div>;
  if (!parts) return null;
  return (
    <>
      {parts.own.map(([k, v], i) => (
        <InspectorBlock key={`${i}:${k}`} title={titleCase(k)}>
          <FieldValue value={v} base={[{ k: "p", n: k }]} titleOf={titleOf} />
        </InspectorBlock>
      ))}
      {parts.rest && (
        <InspectorBlock title={`Other fields (${parts.rest.entries.length.toLocaleString()})`}>
          <div className="gen-note" data-testid="fields-rest">
            {OWN_FIELD_BLOCKS} top-level fields have blocks of their own; the other {parts.rest.entries.length.toLocaleString()} share this tree.
          </div>
          <ObjectTree value={parts.rest} titleOf={titleOf} />
        </InspectorBlock>
      )}
    </>
  );
}

function FieldValue({ value, base, titleOf }: { value: JValue; base: PathSeg[]; titleOf?: TitleOf }) {
  if (value instanceof Arr || value instanceof Obj) return <ObjectTree value={value} base={base} titleOf={titleOf} />;
  return <div className="otree-val"><ScalarValue value={value} /></div>;
}
