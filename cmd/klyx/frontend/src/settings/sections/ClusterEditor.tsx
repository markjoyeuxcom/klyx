import { useEffect, useId, useRef, useState } from "react";
import type { ClusterDraft, ClusterEntry } from "../../bridge/fleetPlans";
import { useFleet } from "../../store/fleet";
import { openEndpointSettings, type EndpointKind } from "../EndpointSettingsLink";
import { SettingField } from "../SettingField";

// The cluster editor (Settings S2 §5): one cluster's S2 fields as
// fleet.yaml says them - context, kubeconfig, region, group, environment,
// tags and protection - edited as a draft. Review and apply plans the draft
// against what runs; nothing is written until the plan is applied. The
// name is read-only: renaming is a later slice.

const input: React.CSSProperties = { width: "100%", fontFamily: "var(--font-mono)", fontSize: 12 };
const small: React.CSSProperties = { padding: "1px 8px", fontSize: 11 };
const KIND_LABEL: Record<string, string> = { metrics: "Metrics", alertmanager: "Alertmanager", loki: "Loki" };

// ProtectionStatus is a cluster's protection as published now, with a held
// attempt's different protection.
export type ProtectionStatus = Pick<ClusterEntry, "protectedNow" | "protectedBy" | "pendingProtected" | "pendingProtectedBy">;

// protectionNow says the published protection, what decides it, and a held
// attempt's different protection, which applies only once it connects
// (Codex's review of S2c, R5).
export function protectionNow(e: ProtectionStatus): string {
  const now = !e.protectedNow ? "Not protected now." : e.protectedBy ? `Protected now, decided by ${e.protectedBy}.` : "Protected now, as published when it last connected.";
  if (e.pendingProtected === undefined || e.pendingProtected === null) return now;
  return `${now} Once its new connection is usable: ${e.pendingProtected ? `protected, decided by ${e.pendingProtectedBy}` : "not protected"}.`;
}

// tagsText is the tags other than region, one key=value a line.
function tagsText(tags: Record<string, string>): string {
  return Object.keys(tags).filter((k) => k !== "region").sort().map((k) => `${k}=${tags[k]}`).join("\n");
}

// parseTags reads key=value lines; a line without "=" is reported.
export function parseTags(text: string): { tags: Record<string, string>; bad: string[] } {
  const tags: Record<string, string> = {};
  const bad: string[] = [];
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line) continue;
    const at = line.indexOf("=");
    if (at <= 0) {
      bad.push(line);
      continue;
    }
    tags[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  return { tags, bad };
}

