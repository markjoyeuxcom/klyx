import { useEffect, useMemo, useRef, useState } from "react";
import { IconAlertTriangle, IconShieldCheck, IconShieldLock, IconShieldX } from "@tabler/icons-react";
import { useFleet } from "../store/fleet";
import type { AccessGrantDTO, AccessQueryDTO, AccessVerdictDTO, AccessWhoCanDTO } from "../store/fleet";
import { canI, whoCan } from "../bridge/rbac";
import { useNavIntent } from "../chrome/useNavIntent";
import { EmptyState } from "../chrome/EmptyState";
import { useListKeys } from "../chrome/useListKeys";
import { LensLayout, LensTable, TABLE_ROW_HEIGHT, type DatasetState } from "../layout/LensLayout";
import { InspectorBlock, InspectorLayout } from "../layout/InspectorLayout";
import { fitColumns, type ColumnDef } from "../layout/columns";

// AccessView - the RBAC access-review lens on the workspace template (spec
// §6 step 5). A query tool, not a dataset: the controls row is the question,
// the body holds the answers. Two answers with DIFFERENT authority, framed as
// such:
//   - Can I? is a SubjectAccessReview evaluated BY THE API SERVER - the
//     authoritative verdict across every authorizer. A virtual POST; nothing
//     is persisted.
//   - Who can? is a static enumeration of the RBAC objects - it can
//     under-report (webhook/ABAC authorizers are invisible to it) but never
//     over-report an RBAC grant. It ignores the subject fields.
// Every answer is pinned to the exact question and cluster it answered: an
// edited form marks it as answering a previous question, a late answer for
// another cluster or an older request is dropped. Read-only throughout.

