import { useEffect, useRef, useState } from "react";
import type { SourceDraft, SourcesDraft, Subscription, SubscriptionEdit } from "../../bridge/sources";
import { SettingField } from "../SettingField";
import { parseTags } from "./ClusterEditor";

// The installation and subscription editors (Settings S3a §8): an
// installation's fields, or a cluster's own subscription block, as
// fleet.yaml would say them, edited as a draft. Review and apply plans the
// draft against what runs; nothing is written until the plan is applied.

const input: React.CSSProperties = { width: "100%", fontFamily: "var(--font-mono)", fontSize: 12 };
const editorBox: React.CSSProperties = { padding: "8px 0 4px", fontFamily: "var(--font-sans)" };

type Locator = "service" | "discover" | "endpoint";

const locatorOf = (d: SourceDraft): Locator => (d.endpoint ? "endpoint" : d.discover ? "discover" : "service");

// listText and parseList: a comma-separated list, trimmed, empties dropped.
const listText = (l?: string[]) => (l ?? []).join(", ");
const parseList = (s: string) => s.split(",").map((x) => x.trim()).filter(Boolean);

// matchText is a match, one key=value a line.
export function matchText(m?: Record<string, string>): string {
  return Object.keys(m ?? {}).sort().map((k) => `${k}=${(m ?? {})[k]}`).join("\n");
}

// draftOfForm is the installation the form says: only its locator's fields.
function draftOfForm(f: Form): SourceDraft {
  const d: SourceDraft = { id: f.id.trim() };
  if (f.name.trim()) d.name = f.name.trim();
  if (f.locator !== "endpoint") d.host = f.host.trim();
  if (f.locator === "service") d.serviceRef = { namespace: f.namespace.trim(), name: f.service.trim(), port: f.port.trim(), scheme: f.scheme || undefined, path: f.path.trim() || undefined };
  if (f.locator === "discover") d.discover = true;
  if (f.locator === "endpoint") {
    d.endpoint = f.endpoint.trim();
    if (f.tlsSkipVerify === true) d.tlsSkipVerify = true;
    if (f.authType && f.authType !== UNDECIDED) {
      d.authType = f.authType;
      if (f.authType === "basic" && f.username.trim()) d.authUsername = f.username.trim();
    }
  }
  if (f.tenant.trim()) d.tenant = f.tenant.trim();
  if (f.memberLabel.trim()) d.memberLabel = f.memberLabel.trim();
  const groups = parseList(f.groups);
  const clusters = parseList(f.clusters);
  if (groups.length) d.servesGroups = groups;
  if (clusters.length) d.servesClusters = clusters;
  return d;
}

// An offer's conflict leaves a field undecided (S3a §7: no default winner):
// auth is UNDECIDED, the certificate check null, until the person chooses.
const UNDECIDED = "?";

type Form = {
  id: string; name: string; locator: Locator; host: string;
  namespace: string; service: string; port: string; scheme: string; path: string;
  endpoint: string; tlsSkipVerify: boolean | null; authType: string; username: string;
  tenant: string; memberLabel: string; groups: string; clusters: string;
};

function formOf(d: SourceDraft, unresolved: string[] = []): Form {
  return {
    id: d.id, name: d.name ?? "", locator: locatorOf(d), host: d.host ?? "",
    namespace: d.serviceRef?.namespace ?? "", service: d.serviceRef?.name ?? "", port: d.serviceRef?.port ?? "", scheme: d.serviceRef?.scheme ?? "", path: d.serviceRef?.path ?? "",
    endpoint: d.endpoint ?? "", tlsSkipVerify: unresolved.includes("tlsSkipVerify") ? null : !!d.tlsSkipVerify,
    authType: unresolved.includes("auth") ? UNDECIDED : d.authType ?? "", username: d.authUsername ?? "",
    tenant: d.tenant ?? "", memberLabel: d.memberLabel ?? "", groups: listText(d.servesGroups), clusters: listText(d.servesClusters),
  };
}

