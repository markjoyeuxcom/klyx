import { useEffect, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { addClusters, refreshNewContextCount, type FleetConfigDTO } from "../../bridge/configsvc";
import { btn, ellipsis } from "../../chrome/primitives";
import { useLine } from "../../store/line";
import { SettingField } from "../SettingField";
import { anchorOf, settingDomId } from "../registry";
import { PathRow, Group, type SectionProps } from "./common";

// Settings > Fleet (S1 §1): the clusters Klyx runs (slice 0's drift
// included), the kubeconfig contexts, and the line's comparison source, as
// it runs. Every cluster field is file-only in S1 (editable in S2): each
// cell carries its anchor, and the fields' contracts follow the table.

const FIELDS: { id: string; head: string }[] = [
  { id: "clusters[].name", head: "name" },
  { id: "clusters[].context", head: "context" },
  { id: "clusters[].environment", head: "env" },
  { id: "clusters[].group", head: "group" },
  { id: "clusters[].protected", head: "protected" },
];
const DESCRIBED = ["clusters[].name", "clusters[].context", "clusters[].kubeconfig", "clusters[].tags", "clusters[].group", "clusters[].environment", "clusters[].protected"];
const IN_TABLE = new Set(FIELDS.map((f) => f.id));
const GRID = "minmax(0,1fr) minmax(0,1.3fr) 80px 80px 90px 70px";

export function sentenceForFleet(cfg: FleetConfigDTO): string {
  const n = cfg.clusters.length;
  const p = cfg.clusters.filter((c) => c.protected).length;
  const running = `${n} cluster${n === 1 ? "" : "s"} running, ${p} protected`;
  if (cfg.loadError) return `${running}; fleet.yaml did not load when Klyx started`;
  if (cfg.fileError) return `${running}; fleet.yaml does not load as it is now`;
  const drift = cfg.clusters.filter((c) => c.drift).length;
  const only = cfg.fileOnly?.length ?? 0;
  const differs = [drift > 0 ? `${drift} differ${drift === 1 ? "s" : ""} from fleet.yaml` : null, only > 0 ? `${only} in fleet.yaml not running` : null].filter(Boolean);
  return differs.length ? `${running}; ${differs.join(", ")}` : `${running}; fleet.yaml agrees`;
}

export function FleetSection({ cfg, reload, ready }: SectionProps & { cfg: FleetConfigDTO; reload: () => void }) {
  const [selected, setSelected] = useState<string[]>([]);
  const [banner, setBanner] = useState<{ kind: "success" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const line = useLine((s) => s.dto);
  useEffect(() => { ready(); }, [ready]);
  const newContexts = cfg.contexts.filter((c) => !c.inFleet);
  const toggle = (name: string) => setSelected((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]));
  const onAdd = async () => {
    if (selected.length === 0 || busy) return;
    setBusy(true);
    const r = await addClusters(selected);
    setBusy(false);
    if (r.ok) {
      setBanner({ kind: "success", text: `Added ${selected.length} cluster${selected.length === 1 ? "" : "s"} to fleet.yaml - connecting now.` });
      setSelected([]);
      reload();
      void refreshNewContextCount();
    } else {
      setBanner({ kind: "error", text: r.error || "Could not update fleet.yaml" });
    }
  };
  const overridden = line?.keyHow === "override" ? line.key : undefined;
  return (
    <>
      {banner && (
        <div data-testid="settings-banner" role="status" className="t-body" style={{ color: banner.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-primary)" }}>
          {banner.kind === "error" ? "▲ " : ""}{banner.text}
        </div>
      )}
      <Group title="Clusters">
        <PathRow label="file" path={cfg.path} />
        <p className="settings-group-note">
          The clusters below are the ones Klyx is running. It reads their identity, tags, protection and endpoints from fleet.yaml when it starts, so an edit to a cluster entry takes effect when Klyx restarts (an edited endpoint also when you Test it under Observability); a row says when the file differs. Klyx writes the file in five places only: it appends the clusters you add below; Settings &gt; AI, Git and Notifications each rewrite their own block (comments inside that block are not kept); and Observability replaces a cluster&apos;s plaintext metrics token line with auth when you move it to the keychain. Every other line stays as you wrote it; edit the file directly for anything else.
        </p>
        {cfg.fileOnly && cfg.fileOnly.length > 0 && (
          <div data-testid="settings-file-only" role="status" style={{ fontSize: 12, color: "var(--color-text-warning)", marginBottom: 8 }}>
            In fleet.yaml but not running: <span style={{ fontFamily: "var(--font-mono)" }}>{cfg.fileOnly.join(", ")}</span>. Klyx connects {cfg.fileOnly.length === 1 ? "it" : "them"} when it restarts.
          </div>
        )}
        {cfg.warnings.length > 0 && (
          <div style={{ marginBottom: 8 }}>
            {cfg.warnings.map((w, i) => (
              <div key={i} style={{ fontSize: 12, color: "var(--color-text-warning)", padding: "2px 0", display: "flex", alignItems: "baseline", gap: 4 }}><IconAlertTriangle size={11} stroke={1.7} style={{ flexShrink: 0, alignSelf: "center" }} /> {w}</div>
            ))}
          </div>
        )}
        <div role="table" aria-label="Clusters running">
          <div role="row" style={{ display: "grid", gridTemplateColumns: GRID, gap: 8, fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", padding: "0 0 4px", borderBottom: "0.5px solid var(--color-border-secondary)" }}>
            {FIELDS.map((f) => <span role="columnheader" key={f.id}>{f.head}</span>)}
            <span role="columnheader">metrics</span>
          </div>
          {cfg.clusters.map((c) => {
            const cell = (id: string, text: string, extra?: React.CSSProperties, title?: string) => (
              <span role="cell" data-setting={id} data-instance={c.name} data-anchor={anchorOf(id, c.name)} id={`${settingDomId(id, c.name)}-row`} tabIndex={-1}
                style={{ ...ellipsis, ...extra }} title={title ?? text}>{text}</span>
            );
            return (
              <div role="row" key={c.name} style={{ display: "grid", gridTemplateColumns: GRID, gap: 8, fontFamily: "var(--font-mono)", fontSize: 12, padding: "6px 0", borderBottom: "0.5px solid var(--color-border-tertiary)", alignItems: "center" }}>
                {cell("clusters[].name", c.name)}
                {cell("clusters[].context", c.context, { color: "var(--color-text-tertiary)" })}
                {cell("clusters[].environment", c.env || "—", { color: "var(--color-text-tertiary)" })}
                {cell("clusters[].group", c.group || "—", { color: "var(--color-text-tertiary)" })}
                {cell("clusters[].protected", c.protected ? (c.protectedInferred ? "inferred" : "yes") : "no", { color: c.protected ? "var(--color-text-warning)" : "var(--color-text-tertiary)" },
                  c.protectedInferred ? "inferred from a prd/prod/production token in its env, name or context - set protected: true to make it explicit" : undefined)}
                <span role="cell" style={{ color: "var(--color-text-tertiary)" }}>{c.hasMetrics ? "yes" : "—"}</span>
                {c.drift && (
                  <span data-testid="settings-drift" style={{ gridColumn: "1 / -1", fontFamily: "var(--font-sans)", color: "var(--color-text-warning)", fontSize: 12 }}>{c.drift}</span>
                )}
              </div>
            );
          })}
        </div>
      </Group>

      <Group title="Each cluster's fields">
        <p className="settings-group-note">What each field of a cluster entry does. In this version they are changed in fleet.yaml; the table above shows each cluster's value in use.</p>
        {DESCRIBED.map((id) => <SettingField key={id} id={id} source="file" value={IN_TABLE.has(id) ? "per cluster, in the table above" : "per cluster, in fleet.yaml; not shown here in this version"} />)}
      </Group>

      <Group title="The line's comparison source">
        <SettingField id="line.source.url" source={overridden ? "file" : "default"}
          value={overridden ? overridden.repo : line?.key ? `not set: comparing ${line.key.label}, ${line.keyHow === "selector" ? "chosen in this session" : "chosen by most stations"}` : "not set"} />
        <SettingField id="line.source.ref.kind" source={overridden ? "file" : "default"} value={overridden?.refKind || "not set"} />
        <SettingField id="line.source.ref.name" source={overridden ? "file" : "default"} value={overridden?.refName || "not set"} />
      </Group>

      <Group title="Kubeconfig contexts">
        <PathRow label="kubeconfig" path={cfg.kubeconfigPath} />
        {cfg.scanError ? (
          <div style={{ fontSize: 12, color: "var(--color-text-danger)" }}>Could not read the kubeconfig: {cfg.scanError}</div>
        ) : cfg.contexts.length === 0 ? (
          <div style={{ fontSize: 12, color: "var(--color-text-tertiary)" }}>No contexts found.</div>
        ) : (
          <>
            <p className="settings-group-note">Re-scanned every time this page opens - a context added to the kubeconfig shows up here without restarting.</p>
            {cfg.contexts.map((c) => (
              <div key={c.name} style={{ display: "flex", alignItems: "center", gap: 8, fontFamily: "var(--font-mono)", fontSize: 12, padding: "4px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
                {c.inFleet ? (
                  <span style={{ width: 14 }} />
                ) : (
                  <input type="checkbox" data-setting-exempt="action" aria-label={`select context ${c.name}`} checked={selected.includes(c.name)} onChange={() => toggle(c.name)} style={{ margin: 0 }} />
                )}
                <span style={{ ...ellipsis, flex: 1 }} title={c.name}>{c.name}</span>
                {c.inFleet ? <span className="t-secondary">in fleet</span> : <span style={{ fontSize: 11, color: "var(--color-text-info)" }}>not in fleet</span>}
              </div>
            ))}
            {newContexts.length > 0 && cfg.loadError && (
              <div style={{ marginTop: 10, fontSize: 12, color: "var(--color-text-tertiary)" }}>Adding clusters waits until fleet.yaml loads.</div>
            )}
            {newContexts.length > 0 && !cfg.loadError && (
              <div style={{ marginTop: 10, display: "flex", alignItems: "center", gap: 8 }}>
                <button onClick={() => void onAdd()} disabled={selected.length === 0 || busy}
                  style={{ ...btn, opacity: selected.length === 0 || busy ? 0.5 : 1, cursor: selected.length === 0 || busy ? "not-allowed" : "pointer" }}>
                  {busy ? "adding…" : `add ${selected.length || ""} to fleet`.replace("  ", " ")}
                </button>
                <span style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>appends to fleet.yaml · connects immediately</span>
              </div>
            )}
          </>
        )}
      </Group>
    </>
  );
}