const VERBS = ["get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "impersonate", "escalate", "bind", "use"];
// Kubernetes names are case-sensitive: WebKit must never auto-capitalize or
// auto-correct a typed resource, group, verb object or namespace.
const RAW = { autoCapitalize: "off", autoCorrect: "off", spellCheck: false } as const;
const cellStyle: React.CSSProperties = { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };

const GRANT_COLUMNS: (ColumnDef & { label: string })[] = [
  { id: "subject", label: "Subject", width: 240, priority: 0, flex: true },
  { id: "binding", label: "Via binding", width: 220, priority: 1, grow: 0.8 },
  { id: "role", label: "Role", width: 200, priority: 1, grow: 0.6 },
];

const grantKey = (g: AccessGrantDTO) => `${g.subjectKind}/${g.subjectNamespace}/${g.subjectName}|${g.bindingKind}/${g.bindingNamespace}/${g.bindingName}|${g.roleKind}/${g.roleName}`;
const queryKey = (q: AccessQueryDTO) => JSON.stringify(q);

// describeQuery names a question in words, so a pinned answer says exactly
// what it answered.
export function describeQuery(q: AccessQueryDTO, withSubject: boolean): string {
  const res = [q.resource || "(no resource)", q.subresource ? `/${q.subresource}` : ""].join("");
  const grp = q.group ? `.${q.group}` : "";
  const obj = q.name ? ` "${q.name}"` : "";
  const where = q.namespace ? ` in ${q.namespace}` : " cluster-wide";
  const who = !withSubject ? "" : q.saName ? ` as ServiceAccount ${q.saNamespace}/${q.saName}` : " as you";
  return `${q.verb} ${res}${grp}${obj}${where}${who}`;
}

type Pinned<T> = { cluster: string; key: string; query: AccessQueryDTO; result: T };

function openBinding(g: AccessGrantDTO) {
  const namespaced = g.bindingKind === "RoleBinding";
  useFleet.getState().openResource({ group: "rbac.authorization.k8s.io", version: "v1", plural: namespaced ? "rolebindings" : "clusterrolebindings", kind: g.bindingKind, scope: namespaced ? "Namespaced" : "Cluster" });
  useFleet.getState().openInstance(g.bindingNamespace, g.bindingName);
}
function openRole(g: AccessGrantDTO) {
  const namespaced = g.roleKind === "Role";
  useFleet.getState().openResource({ group: "rbac.authorization.k8s.io", version: "v1", plural: namespaced ? "roles" : "clusterroles", kind: g.roleKind, scope: namespaced ? "Namespaced" : "Cluster" });
  useFleet.getState().openInstance(namespaced ? g.bindingNamespace : "", g.roleName);
}

export function AccessView({ cluster }: { cluster: string }) {
  const [asSA, setAsSA] = useState(false);
  const [saNamespace, setSaNamespace] = useState("");
  const [saName, setSaName] = useState("");
  const [verb, setVerb] = useState("get");
  const [group, setGroup] = useState("");
  const [resource, setResource] = useState("");
  const [subresource, setSubresource] = useState("");
  const [name, setName] = useState("");
  const [namespace, setNamespace] = useState("");
  const [verdict, setVerdict] = useState<Pinned<AccessVerdictDTO> | null>(null);
  const [grants, setGrants] = useState<Pinned<AccessWhoCanDTO> | null>(null);
  const [busy, setBusy] = useState<"cani" | "whocan" | null>(null);
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [selKey, setSelKey] = useState<string | null>(null);
  const [bodyWidth, setBodyWidth] = useState(1100);
  const gen = useRef(0);

  // The component is reused across clusters: answers never cross over.
  const enteredCluster = useRef(cluster);
  useEffect(() => {
    if (enteredCluster.current === cluster) return;
    enteredCluster.current = cluster;
    gen.current++;
    setVerdict(null); setGrants(null); setBusy(null); setSelKey(null); setCursorKey(null);
  }, [cluster]);

  // A ServiceAccount inspector's "check access" cross-link lands here.
  useNavIntent(cluster, ["access-check"], (intent) => {
    if (intent.kind !== "access-check") return "consumed";
    setAsSA(true);
    setSaNamespace(intent.namespace);
    setSaName(intent.name);
    gen.current++;
    setVerdict(null); setGrants(null); setSelKey(null); setBusy(null);
    return "consumed";
  }, []);

  const query: AccessQueryDTO = useMemo(() => ({
    verb: verb.trim(), group: group.trim(), resource: resource.trim(), subresource: subresource.trim(),
    name: name.trim(), namespace: namespace.trim(),
    saNamespace: asSA ? saNamespace.trim() : "", saName: asSA ? saName.trim() : "",
  }), [verb, group, resource, subresource, name, namespace, asSA, saNamespace, saName]);
  // who-can ignores the subject, so its question excludes it
  const whoQuery = useMemo(() => ({ ...query, saNamespace: "", saName: "" }), [query]);

  const run = async (kind: "cani" | "whocan") => {
    const g = ++gen.current;
    const q = kind === "cani" ? query : whoQuery;
    const origin = cluster;
    setBusy(kind);
    try {
      if (kind === "cani") {
        const r = await canI(origin, q);
        if (g === gen.current) setVerdict({ cluster: origin, key: queryKey(q), query: q, result: r });
      } else {
        const r = await whoCan(origin, q);
        if (g === gen.current) { setGrants({ cluster: origin, key: queryKey(q), query: q, result: r }); setSelKey(null); setCursorKey(null); }
      }
    } finally {
      if (g === gen.current) setBusy(null);
    }
  };

  const verdictNow = verdict && verdict.cluster === cluster ? verdict : null;
  const grantsNow = grants && grants.cluster === cluster ? grants : null;
  const verdictOutdated = !!verdictNow && verdictNow.key !== queryKey(query);
  const grantsOutdated = !!grantsNow && grantsNow.key !== queryKey(whoQuery);
  const grantList = useMemo(() => grantsNow && !grantsNow.result.error ? grantsNow.result.grants ?? [] : [], [grantsNow]);
  const selected = selKey ? grantList.find((g) => grantKey(g) === selKey) : undefined;

  const cursorIdx = cursorKey === null ? -1 : grantList.findIndex((g) => grantKey(g) === cursorKey);
  useListKeys({
    count: grantList.length,
    selected: cursorIdx,
    onSelect: (idx) => { const g = grantList[idx]; if (g) setCursorKey(grantKey(g)); },
    onActivate: (idx) => { const g = grantList[idx]; if (g) { setCursorKey(grantKey(g)); setSelKey(grantKey(g)); } },
    onEscape: () => setSelKey(null),
  });

  const fitted = useMemo(() => fitColumns(GRANT_COLUMNS, bodyWidth), [bodyWidth]);
  const show = (id: string) => fitted.visible.has(id);
  const gridStyle = { gridTemplateColumns: fitted.template };

  const asked = !!verdictNow || !!grantsNow;
  // the form lives in the body, so the body always renders; before any
  // answer the explanatory hint sits below the form
  const state: DatasetState = "ready";
  const canRun = busy === null && query.verb !== "" && query.resource !== "" && (!asSA || (query.saNamespace !== "" && query.saName !== ""));

  const form = (
    <form aria-label="Access question" onSubmit={(e) => { e.preventDefault(); if (canRun) void run("cani"); }}
      style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))", gap: "8px 10px", alignItems: "end", padding: "10px 12px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-tertiary)" }}>
      <Field label="Subject" span={2}>
        <div role="group" aria-label="Subject" style={{ display: "inline-flex", gap: 4 }}>
          <button type="button" className="l-chip" aria-pressed={!asSA} onClick={() => setAsSA(false)}>As me</button>
          <button type="button" className="l-chip" aria-pressed={asSA} onClick={() => setAsSA(true)}>As ServiceAccount</button>
        </div>
      </Field>
      {asSA && <Field label="SA namespace"><input {...RAW} className="l-input" value={saNamespace} onChange={(e) => setSaNamespace(e.target.value)} placeholder="e.g. flux-system" aria-label="service account namespace" /></Field>}
      {asSA && <Field label="SA name"><input {...RAW} className="l-input" value={saName} onChange={(e) => setSaName(e.target.value)} placeholder="e.g. kustomize-controller" aria-label="service account name" /></Field>}
      <Field label="Verb">
        <select className="l-input" value={verb} onChange={(e) => setVerb(e.target.value)} aria-label="verb">
          {VERBS.map((v) => <option key={v} value={v}>{v}</option>)}
        </select>
      </Field>
      <Field label="Resource (plural)"><input {...RAW} className="l-input" value={resource} onChange={(e) => setResource(e.target.value)} placeholder="e.g. deployments" aria-label="resource" /></Field>
      <Field label="Namespace"><input {...RAW} className="l-input" value={namespace} onChange={(e) => setNamespace(e.target.value)} placeholder="empty = cluster-wide" aria-label="namespace" /></Field>
      <Field label="API group (advanced)"><input {...RAW} className="l-input" value={group} onChange={(e) => setGroup(e.target.value)} placeholder="empty = core" aria-label="api group" /></Field>
      <Field label="Subresource (advanced)"><input {...RAW} className="l-input" value={subresource} onChange={(e) => setSubresource(e.target.value)} placeholder="e.g. scale" aria-label="subresource" /></Field>
      <Field label="Object name (advanced)"><input {...RAW} className="l-input" value={name} onChange={(e) => setName(e.target.value)} placeholder="empty = any" aria-label="object name" /></Field>
    </form>
  );

  return (
    <LensLayout
      label="Access"
      title="Access check"
      summary={<span className="t-secondary">read-only simulation - nothing is granted, changed or persisted</span>}
      controls={
        <>
          <span className="t-secondary" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 }} title={describeQuery(query, true)}>
            {query.resource ? describeQuery(query, true) : "Set a verb and resource below"}
          </span>
          <span style={{ marginLeft: "auto", display: "inline-flex", gap: 6, flexShrink: 0 }}>
            <button className="l-btn l-btn-primary" disabled={!canRun} onClick={() => void run("cani")}>{busy === "cani" ? "Checking…" : "Can I?"}</button>
            <button className="l-btn" disabled={busy !== null || query.verb === "" || query.resource === ""} onClick={() => void run("whocan")}>{busy === "whocan" ? "Enumerating…" : "Who can?"}</button>
          </span>
        </>
      }
      state={state}
      body={
        <div style={{ display: "flex", flexDirection: "column", gap: 12, minHeight: 0, flex: 1, overflowY: "auto" }}>
          {form}
          {!asked && !busy && (
            <EmptyState
              icon={<IconShieldLock size={28} stroke={1.2} />}
              title="Ask an access question."
              hint="Can I? asks the API server (SubjectAccessReview) - the authoritative answer across every authorizer. Who can? enumerates RBAC bindings - it can miss webhook or ABAC grants, but never invents one. Set a verb and resource to start."
            />
          )}
          {verdictNow && <VerdictPanel pinned={verdictNow} outdated={verdictOutdated} />}
          {grantsNow && (
            <section data-testid="access-whocan" aria-label="Who can" style={{ display: "flex", flexDirection: "column", gap: 6, minHeight: 0, flex: 1 }}>
              <div className="t-body" style={{ display: "flex", gap: 8, flexWrap: "wrap", alignItems: "baseline" }}>
                <span className="t-status">Who can {describeQuery(grantsNow.query, false)}</span>
                {!grantsNow.result.error && <span className="t-secondary">{grantList.length === 0 ? "no RBAC grant matches" : `${grantList.length} grant path${grantList.length === 1 ? "" : "s"}`}</span>}
              </div>
              {grantsOutdated && <div role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The form has changed since this answer - it answers the question above. Run Who can? again for the new one.</div>}
              {grantsNow.result.error ? (
                <ErrorLine text={grantsNow.result.error} />
              ) : (
                <>
                  {grantsNow.result.clusterWideOnly && <div className="t-secondary" style={{ color: "var(--color-text-warning)" }}>Cluster-wide grants only - set a namespace to include RoleBindings.</div>}
                  {grantList.length > 0 && (
                    <LensTable minWidth={fitted.minWidth} onWidth={setBodyWidth}>
                      <div className="lens-table-head" role="row" style={gridStyle}>
                        {GRANT_COLUMNS.filter((c) => show(c.id)).map((c) => <span key={c.id}>{c.label}</span>)}
                      </div>
                      <div style={{ flex: 1, minHeight: 0, overflowY: "auto" }}>
                        {grantList.map((g) => {
                          const k = grantKey(g);
                          return (
                            <div key={k} role="button" tabIndex={0} className="lens-row" style={{ ...gridStyle, height: TABLE_ROW_HEIGHT }}
                              aria-selected={k === cursorKey} data-cursor={k === cursorKey ? "true" : undefined} data-selected={k === selKey ? "true" : undefined}
                              onClick={() => { setCursorKey(k); setSelKey(k === selKey ? null : k); }}
                              onKeyDown={(ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); setSelKey(k); } }}>
                              {show("subject") && <span className="lens-cell-name"><span style={{ color: "var(--color-text-tertiary)" }}>{g.subjectKind} </span>{g.subjectNamespace ? `${g.subjectNamespace}/` : ""}{g.subjectName}</span>}
                              {show("binding") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}><span style={{ color: "var(--color-text-tertiary)" }}>{g.bindingKind} </span>{g.bindingNamespace ? `${g.bindingNamespace}/` : ""}{g.bindingName}</span>}
                              {show("role") && <span style={{ ...cellStyle, color: "var(--color-text-secondary)" }}><span style={{ color: "var(--color-text-tertiary)" }}>{g.roleKind} </span>{g.roleName}</span>}
                            </div>
                          );
                        })}
                      </div>
                    </LensTable>
                  )}
                </>
              )}
              <div className="t-legend">static enumeration of (Cluster)RoleBindings - webhook/ABAC authorizers are invisible to it; it can under-report, never over-report RBAC</div>
            </section>
          )}
        </div>
      }
      inspector={selected ? <GrantInspector g={selected} onClose={() => setSelKey(null)} /> : undefined}
      inspectorLabel="Grant path details"
      onCloseInspector={() => setSelKey(null)}
    />
  );
}