// subscriptionWords is a cluster's block in words, for a proposal's list.
export function subscriptionWords(b: Subscription | null): string {
  if (!b) return "its own block removed: by name, group or discovery";
  if (b.off) return "off";
  const parts = [`subscribed to ${b.source}`];
  if (b.scope === "tenant") parts.push("everything in its tenant");
  if (b.tenant) parts.push(`tenant ${b.tenant}`);
  const m = matchText(b.match).split("\n").join(", ");
  if (m) parts.push(`match ${m}`);
  return parts.join(", ");
}

export function InstallationEditor({ initial, op, subscriptions, unresolved = [], conflicts = [], hosts, busy, onReview, onCancel, onDraftChange }: {
  initial: SourceDraft;
  op: "add" | "edit";
  // the subscriptions a proposal or an offer carries with it, listed
  subscriptions?: SubscriptionEdit[];
  // an offer's fields left to the person, with the conflicts that say why
  // (one each, in the same order)
  unresolved?: string[];
  conflicts?: string[];
  hosts: string[];
  busy: boolean;
  onReview: (d: SourcesDraft) => void;
  onCancel: () => void;
  onDraftChange?: (changed: boolean) => void;
}) {
  const [f, setF] = useState<Form>(() => formOf(initial, unresolved));
  const start = useRef(JSON.stringify(formOf(initial, unresolved)));
  const why = (field: string) => (unresolved.length === conflicts.length ? conflicts[unresolved.indexOf(field)] : undefined) ?? "The clusters differ: choose.";
  const undecided = f.authType === UNDECIDED || f.tlsSkipVerify === null;
  const opened = useRef(true);
  useEffect(() => {
    if (opened.current) {
      opened.current = false;
      return;
    }
    onDraftChange?.(JSON.stringify(f) !== start.current);
    // the form only: the initial draft is fixed for this editor's life
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [f]);
  const set = <K extends keyof Form>(k: K) => (v: Form[K]) => setF((cur) => ({ ...cur, [k]: v }));
  const key = initial.id || "new";
  const text = (id: string, k: keyof Form, opts: { label?: string; placeholder?: string; disabled?: boolean; note?: React.ReactNode } = {}) => (
    <SettingField id={id} instance={key} label={opts.label} note={opts.note}>
      {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} style={input} value={f[k] as string} disabled={busy || opts.disabled}
        placeholder={opts.placeholder} onChange={(e) => set(k)(e.target.value as never)} />}
    </SettingField>
  );
  const review = () => onReview({ installation: { op, source: draftOfForm(f) }, subscriptions });
  return (
    <div data-testid="installation-editor" role="group" aria-label={op === "add" ? "Add an installation" : `Edit ${initial.id}`} style={editorBox}>
      {text("sources[].id", "id", { disabled: op === "edit", note: op === "edit" ? "An installation keeps its id: remove it and add another to rename it." : undefined })}
      {text("sources[].name", "name")}
      <SettingField id="sources[].discover" instance={key} label="Where it is read" group>
        {() => (
          <div role="radiogroup" aria-label="Where it is read" style={{ display: "flex", gap: 12, flexWrap: "wrap", fontSize: 12 }}>
            {([["service", "a Service in a host"], ["discover", "discovered in a host"], ["endpoint", "an endpoint"]] as const).map(([v, label]) => (
              <label key={v} style={{ display: "inline-flex", gap: 4, alignItems: "center" }}>
                <input type="radio" name={`locator-${key}`} checked={f.locator === v} disabled={busy} onChange={() => set("locator")(v)} />{label}
              </label>
            ))}
          </div>
        )}
      </SettingField>
      {f.locator !== "endpoint" && (
        <SettingField id="sources[].host" instance={key}>
          {(ids) => (
            <select id={ids.id} aria-describedby={ids.describedBy} style={input} value={f.host} disabled={busy} onChange={(e) => set("host")(e.target.value)}>
              <option value="">choose the host cluster</option>
              {hosts.map((h) => <option key={h} value={h}>{h}</option>)}
            </select>
          )}
        </SettingField>
      )}
      {f.locator === "service" && (<>
        {text("sources[].serviceRef.namespace", "namespace")}
        {text("sources[].serviceRef.name", "service")}
        {text("sources[].serviceRef.port", "port")}
        <SettingField id="sources[].serviceRef.scheme" instance={key}>
          {(ids) => (
            <select id={ids.id} aria-describedby={ids.describedBy} style={input} value={f.scheme} disabled={busy} onChange={(e) => set("scheme")(e.target.value)}>
              <option value="">http (the default)</option>
              <option value="http">http</option>
              <option value="https">https</option>
            </select>
          )}
        </SettingField>
        {text("sources[].serviceRef.path", "path", { placeholder: "none" })}
      </>)}
      {f.locator === "endpoint" && (<>
        {text("sources[].endpoint", "endpoint", { placeholder: "https://am.example" })}
        {unresolved.includes("tlsSkipVerify") ? (
          <SettingField id="sources[].tlsSkipVerify" instance={key} group note={why("tlsSkipVerify")}>
            {() => (
              <div role="radiogroup" aria-label="Installation certificate check" style={{ display: "flex", gap: 12, flexWrap: "wrap", fontSize: 12 }}>
                {([[false, "verify the certificate"], [true, "do not verify it (tlsSkipVerify: true)"]] as const).map(([v, label]) => (
                  <label key={String(v)} style={{ display: "inline-flex", gap: 4, alignItems: "center" }}>
                    <input type="radio" name={`tls-${key}`} checked={f.tlsSkipVerify === v} disabled={busy} onChange={() => set("tlsSkipVerify")(v)} />{label}
                  </label>
                ))}
              </div>
            )}
          </SettingField>
        ) : (
          <SettingField id="sources[].tlsSkipVerify" instance={key}>
            {(ids) => (
              <label style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: 12 }}>
                <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={f.tlsSkipVerify === true} disabled={busy} onChange={(e) => set("tlsSkipVerify")(e.target.checked)} />
                tlsSkipVerify: true
              </label>
            )}
          </SettingField>
        )}
        <SettingField id="sources[].auth.type" instance={key} note={unresolved.includes("auth") ? why("auth") : undefined}>
          {(ids) => (
            <select id={ids.id} aria-describedby={ids.describedBy} style={input} value={f.authType} disabled={busy} onChange={(e) => set("authType")(e.target.value)}>
              {unresolved.includes("auth") && <option value={UNDECIDED} disabled>choose: the clusters differ</option>}
              <option value="">none</option>
              <option value="bearer">bearer</option>
              <option value="basic">basic</option>
            </select>
          )}
        </SettingField>
        {f.authType === "basic" && text("sources[].auth.username", "username")}
      </>)}
      {text("sources[].tenant", "tenant", { placeholder: "none" })}
      {text("sources[].memberLabel", "memberLabel", { placeholder: "none" })}
      {text("sources[].serves[].group", "groups", { label: "Serves the groups", placeholder: "comma-separated" })}
      {text("sources[].serves[].cluster", "clusters", { label: "Serves the clusters", placeholder: "comma-separated" })}
      {subscriptions && subscriptions.length > 0 && (
        <div data-testid="installation-editor-subscriptions" style={{ fontSize: 12, margin: "6px 0" }}>
          <div className="t-secondary">With this plan, each cluster&apos;s own block:</div>
          {subscriptions.map((s) => (
            <div key={s.cluster} style={{ fontFamily: "var(--font-mono)", overflowWrap: "anywhere" }}>{s.cluster}: {subscriptionWords(s.block)}</div>
          ))}
        </div>
      )}
      {undecided && <div data-testid="installation-editor-undecided" style={{ fontSize: 12, color: "var(--color-text-warning)" }}>Choose the installation&apos;s {[f.authType === UNDECIDED && "auth", f.tlsSkipVerify === null && "certificate check"].filter(Boolean).join(" and ")} first: the clusters differ, and neither wins by default.</div>}
      <div style={{ display: "flex", gap: 8, marginTop: 6 }}>
        <button className="l-btn l-btn-primary" disabled={busy || !f.id.trim() || undecided} onClick={review}>Review and apply</button>
        <button className="l-btn" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}

