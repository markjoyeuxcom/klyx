import { useState } from "react";
import { useFleet } from "../store/fleet";
import type { AccessQueryDTO, AccessVerdictDTO, AccessWhoCanDTO } from "../store/fleet";
import { canI, whoCan } from "../bridge/rbac";
import { FilterButton, btn, inputWell, selectWell, frame, well, mono } from "../chrome/primitives";
import { useNavIntent } from "../chrome/useNavIntent";
import { IconShieldCheck, IconShieldX, IconAlertTriangle } from "@tabler/icons-react";

// AccessView is the RBAC access-review lens: the daily "can SA X do verb Y on
// resource Z here?" when a Flux/ESO/Argo controller is denied, plus the
// reverse "who can?". Two answers with DIFFERENT authority, framed as such:
//   - can-i is a SubjectAccessReview evaluated BY THE API SERVER - the
//     authoritative verdict, covering every authorizer. A virtual POST;
//     nothing is persisted, nothing is impersonated beyond the review.
//   - who-can is a static enumeration of the RBAC objects - it can
//     under-report (webhook/ABAC authorizers are invisible to it) but never
//     over-report an RBAC grant.
// Read-only throughout; no protected-cluster gating applies.

const VERBS = ["get", "list", "watch", "create", "update", "patch", "delete", "deletecollection", "impersonate", "escalate", "bind", "use"];

const label: React.CSSProperties = { fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)" };

