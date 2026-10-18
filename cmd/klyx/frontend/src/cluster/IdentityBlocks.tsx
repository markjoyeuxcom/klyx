import { Fragment, useEffect, useState } from "react";
import { Aspect } from "../chrome/primitives/Aspect";
import { InspectorBlock } from "../layout/InspectorLayout";
import { clock } from "../layout/coverage";
import { useFleet } from "../store/fleet";
import { WELL_KNOWN_RESOURCE_REFS } from "./resourceRefs";
import { readPodIdentity, readServiceAccountIdentity, readStoreIdentity, type IdDeclared, type IdPod, type IdPodReport, type IdReading, type IdSide, type IdStoreReport,
  type PodIdentityDTO, type SAIdentityDTO, type StoreIdentityDTO } from "../bridge/identity";

// Coverage S5 §5: the workload identity blocks. Each reads when its
// inspector opens and again on request. A reply is held with the request it
// answers and shown only while that request is the current one (S3a's
// render-time fence): another object, cluster or refresh never shows the
// last one's reply, not even for a frame.

function useFencedRead<T>(id: string | null, read: () => Promise<T>): [T | null, () => void] {
  const [held, setHeld] = useState<{ id: string; res: T } | null>(null);
  const [n, setN] = useState(0);
  const key = id === null ? null : `${id}|${n}`;
  useEffect(() => {
    if (key === null) return;
    let live = true;
    void read().then((r) => { if (live) setHeld({ id: key, res: r }); });
    return () => { live = false; };
    // the key carries every input of the read
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);
  return [key !== null && held && held.id === key ? held.res : null, () => setN((x) => x + 1)];
}

const ADAPTER: Record<string, string> = { aws: "AWS", azure: "Azure", gke: "GKE" };
// A reading that disagrees with its declaration is a look-at (the hollow
// mark), never a fault: identity is on demand and raises nothing (S5 §5).
// A reading Klyx could not settle is said in secondary text.
export const LOOK_AT = new Set(["differs", "not-configured", "not-opted-in", "partial", "both-aws"]);
const QUIET = new Set(["unresolved", "excluded", "configured-undeclared", "unlinked", "host-network", "match-client"]);

export const NOT_READ: Record<string, string> = {
  replaced: "This object was replaced since it was opened: its identity is not shown for the new one.",
  "not-found": "Not found: it no longer exists.",
  unreadable: "Could not read its identity.",
};

function Declarations({ d }: { d: IdDeclared }) {
  const rows: [string, string][] = [];
  if (d.roleArn) rows.push(["AWS role (IRSA)", d.roleArn.shown + (d.audience ? ` · audience ${d.audience.shown}` : "")]);
  if (d.azureClient) rows.push(["Azure client ID", d.azureClient.shown + (d.azureTenant ? ` · tenant ${d.azureTenant.shown}` : "")]);
  if (d.azureExpiryOutOfRange) rows.push(["Azure token expiration", "outside 3600-86400: the webhook rejects such pods"]);
  if (d.gkeServiceAccount) rows.push(["GKE linked account", d.gkeServiceAccount.shown]);
  return (
    <div className="insp-kv" data-testid="identity-declared">
      {rows.length === 0 ? <><span>declares</span><span>no identity annotations</span></> : rows.map(([k, v]) => <Fragment key={k}><span>{k}</span><span style={{ overflowWrap: "anywhere" }}>{v}</span></Fragment>)}
      <span>EKS Pod Identity</span><span>declares nothing on a ServiceAccount; its associations live in AWS</span>
      {d.automount !== undefined && <><span>automount</span><span>{d.automount ? "on" : "off"} (the webhooks' own token volumes are separate)</span></>}
    </div>
  );
}

function ReadingLine({ r }: { r: IdReading }) {
  return (
    <div className="t-body" data-testid={`identity-reading-${r.state}`} style={{ overflowWrap: "anywhere", display: "flex", gap: 6, alignItems: "baseline" }}>
      {LOOK_AT.has(r.state) && <Aspect kind="attention" label="look at" />}
      <span>
        <span className="t-legend" style={{ marginRight: 6 }}>{ADAPTER[r.adapter] ?? r.adapter}{r.container ? ` · ${r.container}` : ""}</span>
        <span className={QUIET.has(r.state) ? "t-secondary" : undefined}>{r.text}</span>
      </span>
    </div>
  );
}

function SideLine({ s }: { s: IdSide }) {
  const m = s.management;
  return (
    <div className="t-body" data-testid="identity-side" style={{ overflowWrap: "anywhere" }}>
      {s.fault && <span style={{ marginRight: 6 }}><Aspect kind="fault" label="fault" /></span>}
      <span className="t-legend" style={{ marginRight: 6 }}>{s.component}</span>
      <span>{s.health}</span>
      <div className="t-secondary">
        manager: {m.manager}{m.managerEvidence ? ` (${m.managerEvidence})` : ""} · health {m.observed === "observed" ? "observed" : "not directly observed"}{m.observedEvidence ? ` (${m.observedEvidence})` : ""}
        {s.note ? ` · ${s.note}` : ""}
      </div>
    </div>
  );
}

function PodEvidence({ r, open }: { r: IdPodReport; open?: (p: IdPod) => void }) {
  const p = r.pod;
  return (
    <div style={{ display: "grid", gap: 2, paddingBottom: 6 }} data-testid={`identity-pod-${p.name}`}>
      <div className="t-body">
        {open ? <button onClick={() => open(p)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}>{p.name}</button> : p.name}
        <span className="t-secondary"> · created {clock(p.created)}{p.node ? ` · on ${p.node}` : ""}{p.containersCut ? ` · ${p.containersCut} containers not read` : ""}{p.volumesCut ? " · volumes cut at the bound" : ""}</span>
      </div>
      {(r.readings ?? []).length === 0 ? <div className="t-secondary">No identity configured, and none declared.</div> : (r.readings ?? []).map((x, i) => <ReadingLine key={i} r={x} />)}
      {(r.nodes ?? []).map((s, i) => <SideLine key={i} s={s} />)}
    </div>
  );
}

// Every block can be read again, in every state: a failed read is retried
// here, never left as the last word.
function ReadAgain({ at, reading, onRead }: { at?: string; reading: boolean; onRead: () => void }) {
  return (
    <div className="t-secondary" style={{ marginTop: 4 }}>
      {at ? `Read at ${clock(at)}. ` : ""}<button className="l-chip" onClick={onRead} disabled={reading}>{reading ? "Reading…" : "Read again"}</button>
    </div>
  );
}

function Limits({ limits }: { limits: string[] }) {
  if (limits.length === 0) return null;
  return <ul className="t-secondary" data-testid="identity-limits" style={{ margin: "4px 0 0", paddingLeft: 16 }}>{limits.map((l) => <li key={l}>{l}</li>)}</ul>;
}

// Cross-links: a pod opens UID-pinned in the Pods lens (a replacement is
// said there, never the same-name pod substituted); a ServiceAccount opens
// in the generic inspector.
const openPod = (cluster: string) => (p: IdPod) => {
  const nav = useFleet.getState();
  nav.setSection("pods");
  nav.requestNavIntent({ kind: "select-pod", cluster, namespace: p.namespace, name: p.name, uid: p.uid });
};

const openServiceAccount = (ns: string, name: string) => {
  const nav = useFleet.getState();
  nav.openResource(WELL_KNOWN_RESOURCE_REFS["/ServiceAccount"]);
  nav.openInstance(ns, name);
};

/** The ServiceAccount inspector's Identity block. */
export function ServiceAccountIdentity({ cluster, namespace, name, uid, active }: { cluster: string; namespace: string; name: string; uid: string; active: boolean }) {
  const [dto, refresh] = useFencedRead<SAIdentityDTO>(active && uid ? `${cluster}|${namespace}|${name}|${uid}` : null, () => readServiceAccountIdentity(cluster, namespace, name, uid));
  const r = dto?.report;
  return (
    <InspectorBlock title="Identity">
      <div data-testid="identity-sa">
        {!dto ? <div className="t-secondary">Reading identity…</div>
          : dto.error ? <div className="t-secondary">{dto.error}</div>
          : r && r.state !== "read" ? <div className="t-secondary">{NOT_READ[r.state] ?? r.state}{r.error ? ` (${r.error})` : ""}</div>
          : r && <>
            {r.declared && <Declarations d={r.declared} />}
            <div className="t-legend" style={{ marginTop: 6 }}>Pods using it</div>
            {r.podsRead === "unreadable" ? <div className="t-secondary">Its pods could not be read: none can be said to exist or not.</div>
              : r.pods.length === 0 ? <div className="t-secondary">No pods run as this ServiceAccount.</div>
              : <>
                {r.podsRead === "more" && <div className="t-secondary">More pods than the {r.pods.length + (dto.podsDropped ?? 0)} read; the count is unknown. The page is the first by name, newest first within it.</div>}
                {(dto.podsDropped ?? 0) > 0 && <div className="t-secondary">{dto.podsDropped} pods not shown (the reply's bound).</div>}
                {r.pods.map((p) => <PodEvidence key={p.pod.uid} r={p} open={openPod(cluster)} />)}
              </>}
            {(r.webhooks ?? []).length > 0 && <><div className="t-legend" style={{ marginTop: 6 }}>Webhooks</div>{r.webhooks!.map((s, i) => <SideLine key={i} s={s} />)}</>}
            {(r.stores.length > 0 || r.storesRead === "more") && <div className="t-legend" style={{ marginTop: 6 }}>External Secrets stores using it</div>}
            {r.stores.map((s) => (
              <div key={s.uid} className="t-body" data-testid="identity-store">{s.kind} {s.namespace ? `${s.namespace}/` : ""}{s.name}
                <span className="t-secondary"> · {s.provider} {s.mode} · {s.via === "controller" ? "no auth: the controller runs as this ServiceAccount" : s.referent ? "for ExternalSecrets in this namespace" : "serviceAccountRef"}</span></div>
            ))}
            {r.storesRead === "more" && <div className="t-secondary">The stores were not read completely.</div>}
            {r.storesRead === "cut" && <div className="t-secondary">More stores than shown.</div>}
            <Limits limits={r.limits} />
          </>}
        <ReadAgain at={r?.readAt || undefined} reading={!dto} onRead={refresh} />
      </div>
    </InspectorBlock>
  );
}

/** The Pod inspector's Identity block. */
export function PodIdentity({ cluster, namespace, name, uid, active }: { cluster: string; namespace: string; name: string; uid: string; active: boolean }) {
  const [dto, refresh] = useFencedRead<PodIdentityDTO>(active && uid ? `${cluster}|${namespace}|${name}|${uid}` : null, () => readPodIdentity(cluster, namespace, name, uid));
  const r = dto?.report;
  return (
    <InspectorBlock title="Identity">
      <div data-testid="identity-pod">
        {!dto ? <div className="t-secondary">Reading identity…</div>
          : dto.error ? <div className="t-secondary">{dto.error}</div>
          : r && r.state !== "read" ? <div className="t-secondary">{NOT_READ[r.state] ?? r.state}</div>
          : r && <>
            {r.declared ? <div className="t-body">ServiceAccount <button onClick={() => openServiceAccount(r.declared!.namespace, r.declared!.name)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}>{r.declared.name}</button></div>
              : <div className="t-secondary">Its ServiceAccount could not be read ({r.saState}): nothing is compared.</div>}
            {r.report && <PodEvidence r={r.report} />}
            {(r.webhooks ?? []).map((s, i) => <SideLine key={i} s={s} />)}
            <Limits limits={r.limits} />
          </>}
        <ReadAgain at={r?.readAt || undefined} reading={!dto} onRead={refresh} />
      </div>
    </InspectorBlock>
  );
}

function mechText(p: IdPod): string {
  const parts: string[] = [];
  for (const c of p.containers) {
    for (const m of c.mechanisms ?? []) {
      const id = m.role?.shown ?? m.client?.shown ?? (m.kind === "pod-identity" ? "Pod Identity" : "");
      parts.push(`${c.name}: ${m.kind}${id && m.kind !== "pod-identity" ? ` ${id}` : ""}${m.state !== "complete" ? ` (${m.state})` : ""}`);
    }
  }
  return parts.length > 0 ? parts.join("; ") : "no identity in the pod spec";
}

/** The ESO store inspector's "Authenticates as" block. */
export function StoreAuthenticatesAs({ cluster, kind, namespace, name, uid, active }: { cluster: string; kind: string; namespace: string; name: string; uid: string; active: boolean }) {
  const [dto, refresh] = useFencedRead<StoreIdentityDTO>(active && uid ? `${cluster}|${kind}|${namespace}|${name}|${uid}` : null, () => readStoreIdentity(cluster, kind, namespace, name, uid));
  const r: IdStoreReport | undefined = dto?.report;
  const s = r?.subject;
  return (
    <InspectorBlock title="Authenticates as">
      <div data-testid="identity-store-auth">
        {!dto ? <div className="t-secondary">Reading…</div>
          : dto.error ? <div className="t-secondary">{dto.error}</div>
          : r && r.state !== "read" ? <div className="t-secondary">{NOT_READ[r.state] ?? r.state}</div>
          : r && s && <>
            <div className="insp-kv">
              <span>provider</span><span>{s.provider} · {s.mode}</span>
              <span>base</span><span style={{ overflowWrap: "anywhere" }}>{s.base}</span>
              {s.serviceAccount && <><span>ServiceAccount</span><span>{s.serviceAccount.referent ? `${s.serviceAccount.name} in each ExternalSecret's own namespace`
                : <button onClick={() => openServiceAccount(s.serviceAccount!.namespace ?? "", s.serviceAccount!.name)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}>{s.serviceAccount.namespace}/{s.serviceAccount.name}</button>}</span></>}
              {(s.requested ?? []).length > 0 && <><span>then</span><span style={{ overflowWrap: "anywhere" }}>{s.requested!.join(" → ")}</span></>}
            </div>
            {(s.refusals ?? []).map((x) => <div key={x} className="t-body" data-testid="identity-refusal"><span style={{ marginRight: 6 }}><Aspect kind="attention" label="look at" /></span>{x}</div>)}
            {(s.unresolved ?? []).map((x) => <div key={x} className="t-secondary">Unresolved: {x}</div>)}
            {(s.notes ?? []).map((x) => <div key={x} className="t-secondary">{x}</div>)}
            {r.handlers && <>
              <div className="t-legend" style={{ marginTop: 6 }}>Handling controller</div>
              {r.handlers.unresolved ? <div className="t-secondary" data-testid="identity-handler-unresolved">Not resolved: {r.handlers.unresolved}. The subject is not named.</div>
                : (r.handlers.groups ?? []).map((g) => (
                  <div key={g.candidate + g.serviceAccount} className="t-body" data-testid="identity-handler">
                    {g.candidate} as <button onClick={() => openServiceAccount(g.namespace, g.serviceAccount)} style={{ background: "none", border: 0, padding: 0, cursor: "pointer", color: "var(--color-text-info)", font: "inherit" }}>{g.namespace}/{g.serviceAccount}</button>
                    {g.pods.map((p) => <div key={p.uid} className="t-secondary">{p.name}: {mechText(p)}</div>)}
                  </div>
                ))}
              {(r.handlers.groups ?? []).length > 1 && <div className="t-secondary">Several controllers handle this store; each authenticates as its own identity.</div>}
            </>}
            {(r.envGaps ?? []).map((x) => <div key={x} className="t-secondary">{x}</div>)}
            <Limits limits={r.limits} />
          </>}
        <ReadAgain at={r?.readAt || undefined} reading={!dto} onRead={refresh} />
      </div>
    </InspectorBlock>
  );
}
