import { useEffect, useState } from "react";
import { IconAlertTriangle } from "@tabler/icons-react";
import { getFleetConfig, addClusters, refreshNewContextCount } from "../bridge/configsvc";
import type { FleetConfigDTO } from "../bridge/configsvc";
import { btn, ellipsis } from "../chrome/primitives";

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
      setBanner({ kind: "success", text: `Added ${selected.length} cluster${selected.length === 1 ? "" : "s"} to fleet.yaml — connecting now, watch the fleet spine.` });
      setSelected([]);
      void load();
      void refreshNewContextCount();
    } else {
      setBanner({ kind: "error", text: r.error || "Could not update fleet.yaml" });
    }
  };

  if (loading && !cfg) return <div style={{ padding: "16px 20px", color: "var(--color-text-secondary)", fontSize: 13 }}>Loading configuration…</div>;
  if (!cfg) return <div style={{ padding: "16px 20px", color: "var(--color-text-secondary)", fontSize: 13 }}>Could not load the fleet configuration.</div>;

  const newContexts = cfg.contexts.filter((c) => !c.inFleet);

  return (
    <div style={{ padding: "16px 20px", maxWidth: 760, fontSize: 12 }}>
      {banner && (
        <div
          data-testid="settings-banner"
          style={{
            marginBottom: 14, padding: "8px 10px", borderRadius: 4, fontSize: 12,
            background: banner.kind === "success" ? "var(--color-background-success)" : "var(--color-background-danger)",
            color: banner.kind === "success" ? "var(--color-text-success)" : "var(--color-text-danger)",
            border: `0.5px solid ${banner.kind === "success" ? "var(--color-border-success)" : "var(--color-border-danger)"}`,
          }}
        >
          {banner.text}
        </div>
      )}

      <Section title="Fleet configuration">
        <PathRow label="file" path={cfg.path} />
        <div style={{ color: "var(--color-text-tertiary)", fontSize: 11, marginBottom: 8 }}>
          Cluster identity, tags, protection, and metrics endpoints live here. Edit the file directly for anything beyond adding clusters — Klyx never rewrites your hand-written entries.
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
            <span style={{ color: c.protected ? "var(--color-text-warning)" : "var(--color-text-tertiary)" }}>{c.protected ? "yes" : "no"}</span>
            <span style={{ color: "var(--color-text-tertiary)" }}>{c.hasMetrics ? "yes" : "—"}</span>
          </div>
        ))}
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
              Re-scanned every time this page opens — a context added to the kubeconfig shows up here without restarting.
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
                  <span style={{ fontSize: 10, color: "var(--color-text-success)" }}>in fleet</span>
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
  );
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
    <div style={{ marginBottom: 22 }}>
      <div style={{ fontSize: 10, textTransform: "uppercase", letterSpacing: 0.5, color: "var(--color-text-tertiary)", marginBottom: 8 }}>{title}</div>
      {children}
    </div>
  );
}