function Field({ label, span, children }: { label: string; span?: number; children: React.ReactNode }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 3, minWidth: 0, gridColumn: span ? `span ${span}` : undefined }}>
      <span className="t-secondary" aria-hidden="true">{label}</span>
      {children}
    </div>
  );
}

function ErrorLine({ text }: { text: string }) {
  return (
    <div className="t-body" style={{ display: "flex", alignItems: "flex-start", gap: 6, color: "var(--color-text-danger)" }}>
      <IconAlertTriangle size={13} stroke={1.5} style={{ flexShrink: 0, marginTop: 2 }} />
      <span style={{ overflowWrap: "anywhere" }}>{text}</span>
    </div>
  );
}

// An answer is not a cluster fault: Allowed/Denied are words with a shield
// shape, never a lamp.
function VerdictPanel({ pinned, outdated }: { pinned: Pinned<AccessVerdictDTO>; outdated: boolean }) {
  const v = pinned.result;
  return (
    <section data-testid="access-verdict" aria-label="Can I" className="t-body" style={{ display: "grid", gap: 6, padding: "10px 12px", borderRadius: 4, background: "var(--color-background-secondary)", boxShadow: "inset 0 0 0 1px var(--color-border-tertiary)" }}>
      <div className="t-status">Can I {describeQuery(pinned.query, true)}?</div>
      {outdated && <div role="status" className="t-secondary" style={{ color: "var(--color-text-warning)" }}>The form has changed since this answer - it answers the question above. Run Can I? again for the new one.</div>}
      {v.error ? <ErrorLine text={v.error} /> : (
        <>
          <div style={{ display: "flex", alignItems: "center", gap: 8, flexWrap: "wrap" }}>
            {v.allowed
              ? <span className="t-status" style={{ display: "inline-flex", alignItems: "center", gap: 5, color: "var(--color-text-success)" }}><IconShieldCheck size={15} stroke={1.75} /> Allowed</span>
              : <span className="t-status" style={{ display: "inline-flex", alignItems: "center", gap: 5, color: "var(--color-text-danger)" }}><IconShieldX size={15} stroke={1.75} /> Denied</span>}
            {!v.allowed && <span className="t-secondary">{v.denied ? "an authorizer explicitly denied it" : "no authorizer allowed it (default deny)"}</span>}
          </div>
          {v.subject && <div className="t-secondary" style={{ fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}>checked as {v.subject}</div>}
          {v.reason && <div style={{ overflowWrap: "anywhere" }}>{v.reason}</div>}
          {v.evaluationError && <div style={{ color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>evaluation error: {v.evaluationError}</div>}
        </>
      )}
      <div className="t-legend">evaluated by the API server (SubjectAccessReview) - covers RBAC, webhooks and every configured authorizer</div>
    </section>
  );
}

function GrantInspector({ g, onClose }: { g: AccessGrantDTO; onClose: () => void }) {
  return (
    <div data-testid="access-grant-panel" style={{ display: "flex", flexDirection: "column", height: "100%", minHeight: 0 }}>
      <InspectorLayout
        kind={g.subjectKind}
        namespace={g.subjectNamespace || undefined}
        name={g.subjectName}
        meta={`via ${g.bindingKind} ${g.bindingNamespace ? `${g.bindingNamespace}/` : ""}${g.bindingName}`}
        onClose={onClose}
        primary={{ label: `Open ${g.bindingKind}`, onSelect: () => openBinding(g) }}
        secondary={{ label: `Open ${g.roleKind}`, onSelect: () => openRole(g) }}
      >
        <InspectorBlock title="Grant path">
          <div className="insp-kv">
            <span>subject</span><span style={{ overflowWrap: "anywhere" }}>{g.subjectKind} {g.subjectNamespace ? `${g.subjectNamespace}/` : ""}{g.subjectName}</span>
            <span>binding</span><span style={{ overflowWrap: "anywhere" }}>{g.bindingKind} {g.bindingNamespace ? `${g.bindingNamespace}/` : ""}{g.bindingName}</span>
            <span>role</span><span style={{ overflowWrap: "anywhere" }}>{g.roleKind} {g.roleName}</span>
          </div>
          <div className="t-secondary" style={{ marginTop: 8 }}>This path comes from the RBAC objects alone. Can I? is the authoritative check for a specific subject.</div>
        </InspectorBlock>
      </InspectorLayout>
    </div>
  );
}
