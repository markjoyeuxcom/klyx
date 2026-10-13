import { useEffect, useState } from "react";
import { IconAlertTriangle, IconSettings } from "@tabler/icons-react";
import { EmptyState } from "../chrome/EmptyState";
import { SkeletonRows } from "../chrome/SkeletonRows";
import { LensLayout, type DatasetState } from "../layout/LensLayout";
import { getFleetConfig, addClusters, refreshNewContextCount } from "../bridge/configsvc";
import type { FleetConfigDTO } from "../bridge/configsvc";
import { btn, ellipsis } from "../chrome/primitives";
import { AISettingsPanel } from "./AISettingsPanel";

// SettingsView is the fleet-configuration surface: the loaded fleet.yaml
// (path, clusters, load warnings) and a fresh kubeconfig context scan with
// one-click "add to fleet". Adds append to the file on disk AND join the
// running fleet immediately - the new cluster appears in the spine within a
// push-loop tick, no restart needed.

export function SettingsView() {
  const [cfg, setCfg] = useState<FleetConfigDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<string[]>([]);
  const [banner, setBanner] = useState<{ kind: "success" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = async () => {
    setLoading(true);
    const dto = await getFleetConfig();
    setCfg(dto);
    setLoading(false);
  };
  useEffect(() => {
    void load();
  }, []);

  const toggle = (name: string) =>
    setSelected((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]));

  const onAdd = async () => {
    if (selected.length === 0 || busy) return;
    setBusy(true);
    const r = await addClusters(selected);
    setBusy(false);
    if (r.ok) {
      setBanner({ kind: "success", text: `Added ${selected.length} cluster${selected.length === 1 ? "" : "s"} to fleet.yaml - connecting now, watch the fleet spine.` });
      setSelected([]);
      void load();
      void refreshNewContextCount();
    } else {
      setBanner({ kind: "error", text: r.error || "Could not update fleet.yaml" });
    }
  };

  const frame = (state: DatasetState, stateView: React.ReactNode, body: React.ReactNode, summary?: React.ReactNode) => (
    <LensLayout
      label="Settings"
      title="Settings"
      summary={summary}
      headerEnd={<button className="l-btn" onClick={() => void load()} disabled={loading}>{loading ? "Reading…" : "Re-scan"}</button>}
      notice={banner ? (
        <div data-testid="settings-banner" role="status" className="t-body" style={{ color: banner.kind === "error" ? "var(--color-text-danger)" : "var(--color-text-primary)" }}>
          {banner.kind === "error" ? "▲ " : ""}{banner.text}
        </div>
      ) : undefined}
      state={state}
      stateView={stateView}
      body={body}
    />
  );
  if (loading && !cfg) return frame("loading", <SkeletonRows rows={4} label="reading the fleet configuration" />, null);
  if (!cfg) return frame("unreadable", <EmptyState icon={<IconSettings size={28} stroke={1.2} />} title="Could not load the fleet configuration." action={{ label: "Retry", onClick: () => void load() }} />, null);

  const newContexts = cfg.contexts.filter((c) => !c.inFleet);

  // Step 7d: an app route on the workspace template.
  return frame("ready", null, (
    <div className="lens-canvas" style={{ fontSize: 12, maxWidth: 900 }}>

      <Section title="Fleet configuration">
        <PathRow label="file" path={cfg.path} />
        <div style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginBottom: 8 }}>
          Cluster identity, tags, protection, and metrics endpoints live here. Klyx writes this file in two places only: it appends clusters you add below, and Settings &gt; AI saves the ai block (comments inside that block are not kept). Your cluster entries and every other line stay as you wrote them; edit the file directly for anything else.
        </div>
        {cfg.warnings.length > 0 && (
          <div style={{ marginBottom: 8 }}>
            {cfg.warnings.map((w, i) => (
              <div key={i} style={{ fontSize: 11, color: "var(--color-text-warning)", padding: "2px 0", display: "flex", alignItems: "baseline", gap: 4 }}><IconAlertTriangle size={11} stroke={1.7} style={{ flexShrink: 0, alignSelf: "center" }} /> {w}</div>
            ))}
          </div>
        )}
        <div style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) minmax(0,1.3fr) 70px 70px 70px 60px", gap: 8, fontSize: 9, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", padding: "0 0 4px", borderBottom: "0.5px solid var(--color-border-secondary)" }}>
          <span>name</span><span>context</span><span>env</span><span>group</span><span>protected</span><span>metrics</span>
        </div>
        {cfg.clusters.map((c) => (
          <div key={c.name} style={{ display: "grid", gridTemplateColumns: "minmax(0,1fr) minmax(0,1.3fr) 70px 70px 70px 60px", gap: 8, fontFamily: "var(--font-mono)", fontSize: 11, padding: "5px 0", borderBottom: "0.5px solid var(--color-border-tertiary)", alignItems: "center" }}>
            <span style={ellipsis} title={c.name}>{c.name}</span>
            <span style={{ ...ellipsis, color: "var(--color-text-tertiary)" }} title={c.context}>{c.context}</span>
            <span style={{ color: "var(--color-text-tertiary)" }}>{c.env || "—"}</span>
            <span style={{ color: "var(--color-text-tertiary)" }}>{c.group || "—"}</span>
            <span
              style={{ color: c.protected ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}
              title={c.protectedInferred ? "inferred from a prd/prod/production token in its env, name or context - set protected: true to make it explicit" : undefined}
            >
              {c.protected ? (c.protectedInferred ? "inferred" : "yes") : "no"}
            </span>
            <span style={{ color: "var(--color-text-tertiary)" }}>{c.hasMetrics ? "yes" : "—"}</span>
          </div>
        ))}
      </Section>

      <Section title="AI assist">
        <AISettingsPanel />
      </Section>

      <Section title="Kubeconfig contexts">
        <PathRow label="kubeconfig" path={cfg.kubeconfigPath} />
        {cfg.scanError ? (
          <div style={{ fontSize: 11, color: "var(--color-text-danger)" }}>Could not read the kubeconfig: {cfg.scanError}</div>
        ) : cfg.contexts.length === 0 ? (
          <div style={{ fontSize: 11, color: "var(--color-text-tertiary)" }}>No contexts found.</div>
        ) : (
          <>
            <div style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginBottom: 6 }}>
              Re-scanned every time this page opens - a context added to the kubeconfig shows up here without restarting.
            </div>
            {cfg.contexts.map((c) => (
              <div key={c.name} style={{ display: "flex", alignItems: "center", gap: 8, fontFamily: "var(--font-mono)", fontSize: 11, padding: "4px 0", borderBottom: "0.5px solid var(--color-border-tertiary)" }}>
                {c.inFleet ? (
                  <span style={{ width: 14 }} />
                ) : (
                  <input
                    type="checkbox"
                    aria-label={`select context ${c.name}`}
                    checked={selected.includes(c.name)}
                    onChange={() => toggle(c.name)}
                    style={{ margin: 0 }}
                  />
                )}
                <span style={{ ...ellipsis, flex: 1 }} title={c.name}>{c.name}</span>
                {c.inFleet ? (
                  <span className="t-secondary">in fleet</span>
                ) : (
                  <span style={{ fontSize: 10, color: "var(--color-text-info)" }}>not in fleet</span>
                )}
              </div>
            ))}
            {newContexts.length > 0 && (
              <div style={{ marginTop: 10, display: "flex", alignItems: "center", gap: 8 }}>
                <button
                  onClick={() => void onAdd()}
                  disabled={selected.length === 0 || busy}
                  style={{ ...btn, opacity: selected.length === 0 || busy ? 0.5 : 1, cursor: selected.length === 0 || busy ? "not-allowed" : "pointer" }}
                >
                  {busy ? "adding…" : `add ${selected.length || ""} to fleet`.replace("  ", " ")}
                </button>
                <span style={{ fontSize: 10, color: "var(--color-text-tertiary)" }}>appends to fleet.yaml · connects immediately</span>
              </div>
            )}
          </>
        )}
      </Section>
    </div>
  ), (
    <>
      <span className="t-number" style={{ color: "var(--color-text-secondary)" }}>{cfg.clusters.length}</span>
      <span className="t-secondary">cluster{cfg.clusters.length === 1 ? "" : "s"} in fleet.yaml</span>
      {newContexts.length > 0 && <span className="t-secondary">{newContexts.length} kubeconfig context{newContexts.length === 1 ? "" : "s"} not in the fleet</span>}
    </>
  ));
}

function PathRow({ label, path }: { label: string; path: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 6 }}>
      <span style={{ fontSize: 10, color: "var(--color-text-tertiary)", width: 70 }}>{label}</span>
      <span style={{ fontFamily: "var(--font-mono)", fontSize: 11, ...ellipsis }} title={path}>{path || "—"}</span>
      {path && (
        <button
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(path);
              setCopied(true);
              setTimeout(() => setCopied(false), 1500);
            } catch { /* clipboard unavailable */ }
          }}
          style={{ ...btn, padding: "1px 7px", fontSize: 10 }}
          aria-label={`copy ${label} path`}
        >
          {copied ? "copied" : "copy"}
        </button>
      )}
    </div>
  );
}

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section style={{ marginBottom: 16 }}>
      <h3 className="t-section-label" style={{ marginBottom: 8 }}>{title}</h3>
      {children}
    </section>
  );
}