export function SubscriptionEditor({ cluster, initial, installations, busy, onReview, onCancel, onDraftChange }: {
  cluster: string;
  initial: Subscription | null;
  installations: string[];
  busy: boolean;
  onReview: (d: SourcesDraft) => void;
  onCancel: () => void;
  onDraftChange?: (changed: boolean) => void;
}) {
  const [off, setOff] = useState(!!initial?.off);
  const [source, setSource] = useState(initial?.source ?? "");
  const [scopeTenant, setScopeTenant] = useState(initial?.scope === "tenant");
  const [tenant, setTenant] = useState(initial?.tenant ?? "");
  const [match, setMatch] = useState(matchText(initial?.match));
  const parsed = parseTags(match);
  const opened = useRef(true);
  useEffect(() => {
    if (opened.current) {
      opened.current = false;
      return;
    }
    onDraftChange?.(off !== !!initial?.off || source !== (initial?.source ?? "") || scopeTenant !== (initial?.scope === "tenant")
      || tenant !== (initial?.tenant ?? "") || match !== matchText(initial?.match));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [off, source, scopeTenant, tenant, match]);
  const block = (): Subscription => {
    if (off) return { off: true };
    const b: Subscription = { source };
    if (scopeTenant) b.scope = "tenant";
    if (tenant.trim()) b.tenant = tenant.trim();
    if (!scopeTenant && Object.keys(parsed.tags).length) b.match = parsed.tags;
    return b;
  };
  return (
    <div data-testid="subscription-editor" role="group" aria-label={`Edit ${cluster}'s alert subscription`} style={editorBox}>
      <SettingField id="clusters[].alertmanager.off" instance={cluster}>
        {(ids) => (
          <label style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: 12 }}>
            <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={off} disabled={busy} onChange={(e) => setOff(e.target.checked)} />
            off: true
          </label>
        )}
      </SettingField>
      {!off && (<>
        <SettingField id="clusters[].alertmanager.source" instance={cluster}>
          {(ids) => (
            <select id={ids.id} aria-describedby={ids.describedBy} style={input} value={source} disabled={busy} onChange={(e) => setSource(e.target.value)}>
              <option value="">choose an installation</option>
              {installations.map((i) => <option key={i} value={i}>{i}</option>)}
            </select>
          )}
        </SettingField>
        <SettingField id="clusters[].alertmanager.scope" instance={cluster}>
          {(ids) => (
            <label style={{ display: "inline-flex", gap: 6, alignItems: "center", fontSize: 12 }}>
              <input id={ids.id} aria-describedby={ids.describedBy} type="checkbox" checked={scopeTenant} disabled={busy} onChange={(e) => setScopeTenant(e.target.checked)} />
              scope: tenant
            </label>
          )}
        </SettingField>
        <SettingField id="clusters[].alertmanager.tenant" instance={cluster}>
          {(ids) => <input id={ids.id} aria-describedby={ids.describedBy} style={input} value={tenant} disabled={busy} placeholder="the installation's" onChange={(e) => setTenant(e.target.value)} />}
        </SettingField>
        {!scopeTenant && (
          <SettingField id="clusters[].alertmanager.match" instance={cluster}
            note={parsed.bad.length > 0 ? `Each line needs key=value: ${parsed.bad.join(", ")}` : "One key=value a line, over the installation's member label."}>
            {(ids) => <textarea id={ids.id} aria-describedby={ids.describedBy} rows={3} style={input} value={match} disabled={busy} onChange={(e) => setMatch(e.target.value)} />}
          </SettingField>
        )}
      </>)}
      <div style={{ display: "flex", gap: 8, marginTop: 6, flexWrap: "wrap" }}>
        <button className="l-btn l-btn-primary" disabled={busy || (!off && !source) || parsed.bad.length > 0}
          onClick={() => onReview({ subscriptions: [{ cluster, block: block() }] })}>Review and apply</button>
        {initial && <button className="l-btn" disabled={busy} onClick={() => onReview({ subscriptions: [{ cluster, block: null }] })}>Remove its own block…</button>}
        <button className="l-btn" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}
