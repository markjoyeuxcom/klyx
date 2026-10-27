import { useId, useState } from "react";
import type { ClusterDraft, ClusterEntry } from "../../bridge/fleetPlans";
import { SettingField } from "../SettingField";

// The cluster editor (Settings S2 §5): one cluster's S2 fields as
// fleet.yaml says them - context, kubeconfig, region, group, environment,
// tags and protection - edited as a draft. Review and apply plans the draft
// against what runs; nothing is written until the plan is applied. The
// name is read-only: renaming is a later slice.

const input: React.CSSProperties = { width: "100%", fontFamily: "var(--font-mono)", fontSize: 12 };

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

export function ClusterEditor({ entry, busy, onReview, onCancel }: {
  entry: ClusterEntry;
  busy: boolean;
  onReview: (draft: ClusterDraft) => void;
  onCancel: () => void;
}) {
  const d = entry.draft;
  const [context, setContext] = useState(d.context);
  const [kubeconfig, setKubeconfig] = useState(d.kubeconfig);
  const [region, setRegion] = useState(d.tags.region ?? "");
  const [group, setGroup] = useState(d.group);
  const [environment, setEnvironment] = useState(d.environment);
  const [tags, setTags] = useState(tagsText(d.tags));
  const [isProtected, setProtected] = useState(d.protected);
  const contextsId = useId();
  const parsed = parseTags(tags);
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
        note={entry.contextsError ? `The kubeconfig could not be read: ${entry.contextsError}` : undefined}>
        {(ids) => (
          <>
            <input id={ids.id} aria-describedby={ids.describedBy} list={contextsId} style={input} value={context} disabled={busy}
              placeholder={`empty connects through the context named ${name}`} onChange={(e) => setContext(e.target.value)} />
            <datalist id={contextsId}>{entry.contexts.map((c) => <option key={c} value={c} />)}</datalist>
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
      <SettingField id="clusters[].protected" instance={name} source={d.protected ? "file" : entry.protectedBy ? "inferred" : "default"}
        note={<>
          {entry.protectedBy ? `Protected now, decided by ${entry.protectedBy}.` : "Not protected now."}
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
      <div style={{ display: "flex", gap: 8, marginTop: 6 }}>
        <button className="l-btn l-btn-primary" disabled={busy || parsed.bad.length > 0} onClick={review}>Review and apply</button>
        <button className="l-btn" disabled={busy} onClick={onCancel}>Cancel</button>
      </div>
    </div>
  );
}