export function ClusterEditor({ entry, busy, onReview, onCancel, onDraftChange, status }: {
  entry: ClusterEntry;
  busy: boolean;
  // the protection in force now, read again with the fleet, over the one
  // the entry was opened with: the draft stays the editor's own (Codex's
  // re-review of S2c, RR2)
  status?: ProtectionStatus;
  onReview: (draft: ClusterDraft) => void;
  onCancel: () => void;
  // told of every change to the draft, with whether it now differs from
  // the entry: its plan is withdrawn, and leaving asks (Codex's review of
  // S2c, R1 and R2)
  onDraftChange?: (changed: boolean) => void;
}) {
  const d = entry.draft;
  const st: ProtectionStatus = status ?? entry;
  const [context, setContext] = useState(d.context);
  const [kubeconfig, setKubeconfig] = useState(d.kubeconfig);
  const [region, setRegion] = useState(d.tags.region ?? "");
  const [group, setGroup] = useState(d.group);
  const [environment, setEnvironment] = useState(d.environment);
  const [tags, setTags] = useState(tagsText(d.tags));
  const [isProtected, setProtected] = useState(d.protected);
  const contextsId = useId();
  const parsed = parseTags(tags);
  const opened = useRef(true);
  useEffect(() => {
    if (opened.current) {
      opened.current = false;
      return;
    }
    onDraftChange?.(context !== d.context || kubeconfig !== d.kubeconfig || region !== (d.tags.region ?? "") || group !== d.group
      || environment !== d.environment || tags !== tagsText(d.tags) || isProtected !== d.protected);
    // the draft's fields only: the entry is fixed for this editor's life
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, kubeconfig, region, group, environment, tags, isProtected]);
  // the contexts offered are those of the kubeconfig the entry names; a
  // kubeconfig typed since has not been read, so nothing is suggested from
  // it, and the plan checks the context against it (R6)
  const otherKubeconfig = kubeconfig.trim() !== d.kubeconfig.trim();
  const review = () => {
    const t = { ...parsed.tags };
    if (region.trim()) t.region = region.trim();
    onReview({ context, kubeconfig, group, environment, tags: t, protected: isProtected });
  };
  const name = entry.name;
  if (entry.error) {
    return <div data-testid="cluster-editor-error" role="alert" style={{ color: "var(--color-text-danger)", fontSize: 12 }}>▲ {entry.error}</div>;
  }
  return (
    <div data-testid="cluster-editor" role="group" aria-label={`Edit ${name}`} style={{ gridColumn: "1 / -1", padding: "8px 0 4px", fontFamily: "var(--font-sans)" }}>
      {!entry.inFile && (
        <div role="status" style={{ color: "var(--color-text-warning)", fontSize: 12, marginBottom: 6 }}>
          ▲ fleet.yaml has no entry for {name}: use the file&apos;s values to remove it, or add it back by hand.
        </div>
      )}
      <SettingField id="clusters[].name" instance={name} value={name} />
      <SettingField id="clusters[].context" instance={name} source="file"
        note={otherKubeconfig
          ? `No contexts are suggested for the kubeconfig typed below: it has not been read. Review checks that the context is in it.`
          : entry.contextsError ? `The kubeconfig could not be read: ${entry.contextsError}`
            : `Suggested from ${d.kubeconfig || "the default kubeconfig"}.`}>
        {(ids) => (
          <>
            <input id={ids.id} aria-describedby={ids.describedBy} list={contextsId} style={input} value={context} disabled={busy}
              placeholder={`empty connects through the context named ${name}`} onChange={(e) => setContext(e.target.value)} />
            <datalist id={contextsId}>{otherKubeconfig ? null : entry.contexts.map((c) => <option key={c} value={c} />)}</datalist>
          </>
        )}
      </SettingField>
      <SettingField id="clusters[].kubeconfig" instance={name} source={d.kubeconfig ? "file" : "default"}>
        {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} style={input} value={kubeconfig} disabled={busy} placeholder="the default kubeconfig" onChange={(e) => setKubeconfig(e.target.value)} />}
      </SettingField>
      <SettingField id="clusters[].tags" instance={name} label="Region (tags.region)" source="file">
        {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} style={input} value={region} disabled={busy} onChange={(e) => setRegion(e.target.value)} />}
      </SettingField>
      <SettingField id="clusters[].group" instance={name} source="file">
        {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} style={input} value={group} disabled={busy} onChange={(e) => setGroup(e.target.value)} />}
      </SettingField>
      <SettingField id="clusters[].environment" instance={name} source="file">
        {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} style={input} value={environment} disabled={busy} onChange={(e) => setEnvironment(e.target.value)} />}
      </SettingField>
      <SettingField id="clusters[].tags" instance={`${name}-other`} label="Other tags" source="file"
        note={parsed.bad.length > 0 ? `Each line needs key=value: ${parsed.bad.join(", ")}` : "One key=value a line. Comments inside the tags block are not kept."}>
        {(ids) => <textarea id={ids.id} aria-describedby={ids.describedBy} rows={3} style={input} value={tags} disabled={busy} onChange={(e) => setTags(e.target.value)} />}
      </SettingField>
      <SettingField id="clusters[].protected" instance={name} source={d.protected ? "file" : st.protectedBy ? "inferred" : "default"}
        note={<>
          <span data-testid="cluster-protection-now">{protectionNow(st)}</span>
          {entry.locked && <> <span data-testid="cluster-protection-locked">The {entry.locked}.</span></>}
          {" "}Protection ORs its inputs: clearing this never removes protection another input infers; the plan says the effect.
        </>}>
        {(ids) => (
          <label style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: 12 }}>
            <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={isProtected} disabled={busy} onChange={(e) => setProtected(e.target.checked)} />
            protected: true
          </label>
        )}
      </SettingField>
      <div data-testid="cluster-endpoints" style={{ display: "flex", gap: 6, alignItems: "center", flexWrap: "wrap", fontSize: 12, marginTop: 6 }}>
        <span className="t-secondary">Endpoints, edited in fleet.yaml and tested under Observability:</span>
        {(entry.endpoints ?? []).length === 0
          ? <button type="button" className="l-btn" style={small} onClick={() => useFleet.getState().openSettings("observability")}>none for {name}; open Observability</button>
          : (entry.endpoints ?? []).map((k) => (
            <button key={k} type="button" className="l-btn" style={small} aria-label={`${name}'s ${KIND_LABEL[k] ?? k} endpoint in Observability`}
              onClick={() => openEndpointSettings(name, k as EndpointKind)}>{KIND_LABEL[k] ?? k} in Observability</button>
          ))}
      </div>
      <div style={{ display: "flex", gap: 8, marginTop: 6 }}>
        <button className="l-btn l-btn-primary" disabled={busy || parsed.bad.length > 0} onClick={review}>Review and apply</button>
        <button className="l-btn" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}