function FieldCol({ name, children, width }: { name: string; children: React.ReactNode; width?: number }) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 3, width }}>
      <span style={label}>{name}</span>
      {children}
    </div>
  );
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

  const [verdict, setVerdict] = useState<AccessVerdictDTO | null>(null);
  const [enumeration, setEnumeration] = useState<AccessWhoCanDTO | null>(null);
  const [busy, setBusy] = useState<"cani" | "whocan" | null>(null);

  // A ServiceAccount inspector's "check access" cross-link lands here.
  useNavIntent(cluster, ["access-check"], (intent) => {
    if (intent.kind !== "access-check") return "consumed";
    setAsSA(true);
    setSaNamespace(intent.namespace);
    setSaName(intent.name);
    setVerdict(null);
    setEnumeration(null);
    return "consumed";
  }, []);

  const query = (): AccessQueryDTO => ({
    verb: verb.trim(), group: group.trim(), resource: resource.trim(), subresource: subresource.trim(),
    name: name.trim(), namespace: namespace.trim(),
    saNamespace: asSA ? saNamespace.trim() : "", saName: asSA ? saName.trim() : "",
  });

  const runCanI = async () => {
    setBusy("cani");
    try {
      setVerdict(await canI(cluster, query()));
    } finally {
      setBusy(null);
    }
  };

  const runWhoCan = async () => {
    setBusy("whocan");
    try {
      setEnumeration(await whoCan(cluster, query()));
    } finally {
      setBusy(null);
    }
  };

  const openBinding = (g: { bindingKind: string; bindingNamespace: string; bindingName: string }) => {
    const namespaced = g.bindingKind === "RoleBinding";
    useFleet.getState().openResource({
      group: "rbac.authorization.k8s.io", version: "v1",
      plural: namespaced ? "rolebindings" : "clusterrolebindings",
      kind: g.bindingKind, scope: namespaced ? "Namespaced" : "Cluster",
    });
    useFleet.getState().openInstance(g.bindingNamespace, g.bindingName);
  };

  return (
    <div style={{ height: "100%", minHeight: 0, overflowY: "auto", boxSizing: "border-box", padding: 16 }}>
      <div style={{ maxWidth: 860, margin: "0 auto", display: "flex", flexDirection: "column", gap: 12 }}>
        <div style={{ ...frame, padding: 14, display: "flex", flexDirection: "column", gap: 12 }}>
          <div style={{ display: "flex", alignItems: "baseline", gap: 10 }}>
            <span style={{ fontSize: 11, fontWeight: 600, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-secondary)" }}>access check</span>
            <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>read-only simulation — nothing is granted, changed, or persisted</span>
          </div>

          <div style={{ display: "flex", alignItems: "flex-end", gap: 8, flexWrap: "wrap" }}>
            <FieldCol name="subject">
              <div style={{ display: "flex", gap: 4 }}>
                <FilterButton label="me" active={!asSA} onClick={() => setAsSA(false)} />
                <FilterButton label="service account" active={asSA} onClick={() => setAsSA(true)} />
              </div>
            </FieldCol>
            {asSA && (
              <>
                <FieldCol name="sa namespace" width={140}>
                  <input value={saNamespace} onChange={(e) => setSaNamespace(e.target.value)} placeholder="flux-system" aria-label="service account namespace" style={inputWell} />
                </FieldCol>
                <FieldCol name="sa name" width={180}>
                  <input value={saName} onChange={(e) => setSaName(e.target.value)} placeholder="kustomize-controller" aria-label="service account name" style={inputWell} />
                </FieldCol>
              </>
            )}
          </div>

          <div style={{ display: "flex", alignItems: "flex-end", gap: 8, flexWrap: "wrap" }}>
            <FieldCol name="verb" width={130}>
              <select value={verb} onChange={(e) => setVerb(e.target.value)} aria-label="verb" style={selectWell}>
                {VERBS.map((v) => <option key={v} value={v}>{v}</option>)}
              </select>
            </FieldCol>
            <FieldCol name="resource (plural)" width={160}>
              <input value={resource} onChange={(e) => setResource(e.target.value)} placeholder="deployments" aria-label="resource" style={inputWell} />
            </FieldCol>
            <FieldCol name="api group" width={150}>
              <input value={group} onChange={(e) => setGroup(e.target.value)} placeholder="apps (empty = core)" aria-label="api group" style={inputWell} />
            </FieldCol>
            <FieldCol name="subresource" width={110}>
              <input value={subresource} onChange={(e) => setSubresource(e.target.value)} placeholder="scale" aria-label="subresource" style={inputWell} />
            </FieldCol>
            <FieldCol name="object name" width={140}>
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="any" aria-label="object name" style={inputWell} />
            </FieldCol>
            <FieldCol name="namespace" width={140}>
              <input value={namespace} onChange={(e) => setNamespace(e.target.value)} placeholder="cluster-wide" aria-label="namespace" style={inputWell} />
            </FieldCol>
            <div style={{ flex: 1 }} />
            <button onClick={() => void runCanI()} disabled={busy !== null} style={{ ...btn, fontWeight: 600 }}>
              {busy === "cani" ? "checking…" : "can i?"}
            </button>
            <button onClick={() => void runWhoCan()} disabled={busy !== null} style={btn}>
              {busy === "whocan" ? "enumerating…" : "who can?"}
            </button>
          </div>
        </div>

        {verdict && (
          <div data-testid="access-verdict" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 8 }}>
            {verdict.error ? (
              <div style={{ display: "flex", alignItems: "flex-start", gap: 6, fontSize: 11, color: "var(--color-text-danger)" }}>
                <IconAlertTriangle size={13} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
                <span style={{ overflowWrap: "anywhere" }}>{verdict.error}</span>
              </div>
            ) : (
              <>
                <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                  {verdict.allowed ? (
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 5, fontSize: 12, fontWeight: 700, color: "var(--color-text-success)" }}>
                      <IconShieldCheck size={15} stroke={1.75} /> ALLOWED
                    </span>
                  ) : (
                    <span style={{ display: "inline-flex", alignItems: "center", gap: 5, fontSize: 12, fontWeight: 700, color: "var(--color-text-danger)" }}>
                      <IconShieldX size={15} stroke={1.75} /> DENIED
                    </span>
                  )}
                  {!verdict.allowed && (
                    <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>
                      {verdict.denied ? "an authorizer explicitly denied" : "no authorizer allowed it (default deny)"}
                    </span>
                  )}
                </div>
                <div style={{ ...mono, fontSize: 11, color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>checked as {verdict.subject}</div>
                {verdict.reason && <div style={{ fontSize: 11, color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>{verdict.reason}</div>}
                {verdict.evaluationError && (
                  <div style={{ fontSize: 11, color: "var(--color-text-warning)", overflowWrap: "anywhere" }}>evaluation error: {verdict.evaluationError}</div>
                )}
              </>
            )}
            <div style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>
              evaluated by the API server (SubjectAccessReview) — covers RBAC, webhooks, every configured authorizer
            </div>
          </div>
        )}

        {enumeration && (
          <div data-testid="access-whocan" style={{ ...well, padding: 12, display: "flex", flexDirection: "column", gap: 8 }}>
            {enumeration.error ? (
              <div style={{ display: "flex", alignItems: "flex-start", gap: 6, fontSize: 11, color: "var(--color-text-danger)" }}>
                <IconAlertTriangle size={13} stroke={1.5} style={{ flexShrink: 0, marginTop: 1 }} />
                <span style={{ overflowWrap: "anywhere" }}>{enumeration.error}</span>
              </div>
            ) : (
              <>
                <div style={{ fontSize: 11, fontWeight: 600, color: "var(--color-text-secondary)" }}>
                  who can — {enumeration.grants.length === 0 ? "no RBAC grant matches" : `${enumeration.grants.length} grant path${enumeration.grants.length === 1 ? "" : "s"}`}
                </div>
                {enumeration.clusterWideOnly && (
                  <div style={{ fontSize: 10, color: "var(--color-text-warning)" }}>
                    cluster-wide grants only — set a namespace to include RoleBindings
                  </div>
                )}
                {enumeration.grants.map((g, i) => (
                  <button
                    key={i}
                    onClick={() => openBinding(g)}
                    title={`open ${g.bindingKind} ${g.bindingNamespace ? g.bindingNamespace + "/" : ""}${g.bindingName}`}
                    style={{
                      display: "grid", gridTemplateColumns: "minmax(140px, 1fr) minmax(140px, 1fr) minmax(120px, 1fr)",
                      gap: 10, alignItems: "baseline", textAlign: "left", padding: "5px 8px",
                      border: "0.5px solid var(--color-border-tertiary)", borderRadius: 4,
                      background: "var(--color-background-primary)", cursor: "pointer", fontSize: 11,
                    }}
                  >
                    <span style={{ ...mono, color: "var(--color-text-primary)", overflowWrap: "anywhere" }}>
                      <span style={{ color: "var(--color-text-tertiary)" }}>{g.subjectKind} </span>
                      {g.subjectNamespace ? `${g.subjectNamespace}/` : ""}{g.subjectName}
                    </span>
                    <span style={{ ...mono, color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>
                      <span style={{ color: "var(--color-text-tertiary)" }}>via </span>
                      {g.bindingNamespace ? `${g.bindingNamespace}/` : ""}{g.bindingName}
                    </span>
                    <span style={{ ...mono, color: "var(--color-text-secondary)", overflowWrap: "anywhere" }}>
                      <span style={{ color: "var(--color-text-tertiary)" }}>{g.roleKind} </span>
                      {g.roleName}
                    </span>
                  </button>
                ))}
              </>
            )}
            <div style={{ fontSize: 9, color: "var(--color-text-tertiary)" }}>
              static enumeration of (Cluster)RoleBindings — webhook/ABAC authorizers are invisible to it; it can under-report, never over-report RBAC
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
